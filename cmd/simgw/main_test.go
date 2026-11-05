package main

// Tests for the standalone simulated Envoy: registration, pull-based sync,
// fail modes, local enforcement, the SEC-007 trust boundary on the data
// path, and the admin status surface.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/shadil/raftlite/internal/fleet"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeCP is a stand-in control plane recording what the gateway does.
type fakeCP struct {
	mu          sync.Mutex
	revision    uint64
	policies    []fleet.PolicyView
	registered  bool
	registers   int
	fleetGone   bool // simulate leader failover: new leader has an empty fleet
	reports     []map[string]any
	configPolls int
	srv         *httptest.Server
}

func newFakeCP() *fakeCP {
	cp := &fakeCP{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/fleet/gateways", func(w http.ResponseWriter, _ *http.Request) {
		cp.mu.Lock()
		cp.registered = true
		cp.registers++
		cp.fleetGone = false // (re-)registration restores the fleet entry
		cp.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"gateway": map[string]any{"id": "simgw-1"}})
	})
	mux.HandleFunc("GET /v1/fleet/gateways/{id}/config", func(w http.ResponseWriter, _ *http.Request) {
		cp.mu.Lock()
		cp.configPolls++
		gone, rev, pols := cp.fleetGone, cp.revision, cp.policies
		cp.mu.Unlock()
		if gone {
			// What the new leader answers before the gateway re-registers.
			http.Error(w, `{"error":"no such pull-based gateway"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"revision": rev, "policies": pols})
	})
	mux.HandleFunc("POST /v1/fleet/gateways/{id}/report", func(w http.ResponseWriter, r *http.Request) {
		var rep map[string]any
		_ = json.NewDecoder(r.Body).Decode(&rep)
		cp.mu.Lock()
		if cp.fleetGone {
			cp.mu.Unlock()
			http.Error(w, `{"error":"no such gateway"}`, http.StatusNotFound)
			return
		}
		cp.reports = append(cp.reports, rep)
		cp.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "recorded"})
	})
	cp.srv = httptest.NewServer(mux)
	return cp
}

// failoverForget simulates a leader failover: the new leader's in-memory
// fleet is empty, so config/report 404 until the gateway re-registers.
// It also bumps the revision to prove the gateway re-syncs after recovery.
func (cp *fakeCP) failoverForget(newRev uint64, pols []fleet.PolicyView) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.fleetGone = true
	cp.revision = newRev
	cp.policies = pols
}

func (cp *fakeCP) registerCalls() int {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.registers
}

func (cp *fakeCP) reportCount() int {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return len(cp.reports)
}

func (cp *fakeCP) lastReport() map[string]any {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.reports) == 0 {
		return nil
	}
	return cp.reports[len(cp.reports)-1]
}

func (cp *fakeCP) pollCount() int {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.configPolls
}

func testApp(t *testing.T, cp *fakeCP, failMode string) *app {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("upstream-ok"))
	}))
	t.Cleanup(upstream.Close)
	cfg := simgwConfig{
		ID:           "simgw-1",
		Name:         "edge-test",
		ListenAddr:   "127.0.0.1:0",
		AdminAddr:    "127.0.0.1:0",
		Upstream:     upstream.URL,
		ControlPlane: cp.srv.URL,
		ProxyURL:     "http://gateway.test:18080",
		TrustProbes:  []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		FailMode:     failMode,
		PollInterval: time.Second,
	}
	a, err := newApp(cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func denyPolicy(id, target string, expires *int64) fleet.PolicyView {
	return fleet.PolicyView{ID: id, Type: "ip_block", Target: target, Action: "deny", Status: "active", Expires: expires}
}

func TestSyncRegistersAppliesAndReports(t *testing.T) {
	cp := newFakeCP()
	defer cp.srv.Close()
	past := time.Now().Add(-time.Hour).Unix()
	cp.revision = 1
	cp.policies = []fleet.PolicyView{
		denyPolicy("pol-1", "203.0.113.50", nil),
		{ID: "pol-2", Type: "cidr_block", Target: "198.51.100.0/24", Action: "deny", Status: "active"},
		denyPolicy("pol-3", "192.0.2.44", &past), // expired: must not block
	}
	a := testApp(t, cp, "")

	if err := a.syncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !cp.registered {
		t.Fatal("gateway did not register")
	}
	if !a.set.IsBlocked("203.0.113.50") {
		t.Fatal("exact IP policy not enforced")
	}
	if a.set.IsBlocked("203.0.113.51") {
		t.Fatal("unlisted IP wrongly blocked")
	}
	if !a.set.IsBlocked("198.51.100.77") {
		t.Fatal("CIDR policy not enforced")
	}
	if a.set.IsBlocked("192.0.2.44") {
		t.Fatal("expired policy must not enforce")
	}
	rep := cp.lastReport()
	if rep == nil || rep["status"] != fleet.RollAccepted || rep["revision"] != float64(1) {
		t.Fatalf("missing accepted report: %v", rep)
	}

	// Nothing new: no duplicate report, but the poll still counted.
	before := cp.reportCount()
	if err := a.syncOnce(context.Background()); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if cp.reportCount() != before {
		t.Fatalf("duplicate report for the same revision: %v", cp.reports)
	}
}

// TestSyncReregistersAfterLeaderFailover: fleet state is leader-owned and
// in-memory, so after a leader kill the new leader answers 404 until the
// gateway re-registers its idempotent identity. The gateway must self-heal
// — clear the sticky flag on 404, re-register next tick, and catch up on
// the revision it missed while the fleet was gone.
func TestSyncReregistersAfterLeaderFailover(t *testing.T) {
	cp := newFakeCP()
	defer cp.srv.Close()
	cp.revision = 1
	cp.policies = []fleet.PolicyView{denyPolicy("pol-1", "203.0.113.50", nil)}
	a := testApp(t, cp, "")

	if err := a.syncOnce(context.Background()); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if got := cp.registerCalls(); got != 1 {
		t.Fatalf("want 1 register, got %d", got)
	}

	// Leader failover: new leader has an empty fleet (404) and, once the
	// gateway is back in, carries a newer revision.
	cp.failoverForget(2, []fleet.PolicyView{denyPolicy("pol-2", "203.0.113.60", nil)})

	if err := a.syncOnce(context.Background()); err == nil {
		t.Fatal("config poll against an empty fleet must fail")
	}
	a.mu.Lock()
	stillRegistered := a.registered
	a.mu.Unlock()
	if stillRegistered {
		t.Fatal("a 404 config poll must clear the sticky registered flag")
	}

	// Next tick self-heals: re-register, then apply + report the new rev.
	if err := a.syncOnce(context.Background()); err != nil {
		t.Fatalf("healing sync: %v", err)
	}
	if got := cp.registerCalls(); got != 2 {
		t.Fatalf("want re-registration (2 registers), got %d", got)
	}
	if !a.set.IsBlocked("203.0.113.60") {
		t.Fatal("gateway must catch up on the revision it missed")
	}
	rep := cp.lastReport()
	if rep == nil || rep["status"] != fleet.RollAccepted || rep["revision"] != float64(2) {
		t.Fatalf("want accepted report for revision 2, got %v", rep)
	}
}

func TestSyncRejectModeDoesNotApply(t *testing.T) {
	cp := newFakeCP()
	defer cp.srv.Close()
	cp.revision = 1
	cp.policies = []fleet.PolicyView{denyPolicy("pol-1", "203.0.113.50", nil)}
	a := testApp(t, cp, "reject")

	if err := a.syncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	rep := cp.lastReport()
	if rep == nil || rep["status"] != fleet.RollRejected {
		t.Fatalf("want rejected report: %v", rep)
	}
	if a.set.IsBlocked("203.0.113.50") {
		t.Fatal("a rejected configuration must not be running")
	}
}

func TestSyncTimeoutModeAppliesWithoutReporting(t *testing.T) {
	cp := newFakeCP()
	defer cp.srv.Close()
	cp.revision = 1
	cp.policies = []fleet.PolicyView{denyPolicy("pol-1", "203.0.113.50", nil)}
	a := testApp(t, cp, "timeout")

	if err := a.syncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !a.set.IsBlocked("203.0.113.50") {
		t.Fatal("timeout scenario applies locally; enforcement must be active")
	}
	if cp.reportCount() != 0 {
		t.Fatalf("timeout scenario must never report: %v", cp.reports)
	}
}

func TestSyncOfflineModeKeepsLastKnownGood(t *testing.T) {
	cp := newFakeCP()
	defer cp.srv.Close()
	cp.revision = 1
	cp.policies = []fleet.PolicyView{denyPolicy("pol-1", "203.0.113.50", nil)}
	a := testApp(t, cp, "offline")

	if err := a.syncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if cp.pollCount() != 0 || cp.reportCount() != 0 {
		t.Fatalf("offline mode must not talk to the control plane (polls=%d reports=%d)", cp.pollCount(), cp.reportCount())
	}
	if a.set.IsBlocked("203.0.113.50") {
		t.Fatal("offline gateway never received config; must not enforce invented state")
	}
	// After a real apply, going offline keeps enforcing last-known-good.
	a.cfg.FailMode = ""
	if err := a.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.cfg.FailMode = "offline"
	if err := a.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.set.IsBlocked("203.0.113.50") {
		t.Fatal("offline mode must keep enforcing last-known-good (FR-020)")
	}
}

func TestProxyEnforcesAndTrustBoundary(t *testing.T) {
	cp := newFakeCP()
	defer cp.srv.Close()
	a := testApp(t, cp, "")
	a.set.apply(1, []fleet.PolicyView{denyPolicy("pol-1", "203.0.113.50", nil)})

	// Blocked source (trusted probe hop carries the address): 429, no upstream.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://gateway.test/some/path", nil)
	req.RemoteAddr = "127.0.0.1:5555" // the control plane's probe source
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	a.proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked source must get 429, got %d", rec.Code)
	}

	// Allowed source reaches the upstream through the proxy.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://gateway.test/some/path", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	a.proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "upstream-ok" {
		t.Fatalf("allowed source must reach upstream, got %d %q", rec.Code, rec.Body.String())
	}

	// SEC-007: from an untrusted peer the header is attacker-controlled
	// and must be ignored — the blocked source cannot forge its way out.
	cp2 := newFakeCP()
	defer cp2.srv.Close()
	b := testApp(t, cp2, "")
	b.cfg.TrustProbes = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")} // test peer is not in here
	b2, err := newApp(b.cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	b2.set.apply(1, []fleet.PolicyView{denyPolicy("pol-1", "203.0.113.50", nil)})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://gateway.test/", nil)
	req.RemoteAddr = "203.0.113.50:4444" // direct peer IS the blocked source
	b2.proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked direct peer must get 429 even without trusted hops, got %d", rec.Code)
	}
}

func TestAdminStatusSurfaces(t *testing.T) {
	cp := newFakeCP()
	defer cp.srv.Close()
	cp.revision = 3
	cp.policies = []fleet.PolicyView{denyPolicy("pol-1", "203.0.113.50", nil)}
	a := testApp(t, cp, "")
	if err := a.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	admin := a.adminHandler()
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("status JSON: %v", err)
	}
	if st["applied_revision"] != float64(3) || st["policy_count"] != float64(1) || st["registered"] != true {
		t.Fatalf("status wrong: %v", st)
	}
	if st["role"] != "simulated-envoy data plane" {
		t.Fatalf("status role wrong: %v", st)
	}

	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/policies", nil))
	var pols struct {
		Revision uint64             `json:"revision"`
		Policies []fleet.PolicyView `json:"policies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pols); err != nil {
		t.Fatalf("policies JSON: %v", err)
	}
	if pols.Revision != 3 || len(pols.Policies) != 1 {
		t.Fatalf("policies wrong: %v", pols)
	}
}
