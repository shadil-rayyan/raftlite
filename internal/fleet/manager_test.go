package fleet

import (
	"log/slog"
	"testing"
	"time"
)

func testManager(t *testing.T, timeout time.Duration) (*Manager, *MockAdapter) {
	t.Helper()
	ma := NewMockAdapter()
	m := NewManager(ma, slog.New(slog.DiscardHandler))
	if err := m.SetDeliveryTimeout(timeout); err != nil {
		t.Fatalf("set timeout: %v", err)
	}
	t.Cleanup(m.Stop)
	return m, ma
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestAcceptAfterDelay(t *testing.T) {
	m, _ := testManager(t, 5*time.Second)
	gw, err := m.CreateGateway("edge-1", "", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ro := m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})
	if ro.Results[gw.ID].Status != RollSending {
		t.Fatalf("want sending, got %s", ro.Results[gw.ID].Status)
	}
	waitFor(t, 2*time.Second, func() bool {
		g, _ := m.Get(gw.ID)
		return g.ReportedAcceptedRevision == 1
	})
	g, _ := m.Get(gw.ID)
	if g.DesiredRevision != 1 || g.LastError != nil {
		t.Fatalf("unexpected gateway state: %+v", g)
	}
	roll, _ := m.Rollout(ro.ID)
	if roll.Results[gw.ID].Status != RollAccepted {
		t.Fatalf("want accepted, got %s", roll.Results[gw.ID].Status)
	}
}

func TestRejectDoesNotAdvanceRevision(t *testing.T) {
	m, _ := testManager(t, 5*time.Second)
	gw, _ := m.CreateGateway("edge-1", "", 30*time.Millisecond)
	if err := m.Scenario(gw.ID, "reject-next"); err != nil {
		t.Fatal(err)
	}
	m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})
	waitFor(t, 2*time.Second, func() bool {
		g, _ := m.Get(gw.ID)
		return g.LastRejectedRevision != nil
	})
	g, _ := m.Get(gw.ID)
	if g.ReportedAcceptedRevision != 0 {
		t.Fatalf("rejection advanced accepted revision to %d", g.ReportedAcceptedRevision)
	}
	if g.LastError == nil || g.LastError.Code != "INVALID_CONFIGURATION" {
		t.Fatalf("missing structured error: %+v", g.LastError)
	}
	roll, _ := m.Rollout(m.Rollouts()[0].ID)
	if roll.Results[gw.ID].Status != RollRejected {
		t.Fatalf("want rejected, got %s", roll.Results[gw.ID].Status)
	}
}

func TestTimeoutIsNotRejection(t *testing.T) {
	m, _ := testManager(t, 200*time.Millisecond)
	gw, _ := m.CreateGateway("edge-1", "", 50*time.Millisecond)
	if err := m.Scenario(gw.ID, "timeout-next"); err != nil {
		t.Fatal(err)
	}
	m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})
	waitFor(t, 2*time.Second, func() bool {
		roll := m.Rollouts()
		return len(roll) == 1 && roll[0].Results[gw.ID].Status == RollTimeout
	})
	g, _ := m.Get(gw.ID)
	if g.ReportedAcceptedRevision != 0 {
		t.Fatalf("timeout advanced accepted revision to %d", g.ReportedAcceptedRevision)
	}
	if g.LastError != nil {
		t.Fatalf("timeout must not set a rejection error: %+v", g.LastError)
	}
}

func TestOfflineGatewayKeepsStateAndReconciles(t *testing.T) {
	m, _ := testManager(t, 5*time.Second)
	gw, _ := m.CreateGateway("edge-1", "", 30*time.Millisecond)
	m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})
	waitFor(t, 2*time.Second, func() bool {
		g, _ := m.Get(gw.ID)
		return g.ReportedAcceptedRevision == 1
	})

	if err := m.SetOffline(gw.ID); err != nil {
		t.Fatal(err)
	}
	m.OnRevision(2, "pol-2", ConfigSnapshot{Revision: 2})
	g, _ := m.Get(gw.ID)
	if g.DesiredRevision != 2 || g.ReportedAcceptedRevision != 1 {
		t.Fatalf("offline gateway state wrong: desired=%d reported=%d", g.DesiredRevision, g.ReportedAcceptedRevision)
	}
	roll := m.Rollouts()[0]
	if roll.Results[gw.ID].Status != RollOffline {
		t.Fatalf("want offline result, got %s", roll.Results[gw.ID].Status)
	}

	// Reconnect: identity retained, NOT auto-marked current, reconcile scheduled.
	if err := m.SetOnline(gw.ID); err != nil {
		t.Fatal(err)
	}
	g, _ = m.Get(gw.ID)
	if g.ID != gw.ID || g.ReportedAcceptedRevision != 1 {
		t.Fatalf("reconnect lost state: %+v", g)
	}
	waitFor(t, 4*time.Second, func() bool {
		gg, _ := m.Get(gw.ID)
		return gg.ReportedAcceptedRevision == 2
	})
}

func TestStaleResponseIsSuperseded(t *testing.T) {
	m, _ := testManager(t, 5*time.Second)
	slow, _ := m.CreateGateway("slow", "", 400*time.Millisecond)
	fast, _ := m.CreateGateway("fast", "", 30*time.Millisecond)

	m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})
	m.OnRevision(2, "pol-2", ConfigSnapshot{Revision: 2}) // published before slow's rev-1 result lands

	// Fast gateway accepts both in order.
	waitFor(t, 2*time.Second, func() bool {
		g, _ := m.Get(fast.ID)
		return g.ReportedAcceptedRevision == 2
	})
	// Slow gateway's rev-1 response must not overwrite desired rev 2.
	waitFor(t, 2*time.Second, func() bool {
		g, _ := m.Get(slow.ID)
		return g.ReportedAcceptedRevision == 2
	})
	g, _ := m.Get(slow.ID)
	if g.DesiredRevision != 2 {
		t.Fatalf("stale response overwrote desired revision: %d", g.DesiredRevision)
	}
	// The rev-1 rollout's slow result is superseded, not accepted.
	var rev1Result string
	for _, r := range m.Rollouts() {
		if r.Revision == 1 {
			rev1Result = r.Results[slow.ID].Status
		}
	}
	if rev1Result != RollSuperseded {
		t.Fatalf("want superseded, got %s", rev1Result)
	}
}

func TestPerGatewayIndependence(t *testing.T) {
	m, _ := testManager(t, 5*time.Second)
	ok, _ := m.CreateGateway("ok", "", 30*time.Millisecond)
	bad, _ := m.CreateGateway("bad", "", 30*time.Millisecond)
	if err := m.Scenario(bad.ID, "reject-next"); err != nil {
		t.Fatal(err)
	}
	ro := m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})
	waitFor(t, 2*time.Second, func() bool {
		roll := m.Rollouts()
		return len(roll) == 1 &&
			roll[0].Results[ok.ID].Status == RollAccepted &&
			roll[0].Results[bad.ID].Status == RollRejected
	})
	_ = ro
}

func TestDeleteGatewayRules(t *testing.T) {
	m, _ := testManager(t, 5*time.Second)
	gw, _ := m.CreateGateway("busy", "", time.Second) // long delay keeps delivery in flight
	m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})
	if err := m.DeleteGateway(gw.ID); err != ErrInFlight {
		t.Fatalf("want ErrInFlight, got %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		g, _ := m.Get(gw.ID)
		return g.ReportedAcceptedRevision == 1
	})
	if err := m.DeleteGateway(gw.ID); err != nil {
		t.Fatalf("delete after settle: %v", err)
	}
	if _, ok := m.Get(gw.ID); ok {
		t.Fatal("gateway still present after delete")
	}
}

func TestNewGatewayAutoReconciles(t *testing.T) {
	m, _ := testManager(t, 5*time.Second)
	m.OnRevision(3, "pol-3", ConfigSnapshot{Revision: 3}) // no gateways yet
	gw, _ := m.CreateGateway("late", "", 30*time.Millisecond)
	waitFor(t, 2*time.Second, func() bool {
		g, _ := m.Get(gw.ID)
		return g.ReportedAcceptedRevision == 3
	})
}

func TestEventsPreserveHistory(t *testing.T) {
	m, _ := testManager(t, 5*time.Second)
	gw, _ := m.CreateGateway("edge-1", "", 20*time.Millisecond)
	m.OnRevision(1, "pol-1", ConfigSnapshot{Revision: 1})
	waitFor(t, 2*time.Second, func() bool {
		g, _ := m.Get(gw.ID)
		return g.ReportedAcceptedRevision == 1
	})
	evs := m.Events(100, gw.ID)
	saw := map[string]bool{}
	for _, e := range evs {
		saw[e.Type] = true
	}
	for _, want := range []string{EvGatewayCreated, EvDeliveryScheduled, EvConfigAccepted} {
		if !saw[want] {
			t.Fatalf("missing event %s in %d events", want, len(evs))
		}
	}
}
