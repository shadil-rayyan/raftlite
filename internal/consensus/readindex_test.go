package consensus

// Validation for the ReadIndex leadership-confirmation rounds (Phase 4
// scaffolding). The API read gate is not wired yet; these tests prove the
// consensus primitive itself: a healthy leader confirms a read barrier
// with a quorum heartbeat round, and an isolated leader cannot.

import (
	"context"
	"testing"
	"time"
)

func TestReadIndexConfirmsLeadership(t *testing.T) {
	c := NewInProcessCluster(t, 3)
	leader := c.WaitLeader(3 * time.Second)

	// Commit an entry so the barrier covers real state.
	if _, err := c.ProposeAndWaitTo(context.Background(), leader, []byte("add:10.9.8.7")); err != nil {
		t.Fatalf("propose: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	idx, err := leader.ReadIndex(ctx)
	if err != nil {
		t.Fatalf("ReadIndex on healthy leader: %v", err)
	}
	if idx == 0 {
		t.Fatal("expected a non-zero read barrier")
	}
	// The barrier must cover the committed entry; the FSM must reflect it.
	if !c.FSMs[leader.ID()].IsBlocked("10.9.8.7") {
		t.Fatalf("committed entry not applied at read barrier %d", idx)
	}
}

func TestReadIndexFailsWhenIsolated(t *testing.T) {
	c := NewInProcessCluster(t, 3)
	leader := c.WaitLeader(3 * time.Second)

	// The leader stays in Leader state while isolated (no self-stepdown),
	// so it can accept ReadIndex — but it must never confirm one: no
	// quorum heartbeat round can complete.
	c.Isolate(leader.ID())

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, err := leader.ReadIndex(ctx); err == nil {
		t.Fatal("isolated leader confirmed a read without quorum")
	}

	// The majority side elects a successor while the old leader starves.
	successor := c.WaitNewLeader(leader.ID(), 3*time.Second)

	// Heal: the old leader steps down on the first higher-term heartbeat;
	// the successor serves reads normally.
	c.Heal()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if _, err := successor.ReadIndex(ctx2); err != nil {
		t.Fatalf("ReadIndex on successor after heal: %v", err)
	}
	if _, err := leader.ReadIndex(ctx2); err == nil {
		t.Fatal("deposed leader confirmed a read")
	}
}
