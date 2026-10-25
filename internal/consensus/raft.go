package consensus

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

// ErrNotLeader is returned when a follower receives a write.
var ErrNotLeader = errors.New("not the leader")

// ErrNoQuorum is returned when the leader cannot confirm a commit before its
// deadline. The command may still commit later; the API surfaces this as an
// uncertain outcome, never as a rejection (SRS FR-007, §8).
var ErrNoQuorum = errors.New("commit not confirmed: quorum unavailable")

type NodeState int

const (
	Follower  NodeState = 0
	Candidate NodeState = 1
	Leader    NodeState = 2
)

func (s NodeState) String() string {
	switch s {
	case Follower:
		return "follower"
	case Candidate:
		return "candidate"
	case Leader:
		return "leader"
	default:
		return "unknown"
	}
}

type LogEntry struct {
	Index uint64
	Term  uint64
	Type  raftpb.LogEntry_EntryType
	Data  []byte
}

// MetricsSink receives consensus events for observability (FR-030). Optional;
// a nil sink is a no-op, which keeps unit tests dependency-free.
type MetricsSink interface {
	IncElections()
	SetLeader(bool)
	SetTerm(uint64)
	SetCommitIndex(uint64)
	SetAppliedIndex(uint64)
	ObserveCommitLatency(seconds float64)
	IncCommits()
}

type Node struct {
	mu sync.Mutex

	id    string
	peers []string
	state NodeState

	currentTerm uint64
	votedFor    string
	log         []LogEntry

	commitIndex uint64
	lastApplied uint64

	nextIndex  map[string]uint64
	matchIndex map[string]uint64

	electionTimeoutMs  int
	heartbeatTimeoutMs int

	leaderID string

	clock     transport.Clock
	storage   transport.Storage
	fsm       *FSM
	transport Transport

	// resetTimerCh restarts the election timer without spawning goroutines.
	resetTimerCh chan struct{}
	shutdownCh   chan struct{}
	stopOnce     sync.Once

	// snapshot bookkeeping (see snapshot.go)
	snapshotIndex uint64
	snapshotTerm  uint64

	// commitWaiters are notified when commitIndex advances (ProposeAndWait).
	commitWaiters []chan uint64

	// proposeTimes records leader-side append timestamps so commit latency
	// can be observed when the entry commits (FR-030). Entries are deleted
	// as they commit or when leadership is lost.
	proposeTimes map[uint64]time.Time

	// metrics is an optional observability sink (FR-030); nil-safe.
	metrics MetricsSink

	// heartbeatRunning guards against stacking heartbeat loops across
	// rapid leader transitions (see startHeartbeatLoop).
	heartbeatRunning bool
}

// SetMetrics attaches an observability sink. Call before Start.
func (n *Node) SetMetrics(m MetricsSink) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.metrics = m
}

// reportStateLocked pushes role/term/commit gauges. Caller holds n.mu.
func (n *Node) reportStateLocked() {
	if n.metrics == nil {
		return
	}
	n.metrics.SetLeader(n.state == Leader)
	n.metrics.SetTerm(n.currentTerm)
	n.metrics.SetCommitIndex(n.commitIndex)
	n.metrics.SetAppliedIndex(n.lastApplied)
}

// NewNode constructs a node in follower state and restores durable state
// (hard state, snapshot, log tail) from storage.
func NewNode(id string, peers []string, clock transport.Clock,
	storage transport.Storage, fsm *FSM) *Node {

	n := &Node{
		id:                 id,
		peers:              peers,
		state:              Follower,
		clock:              clock,
		storage:            storage,
		fsm:                fsm,
		electionTimeoutMs:  150,
		heartbeatTimeoutMs: 50,
		nextIndex:          make(map[string]uint64),
		matchIndex:         make(map[string]uint64),
		proposeTimes:       make(map[uint64]time.Time),
		resetTimerCh:       make(chan struct{}, 1),
		shutdownCh:         make(chan struct{}),
	}
	n.restore()
	return n
}

// SetTransport attaches the outbound RPC transport. Must be called before
// Start for multi-node operation.
func (n *Node) SetTransport(t Transport) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.transport = t
}

// restore rebuilds in-memory state from durable storage. Correctness rule
// (FR-023): commitIndex is NEVER inferred from log length — uncommitted tail
// entries must not be applied. Only the persisted snapshot position and the
// committed prefix re-establish applied state.
func (n *Node) restore() {
	var hs transport.HardState
	if h, err := n.storage.LoadHardState(); err == nil {
		hs = h
		n.currentTerm = hs.Term
		n.votedFor = hs.VotedFor
	}

	// Restore latest snapshot if present (see snapshot.go for format).
	if idx, term, data, ok := loadLatestSnapshot(); ok && len(data) > 0 {
		if err := n.fsm.Restore(data); err == nil {
			n.snapshotIndex = idx
			n.snapshotTerm = term
			n.commitIndex = idx
			n.lastApplied = idx
			n.log = []LogEntry{{Index: idx, Term: term}}
		}
	}

	entries, err := n.storage.Scan(n.snapshotIndex + 1)
	if err == nil {
		if len(n.log) == 0 {
			n.log = []LogEntry{{Index: n.snapshotIndex, Term: n.snapshotTerm}}
		}
		for _, e := range entries {
			if e.Index <= n.snapshotIndex {
				continue
			}
			n.log = append(n.log, LogEntry{
				Index: e.Index, Term: e.Term,
				Type: raftpb.LogEntry_EntryType(e.Type), Data: e.Data,
			})
		}
	} else if len(n.log) == 0 {
		n.log = []LogEntry{{Index: n.snapshotIndex, Term: n.snapshotTerm}}
	}

	// Recover the committed prefix using the hard-state hint, capped by the
	// log tail. Never infer commitment from log length alone: tail entries
	// the leader never committed must not be applied (FR-023). The hint is
	// safe because it is persisted only after the commit is established.
	lastIdx := n.log[len(n.log)-1].Index
	if hint := min(hs.CommitIndex, lastIdx); hint > n.commitIndex {
		n.commitIndex = hint
	}
	n.applyCommitted()
}

func (n *Node) ID() string { return n.id }

func (n *Node) State() NodeState {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state
}

func (n *Node) LeaderID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.leaderID
}

func (n *Node) CurrentTerm() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.currentTerm
}

func (n *Node) LastLogIndex() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.log[len(n.log)-1].Index
}

func (n *Node) CommitIndex() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.commitIndex
}

func (n *Node) Peers() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	cp := make([]string, len(n.peers))
	copy(cp, n.peers)
	return cp
}

// persistHardStateLocked records term/vote durably. Caller holds n.mu.
func (n *Node) persistHardStateLocked() error {
	return n.storage.SaveHardState(transport.HardState{
		Term:        n.currentTerm,
		VotedFor:    n.votedFor,
		CommitIndex: n.commitIndex,
	})
}

// Start runs the node. A single-node cluster elects itself immediately.
func (n *Node) Start() {
	n.mu.Lock()
	if len(n.peers) <= 1 {
		n.state = Leader
		n.leaderID = n.id
		last := n.log[len(n.log)-1].Index
		// Single-voter no-op in the current term so any committed prefix
		// from an earlier term becomes committed here too (Figure-8 rule).
		noop := LogEntry{Index: last + 1, Term: n.currentTerm, Type: raftpb.LogEntry_COMMAND}
		n.log = append(n.log, noop)
		if err := n.storage.Append(transport.LogEntry{
			Index: noop.Index, Term: noop.Term, Type: byte(noop.Type),
		}); err != nil {
			n.log = n.log[:len(n.log)-1]
			n.state = Follower
			n.mu.Unlock()
			go n.electionTimerLoop()
			return
		}
		n.matchIndex[n.id] = noop.Index
		n.nextIndex[n.id] = noop.Index + 1
		// The lone voter's match counts as a quorum: commit everything now.
		n.updateCommitIndexLocked()
		n.persistCommitLocked()
		n.reportStateLocked()
		n.mu.Unlock()
		n.notifyCommitWaiters()
		n.startHeartbeatLoop()
		return
	}
	n.mu.Unlock()
	go n.electionTimerLoop()
}

func (n *Node) Stop() {
	n.stopOnce.Do(func() { close(n.shutdownCh) })
}

// resetElectionTimer restarts the election timeout without spawning a
// goroutine per reset (the v0.1 pattern leaked one goroutine per heartbeat).
func (n *Node) resetElectionTimer() {
	select {
	case n.resetTimerCh <- struct{}{}:
	default:
	}
}

// electionTimerLoop owns a single timer. Receiving on resetTimerCh restarts
// it; expiry signals an election attempt.
func (n *Node) electionTimerLoop() {
	timeout := n.randomElectionTimeout()
	for {
		select {
		case <-n.clock.After(time.Duration(timeout) * time.Millisecond):
			n.mu.Lock()
			if n.state != Leader {
				n.mu.Unlock()
				n.startElection()
			} else {
				n.mu.Unlock()
			}
			timeout = n.randomElectionTimeout()
		case <-n.resetTimerCh:
			timeout = n.randomElectionTimeout()
		case <-n.shutdownCh:
			return
		}
	}
}

func (n *Node) randomElectionTimeout() int {
	n.mu.Lock()
	base := n.electionTimeoutMs
	n.mu.Unlock()
	return base + rand.Intn(base)
}

// Propose appends a command to the leader's log and broadcasts it. Returns
// the log index. The command is NOT yet committed when this returns; use
// ProposeAndWait to block for commitment.
func (n *Node) Propose(data []byte) (uint64, error) {
	n.mu.Lock()
	if n.state != Leader {
		n.mu.Unlock()
		return 0, ErrNotLeader
	}
	entry := LogEntry{
		Index: n.log[len(n.log)-1].Index + 1,
		Term:  n.currentTerm,
		Type:  raftpb.LogEntry_COMMAND,
		Data:  data,
	}
	n.log = append(n.log, entry)
	n.proposeTimes[entry.Index] = time.Now()
	if err := n.storage.Append(transport.LogEntry{
		Index: entry.Index, Term: entry.Term,
		Type: byte(entry.Type), Data: entry.Data,
	}); err != nil {
		// Storage failure: drop the entry and surface it. The node must not
		// serve writes it cannot persist (docs/architecture/overview.md).
		n.log = n.log[:len(n.log)-1]
		delete(n.proposeTimes, entry.Index)
		n.mu.Unlock()
		return 0, err
	}
	n.matchIndex[n.id] = entry.Index
	n.nextIndex[n.id] = entry.Index + 1
	n.broadcastAppendEntriesLocked()
	n.updateCommitIndexLocked()
	committed := n.commitIndex >= entry.Index
	if committed {
		// Persist the commit hint so a restart re-applies this entry even
		// without hearing from a leader first.
		n.persistCommitLocked()
	}
	n.mu.Unlock()
	if committed {
		n.notifyCommitWaiters()
		n.CheckSnapshot()
	}
	return entry.Index, nil
}

// ProposeAndWait proposes and blocks until the entry is committed, the
// context expires, or leadership is lost. On context timeout it returns
// ErrNoQuorum — an uncertain outcome, not a rejection.
func (n *Node) ProposeAndWait(ctx context.Context, data []byte) (uint64, error) {
	idx, err := n.Propose(data)
	if err != nil {
		return 0, err
	}
	if n.CommitIndex() >= idx {
		return idx, nil
	}
	ch := make(chan uint64, 1)
	n.mu.Lock()
	n.commitWaiters = append(n.commitWaiters, ch)
	n.mu.Unlock()

	select {
	case committedIdx := <-ch:
		if committedIdx >= idx {
			return idx, nil
		}
		return 0, ErrNoQuorum
	case <-ctx.Done():
		// Remove waiter to avoid leak.
		n.mu.Lock()
		for i, w := range n.commitWaiters {
			if w == ch {
				n.commitWaiters = append(n.commitWaiters[:i], n.commitWaiters[i+1:]...)
				break
			}
		}
		n.mu.Unlock()
		return idx, ErrNoQuorum
	case <-n.shutdownCh:
		return idx, ErrNoQuorum
	}
}

func (n *Node) notifyCommitWaiters() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.commitWaiters) == 0 {
		return
	}
	ci := n.commitIndex
	remaining := n.commitWaiters[:0]
	for _, w := range n.commitWaiters {
		select {
		case w <- ci:
		default:
			remaining = append(remaining, w)
		}
	}
	n.commitWaiters = remaining
}

func (n *Node) GetCommitIndex() uint64 { return n.CommitIndex() }

func min(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
