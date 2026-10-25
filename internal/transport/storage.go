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

// HardState is the Raft persistence required for safety (§5.4.2): a node that
// votes must durably record term and vote before responding, or it can vote
// twice in one term after a crash. CommitIndex is persisted opportunistically
// to speed recovery; correctness does not depend on it.
type HardState struct {
	Term        uint64
	VotedFor    string
	CommitIndex uint64
}

type Storage interface {
	Append(entry LogEntry) error
	Scan(fromIndex uint64) ([]LogEntry, error)
	LastIndex() (uint64, error)
	LastTerm() (uint64, error)
	Truncate(toIndex uint64) error
	Sync() error
	Close() error

	// Hard-state durability. SaveHardState must not return until the state
	// is durable (fsync or equivalent).
	LoadHardState() (HardState, error)
	SaveHardState(hs HardState) error
}

// MockStorage is the DST-friendly in-memory Storage implementation. Failure
// injection targets log appends only; hard-state writes stay reliable so the
// mock keeps modeling a correct disk.
type MockStorage struct {
	entries []LogEntry
	hs      HardState
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
	// Respect an explicit index (log replay after compaction); assign one
	// positionally when unset (legacy call sites).
	if entry.Index == 0 {
		entry.Index = uint64(len(m.entries) + 1)
	}
	m.entries = append(m.entries, entry)
	return nil
}

func (m *MockStorage) Scan(fromIndex uint64) ([]LogEntry, error) {
	if fromIndex < 1 {
		fromIndex = 1
	}
	var out []LogEntry
	for _, e := range m.entries {
		if e.Index >= fromIndex {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *MockStorage) LastIndex() (uint64, error) { return uint64(len(m.entries)), nil }

func (m *MockStorage) LastTerm() (uint64, error) {
	if len(m.entries) == 0 {
		return 0, nil
	}
	return m.entries[len(m.entries)-1].Term, nil
}

// Truncate drops every entry with Index > toIndex (index-aware, matching
// the WAL's on-disk semantics).
func (m *MockStorage) Truncate(toIndex uint64) error {
	keep := 0
	for _, e := range m.entries {
		if e.Index <= toIndex {
			keep++
		}
	}
	m.entries = m.entries[:keep]
	return nil
}

func (m *MockStorage) Sync() error { return nil }

func (m *MockStorage) Close() error { return nil }

func (m *MockStorage) LoadHardState() (HardState, error) { return m.hs, nil }

func (m *MockStorage) SaveHardState(hs HardState) error {
	m.hs = hs
	return nil
}

func (m *MockStorage) SetStallOn(n int)  { m.stallOn = n }
func (m *MockStorage) SetFailOn(n int)   { m.failOn = n }
func (m *MockStorage) HardState() HardState { return m.hs }
