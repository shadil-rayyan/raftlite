package server

// Fleet endpoint contract tests: registry, auto-rollout on policy commit,
// leader forwarding, and dev-control gating.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/fleet"
)

func newFleetAPI(t *testing.T, node *fakeNode, token string, dev bool) (*API, *consensus.FSM) {
	t.Helper()
	api, fsm := newTestAPI(t, node, token)
	api.SetFleetManager(fleet.NewManager(fleet.NewMockAdapter(), nil))
	api.SetDevMode(dev)
	return api, fsm
}

func waitForFleet(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fleet condition not met")
}

func getJSON(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func delReq(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFleetCreateAndListGateways(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newFleetAPI(t, node, "", true)
	h := api.Handler()

	rec := postJSON(t, h, "/v1/fleet/gateways", "", map[string]any{"name": "edge-1", "delay_ms": 20})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create gateway: %d %s", rec.Code, rec.Body.String())
	}
	rec = getJSON(t, h, "/v1/fleet/gateways", "")
	body := decodeBody(t, rec)
	sum := body["summary"].(map[string]any)
	if sum["total"] != float64(1) || sum["online"] != float64(1) {
		t.Fatalf("unexpected summary: %v", sum)
	}
}

func TestPolicyCommitTriggersRollout(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newFleetAPI(t, node, "", true)
	h := api.Handler()

	rec := postJSON(t, h, "/v1/fleet/gateways", "", map[string]any{"name": "edge-1", "delay_ms": 30})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create gateway: %d", rec.Code)
	}

	// Publishing a policy must NOT immediately mark it accepted.
	rec = postJSON(t, h, "/v1/policies/ip-blocks", "", map[string]any{"ip": "203.0.113.50", "name": "sim block"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create policy: %d %s", rec.Code, rec.Body.String())
	}
	gwBody := decodeBody(t, getJSON(t, h, "/v1/fleet/gateways", ""))
	gw := gwBody["gateways"].([]any)[0].(map[string]any)
	if gw["reported_accepted_revision"] != float64(0) {
		t.Fatalf("acceptance was pre-marked: %v", gw)
	}

	waitForFleet(t, func() bool {
		b := decodeBody(t, getJSON(t, h, "/v1/fleet/gateways", ""))
		g := b["gateways"].([]any)[0].(map[string]any)
		return g["reported_accepted_revision"] == float64(1)
	})
}

func TestDevControlsGated(t *testing.T) {
	leader := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}

	// Without dev mode the control routes do not exist (404, or 405 when
	// ServeMux reports the method mismatch against the catch-all).
	off, _ := newFleetAPI(t, leader, "", false)
	if rec := postJSON(t, off.Handler(), "/dev/mock-gateways", "", map[string]any{"name": "x"}); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("dev route must be absent without dev mode, got %d", rec.Code)
	}

	on, _ := newFleetAPI(t, leader, "secret", true)
	h := on.Handler()
	if rec := postJSON(t, h, "/dev/mock-gateways", "", map[string]any{"name": "x"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("dev controls must require auth, got %d", rec.Code)
	}
	rec := postJSON(t, h, "/dev/mock-gateways", "secret", map[string]any{"name": "edge-1", "delay_ms": 20})
	if rec.Code != http.StatusCreated {
		t.Fatalf("dev create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(t, h, "/dev/mock-gateways/gw-001/scenarios/reject-next", "secret", nil); rec.Code != http.StatusOK {
		t.Fatalf("reject-next: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(t, h, "/dev/mock-gateways/gw-001/scenarios/bogus", "secret", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown scenario: %d", rec.Code)
	}
	if rec := delReq(t, h, "/dev/mock-gateways/gw-001", "secret"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFleetForwardsToFollowerLeader(t *testing.T) {
	// Leader serves the fleet; follower forwards GETs to it.
	leaderNode := &fakeNode{id: "n2", state: consensus.Leader, leader: "n2"}
	leaderAPI, _ := newFleetAPI(t, leaderNode, "", true)
	leaderSrv := httptest.NewServer(leaderAPI.Handler())
	defer leaderSrv.Close()

	followerNode := &fakeNode{id: "n1", state: consensus.Follower, leader: "n2"}
	followerAPI, _ := newFleetAPI(t, followerNode, "", true)
	followerAPI.SetPeerAdminAddrs(map[string]string{"n2": leaderSrv.URL})
	followerH := followerAPI.Handler()

	// Create via the follower: forwarded to the leader.
	rec := postJSON(t, followerH, "/v1/fleet/gateways", "", map[string]any{"name": "edge-1", "delay_ms": 20})
	if rec.Code != http.StatusCreated {
		t.Fatalf("forwarded create: %d %s", rec.Code, rec.Body.String())
	}
	// Read via the follower: forwarded too, sees the leader's state.
	rec = getJSON(t, followerH, "/v1/fleet/gateways", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("forwarded list: %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["summary"].(map[string]any)["total"] != float64(1) {
		t.Fatalf("forwarded read returned wrong data: %v", body)
	}
}

func TestFleetGatewayDetailAndErrors(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newFleetAPI(t, node, "", true)
	h := api.Handler()

	postJSON(t, h, "/v1/fleet/gateways", "", map[string]any{"name": "edge-1", "delay_ms": 20})
	rec := getJSON(t, h, "/v1/fleet/gateways/gw-001", "")
	body := decodeBody(t, rec)
	if body["gateway"].(map[string]any)["verification"] != "not_tested" {
		t.Fatalf("mock gateway must start not_tested: %v", body["gateway"])
	}
	if _, ok := body["events"].([]any); !ok {
		t.Fatal("detail must include events")
	}
	if rec := getJSON(t, h, "/v1/fleet/gateways/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown gateway: %d", rec.Code)
	}
	if rec := getJSON(t, h, "/v1/fleet/rollouts/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown rollout: %d", rec.Code)
	}
	// Reconcile with nothing behind → conflict.
	if rec := postJSON(t, h, "/v1/fleet/reconcile", "", nil); rec.Code != http.StatusConflict {
		t.Fatalf("reconcile with nothing behind: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteGatewayInFlightConflict(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newFleetAPI(t, node, "", true)
	h := api.Handler()

	postJSON(t, h, "/v1/fleet/gateways", "", map[string]any{"name": "busy", "delay_ms": 60000})
	postJSON(t, h, "/v1/policies/ip-blocks", "", map[string]any{"ip": "203.0.113.60"})
	if rec := delReq(t, h, "/dev/mock-gateways/gw-001", ""); rec.Code != http.StatusConflict {
		t.Fatalf("want 409 while in flight, got %d", rec.Code)
	}
}
