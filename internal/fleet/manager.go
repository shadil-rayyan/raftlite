package fleet

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Manager owns the gateway registry, rollouts, and the event history. It is
// the only place policy-management business logic lives; adapters stay dumb.
//
// ponytail: one global mutex and in-memory state — right for a simulation;
// shard or replicate through Raft when fleet state must survive restarts.
type Manager struct {
	mu       sync.Mutex
	log      *slog.Logger
	gateways map[string]*Gateway
	rollouts map[string]*Rollout
	order    []string // rollout IDs in creation order
	events   *eventLog
	adapter  Adapter
	seq      uint64
	lastRev  uint64
	lastSnap *ConfigSnapshot
	timeout  time.Duration
	timers   map[string]*time.Timer // gatewayID -> in-flight delivery timer
	stopped  bool
	stopCh   chan struct{}
}

// Errors returned by Manager operations.
var (
	ErrNotFound       = errors.New("gateway not found")
	ErrExists         = errors.New("gateway id already exists")
	ErrInFlight       = errors.New("gateway has an in-flight configuration delivery")
	ErrNotSimulated   = errors.New("gateway is not a simulated (mock) gateway")
	ErrInvalidDelay   = errors.New("delay must be between 0 and 60s")
	ErrInvalidTimeout = errors.New("timeout must be between 100ms and 10m")
)

// NewManager wires a Manager to an adapter and starts its result consumer
// and heartbeat loop.
func NewManager(adapter Adapter, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		log:      log,
		gateways: make(map[string]*Gateway),
		rollouts: make(map[string]*Rollout),
		events:   newEventLog(1000),
		adapter:  adapter,
		timeout:  5 * time.Second,
		timers:   make(map[string]*time.Timer),
		stopCh:   make(chan struct{}),
	}
	go m.consumeResults()
	go m.heartbeatLoop()
	return m
}

// Stop halts background loops. In-flight timers are drained best-effort.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopped = true
	for _, t := range m.timers {
		t.Stop()
	}
	m.mu.Unlock()
	close(m.stopCh)
}

// ---- registry ----

// CreateGateway registers a simulated gateway with a stable identity. If a
// gateway is created after policies exist, it is immediately reconciled to
// the current revision (recorded as its own rollout, never auto-marked
// current).
func (m *Manager) CreateGateway(name, description string, delay time.Duration) (*Gateway, error) {
	if delay < 0 || delay > 60*time.Second {
		return nil, ErrInvalidDelay
	}
	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("gw-%03d", m.seq)
	now := time.Now().UTC()
	gw := &Gateway{
		ID:               id,
		Name:             name,
		Description:      description,
		AdapterType:      AdapterMock,
		ConnectionStatus: StatusOnline,
		LastHeartbeatAt:  now,
		LastUpdateAt:     now,
		Verification:     VerifUntested,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	m.gateways[id] = gw
	m.mu.Unlock()

	m.events.add(Event{Timestamp: now, GatewayID: id, Type: EvGatewayCreated, Result: StatusOnline,
		Metadata: map[string]string{"name": name}})
	if ms, ok := m.adapter.(*MockAdapter); ok {
		ms.SetSim(id, MockSim{Delay: delay})
	}
	if rev := m.CurrentRevision(); rev > 0 {
		m.reconcileTargets([]string{id}, rev, "reconnect")
	}
	return gw, nil
}

// DeleteGateway removes a simulated gateway. Refused while it has an
// in-flight delivery in the newest rollout so results never dangle.
func (m *Manager) DeleteGateway(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	gw, ok := m.gateways[id]
	if !ok {
		return ErrNotFound
	}
	if gw.AdapterType != AdapterMock {
		return ErrNotSimulated
	}
	if len(m.order) > 0 {
		latest := m.rollouts[m.order[len(m.order)-1]]
		if r := latest.Results[id]; r != nil && (r.Status == RollPending || r.Status == RollQueued || r.Status == RollSending) {
			return ErrInFlight
		}
	}
	if t := m.timers[id]; t != nil {
		t.Stop()
		delete(m.timers, id)
	}
	delete(m.gateways, id)
	m.events.add(Event{Timestamp: time.Now().UTC(), GatewayID: id, Type: "gateway_deleted"})
	return nil
}

// Get returns a copy of the gateway's current state.
func (m *Manager) Get(id string) (Gateway, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	gw, ok := m.gateways[id]
	if !ok {
		return Gateway{}, false
	}
	return *gw, true
}

// List returns all gateways sorted by ID.
func (m *Manager) List() []Gateway {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Gateway, 0, len(m.gateways))
	for _, gw := range m.gateways {
		out = append(out, *gw)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// CurrentRevision is the latest revision rollouts target.
func (m *Manager) CurrentRevision() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastRev
}

// ---- operator controls ----

// SetOffline marks a gateway offline. It keeps its identity, reported
// revision, and history; delivery attempts while offline resolve as offline.
func (m *Manager) SetOffline(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	gw, ok := m.gateways[id]
	if !ok {
		return ErrNotFound
	}
	gw.ConnectionStatus = StatusOffline
	gw.UpdatedAt = time.Now().UTC()
	m.events.add(Event{Timestamp: gw.UpdatedAt, GatewayID: id, Type: EvGatewayOffline, Result: StatusOffline})
	return nil
}

// SetOnline brings a gateway back. Identity and history are retained; the
// gateway is NOT marked up to date merely for reconnecting — if it is behind
// the desired revision a reconciliation rollout is scheduled.
func (m *Manager) SetOnline(id string) error {
	m.mu.Lock()
	gw, ok := m.gateways[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	now := time.Now().UTC()
	gw.ConnectionStatus = StatusReconn
	gw.UpdatedAt = now
	behind := gw.DesiredRevision > gw.ReportedAcceptedRevision
	desired := gw.DesiredRevision
	m.mu.Unlock()

	m.events.add(Event{Timestamp: now, GatewayID: id, Type: EvGatewayReconn, Result: StatusReconn})

	// ponytail: fixed 500ms reconnect latency makes the transient state
	// visible in the UI; make configurable if scenarios need it.
	go func() {
		select {
		case <-m.stopCh:
			return
		case <-time.After(500 * time.Millisecond):
		}
		m.mu.Lock()
		gw, ok := m.gateways[id]
		if !ok || gw.ConnectionStatus != StatusReconn {
			m.mu.Unlock()
			return
		}
		gw.ConnectionStatus = StatusOnline
		gw.LastHeartbeatAt = time.Now().UTC()
		gw.UpdatedAt = gw.LastHeartbeatAt
		m.mu.Unlock()
		if behind {
			m.reconcileTargets([]string{id}, desired, "reconnect")
		}
	}()
	return nil
}

// SetDelay configures the simulated response delay for one gateway.
func (m *Manager) SetDelay(id string, d time.Duration) error {
	if d < 0 || d > 60*time.Second {
		return ErrInvalidDelay
	}
	m.mu.Lock()
	_, ok := m.gateways[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if ms, ok := m.adapter.(*MockAdapter); ok {
		s := ms.GetSim(id)
		s.Delay = d
		ms.SetSim(id, s)
	}
	return nil
}

// Scenario applies a dev-only simulator control: reject-next, accept-next,
// timeout-next, or reset (clears one-shot flags).
func (m *Manager) Scenario(id, kind string) error {
	m.mu.Lock()
	_, ok := m.gateways[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	ms, ok := m.adapter.(*MockAdapter)
	if !ok {
		return ErrNotSimulated
	}
	s := ms.GetSim(id)
	switch kind {
	case "reject-next":
		s.RejectNext = true
	case "accept-next":
		s.AcceptNext = true
	case "timeout-next":
		s.TimeoutNext = true
	case "reset":
		s.RejectNext = false
		s.AcceptNext = false
		s.TimeoutNext = false
	default:
		return fmt.Errorf("unknown scenario %q", kind)
	}
	ms.SetSim(id, s)
	return nil
}

// SetDeliveryTimeout bounds how long the control plane waits for a gateway
// result before classifying the delivery as timed out.
func (m *Manager) SetDeliveryTimeout(d time.Duration) error {
	if d < 100*time.Millisecond || d > 10*time.Minute {
		return ErrInvalidTimeout
	}
	m.mu.Lock()
	m.timeout = d
	m.mu.Unlock()
	return nil
}

// ---- rollouts ----

// OnRevision is called after a policy change commits: it creates an immutable
// revision rollout and schedules async delivery to every gateway. Publishing
// never marks anything accepted — acceptance only ever arrives as a Result.
func (m *Manager) OnRevision(rev uint64, policyID string, snap ConfigSnapshot) Rollout {
	return copyRollout(m.startRollout(rev, policyID, "auto", m.allGatewayIDs(), snap))
}

// Reconcile manually re-publishes the current revision to gateways that are
// behind it.
func (m *Manager) Reconcile() (Rollout, error) {
	rev := m.CurrentRevision()
	if rev == 0 {
		return Rollout{}, ErrNotFound
	}
	m.mu.Lock()
	var targets []string
	for _, gw := range m.gateways {
		if gw.ReportedAcceptedRevision < rev {
			targets = append(targets, gw.ID)
		}
	}
	m.mu.Unlock()
	if len(targets) == 0 {
		return Rollout{}, errors.New("no gateway is behind the current revision")
	}
	return copyRollout(m.reconcileTargets(targets, rev, "manual")), nil
}

func (m *Manager) allGatewayIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.gateways))
	for id := range m.gateways {
		ids = append(ids, id)
	}
	return ids
}

func (m *Manager) reconcileTargets(ids []string, rev uint64, trigger string) *Rollout {
	snap := m.snapshotFor(rev)
	return m.startRollout(rev, "", trigger, ids, snap)
}

// snapshotFor builds the config for a revision. ponytail: the caller passes
// the real snapshot for auto rollouts; reconcile reuses the manager's last
// known snapshot, which is exact because every revision change flows through
// OnRevision on the same leader.
func (m *Manager) snapshotFor(rev uint64) ConfigSnapshot {
	if m.lastSnap != nil && m.lastSnap.Revision == rev {
		return *m.lastSnap
	}
	return ConfigSnapshot{Revision: rev}
}

// Rollouts returns rollouts newest-first. Results are deep-copied so
// callers never race with the result consumer.
func (m *Manager) Rollouts() []Rollout {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Rollout, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, copyRollout(m.rollouts[m.order[i]]))
	}
	return out
}

// Rollout returns one rollout by ID.
func (m *Manager) Rollout(id string) (Rollout, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rollouts[id]
	if !ok {
		return Rollout{}, false
	}
	return copyRollout(r), true
}

func copyRollout(r *Rollout) Rollout {
	cp := *r
	cp.Results = make(map[string]*RolloutResult, len(r.Results))
	for k, v := range r.Results {
		vv := *v
		cp.Results[k] = &vv
	}
	return cp
}

// Events returns up to limit recent events, newest first, optionally for one
// gateway. History is preserved, not just latest state.
func (m *Manager) Events(limit int, gatewayID string) []Event {
	if limit <= 0 {
		limit = 100
	}
	return m.events.recent(limit, gatewayID)
}
