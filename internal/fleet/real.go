// Real-gateway support: pull-based configuration serving, report ingestion,
// and enforcement verification for simulated-Envoy gateways (cmd/simgw).
//
// Key invariant (per spec): a report of acceptance NEVER advances
// Verification. Only enforcement probes do — simulated acceptance is not
// enforcement verification. The mock adapter is never involved in any of
// this path.
package fleet

import (
	"errors"
	"fmt"
	"time"
)

// Errors returned by the real-gateway operations.
var (
	ErrInvalidReport  = errors.New("report status must be accepted or rejected")
	ErrNotRealGateway = errors.New("gateway is not a pull-based (envoy-sim) gateway")
)

// PolicyVerification is the outcome of one enforcement probe for one policy
// on one gateway at one revision.
type PolicyVerification struct {
	GatewayID string      `json:"gateway_id"`
	PolicyID  string      `json:"policy_id"`
	Revision  uint64      `json:"revision"`
	Result    string      `json:"result"` // verified | failed
	At        time.Time   `json:"at"`
	Error     *EventError `json:"error,omitempty"`
}

// PolicyGatewayStatus is one gateway's row in a policy's fleet-wide
// enforcement ("is the policy achieved here?") view.
type PolicyGatewayStatus struct {
	GatewayID                string              `json:"gateway_id"`
	Name                     string              `json:"name"`
	ConnectionStatus         string              `json:"connection_status"`
	AdapterType              string              `json:"adapter_type"`
	DesiredRevision          uint64              `json:"desired_revision"`
	ReportedAcceptedRevision uint64              `json:"reported_accepted_revision"`
	GatewayVerification      string              `json:"gateway_verification"`
	PolicyVerification       *PolicyVerification `json:"policy_verification,omitempty"`
}

// RegisterRealGateway registers a pull-based gateway with a stable identity,
// or returns the existing one (restart-safe idempotency: `created` is false
// when the gateway was already registered). Like CreateGateway, a gateway
// registered after policies exist is immediately reconciled to the current
// revision — recorded as its own rollout, never auto-marked current.
func (m *Manager) RegisterRealGateway(id, name, proxyURL string) (gw *Gateway, created bool, err error) {
	m.mu.Lock()
	if id != "" {
		if existing, ok := m.gateways[id]; ok {
			if existing.AdapterType != AdapterEnvoySim {
				m.mu.Unlock()
				return nil, false, ErrExists
			}
			now := time.Now().UTC()
			changed := false
			if name != "" && existing.Name != name {
				existing.Name = name
				changed = true
			}
			if proxyURL != "" && existing.ProxyURL != proxyURL {
				existing.ProxyURL = proxyURL
				changed = true
			}
			if changed {
				existing.UpdatedAt = now
			}
			cp := *existing
			m.mu.Unlock()
			return &cp, false, nil
		}
	} else {
		id = m.nextIDLocked()
	}
	now := time.Now().UTC()
	gw = &Gateway{
		ID:               id,
		Name:             name,
		AdapterType:      AdapterEnvoySim,
		ProxyURL:         proxyURL,
		ConnectionStatus: StatusOnline,
		LastHeartbeatAt:  now,
		LastUpdateAt:     now,
		Verification:     VerifUntested,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	m.gateways[id] = gw
	rev := m.lastRev
	cp := *gw
	m.mu.Unlock()

	m.events.add(Event{Timestamp: now, GatewayID: id, Type: EvGatewayCreated, Result: StatusOnline,
		Metadata: map[string]string{"name": name, "adapter": AdapterEnvoySim}})
	if rev > 0 {
		m.reconcileTargets([]string{id}, rev, "reconnect")
	}
	return &cp, true, nil
}

// nextIDLocked returns the first free gw-NNN identifier. Caller holds m.mu.
func (m *Manager) nextIDLocked() string {
	for {
		m.seq++
		id := fmt.Sprintf("gw-%03d", m.seq)
		if _, taken := m.gateways[id]; !taken {
			return id
		}
	}
}

// ConfigFor serves the configuration snapshot for one pull-based gateway.
// A poll counts as a heartbeat: it is evidence the gateway process is
// alive, so LastHeartbeatAt advances. A poll from an operator-marked
// offline gateway triggers reconnect semantics (identity retained;
// reconciliation scheduled when behind).
func (m *Manager) ConfigFor(id string) (ConfigSnapshot, bool) {
	m.mu.Lock()
	gw, ok := m.gateways[id]
	if !ok || gw.AdapterType != AdapterEnvoySim {
		m.mu.Unlock()
		return ConfigSnapshot{}, false
	}
	now := time.Now().UTC()
	gw.LastHeartbeatAt = now
	gw.LastUpdateAt = now
	wasOffline := gw.ConnectionStatus == StatusOffline
	snap := m.snapshotFor(m.lastRev)
	m.mu.Unlock()

	if wasOffline {
		_ = m.SetOnline(id) // async reconnect + reconcile if behind
	}
	return snap, true
}

// CurrentSnapshot is the configuration at the current revision (read-only;
// does not touch gateway liveness). Used by the enforcement prober.
func (m *Manager) CurrentSnapshot() ConfigSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotFor(m.lastRev)
}

// HandleReport ingests one real gateway's outcome report for a revision.
// The report flows through the same stale-revision guard and rollout
// bookkeeping as adapter results. A report of acceptance advances
// reported_accepted_revision only — it never advances verification.
func (m *Manager) HandleReport(id string, rev uint64, status string, evErr *EventError) error {
	m.mu.Lock()
	gw, ok := m.gateways[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if gw.AdapterType != AdapterEnvoySim {
		m.mu.Unlock()
		return ErrNotRealGateway
	}
	gw.LastHeartbeatAt = time.Now().UTC()
	m.mu.Unlock()

	if status != RollAccepted && status != RollRejected {
		return ErrInvalidReport
	}
	m.handleResult(Result{GatewayID: id, Revision: rev, Status: status, Err: evErr})
	return nil
}

// MarkVerification records one enforcement-probe outcome for one policy on
// one gateway and rolls it up into the gateway-level verification field.
// This is the ONLY path that advances Verification.
func (m *Manager) MarkVerification(gatewayID, policyID string, rev uint64, result string, evErr *EventError) {
	now := time.Now().UTC()
	rec := PolicyVerification{GatewayID: gatewayID, PolicyID: policyID, Revision: rev, Result: result, At: now, Error: evErr}
	m.mu.Lock()
	gw, ok := m.gateways[gatewayID]
	if !ok {
		m.mu.Unlock()
		return
	}
	per, ok := m.verif[gatewayID]
	if !ok {
		per = make(map[string]PolicyVerification)
		m.verif[gatewayID] = per
	}
	per[policyID] = rec
	m.recomputeVerificationLocked(gw)
	m.mu.Unlock()

	m.events.add(Event{Timestamp: now, GatewayID: gatewayID, PolicyID: policyID, Revision: rev,
		Type: EvVerification, Result: result, Error: evErr})
}

// MarkGatewayVerification sets the gateway-level verification state without
// a per-policy record — used when the probe itself failed (the gateway data
// path was unreachable or misbehaved), so no per-policy conclusion is safe.
func (m *Manager) MarkGatewayVerification(gatewayID string, rev uint64, result string, evErr *EventError) {
	now := time.Now().UTC()
	m.mu.Lock()
	gw, ok := m.gateways[gatewayID]
	if !ok {
		m.mu.Unlock()
		return
	}
	gw.Verification = result
	gw.UpdatedAt = now
	m.mu.Unlock()

	m.events.add(Event{Timestamp: now, GatewayID: gatewayID, Revision: rev,
		Type: EvVerification, Result: result, Error: evErr})
}

// recomputeVerificationLocked rolls per-policy probe outcomes at the
// gateway's desired revision up into the gateway-level field: any failure
// fails the gateway; all-verified verifies it. Caller holds m.mu.
func (m *Manager) recomputeVerificationLocked(gw *Gateway) {
	verified, failed := 0, 0
	for _, rec := range m.verif[gw.ID] {
		if rec.Revision != gw.DesiredRevision {
			continue
		}
		switch rec.Result {
		case VerifVerified:
			verified++
		case VerifFailed:
			failed++
		}
	}
	switch {
	case failed > 0:
		gw.Verification = VerifFailed
	case verified > 0:
		gw.Verification = VerifVerified
	default:
		gw.Verification = VerifUntested
	}
	gw.UpdatedAt = time.Now().UTC()
}

// PolicyEnforcement is one policy's status on one gateway: what the
// gateway should be enforcing at its desired revision and whether an
// enforcement probe proved it achieves it. `not_tested` means no probe has
// spoken — never that enforcement is fine.
type PolicyEnforcement struct {
	PolicyID string      `json:"policy_id"`
	Target   string      `json:"target"`
	Type     string      `json:"type"`
	Revision uint64      `json:"revision"`
	Result   string      `json:"result"` // verified | failed | not_tested
	At       *time.Time  `json:"verified_at,omitempty"`
	Error    *EventError `json:"error,omitempty"`
}

// EnforcementFor returns, for one gateway, every policy in the current
// snapshot with the probe result at the gateway's desired revision. This
// is the "did the gateway receive the config, and does it achieve it?"
// view shown in the gateway detail.
func (m *Manager) EnforcementFor(gatewayID string) []PolicyEnforcement {
	m.mu.Lock()
	defer m.mu.Unlock()
	gw, ok := m.gateways[gatewayID]
	if !ok {
		return nil
	}
	rev := gw.DesiredRevision
	var pols []PolicyView
	if m.lastSnap != nil {
		pols = m.lastSnap.Policies
	}
	out := make([]PolicyEnforcement, 0, len(pols))
	for _, p := range pols {
		row := PolicyEnforcement{
			PolicyID: p.ID,
			Target:   p.Target,
			Type:     p.Type,
			Revision: rev,
			Result:   VerifUntested,
		}
		if rec, ok := m.verif[gatewayID][p.ID]; ok && rec.Revision == rev {
			at := rec.At
			row.Result = rec.Result
			row.At = &at
			row.Error = rec.Error
		}
		out = append(out, row)
	}
	return out
}

// PolicyStatus returns the fleet-wide enforcement view for one policy: for
// every registered gateway, whether THIS policy is achieved there (probe
// result at the gateway's current desired revision).
func (m *Manager) PolicyStatus(policyID string) []PolicyGatewayStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PolicyGatewayStatus, 0, len(m.gateways))
	for _, gw := range m.sortedGatewaysLocked() {
		row := PolicyGatewayStatus{
			GatewayID:                gw.ID,
			Name:                     gw.Name,
			ConnectionStatus:         gw.ConnectionStatus,
			AdapterType:              gw.AdapterType,
			DesiredRevision:          gw.DesiredRevision,
			ReportedAcceptedRevision: gw.ReportedAcceptedRevision,
			GatewayVerification:      gw.Verification,
		}
		if rec, ok := m.verif[gw.ID][policyID]; ok && rec.Revision == gw.DesiredRevision {
			cp := rec
			row.PolicyVerification = &cp
		}
		out = append(out, row)
	}
	return out
}
