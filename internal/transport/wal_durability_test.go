package transport

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// failingFile wraps the WAL's active file to inject write/sync failures —
// the fault a healthy disk can never produce, which is exactly what the
// rollback paths in appendRaw must survive.
type failingFile struct {
	walFile
	failWrites int
	failSyncs  int
}

func (f *failingFile) Write(p []byte) (int, error) {
	if f.failWrites > 0 {
		f.failWrites--
		// Partial write + error: the realistic ENOSPC/EIO shape.
		return len(p) / 2, errors.New("injected write failure")
	}
	return f.walFile.Write(p)
}

func (f *failingFile) Sync() error {
	if f.failSyncs > 0 {
		f.failSyncs--
		return errors.New("injected sync failure")
	}
	return f.walFile.Sync()
}

func baseSize(t *testing.T, path string) int64 {
	t.Helper()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return stat.Size()
}

// A failed append must leave no partial record behind: a later successful
// append would bury the torn bytes, and the scan would stop there on the
// next restart — silently losing every record written after it.
func TestWALAppendRollsBackPartialWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollback.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.Append(LogEntry{Index: 1, Term: 1}); err != nil {
		t.Fatal(err)
	}
	good := baseSize(t, path)

	w.mu.Lock()
	w.f = &failingFile{walFile: w.f, failWrites: 1}
	w.mu.Unlock()

	if err := w.Append(LogEntry{Index: 2, Term: 1, Data: []byte("payload")}); err == nil {
		t.Fatal("expected injected write failure")
	}
	if got := baseSize(t, path); got != good {
		t.Fatalf("partial record left on disk: size %d, want %d", got, good)
	}

	// The WAL must keep working and stay scannable end-to-end.
	if err := w.Append(LogEntry{Index: 2, Term: 1, Data: []byte("payload")}); err != nil {
		t.Fatalf("append after injected failure: %v", err)
	}
	w.Close()

	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen after rollback: %v", err)
	}
	defer w2.Close()
	entries, err := w2.Scan(1)
	if err != nil {
		t.Fatalf("scan after rollback: %v", err)
	}
	if len(entries) != 2 || entries[1].Index != 2 {
		t.Fatalf("expected entries [1 2], got %d entries", len(entries))
	}
}

// A sync failure means the bytes may be present but not durable — the file
// must agree with the in-memory log that is rejecting the append.
func TestWALAppendRollsBackFailedSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.Append(LogEntry{Index: 1, Term: 1}); err != nil {
		t.Fatal(err)
	}
	good := baseSize(t, path)

	w.mu.Lock()
	w.f = &failingFile{walFile: w.f, failSyncs: 1}
	w.mu.Unlock()

	if err := w.Append(LogEntry{Index: 2, Term: 1, Data: []byte("payload")}); err == nil {
		t.Fatal("expected injected sync failure")
	}
	if got := baseSize(t, path); got != good {
		t.Fatalf("unsynced record not rolled back: size %d, want %d", got, good)
	}
}

// CompactPrefix must produce a WAL holding exactly the surviving suffix,
// across segment rotation, and remain appendable afterwards.
func TestWALCompactPrefixRoundTrip(t *testing.T) {
	oldSize := walSegmentSize
	walSegmentSize = 512 // force rotation with tiny segments
	defer func() { walSegmentSize = oldSize }()

	dir := t.TempDir()
	path := filepath.Join(dir, "compact.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		if err := w.Append(LogEntry{
			Index: uint64(i), Term: 1, Data: []byte("payload-payload-payload"),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// Ensure the log really spans multiple segments.
	if matches, _ := filepath.Glob(path + ".[0-9]*"); len(matches) == 0 {
		t.Fatal("test requires rotation; segment size not applied")
	}

	if err := w.CompactPrefix(10); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}
	assertIndexes(t, w, 11, 20)
	w.Close()

	// Reopen: the compacted suffix survives a restart.
	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	assertIndexes(t, w2, 11, 20)

	// Appending after compaction continues the sequence.
	if err := w2.Append(LogEntry{Index: 21, Term: 1, Data: []byte("x")}); err != nil {
		t.Fatalf("append after compaction: %v", err)
	}
	assertIndexes(t, w2, 11, 21)
	w2.Close()
}

// The compacted base replaces the old log atomically (rename), then the
// stale rotated segments are removed. A crash between those steps leaves
// both; the next open must prune the stale ones instead of resurrecting
// duplicate indexes beside the compacted base.
func TestWALOpenPrunesStaleRotatedAfterInterruptedCompaction(t *testing.T) {
	oldSize := walSegmentSize
	walSegmentSize = 512
	defer func() { walSegmentSize = oldSize }()

	dir := t.TempDir()
	path := filepath.Join(dir, "interrupted.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		if err := w.Append(LogEntry{Index: uint64(i), Term: 1, Data: []byte("payload-payload-payload")}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	rotated, _ := filepath.Glob(path + ".[0-9]*")
	if len(rotated) == 0 {
		t.Fatal("test requires rotation")
	}
	// Keep copies of the rotated segments as they were pre-compaction.
	stale := make(map[string][]byte, len(rotated))
	for _, p := range rotated {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		stale[p] = b
	}

	if err := w.CompactPrefix(10); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}
	w.Close()

	// Simulate the crash window: put the stale rotated segments back, as
	// if their removal after the publish never ran.
	for p, b := range stale {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen with stale segments: %v", err)
	}
	defer w2.Close()
	// Duplicates must not appear: exactly 11..20.
	assertIndexes(t, w2, 11, 20)
	// And the stale files are gone.
	for _, p := range rotated {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("stale segment %s not pruned", p)
		}
	}
}

// Same crash window, but the compaction dropped everything (snapshot at
// the tail): the base body is empty and the rotated segments are stale.
func TestWALOpenPrunesStaleRotatedAfterEmptyCompaction(t *testing.T) {
	oldSize := walSegmentSize
	walSegmentSize = 512
	defer func() { walSegmentSize = oldSize }()

	dir := t.TempDir()
	path := filepath.Join(dir, "empty.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 15; i++ {
		if err := w.Append(LogEntry{Index: uint64(i), Term: 1, Data: []byte("payload-payload-payload")}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	rotated, _ := filepath.Glob(path + ".[0-9]*")
	stale := make(map[string][]byte, len(rotated))
	for _, p := range rotated {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		stale[p] = b
	}
	if err := w.CompactPrefix(15); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}
	w.Close()
	for p, b := range stale {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer w2.Close()
	entries, err := w2.Scan(0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty log after full compaction, got %d entries", len(entries))
	}
	if err := w2.Append(LogEntry{Index: 16, Term: 1}); err != nil {
		t.Fatalf("append after empty compaction: %v", err)
	}
}

func assertIndexes(t *testing.T, w *WAL, from, to uint64) {
	t.Helper()
	entries, err := w.Scan(0)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := int(to - from + 1)
	if len(entries) != want {
		t.Fatalf("expected %d entries (%d..%d), got %d", want, from, to, len(entries))
	}
	for i, e := range entries {
		if want := from + uint64(i); e.Index != want {
			t.Fatalf("entry %d: index %d, want %d", i, e.Index, want)
		}
	}
}
