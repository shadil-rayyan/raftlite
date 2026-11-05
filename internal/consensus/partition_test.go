package consensus

// Crash and partition scenarios for Milestone 1: a partitioned minority
// leader must never confirm commits, the majority side must keep serving
// writes, and healing must converge everyone onto the majority's
// committed history without losing committed entries.

import (
	"context"
	"testing"
	"time"
)

// A leader isolated from the majority must not confirm commits; the
// majority side elects a new leader and keeps writing; on heal, the old
// leader steps down, discards its uncommitted write, and adopts the
// majority's committed state.
func TestPartitionIsolatesLeaderFromMajority(t *testing.T) {
	c := NewInProcessCluster(t, 3)
	leader := c.WaitLeader(3 * time.Second)

	if _, err := c.ProposeAndWaitTo(context.Background(), leader, []byte("add:1.1.1.1")); err != nil {
		t.Fatalf("propose before partition: %v", err)
	}
	c.WaitApplied(1, 3*time.Second)

	c.Isolate(leader.ID())

	// Minority side: the commit must not be confirmed.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, err := c.ProposeAndWaitTo(ctx, leader, []byte("add:2.2.2.2")); err == nil {
		t.Fatal("minority leader confirmed a commit without quorum")
	}

	// Majority side: new leader, writes keep flowing.
	majority := c.WaitNewLeader(leader.ID(), 3*time.Second)
	if _, err := c.ProposeAndWaitTo(context.Background(), majority, []byte("add:3.3.3.3")); err != nil {
		t.Fatalf("majority propose: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.FSMs[majority.ID()].IsBlocked("3.3.3.3") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !c.FSMs[majority.ID()].IsBlocked("3.3.3.3") {
		t.Fatal("majority did not apply the post-partition write")
	}

	// Heal: the old leader converges onto the majority history — the
	// committed write is applied and the node is a follower.
	c.Heal()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.FSMs[leader.ID()].IsBlocked("3.3.3.3") && leader.State() == Follower {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !c.FSMs[leader.ID()].IsBlocked("3.3.3.3") {
		t.Fatal("old leader did not adopt the majority's committed write")
	}
	if leader.State() != Follower {
		t.Fatalf("old leader did not step down after heal, state=%v", leader.State())
	}
	// The committed pre-partition entry survived everywhere.
	for id, fsm := range c.FSMs {
		if !fsm.IsBlocked("1.1.1.1") {
			t.Fatalf("committed entry lost on %s", id)
		}
	}
}

// A crashed leader (isolated + stopped) must not take its committed
// history down with it: the majority elects a successor and keeps
// committing on top of the surviving log.
func TestLeaderCrashFailoverKeepsCommittedEntries(t *testing.T) {
	c := NewInProcessCluster(t, 3)
	leader := c.WaitLeader(3 * time.Second)

	if _, err := c.ProposeAndWaitTo(context.Background(), leader, []byte("add:5.5.5.5")); err != nil {
		t.Fatalf("propose before crash: %v", err)
	}
	c.WaitApplied(1, 3*time.Second) // committed on every node, including the doomed leader

	// Crash model: unreachable in both directions, then stopped.
	c.Isolate(leader.ID())
	leader.Stop()

	successor := c.WaitNewLeader(leader.ID(), 3*time.Second)
	t.Logf("crashed=%s successor=%s", leader.ID(), successor.ID())
	if _, err := c.ProposeAndWaitTo(context.Background(), successor, []byte("add:6.6.6.6")); err != nil {
		t.Fatalf("propose after failover: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for _, id := range c.IDs {
			if id == leader.ID() {
				continue
			}
			if !c.FSMs[id].IsBlocked("6.6.6.6") {
				ok = false
				break
			}
		}
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, id := range c.IDs {
		n := c.Nodes[id]
		n.mu.Lock()
		applied := n.lastApplied
		var tail uint64
		if len(n.log) > 0 {
			tail = n.log[len(n.log)-1].Index
		}
		n.mu.Unlock()
		fsm := c.FSMs[id]
		t.Logf("node %s: state=%v term=%d commit=%d applied=%d logtail=%d rev=%d blocked5=%v blocked6=%v",
			id, n.State(), n.CurrentTerm(), n.CommitIndex(), applied, tail,
			fsm.Revision(), fsm.IsBlocked("5.5.5.5"), fsm.IsBlocked("6.6.6.6"))
	}
	for _, id := range c.IDs {
		if id == leader.ID() {
			continue
		}
		fsm := c.FSMs[id]
		n := c.Nodes[id]
		if !fsm.IsBlocked("5.5.5.5") {
			t.Fatalf("pre-crash committed entry lost on %s", id)
		}
		if !fsm.IsBlocked("6.6.6.6") {
			n.mu.Lock()
			applied := n.lastApplied
			n.mu.Unlock()
			t.Fatalf("post-failover entry not applied on %s: revision=%d state=%v term=%d commit=%d applied=%d logtail=%d",
				id, fsm.Revision(), n.State(), n.CurrentTerm(), n.CommitIndex(), applied, n.LastLogIndex())
		}
	}
}
