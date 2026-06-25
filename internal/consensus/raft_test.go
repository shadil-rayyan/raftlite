package consensus

import (
	"testing"
	"time"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

func timeNow() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func newNodeForTest(id string, peers []string) *Node {
	clock := transport.NewMockClock(timeNow())
	fsm := NewFSM()
	return NewNode(id, peers, clock, transport.NewMockStorage(), fsm)
}

func TestNewNodeFollower(t *testing.T) {
	n := newNodeForTest("node1", []string{"node1", "node2", "node3"})
	if n.State() != Follower {
		t.Fatalf("expected Follower, got %v", n.State())
	}
}

func TestSingleNodeElection(t *testing.T) {
	n := newNodeForTest("node1", []string{"node1"})
	n.Start()
	// single node should become leader immediately
	if n.State() != Leader {
		t.Fatalf("expected Leader for single node, got %v", n.State())
	}
	n.Stop()
}

func TestProposeToLeader(t *testing.T) {
	n := newNodeForTest("node1", []string{"node1"})
	n.Start()
	_, err := n.Propose([]byte("add:1.2.3.4"))
	if err != nil {
		t.Fatalf("Propose failed: %v", err)
	}
	if !n.fsm.IsBlocked("1.2.3.4") {
		t.Fatal("expected block after commit")
	}
	n.Stop()
}

func TestHandleVoteRequest(t *testing.T) {
	n1 := newNodeForTest("node1", []string{"node1", "node2"})
	n2 := newNodeForTest("node2", []string{"node1", "node2"})

	req := &raftpb.RequestVoteRequest{
		Term:         1,
		CandidateId:  []byte("node2"),
		LastLogIndex: 0,
		LastLogTerm:  0,
	}
	resp := n1.HandleVoteRequest(req)
	if !resp.VoteGranted {
		t.Fatal("node1 should grant vote to node2")
	}

	// second vote for same candidate same term should be granted (idempotent)
	resp2 := n1.HandleVoteRequest(req)
	if !resp2.VoteGranted {
		t.Fatal("node1 should grant same candidate again")
	}

	// different candidate for same term should be rejected
	req3 := &raftpb.RequestVoteRequest{
		Term:        1,
		CandidateId: []byte("node3"),
		LastLogIndex: 0,
		LastLogTerm:  0,
	}
	resp3 := n1.HandleVoteRequest(req3)
	if resp3.VoteGranted {
		t.Fatal("node1 should not vote for different candidate in same term")
	}
	_ = n2
}

func TestLogReplication(t *testing.T) {
	leader := newNodeForTest("leader", []string{"leader", "follower"})
	leader.state = Leader
	leader.currentTerm = 1

	follower := newNodeForTest("follower", []string{"leader", "follower"})

	_, err := leader.Propose([]byte("add:10.0.0.1"))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	// replicate
	entries := leader.log[1:] // skip the zero entry
	for _, entry := range entries {
		req := &raftpb.AppendEntriesRequest{
			Term:         leader.currentTerm,
			LeaderId:     []byte(leader.id),
			PrevLogIndex: entry.Index - 1,
			PrevLogTerm:  leader.log[entry.Index-1].Term,
			Entries:      []*raftpb.LogEntry{{Index: entry.Index, Term: entry.Term, Type: entry.Type, Data: entry.Data}},
			LeaderCommit: leader.commitIndex,
		}
		resp := follower.HandleAppendEntries(req)
		if !resp.Success {
			t.Fatalf("AppendEntries failed: conflict idx=%d term=%d", resp.ConflictIndex, resp.ConflictTerm)
		}
	}

	if follower.log[len(follower.log)-1].Index != 1 {
		t.Fatalf("expected log index 1 on follower, got %d", follower.log[len(follower.log)-1].Index)
	}
}

func TestJointConfig(t *testing.T) {
	n := newNodeForTest("node1", []string{"node1", "node2", "node3"})
	n.state = Leader
	n.currentTerm = 1
	n.matchIndex = map[string]uint64{"node1": 0, "node2": 0, "node3": 0}
	n.nextIndex = map[string]uint64{"node1": 1, "node2": 1, "node3": 1}
	for _, p := range n.peers {
		n.nextIndex[p] = 1
		n.matchIndex[p] = 0
	}

	_, err := n.ProposeJointConfig([]string{"node1", "node2", "node3", "node4"})
	if err != nil {
		t.Fatalf("ProposeJointConfig: %v", err)
	}

	if n.IsInJointConfig() {
		t.Fatal("should not be in joint config after transition")
	}
	if len(n.peers) != 4 {
		t.Fatalf("expected 4 peers, got %d", len(n.peers))
	}
}

func TestDSTDeterministicElection(t *testing.T) {
	cfg := transport.SimulationConfig{
		Seed:     42,
		Nodes:    3,
		MaxSteps: 10,
	}
	h := transport.NewSimulation(cfg)

	nodes := make([]*Node, 3)
	for i := 0; i < 3; i++ {
		id := string(rune('A' + i))
		peers := []string{string(rune('A')), string(rune('B')), string(rune('C'))}
		nodes[i] = NewNode(id, peers, h.Clock(i), h.Storage(i), NewFSM())
		nodes[i].Start()
	}

	for h.Step() {
		t.Logf("step %d", h.StepNum())
		for i, n := range nodes {
			if !h.NodeAlive(i) {
				continue
			}
			_ = n.State()
		}
	}

	for _, n := range nodes {
		n.Stop()
	}
}
