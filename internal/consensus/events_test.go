package consensus

import (
	"testing"
	"time"
)

func TestEventLogBoundedAndNewestFirst(t *testing.T) {
	l := newEventLog(3)
	for i := 1; i <= 5; i++ {
		l.record("n1", uint64(i), EvElectionStarted, "campaigning")
	}
	all := l.recent(100)
	if len(all) != 3 {
		t.Fatalf("bounded log should hold 3 events, got %d", len(all))
	}
	// Newest first.
	if all[0].Term != 5 || all[2].Term != 3 {
		t.Fatalf("expected newest-first terms [5 4 3], got [%d %d %d]",
			all[0].Term, all[1].Term, all[2].Term)
	}
	// Limit respected.
	if got := l.recent(2); len(got) != 2 {
		t.Fatalf("limit=2 should return 2 events, got %d", len(got))
	}
	// Sequence IDs stay unique across eviction.
	if all[0].ID == all[1].ID || all[1].ID == all[2].ID {
		t.Fatal("event IDs must be unique")
	}
}

// TestClusterRaftEvents drives a real 3-node in-process cluster (RealClock)
// through one election and asserts the Raft-conscious event log records it:
// the leader records leader_elected, followers record leader_observed.
func TestClusterRaftEvents(t *testing.T) {
	c := NewInProcessCluster(t, 3)
	leader := c.WaitLeader(5 * time.Second)
	if leader == nil {
		t.Fatal("no leader elected")
	}

	// The winner must record its own election.
	if !hasEvent(leader, EvLeaderElected) {
		t.Fatalf("leader %s has no %s event: %+v",
			leader.ID(), EvLeaderElected, leader.RaftEvents(20))
	}
	if !hasEvent(leader, EvElectionStarted) {
		t.Fatalf("leader %s has no %s event", leader.ID(), EvElectionStarted)
	}

	// Followers learn the leader from AppendEntries within a few heartbeats.
	deadline := time.Now().Add(3 * time.Second)
	for _, id := range c.IDs {
		n := c.Nodes[id]
		if n == leader {
			continue
		}
		for time.Now().Before(deadline) {
			if hasEvent(n, EvLeaderObserved) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !hasEvent(n, EvLeaderObserved) {
			t.Fatalf("follower %s recorded no %s event: %+v",
				id, EvLeaderObserved, n.RaftEvents(20))
		}
	}
}

func hasEvent(n *Node, typ string) bool {
	for _, e := range n.RaftEvents(100) {
		if e.Type == typ {
			return true
		}
	}
	return false
}
