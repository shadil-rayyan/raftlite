package audit

import (
	"io"
	"testing"
)

func TestRecentReturnsInMemoryEntries(t *testing.T) {
	l := New(io.Discard)
	l.Record(Entry{Actor: "admin", Operation: "create", Outcome: "committed"})
	l.Record(Entry{Actor: "admin", Operation: "revoke", Outcome: "committed"})
	got := l.Recent(10)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].Operation != "create" || got[1].Operation != "revoke" {
		t.Fatalf("wrong order: %+v", got)
	}
}

func TestRecentRingBufferTrims(t *testing.T) {
	l := New(io.Discard)
	for i := 0; i < 600; i++ {
		l.Record(Entry{Operation: "op"})
	}
	got := l.Recent(1000)
	if len(got) != 500 {
		t.Fatalf("want 500 entries, got %d", len(got))
	}
}
