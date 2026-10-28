// Package fleet implements the gateway fleet model: gateway registry,
// asynchronous configuration rollouts, and a normalized event stream.
//
// The model is deliberately adapter-agnostic. The mock adapter simulates
// gateways for development and testing; a future Envoy adapter (xDS via
// envoyproxy/go-control-plane) implements the same Adapter interface without
// touching this package's business logic.
//
// State here is in-memory and owned by the leader node only. Followers
// forward fleet requests to the leader (see internal/server). This is a
// deliberate simplification for the simulation era; replicating fleet state
// through Raft is the documented upgrade path.
//
// Key invariant (per spec): simulated acceptance is NOT enforcement
// verification. Verification has its own state and is never advanced by the
// mock adapter.
package fleet

import (
	"sync"
	"time"
)

// Gateway lifecycle and rollout states.
const (
	StatusOnline   = "online"
	StatusOffline  = "offline"
	StatusReconn   = "reconnecting"
	AdapterMock    = "mock"
	AdapterEnvoy   = "envoy" // reserved for the future real adapter
	VerifUntested  = "not_tested"
	VerifVerified  = "verified"
	VerifFailed    = "failed"
	VerifUnknown   = "unknown"
	RollPending    = "pending"
	RollQueued     = "queued"
	RollSending    = "sending"
	RollAccepted   = "accepted"
	RollRejected   = "rejected"
	RollTimeout    = "timeout"
	RollOffline    = "offline"
	RollSuperseded = "superseded"
)

// Event types.
const (
	EvGatewayCreated   = "gateway_created"
	EvGatewayOffline   = "gateway_offline"
	EvGatewayReconn    = "gateway_reconnected"
	EvPolicyPublished  = "policy_published"
	EvRolloutCreated   = "rollout_created"
	EvDeliveryScheduled = "delivery_scheduled"
	EvConfigAccepted   = "config_accepted"
	EvConfigRejected   = "config_rejected"
	EvDeliveryTimeout  = "delivery_timed_out"
	EvReconciled       = "gateway_reconciled"
	EvSuperseded       = "rollout_superseded"
	EvVerification     = "verification"
)

// Gateway is a fleet member with a stable identity. Desired and reported
// state are separate fields by design; never collapse them into one
// "current" value.
type Gateway struct {
	ID                       string    `json:"id"`
	Name                     string    `json:"name"`
	Description              string    `json:"description,omitempty"`
	AdapterType              string    `json:"adapter_type"`
	ConnectionStatus         string    `json:"connection_status"`
	DesiredRevision          uint64    `json:"desired_revision"`
	ReportedAcceptedRevision uint64    `json:"reported_accepted_revision"`
	LastRejectedRevision     *uint64   `json:"last_rejected_revision,omitempty"`
	LastHeartbeatAt          time.Time `json:"last_heartbeat_at"`
	LastUpdateAt             time.Time `json:"last_update_at"`
	LastError                *EventError `json:"last_error,omitempty"`
	Verification             string    `json:"verification"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

// EventError carries structured adapter error details.
type EventError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Event is one entry in the preserved fleet history. Fields are generic
// enough for both simulated and real gateways.
type Event struct {
	ID        string            `json:"id"`
	Timestamp time.Time         `json:"timestamp"`
	GatewayID string            `json:"gateway_id,omitempty"`
	PolicyID  string            `json:"policy_id,omitempty"`
	Revision  uint64            `json:"revision,omitempty"`
	RolloutID string            `json:"rollout_id,omitempty"`
	Type      string            `json:"type"`
	Result    string            `json:"result,omitempty"`
	Error     *EventError       `json:"error,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// RolloutResult is one target gateway's outcome within a rollout.
type RolloutResult struct {
	GatewayID  string      `json:"gateway_id"`
	Status     string      `json:"status"`
	Revision   uint64      `json:"revision"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
	Error      *EventError `json:"error,omitempty"`
}

// Rollout is one delivery attempt of one revision to the fleet.
type Rollout struct {
	ID          string                    `json:"id"`
	Revision    uint64                    `json:"revision"`
	PolicyID    string                    `json:"policy_id,omitempty"`
	CreatedAt   time.Time                 `json:"created_at"`
	TriggerType string                    `json:"trigger_type"` // auto | manual
	Results     map[string]*RolloutResult `json:"results"`
}

// ConfigSnapshot is the gateway-independent configuration published at a
// revision. Deliberately free of Envoy/xDS resource formats; the future
// Envoy adapter translates this into xDS resources itself.
type ConfigSnapshot struct {
	Revision uint64       `json:"revision"`
	Policies []PolicyView `json:"policies"`
}

// PolicyView is the adapter-facing projection of one policy.
type PolicyView struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type"` // ip_block | cidr_block
	Target  string `json:"target"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Expires *int64 `json:"expires_at_unix,omitempty"` // unix seconds; nil = permanent
}

// eventLog is a bounded FIFO of events preserving history.
type eventLog struct {
	mu   sync.Mutex
	max  int
	seq  uint64
	all  []Event
}

func newEventLog(max int) *eventLog { return &eventLog{max: max} }

func (l *eventLog) add(e Event) Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.ID = "evt-" + uintToHex(l.seq)
	l.all = append(l.all, e)
	if len(l.all) > l.max {
		l.all = l.all[len(l.all)-l.max:]
	}
	return e
}

func (l *eventLog) recent(limit int, gatewayID string) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, 0, min(limit, len(l.all)))
	for i := len(l.all) - 1; i >= 0 && len(out) < limit; i-- {
		if gatewayID == "" || l.all[i].GatewayID == gatewayID {
			out = append(out, l.all[i])
		}
	}
	return out
}

func uintToHex(n uint64) string {
	const hexdigits = "0123456789abcdef"
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = hexdigits[n&0xf]
		n >>= 4
	}
	return string(b[i:])
}
