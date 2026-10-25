package transport

// Durability tests for the segmented WAL's hard-state sidecar and crash
// recovery: vote persistence (Raft §5.4.2), torn-tail discard (FR-022), and
// cross-segment truncation.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHardStateRoundTrip(t *testing.T) {
	w, err := OpenWAL(filepath.Join(t.TempDir(), "hs.wal"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Missing sidecar yields the zero value, not an error: fresh node.
	if hs, err := w.LoadHardState(); err != nil || hs != (HardState{}) {
		t.Fatalf("expected zero hard state, got %+v (err %v)", hs, err)
	}

	want := HardState{Term: 7, VotedFor: "node3", CommitIndex: 42}
	if err := w.SaveHardState(want); err != nil {
		t.Fatal(err)
	}
	got, err := w.LoadHardState()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("hard state mismatch: got %+v, want %+v", got, want)
	}
}

// The hard state must survive a reopen — this is the durability promise that
// prevents double-voting after a crash (§5.4.2).
func TestHardStateSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hs2.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SaveHardState(HardState{Term: 3, VotedFor: "node2"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	hs, err := w2.LoadHardState()
	if err != nil {
		t.Fatal(err)
	}
	if hs.Term != 3 || hs.VotedFor != "node2" {
		t.Fatalf("vote lost across reopen: %+v", hs)
	}
}

// A corrupt hard-state file is an error, never a silent reset to term 0 —
// restarting a voting node with a forgotten vote can cause double-voting.
func TestHardStateCorruptionDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hs3.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SaveHardState(HardState{Term: 9, VotedFor: "node1"}); err != nil {
		t.Fatal(err)
	}
	w.Close()

	data, err := os.ReadFile(path + ".hs")
	if err != nil {
		t.Fatal(err)
	}
	data[10] ^= 0xFF // flip a bit inside the term field
	if err := os.WriteFile(path+".hs", data, 0644); err != nil {
		t.Fatal(err)
	}

	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if _, err := w2.LoadHardState(); err == nil {
		t.Fatal("corrupt hard state silently accepted")
	}
}

// A crash mid-append leaves a torn trailing record; open must discard it and
// keep every complete record (FR-022).
func TestWALTornTailDiscarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := w.Append(LogEntry{Index: uint64(i), Term: 1, Data: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	// Append half a record, simulating a crash mid-write.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0x01, 0x02, 0x03, 0x04, 0x05}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open after torn write: %v", err)
	}
	defer w2.Close()
	entries, err := w2.Scan(1)
	if err != nil {
		t.Fatalf("scan after torn write: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 complete records, got %d", len(entries))
	}
	if idx, _ := w2.LastIndex(); idx != 3 {
		t.Fatalf("expected last index 3, got %d", idx)
	}
	// The WAL must be writable again after discarding the tail, and the new
	// record must be readable after a reopen — before the fix, appends after
	// torn garbage were silently unscannable.
	if err := w2.Append(LogEntry{Index: 4, Term: 1, Data: []byte{4}}); err != nil {
		t.Fatalf("append after torn tail: %v", err)
	}
	w2.Close()

	w3, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w3.Close()
	entries, err = w3.Scan(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("record appended after torn tail is unreadable: got %d entries, want 4", len(entries))
	}
}

// Rotation: segments split at walSegmentSize and the log stays readable
// across the boundary.
func TestWALSegmentRotation(t *testing.T) {
	w, err := OpenWAL(filepath.Join(t.TempDir(), "rot.wal"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// ~400 KiB per record: 22 records ≈ 8.8 MiB forces at least one rotation.
	payload := make([]byte, 400<<10)
	const records = 22
	for i := 1; i <= records; i++ {
		if err := w.Append(LogEntry{Index: uint64(i), Term: 1, Data: payload}); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.segs) < 2 {
		t.Fatalf("expected rotation to create a second segment, got %d", len(w.segs))
	}

	size, err := w.Size()
	if err != nil {
		t.Fatal(err)
	}
	if size < int64(records)*int64(len(payload)) {
		t.Fatalf("Size() under-reports: %d", size)
	}

	entries, err := w.Scan(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != records {
		t.Fatalf("cross-segment scan: got %d entries, want %d", len(entries), records)
	}
	for i, e := range entries {
		if e.Index != uint64(i+1) {
			t.Fatalf("order broken at %d: index %d", i, e.Index)
		}
	}
}

// Truncation that cuts inside a rotated segment removes the boundary record
// and every later segment.
func TestWALTruncateAcrossSegments(t *testing.T) {
	w, err := OpenWAL(filepath.Join(t.TempDir(), "rot2.wal"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	payload := make([]byte, 400<<10)
	const records = 22
	for i := 1; i <= records; i++ {
		if err := w.Append(LogEntry{Index: uint64(i), Term: 1, Data: payload}); err != nil {
			t.Fatal(err)
		}
	}
	// Keep 12 records: the boundary falls inside the second segment.
	if err := w.Truncate(12); err != nil {
		t.Fatal(err)
	}
	entries, err := w.Scan(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 12 {
		t.Fatalf("expected 12 entries after cross-segment truncate, got %d", len(entries))
	}
	if idx, _ := w.LastIndex(); idx != 12 {
		t.Fatalf("last index after truncate: %d", idx)
	}
	if len(w.segs) != 1 {
		t.Fatalf("expected later segments removed, segs=%d", len(w.segs))
	}
	// Writes continue from the truncation point with matching indexes.
	if err := w.Append(LogEntry{Index: 13, Term: 2, Data: []byte("new")}); err != nil {
		t.Fatal(err)
	}
	entries, _ = w.Scan(13)
	if len(entries) != 1 || entries[0].Term != 2 {
		t.Fatalf("post-truncate append broken: %+v", entries)
	}
}
