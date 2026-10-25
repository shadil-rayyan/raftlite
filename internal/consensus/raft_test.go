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

func TestMembershipAddAndRemoveVoter(t *testing.T) {
	// Single-node leader: quorum is 1, so the first config change commits
	// under the OLD config (correct Raft: the change is committed by the
	// configuration that existed when it was proposed).
	n := newNodeForTest("node1", []string{"node1"})
	n.Start()
	if n.State() != Leader {
		t.Fatal("single node should be leader")
	}

	// ack simulates the (nonexistent) node2 confirming replication, which
	// real deployments get from the joining node itself.
	ack := func(peer string) {
		n.HandleAppendEntriesResponse(peer, n.LastLogIndex(),
			&raftpb.AppendEntriesResponse{Term: n.CurrentTerm(), Success: true})
	}

	idx, err := n.ProposeAddVoter("node2")
	if err != nil {
		t.Fatalf("ProposeAddVoter: %v", err)
	}
	if idx == 0 {
		t.Fatal("expected non-zero index")
	}
	peers := n.Peers()
	if len(peers) != 2 || peers[1] != "node2" {
		t.Fatalf("expected peers [node1 node2], got %v", peers)
	}

	// Adding an existing member must be rejected before touching the log.
	if _, err := n.ProposeAddVoter("node2"); err == nil {
		t.Fatal("expected error adding duplicate voter")
	}

	// The config is now [node1 node2], so quorum is 2. The removal entry
	// cannot commit until node2 acks — this is why membership changes must
	// be driven against a live joining node in production.
	if _, err := n.ProposeRemoveVoter("node2"); err != nil && err != ErrNoQuorum {
		t.Fatalf("ProposeRemoveVoter: %v", err)
	}
	ack("node2")
	peers = n.Peers()
	if len(peers) != 1 || peers[0] != "node1" {
		t.Fatalf("expected peers [node1] after removal commits, got %v", peers)
	}

	// Removing the last voter must be refused.
	if _, err := n.ProposeRemoveVoter("node1"); err == nil {
		t.Fatal("expected error removing last voter")
	}
	n.Stop()
}

func TestMembershipRequiresLeadership(t *testing.T) {
	n := newNodeForTest("node1", []string{"node1", "node2", "node3"})
	if _, err := n.ProposeAddVoter("node4"); err != ErrNotLeader {
		t.Fatalf("expected ErrNotLeader, got %v", err)
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
