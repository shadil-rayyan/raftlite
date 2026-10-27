package consensus

import (
	"context"
	"testing"
	"time"
)

func TestClusterUIFlow(t *testing.T) {
	c := newTestCluster(t, 3)
	leader := c.waitLeader(5 * time.Second)
	if leader.CurrentTerm() == 0 {
		t.Fatal("expected non-zero term after election")
	}
	// Single-node no-op rule generalizes: leader's first entry in its term
	// is the no-op at index 1 on a fresh cluster.
	if leader.LastLogIndex() < 1 {
		t.Fatalf("expected no-op entry, got lastLog=%d", leader.LastLogIndex())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	idx, err := leader.ProposeAndWait(ctx, createBlockCommand(t, "10.0.0.5"))
	if err != nil {
		t.Fatalf("ProposeAndWait: %v", err)
	}
	if idx <= 1 {
		t.Fatalf("expected data entry after no-op, got idx=%d", idx)
	}
	c.waitApplied(idx, 3*time.Second)

	for _, id := range c.ids {
		if !c.fsms[id].IsBlocked("10.0.0.5") {
			t.Fatalf("node %s did not apply block", id)
		}
		if c.nodes[id].CommitIndex() < idx {
			t.Fatalf("node %s commit=%d want>=%d", id, c.nodes[id].CommitIndex(), idx)
		}
	}

	// Diagnostics-style quorum check: majority of nodes have the committed log.
	majority := 0
	for _, id := range c.ids {
		if c.nodes[id].LastLogIndex() >= idx {
			majority++
		}
	}
	if majority < len(c.ids)/2+1 {
		t.Fatalf("majority_has_log=%d, want quorum", majority)
	}
}
