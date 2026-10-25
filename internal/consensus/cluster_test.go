package consensus

// Multi-node integration tests using an in-process transport: real message
// flow through HandleVoteRequest / HandleAppendEntries, real timers, no
// network. These exercise the paths the unit tests bypass by calling
// handlers directly — the exact gap that let the v0.1 stubs pass CI while
// the cluster never actually elected or replicated.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/policy"
	"github.com/shadil/raftlite/internal/transport"
)

// localTransport routes RPCs directly to in-process nodes, with optional
// asymmetric partitions for failure injection.
type localTransport struct {
	mu      sync.RWMutex
	nodes   map[string]*Node
	blocked map[string]bool // "from|to"
}

func newLocalTransport() *localTransport {
	return &localTransport{
		nodes:   make(map[string]*Node),
		blocked: make(map[string]bool),
	}
}

func (t *localTransport) register(id string, n *Node) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nodes[id] = n
}

func (t *localTransport) unregister(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.nodes, id)
}

// setPartition blocks traffic in either direction between a and b.
func (t *localTransport) setPartition(a, b string, blocked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.blocked[a+"|"+b] = blocked
	t.blocked[b+"|"+a] = blocked
}

// route returns the target node or an error when partitioned/unknown.
func (t *localTransport) route(from, to string) (*Node, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.blocked[from+"|"+to] {
		return nil, fmt.Errorf("partitioned: %s -> %s", from, to)
	}
	n, ok := t.nodes[to]
	if !ok {
		return nil, fmt.Errorf("node %q unavailable", to)
	}
	return n, nil
}

func (t *localTransport) SendRequestVote(ctx context.Context, to string, req *raftpb.RequestVoteRequest) (*raftpb.RequestVoteResponse, error) {
	n, err := t.route(string(req.CandidateId), to)
	if err != nil {
		return nil, err
	}
	return n.HandleVoteRequest(req), nil
}

func (t *localTransport) SendAppendEntries(ctx context.Context, to string, req *raftpb.AppendEntriesRequest) (*raftpb.AppendEntriesResponse, error) {
	n, err := t.route(string(req.LeaderId), to)
	if err != nil {
		return nil, err
	}
	return n.HandleAppendEntries(req), nil
}

func (t *localTransport) SendInstallSnapshot(ctx context.Context, to string, req *raftpb.InstallSnapshotRequest) (*raftpb.InstallSnapshotResponse, error) {
	n, err := t.route(string(req.LeaderId), to)
	if err != nil {
		return nil, err
	}
	return n.HandleInstallSnapshot(req), nil
}

// testCluster builds and starts an in-process N-node cluster.
type testCluster struct {
	t     *testing.T
	tr    *localTransport
	nodes map[string]*Node
	fsms  map[string]*FSM
	ids   []string
}

func newTestCluster(t *testing.T, n int) *testCluster {
	t.Helper()
	tr := newLocalTransport()
	c := &testCluster{
		t:     t,
		tr:    tr,
		nodes: make(map[string]*Node),
		fsms:  make(map[string]*FSM),
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("node%d", i+1)
		c.ids = append(c.ids, id)
	}
	for _, id := range c.ids {
		fsm := NewFSM()
		node := NewNode(id, c.ids, transport.RealClock{}, transport.NewMockStorage(), fsm)
		node.electionTimeoutMs = 100 // speed up tests; randomized below
		node.SetTransport(tr)
		tr.register(id, node)
		c.nodes[id] = node
		c.fsms[id] = fsm
		node.Start()
	}
	t.Cleanup(func() {
		for _, node := range c.nodes {
			node.Stop()
		}
	})
	return c
}

// waitLeader polls until exactly one leader exists (or fails the test).
func (c *testCluster) waitLeader(timeout time.Duration) *Node {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var leaders []*Node
		for _, n := range c.nodes {
			if n.State() == Leader {
				leaders = append(leaders, n)
			}
		}
		if len(leaders) == 1 {
			return leaders[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.t.Fatalf("no single leader within %v", timeout)
	return nil
}

// waitApplied polls until every node's FSM reflects the given revision.
func (c *testCluster) waitApplied(revision uint64, timeout time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok := true
		for _, fsm := range c.fsms {
			if fsm.Revision() < revision {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.t.Fatalf("cluster did not apply revision %d within %v", revision, timeout)
}

func createBlockCommand(t *testing.T, ip string) []byte {
	t.Helper()
	id, err := policy.NewID()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(policy.Command{
		Op: policy.OpCreate, ID: id, Type: policy.TypeIPBlock,
		Target: ip, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestClusterElectsOneLeader(t *testing.T) {
	c := newTestCluster(t, 3)
	leader := c.waitLeader(5 * time.Second)
	if leader.CurrentTerm() == 0 {
		t.Fatal("expected a non-zero term after election")
	}
}

func TestClusterReplicatesAndApplies(t *testing.T) {
	c := newTestCluster(t, 3)
	leader := c.waitLeader(5 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	idx, err := leader.ProposeAndWait(ctx, createBlockCommand(t, "10.1.2.3"))
	if err != nil {
		t.Fatalf("ProposeAndWait: %v", err)
	}
	if idx == 0 {
		t.Fatal("expected non-zero index")
	}

	// Every node — leader and followers — must apply the committed entry.
	c.waitApplied(idx, 5*time.Second)
	for id, fsm := range c.fsms {
		if !fsm.IsBlocked("10.1.2.3") {
			t.Fatalf("%s did not apply the block", id)
		}
	}
}

func TestClusterLeaderFailover(t *testing.T) {
	c := newTestCluster(t, 3)
	oldLeader := c.waitLeader(5 * time.Second)
	oldID := oldLeader.ID()

	// Isolate the leader; the remaining two must elect a new one and keep
	// serving writes (NFR-004: majority side continues).
	for _, other := range c.ids {
		if other != oldID {
			c.tr.setPartition(oldID, other, true)
		}
	}
	oldLeader.Stop()
	c.tr.unregister(oldID)

	deadline := time.Now().Add(5 * time.Second)
	var newLeader *Node
	for time.Now().Before(deadline) {
		for id, n := range c.nodes {
			if id == oldID {
				continue
			}
			if n.State() == Leader {
				newLeader = n
				break
			}
		}
		if newLeader != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if newLeader == nil {
		t.Fatal("no new leader after isolating the old one")
	}
	if newLeader.ID() == oldID {
		t.Fatal("isolated node was elected — partition not enforced")
	}

	// The majority side must still commit.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := newLeader.ProposeAndWait(ctx, createBlockCommand(t, "10.9.9.9")); err != nil {
		t.Fatalf("write on majority side failed: %v", err)
	}
}

func TestClusterMinorityCannotCommit(t *testing.T) {
	c := newTestCluster(t, 3)
	leader := c.waitLeader(5 * time.Second)
	leaderID := leader.ID()

	// Cut the leader off from BOTH followers: it now commands a minority.
	var followers []string
	for _, id := range c.ids {
		if id != leaderID {
			followers = append(followers, id)
			c.tr.setPartition(leaderID, id, true)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := leader.ProposeAndWait(ctx, createBlockCommand(t, "10.5.5.5"))
	if err == nil {
		t.Fatal("minority leader reported a committed write — safety violation")
	}
	if err != ErrNoQuorum {
		t.Fatalf("expected ErrNoQuorum (uncertain outcome), got %v", err)
	}

	// The two followers must converge without the old leader.
	var newLeader *Node
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && newLeader == nil {
		for _, id := range followers {
			if c.nodes[id].State() == Leader {
				newLeader = c.nodes[id]
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if newLeader == nil {
		t.Fatal("followers failed to elect a leader among themselves")
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if _, err := newLeader.ProposeAndWait(ctx2, createBlockCommand(t, "10.6.6.6")); err != nil {
		t.Fatalf("majority-side write failed: %v", err)
	}
}

func TestClusterRestartRecovery(t *testing.T) {
	// One node with shared storage: commit, stop, rebuild, verify applied
	// state and that the uncommitted tail (if any) is not applied (FR-023).
	storage := transport.NewMockStorage()
	fsm := NewFSM()
	node := NewNode("solo", []string{"solo"}, transport.RealClock{}, storage, fsm)
	node.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := node.ProposeAndWait(ctx, createBlockCommand(t, "10.20.30.40")); err != nil {
		t.Fatalf("propose: %v", err)
	}
	node.Stop()

	// Append an UNCOMMITTED entry directly to storage — this models a
	// leader crash after appending but before committing. It must not be
	// applied after restart.
	if err := storage.Append(transport.LogEntry{
		Index: 3, Term: 5, Type: 0,
		Data: createBlockCommand(t, "10.99.99.99"),
	}); err != nil {
		t.Fatal(err)
	}

	fsm2 := NewFSM()
	node2 := NewNode("solo", []string{"solo"}, transport.RealClock{}, storage, fsm2)

	if !fsm2.IsBlocked("10.20.30.40") {
		t.Fatal("committed entry lost across restart")
	}
	if fsm2.IsBlocked("10.99.99.99") {
		t.Fatal("uncommitted tail applied after restart — FR-023 violation")
	}
	node2.Stop()
}
