package server

// Linearizable read gate tests (Phase 4, P0-D): leader-side ReadIndex
// confirmation, follower forwarding with a stale local FSM, the stale
// opt-in, and the no-leader / unreachable-leader failure modes.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shadil/raftlite/internal/audit"
	"github.com/shadil/raftlite/internal/consensus"
)

// readViewNode is a fakeNode that also implements ReadView so tests can
// observe (and fail) the leader's leadership confirmation. It must be the
// API's node — not the embedded fakeNode — or the optional interface
// assertion in readGate would not see ReadIndex.
type readViewNode struct {
	*fakeNode
	readErr   error
	readCalls int
}

func (f *readViewNode) ReadIndex(context.Context) (uint64, error) {
	f.readCalls++
	return 1, f.readErr
}

func newReadGateAPI(t *testing.T, node *readViewNode) (*API, *consensus.FSM) {
	t.Helper()
	fsm := consensus.NewFSM()
	node.fsm = fsm
	api := NewAPI(node, fsm, audit.New(io.Discard), "", nil)
	return api, fsm
}

func TestLeaderReadConfirmsLeadership(t *testing.T) {
	node := &readViewNode{fakeNode: &fakeNode{
		id: "n1", state: consensus.Leader, leader: "n1", peers: []string{"n1"},
	}}
	api, _ := newReadGateAPI(t, node)
	h := api.Handler()

	// Writes do not touch the gate.
	if rec := postJSON(t, h, "/v1/policies/ip-blocks", "", map[string]string{"ip": "10.0.0.1"}); rec.Code != http.StatusCreated {
		t.Fatalf("seed create: %d %s", rec.Code, rec.Body.String())
	}

	if rec := getJSON(t, h, "/v1/policies", ""); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if node.readCalls != 1 {
		t.Fatalf("list did not confirm via ReadIndex: calls=%d", node.readCalls)
	}

	if rec := getJSON(t, h, "/v1/blocked/10.0.0.1", ""); rec.Code != http.StatusOK {
		t.Fatalf("blocked: %d %s", rec.Code, rec.Body.String())
	}
	if node.readCalls != 2 {
		t.Fatalf("blocked did not confirm via ReadIndex: calls=%d", node.readCalls)
	}
	if m := decodeBody(t, getJSON(t, h, "/v1/policies", "")); m["revision"] == nil {
		t.Fatalf("missing revision: %v", m)
	}
}

func TestLeaderReadConfirmationFailureReturns503(t *testing.T) {
	node := &readViewNode{
		fakeNode: &fakeNode{id: "n1", state: consensus.Leader, leader: "n1", peers: []string{"n1"}},
		readErr:  errors.New("no quorum"),
	}
	api, _ := newReadGateAPI(t, node)
	h := api.Handler()

	for _, path := range []string{"/v1/policies", "/v1/blocked/10.0.0.1"} {
		rec := getJSON(t, h, path, "")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status %d, want 503 (%s)", path, rec.Code, rec.Body.String())
		}
		if m := decodeBody(t, rec); m["error"] != "read_unavailable" {
			t.Fatalf("%s: error %v, want read_unavailable", path, m["error"])
		}
	}
	if node.readCalls != 2 {
		t.Fatalf("expected both reads to attempt confirmation: calls=%d", node.readCalls)
	}
}

func TestStaleOptInSkipsConfirmation(t *testing.T) {
	node := &readViewNode{
		fakeNode: &fakeNode{id: "n1", state: consensus.Leader, leader: "n1", peers: []string{"n1"}},
		readErr:  errors.New("would fail"),
	}
	api, _ := newReadGateAPI(t, node)
	h := api.Handler()

	if rec := getJSON(t, h, "/v1/policies?consistency=stale", ""); rec.Code != http.StatusOK {
		t.Fatalf("stale list: %d %s", rec.Code, rec.Body.String())
	}
	if node.readCalls != 0 {
		t.Fatalf("stale read must not confirm leadership: calls=%d", node.readCalls)
	}
	if rec := getJSON(t, h, "/v1/blocked/10.0.0.1?consistency=stale", ""); rec.Code != http.StatusOK {
		t.Fatalf("stale blocked: %d %s", rec.Code, rec.Body.String())
	}
	if node.readCalls != 0 {
		t.Fatalf("stale read must not confirm leadership: calls=%d", node.readCalls)
	}
}

// The stale-follower scenario: the leader has committed a policy the
// follower's local FSM never saw. A default read must return the leader's
// state (forwarded), while ?consistency=stale documents the local lag.
func TestFollowerReadsForwardToLeaderNotLocalState(t *testing.T) {
	leaderNode := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1", peers: []string{"n1", "n2"}}
	followerNode := &fakeNode{id: "n2", state: consensus.Follower, leader: "n1", peers: []string{"n1", "n2"}}
	leaderAPI, _ := newTestAPI(t, leaderNode, "")
	followerAPI, _ := newTestAPI(t, followerNode, "")

	leaderSrv := httptest.NewServer(leaderAPI.Handler())
	defer leaderSrv.Close()
	followerAPI.SetPeerAdminAddrs(map[string]string{"n1": leaderSrv.URL})

	// Commit on the leader only; the follower FSM stays empty (stale).
	if rec := postJSON(t, leaderAPI.Handler(), "/v1/policies/ip-blocks", "", map[string]string{"ip": "203.0.113.20"}); rec.Code != http.StatusCreated {
		t.Fatalf("leader create: %d %s", rec.Code, rec.Body.String())
	}

	// Default read: forwarded — the follower's lagging FSM must not answer.
	rec := getJSON(t, followerAPI.Handler(), "/v1/policies", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("forwarded list: %d %s", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	pols, _ := m["policies"].([]any)
	if len(pols) != 1 {
		t.Fatalf("expected the leader's policy, got %d (stale local state leaked)", len(pols))
	}

	// Stale opt-in: the local, knowingly-lagging view is served.
	rec = getJSON(t, followerAPI.Handler(), "/v1/policies?consistency=stale", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stale list: %d %s", rec.Code, rec.Body.String())
	}
	m = decodeBody(t, rec)
	if pols, _ = m["policies"].([]any); len(pols) != 0 {
		t.Fatalf("stale read should show the empty local FSM, got %d", len(pols))
	}
}

func TestFollowerReadWithoutLeaderReturns503(t *testing.T) {
	node := &fakeNode{id: "n2", state: consensus.Follower, leader: "", peers: []string{"n1", "n2"}}
	api, _ := newTestAPI(t, node, "")
	for _, path := range []string{"/v1/policies", "/v1/blocked/10.0.0.1"} {
		rec := getJSON(t, api.Handler(), path, "")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status %d, want 503 (%s)", path, rec.Code, rec.Body.String())
		}
		if m := decodeBody(t, rec); m["error"] != "no_leader" {
			t.Fatalf("%s: error %v, want no_leader", path, m["error"])
		}
	}
}

func TestFollowerReadUnreachableLeaderReturns503(t *testing.T) {
	// Leader known by name but no admin address configured: we must not
	// silently serve the local state.
	node := &fakeNode{id: "n2", state: consensus.Follower, leader: "n1", peers: []string{"n1", "n2"}}
	api, _ := newTestAPI(t, node, "")
	rec := getJSON(t, api.Handler(), "/v1/policies", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	if m["error"] != "unreachable_leader" {
		t.Fatalf("error %v, want unreachable_leader", m["error"])
	}
}

func TestFollowerReadRedirectsWhenProxyFails(t *testing.T) {
	// peerAddrs points at an unreachable leader: forwarding fails, so the
	// gate falls back to a browser-facing 307 instead of a stale read.
	node := &fakeNode{id: "n2", state: consensus.Follower, leader: "n1", peers: []string{"n1", "n2"}}
	api, _ := newTestAPI(t, node, "")
	api.SetPeerAdminAddrs(map[string]string{"n1": "http://127.0.0.1:1"})

	rec := getJSON(t, api.Handler(), "/v1/policies", "")
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status %d, want 307 (%s)", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc != "http://127.0.0.1:1/v1/policies" {
		t.Fatalf("location %q", loc)
	}
}
