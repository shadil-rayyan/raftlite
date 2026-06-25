package transport

import (
	"errors"
	"sync"
)

var ErrNodeDown = errors.New("node is down")

type Address string

type Envelope struct {
	From Address
	To   Address
	Msg  []byte
}

type Network interface {
	Send(to Address, msg []byte) error
	Receive() (Envelope, error)
}

type RealNetwork struct {
	// stub — wired to gRPC in Phase 3
}

func (RealNetwork) Send(_ Address, _ []byte) error { return nil }

func (RealNetwork) Receive() (Envelope, error) { return Envelope{}, nil }

type MockNetwork struct {
	mu         sync.Mutex
	inbox      map[Address][]Envelope
	dropRate   float64
	minLatency int
	maxLatency int
	partitions map[Address]bool
}

func NewMockNetwork() *MockNetwork {
	return &MockNetwork{
		inbox:      make(map[Address][]Envelope),
		dropRate:   0,
		partitions: make(map[Address]bool),
	}
}

func (m *MockNetwork) Send(to Address, msg []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.partitions[to] {
		return ErrNodeDown
	}
	m.inbox[to] = append(m.inbox[to], Envelope{Msg: msg, To: to})
	return nil
}

func (m *MockNetwork) Receive() (Envelope, error) {
	return Envelope{}, nil
}

func (m *MockNetwork) SetDropRate(r float64) { m.dropRate = r }

func (m *MockNetwork) SetPartition(addr Address, partitioned bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.partitions[addr] = partitioned
}

func (m *MockNetwork) DeliverAll(from Address) []Envelope {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Envelope
	for addr, msgs := range m.inbox {
		for _, e := range msgs {
			if e.From == from || from == "" {
				out = append(out, e)
			}
		}
		delete(m.inbox, addr)
	}
	return out
}
