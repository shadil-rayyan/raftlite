package fleet

// Pull-based (envoy-sim) gateway tests: registration idempotency, config
// serving, report ingestion, heartbeat-driven offline detection, and the
// core invariant that ONLY enforcement probes advance verification.

import (
	"testing"
	"time"
)

func realManager(t *testing.T) *Manager {
	t.Helper()
	m, _ := testManager(t, 5*time.Second)
	return m
}

func mustRegister(t *testing.T, m *Manager, id string) *Gateway {
	t.Helper()
	gw, created, err := m.RegisterRealGateway(id, "edge-real", "http://127.0.0.1:18080")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !created {
		t.Fatal("first registration must create")
	}
	return gw
}

func TestRegisterRealGatewayIdempotent(t *testing.T) {
	m := realManager(t)
	mustRegister(t, m, "simgw-1")

	gw, created, err := m.RegisterRealGateway("simgw-1", "edge-real", "http://127.0.0.1:18080")
	if err != nil || created {
		t.Fatalf("re-registration must be idempotent: created=%v err=%v", created, err)
	}
	if gw.ProxyURL != "http://127.0.0.1:18080" {
		t.Fatalf("re-registration lost proxy URL: %+v", gw)
	}
	if got := len(m.List()); got != 1 {
		t.Fatalf("re-registration created a duplicate: %d gateways", got)
	}
}

func TestRegisterRealGatewayRejectsMockIDCollision(t *testing.T) {
	m := realManager(t)
	if _, err := m.CreateGateway("edge-mock", "", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.RegisterRealGateway("gw-001", "x", "http://x"); err != ErrExists {
		t.Fatalf("want ErrExists for mock-owned id, got %v", err)
	}
}

func TestPullConfigAndAcceptReport(t *testing.T) {
	m := realManager(t)
	gw := mustRegister(t, m, "simgw-1")

	snap := ConfigSnapshot{Revision: 1, Policies: []PolicyView{{ID: "pol-1", Type: "ip_block", Target: "203.0.113.7", Action: "deny", Status: "active"}}}
	ro := m.OnRevision(1, "pol-1", snap)
	if ro.Results[gw.ID].Status != RollSending {
		t.Fatalf("want sending until a report arrives, got %s", ro.Results[gw.ID].Status)
	}

	served, ok := m.ConfigFor(gw.ID)
	if !ok || served.Revision != 1 || len(served.Policies) != 1 {
		t.Fatalf("config serve: ok=%v snap=%+v", ok, served)
	}
	// Serving config must NOT mark acceptance.
	if g, _ := m.Get(gw.ID); g.ReportedAcceptedRevision != 0 {
		t.Fatalf("config serve pre-marked acceptance: %+v", g)
	}

	if err := m.HandleReport(gw.ID, 1, RollAccepted, nil); err != nil {
		t.Fatalf("report: %v", err)
	}
	g, _ := m.Get(gw.ID)
	if g.ReportedAcceptedRevision != 1 || g.DesiredRevision != 1 {
		t.Fatalf("accept report not applied: %+v", g)
	}
	// INVARIANT: acceptance is not enforcement verification.
	if g.Verification != VerifUntested {
		t.Fatalf("accept report advanced verification to %q", g.Verification)
	}
	roll, _ := m.Rollout(ro.ID)
	if roll.Results[gw.ID].Status != RollAccepted {
		t.Fatalf("rollout result not resolved: %s", roll.Results[gw.ID].Status)
	}
}

func TestRejectReportKeepsRevision(t *testing.T) {
	m := realManager(t)
	gw := mustRegister(t, m, "simgw-1")
	m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})

	evErr := &EventError{Code: "INVALID_CONFIGURATION", Message: "bad"}
	if err := m.HandleReport(gw.ID, 1, RollRejected, evErr); err != nil {
		t.Fatalf("report: %v", err)
	}
	g, _ := m.Get(gw.ID)
	if g.ReportedAcceptedRevision != 0 {
		t.Fatalf("rejection advanced revision: %+v", g)
	}
	if g.LastError == nil || g.LastError.Code != "INVALID_CONFIGURATION" {
		t.Fatalf("missing structured error: %+v", g.LastError)
	}
}

func TestStaleReportSuperseded(t *testing.T) {
	m := realManager(t)
	gw := mustRegister(t, m, "simgw-1")
	m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})
	m.OnRevision(2, "pol-2", ConfigSnapshot{Revision: 2})

	if err := m.HandleReport(gw.ID, 1, RollAccepted, nil); err != nil {
		t.Fatalf("stale report: %v", err)
	}
	g, _ := m.Get(gw.ID)
	if g.ReportedAcceptedRevision != 0 || g.DesiredRevision != 2 {
		t.Fatalf("stale report overwrote state: %+v", g)
	}
}

func TestInvalidReportStatus(t *testing.T) {
	m := realManager(t)
	gw := mustRegister(t, m, "simgw-1")
	if err := m.HandleReport(gw.ID, 1, "applied", nil); err != ErrInvalidReport {
		t.Fatalf("want ErrInvalidReport, got %v", err)
	}
	if err := m.HandleReport("nope", 1, RollAccepted, nil); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMockGatewayCannotPullOrReport(t *testing.T) {
	m := realManager(t)
	gw, err := m.CreateGateway("edge-mock", "", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.ConfigFor(gw.ID); ok {
		t.Fatal("mock gateway must not serve pull config")
	}
	if err := m.HandleReport(gw.ID, 1, RollAccepted, nil); err != ErrNotRealGateway {
		t.Fatalf("want ErrNotRealGateway, got %v", err)
	}
}

func TestHeartbeatLossMarksRealGatewayOffline(t *testing.T) {
	m := realManager(t)
	if err := m.SetHeartbeatTimeout(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	real := mustRegister(t, m, "simgw-1")
	mock, err := m.CreateGateway("edge-mock", "", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	// Silence beyond the window: real gateway offline, mock untouched —
	// mock liveness is simulated, never derived from absence.
	m.sweepHeartbeats(time.Now().UTC().Add(2 * time.Second))
	g, _ := m.Get(real.ID)
	if g.ConnectionStatus != StatusOffline {
		t.Fatalf("silent pull gateway must go offline, got %s", g.ConnectionStatus)
	}
	mg, _ := m.Get(mock.ID)
	if mg.ConnectionStatus != StatusOnline {
		t.Fatalf("mock gateway liveness must stay simulated, got %s", mg.ConnectionStatus)
	}
	evs := m.Events(10, real.ID)
	found := false
	for _, e := range evs {
		if e.Type == EvGatewayOffline && e.Metadata["reason"] == "heartbeat_lost" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing heartbeat_lost offline event")
	}

	// A poll proves liveness again.
	if _, ok := m.ConfigFor(real.ID); !ok {
		t.Fatal("config serve failed")
	}
	g, _ = m.Get(real.ID)
	if g.ConnectionStatus == StatusOffline {
		t.Fatal("poll did not clear offline state")
	}
}

func TestVerificationOnlyFromProbesAndResets(t *testing.T) {
	m := realManager(t)
	gw := mustRegister(t, m, "simgw-1")
	snap := ConfigSnapshot{Revision: 1, Policies: []PolicyView{{ID: "pol-1", Type: "ip_block", Target: "203.0.113.7", Action: "deny", Status: "active"}}}
	m.OnRevision(1, "pol-1", snap)
	if err := m.HandleReport(gw.ID, 1, RollAccepted, nil); err != nil {
		t.Fatal(err)
	}

	m.MarkVerification(gw.ID, "pol-1", 1, VerifVerified, nil)
	g, _ := m.Get(gw.ID)
	if g.Verification != VerifVerified {
		t.Fatalf("probe did not verify gateway: %q", g.Verification)
	}
	rows := m.PolicyStatus("pol-1")
	if len(rows) != 1 || rows[0].PolicyVerification == nil || rows[0].PolicyVerification.Result != VerifVerified {
		t.Fatalf("policy status missing probe result: %+v", rows)
	}

	// A failed probe fails the gateway rollup.
	m.MarkVerification(gw.ID, "pol-1", 1, VerifFailed, &EventError{Code: "ENFORCEMENT_MISSING"})
	g, _ = m.Get(gw.ID)
	if g.Verification != VerifFailed {
		t.Fatalf("failed probe not rolled up: %q", g.Verification)
	}

	// A new revision resets verification: the question changed.
	m.MarkVerification(gw.ID, "pol-1", 1, VerifVerified, nil)
	m.OnRevision(2, "pol-2", ConfigSnapshot{Revision: 2})
	g, _ = m.Get(gw.ID)
	if g.Verification != VerifUntested {
		t.Fatalf("new revision must reset verification, got %q", g.Verification)
	}
	rows = m.PolicyStatus("pol-1")
	if rows[0].PolicyVerification != nil {
		t.Fatalf("stale probe record must not survive a new revision: %+v", rows[0].PolicyVerification)
	}
}

func TestGatewayVerificationProbeFailure(t *testing.T) {
	m := realManager(t)
	gw := mustRegister(t, m, "simgw-1")
	m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})

	m.MarkGatewayVerification(gw.ID, 1, VerifFailed, &EventError{Code: "PROBE_FAILED", Message: "unreachable"})
	g, _ := m.Get(gw.ID)
	if g.Verification != VerifFailed {
		t.Fatalf("probe failure not recorded: %q", g.Verification)
	}
}

// TestEnforcementFor covers the per-gateway detail view: every policy in
// the desired-revision snapshot with its probe result (not_tested until a
// probe speaks; stale-revision records never count).
func TestEnforcementFor(t *testing.T) {
	m := realManager(t)
	if rows := m.EnforcementFor("nope"); rows != nil {
		t.Fatalf("unknown gateway must have no rows: %+v", rows)
	}

	gw := mustRegister(t, m, "simgw-1")
	snap := ConfigSnapshot{Revision: 3, Policies: []PolicyView{
		{ID: "pol-1", Type: "ip_block", Target: "203.0.113.7", Action: "deny", Status: "active"},
		{ID: "pol-2", Type: "ip_block", Target: "203.0.113.8", Action: "deny", Status: "active"},
	}}
	m.OnRevision(3, "pol-1", snap)

	rows := m.EnforcementFor(gw.ID)
	if len(rows) != 2 {
		t.Fatalf("want 2 enforcement rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Result != VerifUntested {
			t.Fatalf("pre-probe row must be not_tested: %+v", r)
		}
		if r.Revision != 3 {
			t.Fatalf("row must carry the gateway's desired revision: %+v", r)
		}
	}

	// A probe result shows up for exactly the probed policy.
	m.MarkVerification(gw.ID, "pol-1", 3, VerifVerified, nil)
	rows = m.EnforcementFor(gw.ID)
	byID := map[string]PolicyEnforcement{}
	for _, r := range rows {
		byID[r.PolicyID] = r
	}
	if byID["pol-1"].Result != VerifVerified || byID["pol-1"].At == nil {
		t.Fatalf("pol-1 must be verified: %+v", byID["pol-1"])
	}
	if byID["pol-2"].Result != VerifUntested {
		t.Fatalf("pol-2 must stay not_tested: %+v", byID["pol-2"])
	}

	// A record from an older revision must not satisfy the current view.
	m.MarkVerification(gw.ID, "pol-2", 2, VerifVerified, nil)
	for _, r := range m.EnforcementFor(gw.ID) {
		if r.PolicyID == "pol-2" && r.Result != VerifUntested {
			t.Fatalf("stale-revision record must not count: %+v", r)
		}
	}
}

func TestMockNeverGetsVerificationRecords(t *testing.T) {
	m := realManager(t)
	gw, err := m.CreateGateway("edge-mock", "", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})
	waitFor(t, 2*time.Second, func() bool {
		g, _ := m.Get(gw.ID)
		return g.ReportedAcceptedRevision == 1
	})
	if rows := m.PolicyStatus("pol-1"); len(rows) != 1 || rows[0].PolicyVerification != nil {
		t.Fatalf("mock gateway must never carry verification records: %+v", rows)
	}
	if g, _ := m.Get(gw.ID); g.Verification != VerifUntested {
		t.Fatalf("mock verification advanced without a probe: %q", g.Verification)
	}
}
