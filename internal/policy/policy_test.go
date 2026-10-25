package policy

import (
	"encoding/json"
	"net/netip"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

var now = time.Unix(1_000_000, 0).UTC()

func applyCreate(t *testing.T, e *Engine, id, target string, rev uint64) {
	t.Helper()
	if _, err := e.Apply(Command{Op: OpCreate, ID: id, Target: target, CreatedAt: time.Unix(0, 0).UTC()}, rev); err != nil {
		t.Fatalf("apply create %s: %v", id, err)
	}
}

// Bare IPs and CIDRs both parse; a bare IP becomes the host's own prefix.
func TestCreateAndLookup(t *testing.T) {
	e := NewEngine()
	applyCreate(t, e, "a", "10.0.0.5", 1)
	applyCreate(t, e, "b", "192.168.0.0/16", 2)

	if !e.LookupString("10.0.0.5", now).Blocked {
		t.Fatal("exact IP not blocked")
	}
	if e.LookupString("10.0.0.6", now).Blocked {
		t.Fatal("neighbor IP wrongly blocked by /32 policy")
	}
	if !e.LookupString("192.168.7.9", now).Blocked {
		t.Fatal("CIDR member not blocked")
	}
	if e.LookupString("192.169.0.1", now).Blocked {
		t.Fatal("address outside CIDR blocked")
	}
}

// CIDR membership is masked: 10.1.2.3/24 must cover the whole /24, not the
// literal address.
func TestCIDRMasking(t *testing.T) {
	e := NewEngine()
	applyCreate(t, e, "c", "10.1.2.3/24", 1)
	if !e.LookupString("10.1.2.200", now).Blocked {
		t.Fatal("masked CIDR did not cover its /24 range")
	}
	if got := e.policies["c"].Type; got != TypeCIDR {
		t.Fatalf("expected cidr_block type, got %q", got)
	}
}

// SRS §7.2 precedence: exact IP > longest prefix > lowest ID. The result must
// never depend on map iteration order.
func TestDeterministicPrecedence(t *testing.T) {

	orderings := [][]struct {
		id     string
		target string
	}{
		{{"z-exact", "203.0.113.7"}, {"a-cidr16", "203.0.113.0/24"}, {"m-cidr28", "203.0.113.0/28"}},
		{{"a-cidr16", "203.0.113.0/24"}, {"m-cidr28", "203.0.113.0/28"}, {"z-exact", "203.0.113.7"}},
		{{"m-cidr28", "203.0.113.0/28"}, {"z-exact", "203.0.113.7"}, {"a-cidr16", "203.0.113.0/24"}},
	}
	for i, ordering := range orderings {
		e := NewEngine()
		for rev, p := range ordering {
			applyCreate(t, e, p.id, p.target, uint64(rev+1))
		}
		d := e.LookupString("203.0.113.7", now)
		if !d.Blocked || d.Policy.ID != "z-exact" {
			t.Fatalf("ordering %d: exact IP must win, got %+v", i, d.Policy)
		}
	}
}

// Two policies with the same prefix: the lexicographically smaller ID wins,
// regardless of insertion order.
func TestIDTieBreak(t *testing.T) {
	e := NewEngine()
	applyCreate(t, e, "zzz", "10.5.0.0/16", 1)
	applyCreate(t, e, "aaa", "10.5.0.0/16", 2)
	d := e.LookupString("10.5.9.9", now)
	if !d.Blocked || d.Policy.ID != "aaa" {
		t.Fatalf("ID tie-break broken: got %+v, want aaa", d.Policy)
	}
}

// Expiration uses only the committed timestamp: the policy blocks strictly
// before expires_at and stops at it, with no local-timer mutation (ADR-0003,
// NFR-015).
func TestExpiryFromCommittedTimestamp(t *testing.T) {
	e := NewEngine()
	start := mustTime(t, "2026-01-01T00:00:00Z")
	exp := mustTime(t, "2026-01-01T01:00:00Z")
	if _, err := e.Apply(Command{
		Op: OpCreate, ID: "tmp", Target: "10.20.30.40",
		CreatedAt: start, ExpiresAt: &exp,
	}, 1); err != nil {
		t.Fatal(err)
	}

	if !e.LookupString("10.20.30.40", start).Blocked {
		t.Fatal("temporary policy not active before expiry")
	}
	if !e.LookupString("10.20.30.40", exp.Add(-time.Second)).Blocked {
		t.Fatal("temporary policy expired early")
	}
	if e.LookupString("10.20.30.40", exp).Blocked {
		t.Fatal("temporary policy still active at/after expiry")
	}
	// Expiry is state-free: nothing was mutated, the same engine still
	// reports active before the instant if asked with an earlier clock.
	if !e.LookupString("10.20.30.40", start).Blocked {
		t.Fatal("expiry mutated state instead of evaluating at lookup")
	}
}

func TestRevoke(t *testing.T) {
	e := NewEngine()
	applyCreate(t, e, "p1", "10.0.0.9", 1)
	if !e.LookupString("10.0.0.9", now).Blocked {
		t.Fatal("policy should block before revoke")
	}
	if _, err := e.Apply(Command{Op: OpRevoke, ID: "p1"}, 2); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if e.LookupString("10.0.0.9", now).Blocked {
		t.Fatal("revoked policy still blocks")
	}
	// Policy remains visible for audit with includeInactive.
	all := e.List(now, true)
	if len(all) != 1 || all[0].Status != StatusRevoked || all[0].Version != 2 {
		t.Fatalf("revoked policy missing/mis-versioned: %+v", all)
	}
}

// Revoking an unknown ID is a deterministic no-op (no error, no state change).
func TestRevokeUnknownIsNoop(t *testing.T) {
	e := NewEngine()
	if _, err := e.Apply(Command{Op: OpRevoke, ID: "ghost"}, 1); err != nil {
		t.Fatalf("revoking unknown id must be a no-op, got %v", err)
	}
	if e.Revision() != 1 {
		t.Fatalf("revision changed on no-op: %d", e.Revision())
	}
}

// FR-016: a replayed idempotency key returns the original outcome and does
// not create a second policy.
func TestIdempotencyReplay(t *testing.T) {
	e := NewEngine()
	cmd := Command{
		Op: OpCreate, ID: "p1", Target: "10.7.7.7", IdemKey: "req-123",
		CreatedAt: time.Unix(0, 0).UTC(),
	}
	first, err := e.Apply(cmd, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Replay with a DIFFERENT policy id (client retry generated a new one):
	// the engine must return the original outcome, not create p2.
	cmd.ID = "p2"
	second, err := e.Apply(cmd, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.PolicyID != "p1" || second.PolicyID != "p1" {
		t.Fatalf("idempotency broken: first=%+v second=%+v", first, second)
	}
	if _, ok := e.Get("p2"); ok {
		t.Fatal("replayed command created a second policy")
	}
}

// Invalid commands are rejected deterministically.
func TestCommandValidation(t *testing.T) {
	e := NewEngine()
	if _, err := e.Apply(Command{Op: OpCreate, ID: "x", Target: "not-an-ip"}, 1); err == nil {
		t.Fatal("invalid target accepted")
	}
	if _, err := e.Apply(Command{Op: OpCreate, Target: "10.0.0.1"}, 1); err == nil {
		t.Fatal("missing id accepted")
	}
	if _, err := e.Apply(Command{Op: "explode"}, 1); err == nil {
		t.Fatal("unknown op accepted")
	}
	if e.CountActive(time.Unix(1_000_000, 0)) != 0 {
		t.Fatal("failed commands left state behind")
	}
}

// Snapshot/restore round trip preserves policies, revisions, expiry, and
// idempotency records — the restart path (FR-024).
func TestSnapshotRestoreRoundTrip(t *testing.T) {
	e := NewEngine()
	applyCreate(t, e, "p1", "10.1.1.1", 4)
	applyCreate(t, e, "p2", "fd00::/8", 5)
	exp := now.Add(time.Hour)
	if _, err := e.Apply(Command{
		Op: OpCreate, ID: "p3", Target: "10.2.2.2", IdemKey: "k9",
		CreatedAt: now, ExpiresAt: &exp,
	}, 6); err != nil {
		t.Fatal(err)
	}

	data, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	restored := NewEngine()
	if err := restored.Restore(data); err != nil {
		t.Fatal(err)
	}
	if restored.Revision() != 6 {
		t.Fatalf("revision lost: %d", restored.Revision())
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		if _, ok := restored.Get(id); !ok {
			t.Fatalf("policy %s lost in restore", id)
		}
	}
	// IPv6 prefix must still match after re-parse.
	if !restored.LookupString("fd00::1", now).Blocked {
		t.Fatal("IPv6 CIDR not effective after restore")
	}
	// Expiry survives restore.
	if restored.LookupString("10.2.2.2", now.Add(2*time.Hour)).Blocked {
		t.Fatal("expired policy still blocks after restore")
	}
	// Idempotency records survive restore.
	if _, err := restored.Apply(Command{
		Op: OpCreate, ID: "other", Target: "10.2.2.2", IdemKey: "k9",
	}, 7); err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.Get("other"); ok {
		t.Fatal("idempotency record lost in restore")
	}
}

// Restore with garbage must fail loudly rather than produce empty state.
func TestRestoreRejectsGarbage(t *testing.T) {
	e := NewEngine()
	if err := e.Restore([]byte("{not json")); err == nil {
		t.Fatal("garbage snapshot accepted")
	}
}

// Legacy add:/del: commands from v0.1 logs still apply.
func TestLegacyCommands(t *testing.T) {
	e := NewEngine()
	cmd, ok := LegacyCommand("add:10.0.0.1")
	if !ok {
		t.Fatal("legacy add not recognized")
	}
	if _, err := e.Apply(cmd, 1); err != nil {
		t.Fatal(err)
	}
	if !e.LookupString("10.0.0.1", now).Blocked {
		t.Fatal("legacy add did not block")
	}
	del, ok := LegacyCommand("del:10.0.0.1")
	if !ok {
		t.Fatal("legacy del not recognized")
	}
	if _, err := e.Apply(del, 2); err != nil {
		t.Fatal(err)
	}
	if e.LookupString("10.0.0.1", now).Blocked {
		t.Fatal("legacy del did not unblock")
	}
	if _, ok := LegacyCommand("frobnicate:xyz"); ok {
		t.Fatal("unknown command accepted as legacy")
	}
}

// Two replicas applying the same command sequence converge byte-for-byte.
func TestDeterministicConvergence(t *testing.T) {
	build := func() *Engine {
		e := NewEngine()
		applyCreate(t, e, "a", "10.0.0.0/8", 1)
		applyCreate(t, e, "b", "10.1.2.3", 2)
		applyCreate(t, e, "c", "2001:db8::/32", 3)
		if _, err := e.Apply(Command{Op: OpRevoke, ID: "a"}, 4); err != nil {
			t.Fatal(err)
		}
		return e
	}
	s1, err := build().Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := build().Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if string(s1) != string(s2) {
		t.Fatalf("replicas diverged:\n%s\n%s", s1, s2)
	}
}

// JSON marshaling of a Command with an expiry survives the round trip, since
// the leader encodes timestamps before proposing.
func TestCommandJSONRoundTrip(t *testing.T) {
	exp := time.Unix(2_000_000, 0).UTC()
	cmd := Command{Op: OpCreate, ID: "p", Target: "10.0.0.1", ExpiresAt: &exp, CreatedAt: exp}
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var back Command
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.ExpiresAt == nil || !back.ExpiresAt.Equal(exp) {
		t.Fatalf("expiry lost in JSON: %+v", back)
	}
}

// Lookup on an unparsable address is a safe miss, not a panic.
func TestLookupInvalidAddress(t *testing.T) {
	e := NewEngine()
	if d := e.Lookup(netip.Addr{}, time.Unix(1_000_000, 0)); d.Blocked {
		t.Fatal("invalid address reported blocked")
	}
	if d := e.LookupString("not-an-ip", time.Unix(1_000_000, 0)); d.Blocked {
		t.Fatal("unparsable string reported blocked")
	}
}
