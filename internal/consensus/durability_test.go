package consensus

// Durability tests for Milestone 1 (Reliability): a follower never
// acknowledges an entry it failed to persist, a node never grants a vote
// without durable term/vote state, snapshot persistence gates log
// compaction, and storage failures are visible to operators instead of
// silently swallowed.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	raftpb "github.com/shadil/raftlite/api/go/raftpb"
	"github.com/shadil/raftlite/internal/transport"
)

// faultyStorage wraps MockStorage with injectable failures for the three
// durability surfaces the consensus layer depends on.
type faultyStorage struct {
	*transport.MockStorage
	appendErr    error
	truncateErr  error
	hardStateErr error
}

func (f *faultyStorage) Append(entry transport.LogEntry) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	return f.MockStorage.Append(entry)
}

func (f *faultyStorage) Truncate(toIndex uint64) error {
	if f.truncateErr != nil {
		return f.truncateErr
	}
	return f.MockStorage.Truncate(toIndex)
}

func (f *faultyStorage) SaveHardState(hs transport.HardState) error {
	if f.hardStateErr != nil {
		return f.hardStateErr
	}
	return f.MockStorage.SaveHardState(hs)
}

// A follower that cannot persist an entry must respond Success=false (with
// no conflict hints, so the leader waits for a heartbeat instead of
// hot-looping), must not advance commitIndex or apply, and must not keep
// the entry in memory — memory and disk stay in lockstep.
func TestFollowerNACKsWhenAppendPersistenceFails(t *testing.T) {
	follower := newNodeForTest("follower", []string{"leader", "follower"})
	fs := &faultyStorage{MockStorage: transport.NewMockStorage()}
	follower.storage = fs
	fs.appendErr = errors.New("disk full")

	req := &raftpb.AppendEntriesRequest{
		Term:         1,
		LeaderId:     []byte("leader"),
		PrevLogIndex: 0,
		PrevLogTerm:  0,
		Entries: []*raftpb.LogEntry{
			{Index: 1, Term: 1, Type: raftpb.LogEntry_COMMAND, Data: []byte("add:1.2.3.4")},
		},
		LeaderCommit: 5, // must not advance off a failed append
	}
	resp := follower.HandleAppendEntries(req)
	if resp.Success {
		t.Fatal("follower acknowledged an entry it failed to persist")
	}
	if resp.ConflictIndex != 0 || resp.ConflictTerm != 0 {
		t.Fatal("storage-failure NACK must carry no conflict hints")
	}
	if len(follower.log) != 1 {
		t.Fatalf("in-memory log mutated despite failed append: %d entries", len(follower.log))
	}
	if follower.commitIndex != 0 {
		t.Fatalf("commitIndex advanced despite failed append: %d", follower.commitIndex)
	}
	if follower.StorageHealthy() {
		t.Fatal("storage failure not flagged")
	}
	if !hasEvent(follower, EvStorageError) {
		t.Fatal("storage failure not surfaced as a raft event")
	}

	// Once the disk recovers the same request must succeed and health
	// must clear.
	fs.appendErr = nil
	resp2 := follower.HandleAppendEntries(req)
	if !resp2.Success {
		t.Fatalf("healed follower still NACKs: conflict idx=%d", resp2.ConflictIndex)
	}
	if len(follower.log) != 2 {
		t.Fatalf("entry not applied to memory after heal: %d entries", len(follower.log))
	}
	if !follower.StorageHealthy() {
		t.Fatal("storage health not restored after successful write")
	}
	if !hasEvent(follower, EvStorageRecovered) {
		t.Fatal("recovery not surfaced as a raft event")
	}
	entries, err := fs.Scan(0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("durable log after heal: %d entries, err=%v", len(entries), err)
	}
}

// A failed durable truncate must leave both disk and memory holding the
// old (conflicting) entry — never ack the new one, never half-truncate.
func TestFollowerNACKsWhenTruncatePersistenceFails(t *testing.T) {
	follower := newNodeForTest("follower", []string{"leader", "follower"})
	fs := &faultyStorage{MockStorage: transport.NewMockStorage()}
	follower.storage = fs
	if err := fs.Append(transport.LogEntry{Index: 1, Term: 1, Data: []byte("old")}); err != nil {
		t.Fatal(err)
	}
	follower.log = append(follower.log, LogEntry{Index: 1, Term: 1})

	fs.truncateErr = errors.New("disk gone")
	req := &raftpb.AppendEntriesRequest{
		Term:         2,
		LeaderId:     []byte("leader"),
		PrevLogIndex: 0,
		PrevLogTerm:  0,
		Entries: []*raftpb.LogEntry{
			{Index: 1, Term: 2, Type: raftpb.LogEntry_COMMAND, Data: []byte("new")},
		},
	}
	resp := follower.HandleAppendEntries(req)
	if resp.Success {
		t.Fatal("follower acknowledged an entry whose truncate failed")
	}
	if len(follower.log) != 2 || follower.log[1].Term != 1 {
		t.Fatal("in-memory log truncated despite failed durable truncate")
	}
	entries, _ := fs.Scan(0)
	if len(entries) != 1 || entries[0].Term != 1 {
		t.Fatal("durable log mutated despite failed truncate")
	}
	if follower.StorageHealthy() {
		t.Fatal("storage failure not flagged")
	}
}

// §5.4.2: a vote must never be granted unless term and vote are durable.
func TestVoteNotGrantedWhenHardStatePersistFails(t *testing.T) {
	n := newNodeForTest("node1", []string{"node1", "node2"})
	fs := &faultyStorage{MockStorage: transport.NewMockStorage()}
	n.storage = fs
	fs.hardStateErr = errors.New("disk gone")

	resp := n.HandleVoteRequest(&raftpb.RequestVoteRequest{
		Term:        1,
		CandidateId: []byte("node2"),
	})
	if resp.VoteGranted {
		t.Fatal("vote granted without durable term/vote state")
	}
	if n.StorageHealthy() {
		t.Fatal("storage failure not flagged")
	}
	if !hasEvent(n, EvStorageError) {
		t.Fatal("storage failure not surfaced as a raft event")
	}

	// Healed disk: the same request now succeeds.
	fs.hardStateErr = nil
	resp2 := n.HandleVoteRequest(&raftpb.RequestVoteRequest{
		Term:        1,
		CandidateId: []byte("node2"),
	})
	if !resp2.VoteGranted {
		t.Fatal("vote refused after storage recovered")
	}
	if !n.StorageHealthy() {
		t.Fatal("storage health not restored")
	}
}

// A failed snapshot write must block log compaction: without the file the
// compacted prefix exists nowhere, and a restart would lose it.
func TestSnapshotWriteFailureBlocksLogCompaction(t *testing.T) {
	badDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevDir := snapshotEnabled.dir
	snapshotEnabled.dir = badDir
	defer func() { snapshotEnabled.dir = prevDir }()

	n := newNodeForTest("node1", []string{"node1"})
	total := uint64(snapshotThreshold + 5)
	for i := uint64(1); i <= total; i++ {
		n.log = append(n.log, LogEntry{Index: i, Term: 1})
		if err := n.storage.Append(transport.LogEntry{Index: i, Term: 1}); err != nil {
			t.Fatal(err)
		}
	}
	n.lastApplied = total

	n.takeSnapshotLocked()

	if n.snapshotIndex != 0 {
		t.Fatal("log compacted despite failed snapshot write")
	}
	if n.log[0].Index != 0 || len(n.log) != int(total)+1 {
		t.Fatal("in-memory log compacted despite failed snapshot write")
	}
	entries, _ := n.storage.Scan(0)
	if len(entries) != int(total) {
		t.Fatalf("durable prefix lost despite failed snapshot write: %d entries", len(entries))
	}
	if !hasEvent(n, EvStorageError) {
		t.Fatal("snapshot failure not surfaced as a raft event")
	}

	// With a writable dir the same call snapshots, then compacts.
	goodDir := t.TempDir()
	snapshotEnabled.dir = goodDir
	n.takeSnapshotLocked()
	if n.snapshotIndex != total {
		t.Fatalf("snapshotIndex=%d, want %d", n.snapshotIndex, total)
	}
	if n.log[0].Index != total || len(n.log) != 1 {
		t.Fatal("in-memory log not compacted after durable snapshot")
	}
	entries, _ = n.storage.Scan(0)
	if len(entries) != 0 {
		t.Fatalf("durable prefix not compacted: %d entries remain", len(entries))
	}
	if _, err := os.Stat(filepath.Join(goodDir, "snapshot-node1.json")); err != nil {
		t.Fatalf("snapshot file missing after successful take: %v", err)
	}
	if !n.StorageHealthy() {
		t.Fatal("storage health not restored")
	}
	if !hasEvent(n, EvStorageRecovered) {
		t.Fatal("recovery not surfaced as a raft event")
	}
}

// An install whose snapshot file cannot be persisted must be rejected:
// no acknowledgment, no memory mutation, no durable-log change.
func TestInstallSnapshotNacksWhenSnapshotWriteFails(t *testing.T) {
	badDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevDir := snapshotEnabled.dir
	snapshotEnabled.dir = badDir
	defer func() { snapshotEnabled.dir = prevDir }()

	n := newNodeForTest("follower", []string{"leader", "follower"})
	if err := n.storage.Append(transport.LogEntry{Index: 1, Term: 1, Data: []byte("old")}); err != nil {
		t.Fatal(err)
	}
	n.log = append(n.log, LogEntry{Index: 1, Term: 1})

	raw, err := json.Marshal(snapshotEnvelope{LastIndex: 5, LastTerm: 2, Data: []byte(`{"revision":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	resp := n.HandleInstallSnapshot(&raftpb.InstallSnapshotRequest{
		Term:     2,
		LeaderId: []byte("leader"),
		Data:     raw,
	})
	if len(resp.BytesWritten) != 0 {
		t.Fatal("acknowledged an install that was not persisted")
	}
	if n.snapshotIndex != 0 {
		t.Fatal("snapshotIndex advanced despite failed snapshot write")
	}
	if n.fsm.Revision() != 0 {
		t.Fatal("FSM restored despite failed durable install")
	}
	if len(n.log) != 2 {
		t.Fatal("in-memory log mutated despite failed durable install")
	}
	entries, _ := n.storage.Scan(0)
	if len(entries) != 1 {
		t.Fatal("durable log mutated despite failed durable install")
	}
	if n.StorageHealthy() {
		t.Fatal("storage failure not flagged")
	}
}

// A leader must not treat a hint-less NACK (follower storage failure or
// stale term) as a log mismatch: nextIndex stays put and the next
// heartbeat retries — no hot loop against a dead disk.
func TestLeaderTreatsHintlessNACKAsTransient(t *testing.T) {
	n := newNodeForTest("leader", []string{"leader", "follower"})
	n.state = Leader
	n.currentTerm = 3
	n.nextIndex["follower"] = 7

	n.HandleAppendEntriesResponse("follower", 6, &raftpb.AppendEntriesResponse{Term: 3, Success: false})
	if n.nextIndex["follower"] != 7 {
		t.Fatalf("nextIndex rewound on hint-less NACK: %d", n.nextIndex["follower"])
	}

	// A real conflict still backtracks.
	n.HandleAppendEntriesResponse("follower", 5, &raftpb.AppendEntriesResponse{
		Term: 3, Success: false, ConflictIndex: 4,
	})
	if n.nextIndex["follower"] != 4 {
		t.Fatalf("nextIndex=%d on conflict NACK, want 4", n.nextIndex["follower"])
	}
}

// Restart must apply exactly the committed prefix — never the uncommitted
// tail, however complete it looks (FR-023).
func TestRestoreNeverAppliesUncommittedTail(t *testing.T) {
	st := transport.NewMockStorage()
	for i := uint64(1); i <= 5; i++ {
		if err := st.Append(transport.LogEntry{
			Index: i, Term: 1, Data: []byte("add:9.9.9.9"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveHardState(transport.HardState{Term: 1, CommitIndex: 3}); err != nil {
		t.Fatal(err)
	}

	n := NewNode("n1", []string{"n1"}, transport.NewMockClock(timeNow()), st, NewFSM())
	if n.commitIndex != 3 || n.lastApplied != 3 {
		t.Fatalf("commit=%d applied=%d, want 3/3", n.commitIndex, n.lastApplied)
	}
	if !n.fsm.IsBlocked("9.9.9.9") {
		t.Fatal("committed prefix not applied after restore")
	}
	if len(n.log) != 6 {
		t.Fatalf("log should hold base+5 entries, got %d", len(n.log))
	}
}

// restore() must drop duplicate or non-contiguous durable records instead
// of building a log whose positional indexing silently maps wrong entries
// (belt-and-braces with the WAL's own crash-safety).
func TestRestoreDropsNonContiguousDurableRecords(t *testing.T) {
	st := transport.NewMockStorage()
	seq := []transport.LogEntry{
		{Index: 1, Term: 1}, {Index: 2, Term: 1}, {Index: 3, Term: 1},
		{Index: 3, Term: 1}, // duplicate from an interrupted compaction
		{Index: 4, Term: 1},
	}
	for _, e := range seq {
		if err := st.Append(e); err != nil {
			t.Fatal(err)
		}
	}

	n := NewNode("n1", []string{"n1"}, transport.NewMockClock(timeNow()), st, NewFSM())
	if len(n.log) != 4 {
		t.Fatalf("expected base+3 entries after contiguity guard, got %d", len(n.log))
	}
	if n.log[3].Index != 3 {
		t.Fatalf("log must stop at the duplicate, tail index=%d", n.log[3].Index)
	}
	for i := 1; i < len(n.log); i++ {
		if n.log[i].Index != n.log[i-1].Index+1 {
			t.Fatalf("non-contiguous log at position %d: %d -> %d",
				i, n.log[i-1].Index, n.log[i].Index)
		}
	}
}
