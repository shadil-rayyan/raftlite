package transport

import "time"

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type MockClock struct {
	now    time.Time
	afterC chan chan time.Time
	drift  time.Duration
}

func NewMockClock(start time.Time) *MockClock {
	return &MockClock{now: start, afterC: make(chan chan time.Time, 64)}
}

func (m *MockClock) Now() time.Time { return m.now.Add(m.drift) }

func (m *MockClock) After(d time.Duration) <-chan time.Time {
	c := make(chan time.Time, 1)
	m.afterC <- c
	return c
}

func (m *MockClock) Advance(d time.Duration) {
	m.now = m.now.Add(d)
	n := m.now
	for {
		select {
		case c := <-m.afterC:
			c <- n
		default:
			return
		}
	}
}

func (m *MockClock) SetDrift(d time.Duration) { m.drift = d }
