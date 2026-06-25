package consensus

import (
	"errors"
	"math/rand"
	"sync"
	"time"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

var errNotLeader = errors.New("not the leader")

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

type Node struct {
	mu sync.Mutex

	id       string
	peers    []string
	state    NodeState

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

	clock   transport.Clock
	storage transport.Storage
	fsm     *FSM

	electionTimer chan struct{}
	shutdownCh    chan struct{}
}

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
		electionTimer:      make(chan struct{}, 1),
		shutdownCh:         make(chan struct{}),
	}
	n.restoreFromStorage()
	return n
}

func (n *Node) restoreFromStorage() {
	entries, err := n.storage.Scan(1)
	if err != nil || len(entries) == 0 {
		n.log = []LogEntry{{Index: 0, Term: 0}}
		return
	}
	n.log = make([]LogEntry, 0, len(entries)+1)
	n.log = append(n.log, LogEntry{Index: 0, Term: 0})
	for _, e := range entries {
		le := LogEntry{
			Index: e.Index, Term: e.Term,
			Type: raftpb.LogEntry_EntryType(e.Type), Data: e.Data,
		}
		n.log = append(n.log, le)
	}
	n.commitIndex = uint64(len(n.log) - 1)
	n.currentTerm, _ = n.storage.LastTerm()
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

func (n *Node) Peers() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	cp := make([]string, len(n.peers))
	copy(cp, n.peers)
	return cp
}

func (n *Node) Start() {
	if len(n.peers) <= 1 {
		n.state = Leader
		n.leaderID = n.id
		go n.heartbeatLoop()
		return
	}
	go n.electionLoop()
	n.resetElectionTimer()
}

func (n *Node) Stop() {
	close(n.shutdownCh)
}

func (n *Node) resetElectionTimer() {
	timeout := n.electionTimeoutMs + rand.Intn(n.electionTimeoutMs)
	go func() {
		select {
		case <-n.clock.After(time.Duration(timeout) * time.Millisecond):
			select {
			case n.electionTimer <- struct{}{}:
			default:
			}
		case <-n.shutdownCh:
		}
	}()
}

func (n *Node) electionLoop() {
	for {
		select {
		case <-n.electionTimer:
			n.mu.Lock()
			if n.state != Leader {
				n.startElection()
			}
			n.mu.Unlock()
		case <-n.shutdownCh:
			return
		}
	}
}

func (n *Node) Propose(data []byte) (uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state != Leader {
		return 0, errNotLeader
	}
	entry := LogEntry{
		Index: n.log[len(n.log)-1].Index + 1,
		Term:  n.currentTerm,
		Type:  raftpb.LogEntry_COMMAND,
		Data:  data,
	}
	n.log = append(n.log, entry)
	if err := n.storage.Append(transport.LogEntry{
		Index: entry.Index, Term: entry.Term,
		Type: byte(entry.Type), Data: entry.Data,
	}); err != nil {
		return 0, err
	}
	n.matchIndex[n.id] = entry.Index
	n.nextIndex[n.id] = entry.Index + 1
	n.broadcastAppendEntries()
	n.updateCommitIndex()
	return entry.Index, nil
}

func (n *Node) ProposeConfig(configData []byte) (uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state != Leader {
		return 0, errNotLeader
	}
	entry := LogEntry{
		Index: n.log[len(n.log)-1].Index + 1,
		Term:  n.currentTerm,
		Type:  raftpb.LogEntry_CONFIG,
		Data:  configData,
	}
	n.log = append(n.log, entry)
	if err := n.storage.Append(transport.LogEntry{
		Index: entry.Index, Term: entry.Term,
		Type: byte(entry.Type), Data: entry.Data,
	}); err != nil {
		return 0, err
	}
	n.broadcastAppendEntries()
	return entry.Index, nil
}

func (n *Node) GetCommitIndex() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.commitIndex
}

func joinConfigStrings(peers []string) string {
	b := make([]byte, 0, 1024)
	for i, p := range peers {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, p...)
	}
	return string(b)
}

func splitConfigStrings(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
