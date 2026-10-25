package consensus

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/shadil/raftlite/internal/policy"
)

// FSM is the replicated state machine. Apply is deterministic: every byte of
// decision data (IDs, timestamps, expiry) arrives inside the command; the FSM
// never reads wall-clock time while applying (SRS FR-004, NFR-015).
//
// Legacy compatibility: bare "add:x" / "del:x" command strings from the v0.1
// prototype are translated into structured policy commands so old logs replay
// correctly. New writes use structured Commands exclusively.
type FSM struct {
	mu     sync.Mutex
	engine *policy.Engine
}

// NewFSM returns an empty state machine.
func NewFSM() *FSM {
	return &FSM{engine: policy.NewEngine()}
}

// Apply executes one committed command. revision is the Raft log index of the
// entry, giving every replica the same version stamp without coordination.
func (f *FSM) Apply(data []byte, revision uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	cmd, ok := decodeCommand(data)
	if !ok {
		return // unknown commands are ignored deterministically on all nodes
	}
	_, _ = f.engine.Apply(cmd, revision) // validation errors: skip, keep state consistent
}

// decodeCommand accepts both structured JSON commands and legacy strings.
func decodeCommand(data []byte) (policy.Command, bool) {
	if len(data) == 0 {
		return policy.Command{}, false
	}
	if data[0] == '{' {
		var cmd policy.Command
		if err := json.Unmarshal(data, &cmd); err != nil {
			return policy.Command{}, false
		}
		return cmd, true
	}
	return policy.LegacyCommand(string(data))
}

// IsBlocked evaluates a client address at the current time.
func (f *FSM) IsBlocked(ip string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.engine.LookupString(ip, time.Now()).Blocked
}

// Lookup evaluates an address at a given instant (deterministic view).
func (f *FSM) Lookup(ip string, now time.Time) policy.Decision {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.engine.LookupString(ip, now)
}

// Get returns a policy by id.
func (f *FSM) Get(id string) (*policy.Policy, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.engine.Get(id)
}

// List returns policies visible at the given instant.
func (f *FSM) List(now time.Time, includeInactive bool) []*policy.Policy {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.engine.List(now, includeInactive)
}

// Revision is the last applied policy revision (Raft log index).
func (f *FSM) Revision() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.engine.Revision()
}

// CountActive returns the number of enforceable policies at the instant.
func (f *FSM) CountActive(now time.Time) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.engine.CountActive(now)
}

// Snapshot serializes the full state for persistence or transfer.
func (f *FSM) Snapshot() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.engine.Snapshot()
}

// Restore replaces the state machine from a snapshot.
func (f *FSM) Restore(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.engine.Restore(data)
}
