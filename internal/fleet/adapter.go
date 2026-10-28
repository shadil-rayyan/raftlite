package fleet

import (
	"context"
	"sync"
	"time"
)

// Adapter is the gateway integration boundary. Implementations translate
// desired configuration into gateway-specific delivery and report outcomes
// as normalized Results. The mock adapter simulates gateways; the future
// Envoy adapter (xDS) implements the same interface.
//
// Delivery is asynchronous by contract: Publish schedules work and returns
// immediately; acceptance is only ever reported later via a Result. Nothing
// here contains policy-management business logic — that lives in Manager.
type Adapter interface {
	// Publish schedules delivery of the revision's configuration to one
	// gateway. Implementations must not block on the simulated gateway.
	Publish(ctx context.Context, gatewayID string, revision uint64, snap ConfigSnapshot)

	// Results is the normalized outcome stream consumed by the Manager.
	Results() <-chan Result
}

// Result is one reported configuration outcome.
type Result struct {
	GatewayID string
	Revision  uint64
	Status    string // accepted | rejected
	Err       *EventError
}

// MockSim holds the per-gateway simulation controls.
type MockSim struct {
	Delay       time.Duration
	RejectNext  bool
	AcceptNext  bool // overrides RejectNext for exactly one delivery
	TimeoutNext bool // never respond; manager's timeout timer fires
}

// MockAdapter simulates gateways. Configuration acceptance is simulated;
// actual network enforcement is never verified by this adapter.
type MockAdapter struct {
	mu   sync.Mutex
	sims map[string]*MockSim
	ch   chan Result
}

// NewMockAdapter returns a simulator with default per-gateway settings.
func NewMockAdapter() *MockAdapter {
	return &MockAdapter{sims: make(map[string]*MockSim), ch: make(chan Result, 64)}
}

func (m *MockAdapter) Results() <-chan Result { return m.ch }

func (m *MockAdapter) sim(id string) *MockSim {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sims[id]
	if !ok {
		s = &MockSim{Delay: 100 * time.Millisecond}
		m.sims[id] = s
	}
	return s
}

// SetSim replaces the scenario controls for one gateway.
func (m *MockAdapter) SetSim(id string, s MockSim) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sims[id] = &s
}

// GetSim returns a copy of the scenario controls for one gateway.
func (m *MockAdapter) GetSim(id string) MockSim {
	s := m.sim(id)
	return *s
}

// Publish simulates the gateway receiving the update: it waits the
// configured delay, then reports acceptance or a structured rejection. A
// timeout scenario sends nothing; the Manager's delivery timer classifies
// it. reported_accepted_revision is advanced only by the Manager after the
// acceptance result is consumed — never by the adapter.
func (m *MockAdapter) Publish(_ context.Context, gatewayID string, revision uint64, _ ConfigSnapshot) {
	s := m.sim(gatewayID)
	m.mu.Lock()
	delay := s.Delay
	reject := s.RejectNext && !s.AcceptNext
	timeout := s.TimeoutNext
	s.RejectNext = false
	s.AcceptNext = false
	s.TimeoutNext = false
	m.mu.Unlock()

	if timeout {
		return // no response ever; manager times out
	}
	go func() {
		time.Sleep(delay)
		res := Result{GatewayID: gatewayID, Revision: revision, Status: RollAccepted}
		if reject {
			res.Status = RollRejected
			res.Err = &EventError{
				Code:    "INVALID_CONFIGURATION",
				Message: "The simulated gateway rejected this configuration.",
			}
		}
		select {
		case m.ch <- res:
		default: // ponytail: drop on full buffer; manager timeout covers it
		}
	}()
}
