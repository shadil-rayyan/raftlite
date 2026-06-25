package transport

import "errors"

var (
	ErrDiskStall   = errors.New("disk stall simulated")
	ErrDiskFailure = errors.New("disk failure simulated")
)

type LogEntry struct {
	Index uint64
	Term  uint64
	Type  byte
	Data  []byte
}

type Storage interface {
	Append(entry LogEntry) error
	Scan(fromIndex uint64) ([]LogEntry, error)
	LastIndex() (uint64, error)
	LastTerm() (uint64, error)
	Truncate(toIndex uint64) error
	Sync() error
	Close() error
}

type MockStorage struct {
	entries []LogEntry
	stallOn int
	failOn  int
	callCnt int
}

func NewMockStorage() *MockStorage {
	return &MockStorage{stallOn: -1, failOn: -1}
}

func (m *MockStorage) Append(entry LogEntry) error {
	m.callCnt++
	if m.failOn == m.callCnt {
		return ErrDiskFailure
	}
	if m.stallOn == m.callCnt {
		return ErrDiskStall
	}
	entry.Index = uint64(len(m.entries) + 1)
	m.entries = append(m.entries, entry)
	return nil
}

func (m *MockStorage) Scan(fromIndex uint64) ([]LogEntry, error) {
	if fromIndex < 1 {
		fromIndex = 1
	}
	if fromIndex > uint64(len(m.entries)) {
		return nil, nil
	}
	return m.entries[fromIndex-1:], nil
}

func (m *MockStorage) LastIndex() (uint64, error) { return uint64(len(m.entries)), nil }

func (m *MockStorage) LastTerm() (uint64, error) {
	if len(m.entries) == 0 {
		return 0, nil
	}
	return m.entries[len(m.entries)-1].Term, nil
}

func (m *MockStorage) Truncate(toIndex uint64) error {
	if toIndex >= uint64(len(m.entries)) {
		return nil
	}
	m.entries = m.entries[:toIndex]
	return nil
}

func (m *MockStorage) Sync() error { return nil }

func (m *MockStorage) Close() error { return nil }

func (m *MockStorage) SetStallOn(n int) { m.stallOn = n }

func (m *MockStorage) SetFailOn(n int) { m.failOn = n }
