package consensus

// Raft-conscious event log: a bounded, in-memory record of the consensus
// state transitions an operator needs to see — elections, leadership,
// term bumps, votes, snapshots, restore. This is deliberately NOT the WAL
// (that is the durable replicated log) and not per-entry replication noise
// (that is /metrics). It answers "who became leader, when, in which term"
// and "why did this node step down" for the dashboard.

import (
	"sync"
	"time"
)

// Consensus event types recorded by a node.
const (
	// EvElectionStarted: this node became a candidate and is campaigning.
	EvElectionStarted = "election_started"
	// EvLeaderElected: this node won an election and is now leader.
	EvLeaderElected = "leader_elected"
	// EvLeaderObserved: a follower learned the leader's identity from
	// AppendEntries (or InstallSnapshot) contact.
	EvLeaderObserved = "leader_observed"
	// EvTermBumped: the node saw a higher term and stepped down to follower.
	EvTermBumped = "term_bumped"
	// EvVoteGranted: the node granted its vote to a candidate.
	EvVoteGranted = "vote_granted"
	// EvSnapshotTaken: FSM state was snapshotted and the log compacted.
	EvSnapshotTaken = "snapshot_taken"
	// EvStateRestored: durable state (snapshot + log tail + hard state)
	// was restored at startup.
	EvStateRestored = "state_restored"
	// EvStorageError: a durability operation (WAL append/truncate or
	// hard-state write) failed. The node refuses safety-critical
	// acknowledgements until persistence works again.
	EvStorageError = "storage_error"
	// EvStorageRecovered: a storage write succeeded after an earlier
	// failure; the node is writable again.
	EvStorageRecovered = "storage_recovered"
)

// RaftEvent is one consensus-state transition, stamped with the node that
// observed it. Events are per-node local knowledge: each node records its
// own view, which is what an operator debugs from.
type RaftEvent struct {
	ID        uint64    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	NodeID    string    `json:"node_id"`
	Type      string    `json:"type"`
	Term      uint64    `json:"term"`
	Detail    string    `json:"detail,omitempty"`
}

// RaftEventView is implemented by nodes that record consensus events.
// Optional (same pattern as DiagnosticsView) so test fakes stay small.
type RaftEventView interface {
	RaftEvents(limit int) []RaftEvent
}

// eventLog is a bounded FIFO of consensus events. Its own mutex is a leaf
// lock: record() is safe to call while the node lock is held.
type eventLog struct {
	mu     sync.Mutex
	max    int
	seq    uint64
	events []RaftEvent
}

func newEventLog(max int) *eventLog { return &eventLog{max: max} }

func (l *eventLog) record(nodeID string, term uint64, typ, detail string) RaftEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e := RaftEvent{
		ID:        l.seq,
		Timestamp: time.Now().UTC(),
		NodeID:    nodeID,
		Type:      typ,
		Term:      term,
		Detail:    detail,
	}
	l.events = append(l.events, e)
	if len(l.events) > l.max {
		l.events = l.events[len(l.events)-l.max:]
	}
	return e
}

// recent returns up to limit events, newest first.
func (l *eventLog) recent(limit int) []RaftEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > len(l.events) {
		limit = len(l.events)
	}
	out := make([]RaftEvent, 0, limit)
	for i := len(l.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, l.events[i])
	}
	return out
}

// recordEvent appends one consensus event. Safe to call with n.mu held.
func (n *Node) recordEvent(term uint64, typ, detail string) {
	if n.events == nil {
		return // optional: tests may construct nodes before wiring
	}
	n.events.record(n.id, term, typ, detail)
}

// RaftEvents returns this node's recent consensus events, newest first.
func (n *Node) RaftEvents(limit int) []RaftEvent {
	if n.events == nil {
		return nil
	}
	return n.events.recent(limit)
}
