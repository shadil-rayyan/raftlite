package server

// End-to-end tests for the pull-based envoy-sim gateway loop: register,
// pull config after a policy commit, report acceptance, then ACTIVE PROBES
// through a real data path decide whether the policy is achieved. The mock
// adapter never advances verification.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/fleet"
	"github.com/shadil/raftlite/internal/gateway"
	"github.com/shadil/raftlite/internal/policy"
)

// testChecker is a gateway-side local policy set, exactly like cmd/simgw's:
// evaluated from the applied snapshot, no control-plane round trips.
type testChecker struct {
	pols []fleet.PolicyView
}

func (c *testChecker) IsBlocked(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	now := time.Now()
	for _, p := range c.pols {
		if p.Action != policy.ActionDeny || p.Status != string(policy.StatusActive) {
			continue
		}
		if p.Expires != nil && *p.Expires <= now.Unix() {
			continue
		}
		switch p.Type {
		case string(policy.TypeIPBlock):
			if t, err := netip.ParseAddr(p.Target); err == nil && t == addr {
				return true
			}
		case string(policy.TypeCIDR):
			if pre, err := netip.ParsePrefix(p.Target); err == nil && pre.Contains(addr) {
				return true
			}
		}
	}
	return false
}

// startDataPath runs a real enforcement data path — the same middleware
// cmd/simgw wires: blocklist first, reverse proxy to an echo upstream
// behind it. The test source (127.0.0.1) is trusted as a proxy hop so the
// control plane's probes can carry a client address (SEC-007).
func startDataPath(t *testing.T, checker *testChecker) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("upstream-ok"))
	}))
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	ds := httptest.NewServer(gateway.MiddlewareTrusted(checker, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, rp))
	t.Cleanup(ds.Close)
	return ds
}

// gatewayApply polls the control plane's config endpoint and applies the
// snapshot locally — what cmd/simgw's sync loop does in one step.
func gatewayApply(t *testing.T, h http.Handler, id string, checker *testChecker) uint64 {
	t.Helper()
	rec := getJSON(t, h, "/v1/fleet/gateways/"+id+"/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("config poll: %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	rev := uint64(body["revision"].(float64))
	var pols []fleet.PolicyView
	if raw, ok := body["policies"].([]any); ok {
		for _, p := range raw {
			pm := p.(map[string]any)
			pv := fleet.PolicyView{
				ID:     pm["id"].(string),
				Type:   pm["type"].(string),
				Target: pm["target"].(string),
				Action: pm["action"].(string),
				Status: pm["status"].(string),
			}
			pols = append(pols, pv)
		}
	}
	checker.pols = pols
	return rev
}

// gatewayReport sends the ACK for an applied revision.
func gatewayReport(t *testing.T, h http.Handler, id string, rev uint64, status string) {
	t.Helper()
	rec := postJSON(t, h, "/v1/fleet/gateways/"+id+"/report", "",
		map[string]any{"revision": rev, "status": status})
	if rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRealGatewayRegisterViaAPI(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newFleetAPI(t, node, "", false)
	h := api.Handler()

	rec := postJSON(t, h, "/v1/fleet/gateways", "",
		map[string]any{"id": "simgw-1", "name": "edge", "adapter_type": "envoy-sim", "proxy_url": "http://127.0.0.1:18080"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	gw := decodeBody(t, rec)["gateway"].(map[string]any)
	if gw["adapter_type"] != "envoy-sim" || gw["proxy_url"] != "http://127.0.0.1:18080" {
		t.Fatalf("bad gateway record: %v", gw)
	}
	// Restart-safe: re-registration returns the same identity with 200.
	rec = postJSON(t, h, "/v1/fleet/gateways", "",
		map[string]any{"id": "simgw-1", "name": "edge", "adapter_type": "envoy-sim", "proxy_url": "http://127.0.0.1:18080"})
	if rec.Code != http.StatusOK || decodeBody(t, rec)["existing"] != true {
		t.Fatalf("re-register: %d %s", rec.Code, rec.Body.String())
	}
	// proxy_url is the verification target: required, not optional.
	rec = postJSON(t, h, "/v1/fleet/gateways", "",
		map[string]any{"id": "simgw-2", "name": "edge2", "adapter_type": "envoy-sim"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing proxy_url must be rejected: %d", rec.Code)
	}
}

func TestPolicyCommitRolloutReportLifecycle(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newFleetAPI(t, node, "", false)
	h := api.Handler()

	if rec := postJSON(t, h, "/v1/fleet/gateways", "",
		map[string]any{"id": "simgw-1", "name": "edge", "adapter_type": "envoy-sim", "proxy_url": "http://127.0.0.1:18080"}); rec.Code != http.StatusCreated {
		t.Fatalf("register: %d", rec.Code)
	}
	if rec := postJSON(t, h, "/v1/policies/ip-blocks", "", map[string]any{"ip": "203.0.113.50"}); rec.Code != http.StatusCreated {
		t.Fatalf("create policy: %d %s", rec.Code, rec.Body.String())
	}

	// Publishing must NOT pre-mark acceptance.
	body := decodeBody(t, getJSON(t, h, "/v1/fleet/gateways", ""))
	gw := body["gateways"].([]any)[0].(map[string]any)
	if gw["reported_accepted_revision"] != float64(0) {
		t.Fatalf("acceptance pre-marked: %v", gw)
	}

	checker := &testChecker{}
	rev := gatewayApply(t, h, "simgw-1", checker)
	if rev != 1 {
		t.Fatalf("applied revision %d, want 1", rev)
	}
	gatewayReport(t, h, "simgw-1", rev, fleet.RollAccepted)

	waitForFleet(t, func() bool {
		b := decodeBody(t, getJSON(t, h, "/v1/fleet/gateways", ""))
		g := b["gateways"].([]any)[0].(map[string]any)
		return g["reported_accepted_revision"] == float64(1)
	})
	// The policy status endpoint shows the gateway as applied but NOT yet
	// achieved: acceptance is not enforcement verification.
	pols := decodeBody(t, getJSON(t, h, "/v1/policies", ""))
	pid := pols["policies"].([]any)[0].(map[string]any)["id"].(string)
	ps := decodeBody(t, getJSON(t, h, "/v1/fleet/policies/"+pid+"/status", ""))
	gws := ps["gateways"].([]any)
	row := gws[0].(map[string]any)
	sum := ps["summary"].(map[string]any)
	if row["reported_accepted_revision"] != float64(1) || row["policy_verification"] != nil {
		t.Fatalf("policy status row wrong: %v", row)
	}
	if sum["applied"] != float64(1) || sum["verified"] != float64(0) || sum["not_tested"] != float64(1) {
		t.Fatalf("policy status summary wrong: %v", sum)
	}
}

func TestVerifyFleetOnceProbeLifecycle(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newFleetAPI(t, node, "", false)
	h := api.Handler()

	checker := &testChecker{}
	ds := startDataPath(t, checker)

	if rec := postJSON(t, h, "/v1/fleet/gateways", "",
		map[string]any{"id": "simgw-1", "name": "edge", "adapter_type": "envoy-sim", "proxy_url": ds.URL}); rec.Code != http.StatusCreated {
		t.Fatalf("register: %d", rec.Code)
	}
	if rec := postJSON(t, h, "/v1/policies/ip-blocks", "", map[string]any{"ip": "203.0.113.50"}); rec.Code != http.StatusCreated {
		t.Fatalf("create policy: %d %s", rec.Code, rec.Body.String())
	}

	// The "gateway" applies the snapshot locally and ACKs.
	rev := gatewayApply(t, h, "simgw-1", checker)
	gatewayReport(t, h, "simgw-1", rev, fleet.RollAccepted)

	// The control plane now probes the data path: blocked target must get
	// 429 through the real middleware, control address must get 200.
	api.verifyFleetOnce(context.Background())

	gwBody := decodeBody(t, getJSON(t, h, "/v1/fleet/gateways/simgw-1", ""))
	gw := gwBody["gateway"].(map[string]any)
	if gw["verification"] != "verified" {
		t.Fatalf("probe did not verify the enforcing gateway: %v (events %+v)", gw, gwBody["events"])
	}

	pols := decodeBody(t, getJSON(t, h, "/v1/policies", ""))
	pid := pols["policies"].([]any)[0].(map[string]any)["id"].(string)
	ps := decodeBody(t, getJSON(t, h, "/v1/fleet/policies/"+pid+"/status", ""))
	sum := ps["summary"].(map[string]any)
	if sum["verified"] != float64(1) {
		t.Fatalf("policy not achieved per status: %v", sum)
	}
	row := ps["gateways"].([]any)[0].(map[string]any)
	pv := row["policy_verification"].(map[string]any)
	if pv["result"] != "verified" {
		t.Fatalf("per-policy verification wrong: %v", pv)
	}

	// Now a BROKEN gateway: registered, accepts revisions, but enforces
	// nothing. Probes must fail it — acceptance alone proved nothing.
	if rec := postJSON(t, h, "/v1/policies/ip-blocks", "", map[string]any{"ip": "203.0.113.60"}); rec.Code != http.StatusCreated {
		t.Fatalf("second policy: %d", rec.Code)
	}
	rev = gatewayApply(t, h, "simgw-1", checker)
	checker.pols = nil // enforcement lost (e.g. a bad snapshot translation)
	gatewayReport(t, h, "simgw-1", rev, fleet.RollAccepted)
	api.verifyFleetOnce(context.Background())

	gw = decodeBody(t, getJSON(t, h, "/v1/fleet/gateways/simgw-1", ""))["gateway"].(map[string]any)
	if gw["verification"] != "failed" {
		t.Fatalf("non-enforcing gateway must fail verification: %v", gw)
	}
	ps = decodeBody(t, getJSON(t, h, "/v1/fleet/policies/"+pid+"/status", ""))
	row = ps["gateways"].([]any)[0].(map[string]any)
	pv = row["policy_verification"].(map[string]any)
	if pv["result"] != "failed" {
		t.Fatalf("policy must be reported not achieved: %v", pv)
	}
	errObj := pv["error"].(map[string]any)
	if errObj["code"] != "ENFORCEMENT_MISSING" {
		t.Fatalf("wrong probe error: %v", errObj)
	}
}

func TestVerifySkipsMockAndStaleGateways(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newFleetAPI(t, node, "", false)
	h := api.Handler()

	// A mock gateway accepting revisions must never be probed or verified.
	if rec := postJSON(t, h, "/v1/fleet/gateways", "", map[string]any{"name": "edge-mock", "delay_ms": 1}); rec.Code != http.StatusCreated {
		t.Fatalf("create mock: %d", rec.Code)
	}
	if rec := postJSON(t, h, "/v1/policies/ip-blocks", "", map[string]any{"ip": "203.0.113.50"}); rec.Code != http.StatusCreated {
		t.Fatalf("create policy: %d", rec.Code)
	}
	waitForFleet(t, func() bool {
		b := decodeBody(t, getJSON(t, h, "/v1/fleet/gateways", ""))
		g := b["gateways"].([]any)[0].(map[string]any)
		return g["reported_accepted_revision"] == float64(1)
	})
	api.verifyFleetOnce(context.Background())
	gw := decodeBody(t, getJSON(t, h, "/v1/fleet/gateways/gw-001", ""))["gateway"].(map[string]any)
	if gw["verification"] != "not_tested" {
		t.Fatalf("mock gateway must stay not_tested: %v", gw)
	}

	// A registered envoy-sim gateway that has NOT applied the revision is
	// skipped: stale state is its own (reported) question.
	ds := startDataPath(t, &testChecker{})
	if rec := postJSON(t, h, "/v1/fleet/gateways", "",
		map[string]any{"id": "simgw-stale", "name": "stale", "adapter_type": "envoy-sim", "proxy_url": ds.URL}); rec.Code != http.StatusCreated {
		t.Fatalf("register stale: %d", rec.Code)
	}
	api.verifyFleetOnce(context.Background())
	gw = decodeBody(t, getJSON(t, h, "/v1/fleet/gateways/simgw-stale", ""))["gateway"].(map[string]any)
	if gw["verification"] != "not_tested" {
		t.Fatalf("stale gateway must not be probed: %v", gw)
	}
}

func TestVerifyControlFailureMarksProbeFailed(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newFleetAPI(t, node, "", false)
	h := api.Handler()

	// Data path that 500s everything — even the negative control fails.
	ds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ds.Close()

	if rec := postJSON(t, h, "/v1/fleet/gateways", "",
		map[string]any{"id": "simgw-1", "name": "edge", "adapter_type": "envoy-sim", "proxy_url": ds.URL}); rec.Code != http.StatusCreated {
		t.Fatalf("register: %d", rec.Code)
	}
	if rec := postJSON(t, h, "/v1/policies/ip-blocks", "", map[string]any{"ip": "203.0.113.50"}); rec.Code != http.StatusCreated {
		t.Fatalf("create policy: %d", rec.Code)
	}
	gatewayReport(t, h, "simgw-1", 1, fleet.RollAccepted)

	api.verifyFleetOnce(context.Background())
	gwBody := decodeBody(t, getJSON(t, h, "/v1/fleet/gateways/simgw-1", ""))
	gw := gwBody["gateway"].(map[string]any)
	if gw["verification"] != "failed" {
		t.Fatalf("broken data path must fail verification: %v", gw)
	}
	// No per-policy conclusion was safe; the gateway-level error is PROBE_FAILED.
	events := gwBody["events"].([]any)
	found := false
	for _, e := range events {
		em := e.(map[string]any)
		if em["type"] == "verification" && em["error"] != nil &&
			em["error"].(map[string]any)["code"] == "PROBE_FAILED" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing PROBE_FAILED event: %+v", events)
	}
}
