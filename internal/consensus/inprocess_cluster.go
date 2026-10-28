package consensus

import (
	"context"
	"fmt"
	"testing"
	"time"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

// InProcessTransport routes Raft RPCs directly between in-process nodes.
type InProcessTransport struct {
	nodes map[string]*Node
}

// NewInProcessTransport creates an empty transport; Register nodes before calls.
func NewInProcessTransport() *InProcessTransport {
	return &InProcessTransport{nodes: make(map[string]*Node)}
}

// Register wires a node into the transport.
func (t *InProcessTransport) Register(id string, n *Node) {
	t.nodes[id] = n
}

func (t *InProcessTransport) route(from, to string) (*Node, error) {
	n, ok := t.nodes[to]
	if !ok {
		return nil, fmt.Errorf("node %q unavailable", to)
	}
	return n, nil
}

func (t *InProcessTransport) SendRequestVote(ctx context.Context, to string, req *raftpb.RequestVoteRequest) (*raftpb.RequestVoteResponse, error) {
	n, err := t.route(string(req.CandidateId), to)
	if err != nil {
		return nil, err
	}
	return n.HandleVoteRequest(req), nil
}

func (t *InProcessTransport) SendAppendEntries(ctx context.Context, to string, req *raftpb.AppendEntriesRequest) (*raftpb.AppendEntriesResponse, error) {
	n, err := t.route(string(req.LeaderId), to)
	if err != nil {
		return nil, err
	}
	return n.HandleAppendEntries(req), nil
}

func (t *InProcessTransport) SendInstallSnapshot(ctx context.Context, to string, req *raftpb.InstallSnapshotRequest) (*raftpb.InstallSnapshotResponse, error) {
	n, err := t.route(string(req.LeaderId), to)
	if err != nil {
		return nil, err
	}
	return n.HandleInstallSnapshot(req), nil
}

// InProcessCluster is an N-node cluster with real message flow, no network.
type InProcessCluster struct {
	t     *testing.T
	IDs   []string
	Nodes map[string]*Node
	FSMs  map[string]*FSM
}

// NewInProcessCluster builds and starts n consensus nodes.
func NewInProcessCluster(t *testing.T, n int) *InProcessCluster {
	t.Helper()
	tr := NewInProcessTransport()
	c := &InProcessCluster{t: t, Nodes: make(map[string]*Node), FSMs: make(map[string]*FSM)}
	for i := 0; i < n; i++ {
		c.IDs = append(c.IDs, fmt.Sprintf("node%d", i+1))
	}
	for _, id := range c.IDs {
		fsm := NewFSM()
		node := NewNode(id, c.IDs, transport.RealClock{}, transport.NewMockStorage(), fsm)
		node.electionTimeoutMs = 100
		node.SetTransport(tr)
		tr.Register(id, node)
		c.Nodes[id] = node
		c.FSMs[id] = fsm
		node.Start()
	}
	t.Cleanup(func() {
		for _, node := range c.Nodes {
			node.Stop()
		}
	})
	return c
}

// WaitLeader polls until exactly one leader exists.
func (c *InProcessCluster) WaitLeader(timeout time.Duration) *Node {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var leaders []*Node
		for _, n := range c.Nodes {
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

// WaitApplied polls until every node's FSM reflects the given revision.
func (c *InProcessCluster) WaitApplied(revision uint64, timeout time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok := true
		for _, fsm := range c.FSMs {
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

// ProposeAndWaitTo commits data through the leader with a bounded context.
func (c *InProcessCluster) ProposeAndWaitTo(ctx context.Context, leader *Node, data []byte) (uint64, error) {
	return leader.ProposeAndWait(ctx, data)
}
