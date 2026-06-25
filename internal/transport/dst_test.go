package transport

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWALAppendAndScan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Append(LogEntry{Index: 1, Term: 1, Type: 0, Data: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(LogEntry{Index: 2, Term: 1, Type: 0, Data: []byte("world")}); err != nil {
		t.Fatal(err)
	}

	entries, err := w.Scan(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if string(entries[0].Data) != "hello" || string(entries[1].Data) != "world" {
		t.Fatal("data mismatch")
	}

	w.Close()

	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w2.LastIndex()
	if err != nil {
		t.Fatal(err)
	}
	if idx != 2 {
		t.Fatalf("expected last index 2, got %d", idx)
	}
	w2.Close()
}

func TestWALTruncate(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(filepath.Join(dir, "trunc.wal"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		w.Append(LogEntry{Index: uint64(i), Term: 1, Type: 0, Data: []byte{byte(i)}})
	}
	if err := w.Truncate(3); err != nil {
		t.Fatal(err)
	}
	entries, _ := w.Scan(1)
	if len(entries) != 3 {
		t.Fatalf("expected 3 after truncate, got %d", len(entries))
	}
	w.Close()
}

func TestWALCorruptionDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.wal")
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Append(LogEntry{Index: 1, Term: 1, Type: 0, Data: []byte("data")})
	w.Close()

	// Corrupt the data section (after header+header fields)
	f, _ := os.OpenFile(path, os.O_WRONLY, 0644)
	f.WriteAt([]byte("XXXX"), 45)
	f.Close()

	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w2.Scan(1)
	if err != ErrCorruptWAL {
		t.Fatalf("expected ErrCorruptWAL, got %v", err)
	}
	w2.Close()
}

func TestDSTDeterminism(t *testing.T) {
	cfg := SimulationConfig{
		Seed:     42,
		Nodes:    3,
		MaxSteps: 100,
	}
	h1 := NewSimulation(cfg)
	h2 := NewSimulation(cfg)

	steps1 := 0
	for h1.Step() {
		steps1++
	}
	steps2 := 0
	for h2.Step() {
		steps2++
	}

	if steps1 != steps2 {
		t.Fatalf("step count differs: %d vs %d", steps1, steps2)
	}
}

func TestMockStorage(t *testing.T) {
	s := NewMockStorage()
	if err := s.Append(LogEntry{Term: 1, Data: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	idx, _ := s.LastIndex()
	if idx != 1 {
		t.Fatalf("expected last index 1, got %d", idx)
	}
	entries, _ := s.Scan(1)
	if len(entries) != 1 {
		t.Fatal("expected 1 entry")
	}
}

func TestMockClockAdvance(t *testing.T) {
	start := time.Now()
	c := NewMockClock(start)
	c.Advance(100 * time.Millisecond)
	if c.Now().Sub(start) < 100*time.Millisecond {
		t.Fatal("clock did not advance")
	}
}

func TestMockNetworkPartition(t *testing.T) {
	n := NewMockNetwork()
	n.SetPartition("node2", true)
	err := n.Send("node2", []byte("ping"))
	if err != ErrNodeDown {
		t.Fatal("expected ErrNodeDown on partitioned node")
	}
}
