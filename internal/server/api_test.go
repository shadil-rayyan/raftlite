package server

// Admin API contract tests (SRS §6.1). A fake NodeView stands in for
// consensus so the HTTP contract — auth, validation, outcome mapping,
// idempotency, redirects — is tested without a cluster.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shadil/raftlite/internal/audit"
	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/transport"
)

// fakeNode implements NodeView. ProposeAndWait applies the command to the FSM
// exactly like a real leader does, so read endpoints reflect committed writes.
type fakeNode struct {
	id       string
	state    consensus.NodeState
	leader   string
	peers    []string
	err      error // injected consensus failure
	fsm      *consensus.FSM
	proposed [][]byte
	nextIdx  uint64
	term     uint64
	lastLog  uint64
	commit   uint64
}

func (f *fakeNode) ID() string                 { return f.id }
func (f *fakeNode) State() consensus.NodeState { return f.state }
func (f *fakeNode) LeaderID() string           { return f.leader }
func (f *fakeNode) Peers() []string            { return append([]string(nil), f.peers...) }
func (f *fakeNode) CurrentTerm() uint64        { return f.term }
func (f *fakeNode) LastLogIndex() uint64       { return f.lastLog }
func (f *fakeNode) CommitIndex() uint64        { return f.commit }

func (f *fakeNode) ProposeAndWait(_ context.Context, data []byte) (uint64, error) {
	f.proposed = append(f.proposed, data)
	if f.err != nil {
		return 0, f.err
	}
	f.nextIdx++
	f.fsm.Apply(data, f.nextIdx) // mirror the real leader's commit path
	return f.nextIdx, nil
}

func newTestAPI(t *testing.T, node *fakeNode, token string) (*API, *consensus.FSM) {
	t.Helper()
	fsm := consensus.NewFSM()
	node.fsm = fsm
	api := NewAPI(node, fsm, audit.New(io.Discard), token, nil)
	return api, fsm
}

func postJSON(t *testing.T, h http.Handler, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad JSON response %q: %v", rec.Body.String(), err)
	}
	return m
}

// ---- liveness vs readiness ----

func TestHealthzIsAlwaysOK(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Follower}
	api, _ := newTestAPI(t, node, "")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness must not depend on cluster state: %d", rec.Code)
	}
}

// FR-028: readiness reports write capability honestly — only the leader is
// write-ready; a follower is still ready for reads.
func TestReadyzDistinguishesWriteReadiness(t *testing.T) {
	follower := &fakeNode{id: "n1", state: consensus.Follower}
	api, _ := newTestAPI(t, follower, "")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	body := decodeBody(t, rec)
	if body["write_ready"] != false {
		t.Fatalf("follower claimed write readiness: %v", body)
	}
	if body["role"] != "follower" {
		t.Fatalf("unexpected role: %v", body)
	}

	leader := &fakeNode{id: "n2", state: consensus.Leader}
	api2, _ := newTestAPI(t, leader, "")
	rec2 := httptest.NewRecorder()
	api2.Handler().ServeHTTP(rec2, httptest.NewRequest("GET", "/readyz", nil))
	if decodeBody(t, rec2)["write_ready"] != true {
		t.Fatal("leader not write-ready")
	}
}

// ---- authentication (SEC-001) ----

func TestWriteRequiresBearerToken(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newTestAPI(t, node, "s3cret")
	h := api.Handler()

	rec := postJSON(t, h, "/v1/policies/ip-blocks", "", map[string]any{"ip": "10.0.0.1"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated write accepted: %d", rec.Code)
	}
	rec = postJSON(t, h, "/v1/policies/ip-blocks", "wrong", map[string]any{"ip": "10.0.0.1"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token accepted: %d", rec.Code)
	}
	if len(node.proposed) != 0 {
		t.Fatalf("rejected auth reached consensus: %d proposes", len(node.proposed))
	}

	rec = postJSON(t, h, "/v1/policies/ip-blocks", "s3cret", map[string]any{"ip": "10.0.0.1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("authenticated write failed: %d %s", rec.Code, rec.Body.String())
	}
}

// ---- validation before the log (FR-015, SEC-006) ----

func TestInvalidInputsNeverReachConsensus(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newTestAPI(t, node, "")
	h := api.Handler()

	cases := []struct {
		name string
		body map[string]any
	}{
		{"garbage ip", map[string]any{"ip": "not-an-ip"}},
		{"private-any", map[string]any{"ip": "0.0.0.0"}},
		{"multicast", map[string]any{"ip": "224.0.0.1"}},
		{"bad duration", map[string]any{"ip": "10.0.0.1", "duration": "-5m"}},
		{"absurd duration", map[string]any{"ip": "10.0.0.1", "duration": "90000h"}},
	}
	for _, tc := range cases {
		rec := postJSON(t, h, "/v1/policies/ip-blocks", "", tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d (%s)", tc.name, rec.Code, rec.Body.String())
		}
	}
	if len(node.proposed) != 0 {
		t.Fatalf("invalid input entered the log: %d proposes", len(node.proposed))
	}

	// Malformed JSON body.
	req := httptest.NewRequest("POST", "/v1/policies/ip-blocks", bytes.NewReader([]byte("{oops")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON: expected 400, got %d", rec.Code)
	}
}

// ---- the committed lifecycle ----

// PRD §13.1: a successful write reports committed state with its log index,
// and the policy is immediately visible to reads on the same node.
func TestCreateThenReadBack(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newTestAPI(t, node, "")
	h := api.Handler()

	rec := postJSON(t, h, "/v1/policies/ip-blocks", "", map[string]any{
		"ip": "203.0.113.9", "reason": "abuse", "created_by": "secops",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["status"] != "committed" {
		t.Fatalf("expected committed outcome, got %v", body["status"])
	}
	id, _ := body["policy_id"].(string)
	if id == "" {
		t.Fatal("missing policy_id")
	}
	if idx, _ := body["raft_index"].(float64); idx < 1 {
		t.Fatalf("raft_index missing: %v", body)
	}

	// GET the policy.
	req := httptest.NewRequest("GET", "/v1/policies/"+id, nil)
	get := httptest.NewRecorder()
	h.ServeHTTP(get, req)
	if get.Code != http.StatusOK {
		t.Fatalf("get policy: %d", get.Code)
	}
	pol := decodeBody(t, get)
	if pol["target"] != "203.0.113.9" || pol["action"] != "deny" || pol["status"] != "active" {
		t.Fatalf("policy shape wrong: %v", pol)
	}

	// /v1/blocked/{ip} reflects the committed policy.
	req = httptest.NewRequest("GET", "/v1/blocked/203.0.113.9", nil)
	blocked := httptest.NewRecorder()
	h.ServeHTTP(blocked, req)
	if blocked.Code != http.StatusOK {
		t.Fatalf("blocked lookup: %d", blocked.Code)
	}
	if b := decodeBody(t, blocked); b["blocked"] != true || b["policy_id"] != id {
		t.Fatalf("blocked lookup wrong: %v", b)
	}

	// An unrelated address is not blocked.
	req = httptest.NewRequest("GET", "/v1/blocked/203.0.113.10", nil)
	clear := httptest.NewRecorder()
	h.ServeHTTP(clear, req)
	if decodeBody(t, clear)["blocked"] != false {
		t.Fatal("unrelated address blocked")
	}

	// Revoke, then the address is released.
	req = httptest.NewRequest("DELETE", "/v1/policies/"+id+"?reason=test", nil)
	del := httptest.NewRecorder()
	h.ServeHTTP(del, req)
	if del.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", del.Code, del.Body.String())
	}
	req = httptest.NewRequest("GET", "/v1/blocked/203.0.113.9", nil)
	after := httptest.NewRecorder()
	h.ServeHTTP(after, req)
	if decodeBody(t, after)["blocked"] != false {
		t.Fatal("revoked policy still blocks")
	}
}

func TestRevokeUnknownPolicyIs404(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newTestAPI(t, node, "")
	req := httptest.NewRequest("DELETE", "/v1/policies/pol-ghost", nil)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if len(node.proposed) != 0 {
		t.Fatal("revoke of unknown policy entered the log")
	}
}

// ---- leader redirection (FR-007) ----

// A follower never fakes a commit: it answers 307 pointing at the leader.
func TestFollowerRedirectsToLeader(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Follower, leader: "n2", peers: []string{"n1", "n2"}}
	api, _ := newTestAPI(t, node, "")
	api.SetPeerAdminAddrs(map[string]string{"n2": "http://10.0.0.2:8080"})

	rec := postJSON(t, api.Handler(), "/v1/policies/ip-blocks", "", map[string]any{"ip": "10.0.0.1"})
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307, got %d (%s)", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc != "http://10.0.0.2:8080/v1/policies/ip-blocks" {
		t.Fatalf("wrong Location: %q", loc)
	}
	if len(node.proposed) != 0 {
		t.Fatal("follower proposed a write locally")
	}
}

// With no leader elected, the answer is 503 no_leader — not a fake commit.
func TestNoLeaderReturns503(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Follower, leader: "", peers: []string{"n1", "n2"}}
	api, _ := newTestAPI(t, node, "")
	rec := postJSON(t, api.Handler(), "/v1/policies/ip-blocks", "", map[string]any{"ip": "10.0.0.1"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if decodeBody(t, rec)["error"] != "no_leader" {
		t.Fatalf("wrong error code: %v", decodeBody(t, rec))
	}
}

// ---- uncertain outcomes (FR-007, SRS §8) ----

// A commit-wait timeout is NEVER reported as failure: the client gets an
// explicit "uncertain" outcome with recovery instructions.
func TestQuorumTimeoutIsUncertainNotFailed(t *testing.T) {
	node := &fakeNode{
		id: "n1", state: consensus.Leader, leader: "n1",
		err: consensus.ErrNoQuorum,
	}
	api, _ := newTestAPI(t, node, "")
	rec := postJSON(t, api.Handler(), "/v1/policies/ip-blocks", "", map[string]any{
		"ip": "10.0.0.1", "request_id": "req-1",
	})
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["error"] != "outcome_uncertain" {
		t.Fatalf("timeout misreported as %v", body["error"])
	}
	if msg, _ := body["message"].(string); msg == "" {
		t.Fatal("missing recovery guidance in uncertain response")
	}
}

// ---- idempotency (FR-016) ----

// A retried request_id replays the original outcome without a second commit.
func TestIdempotentCreateReplay(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1"}
	api, _ := newTestAPI(t, node, "")
	h := api.Handler()

	body := map[string]any{"ip": "198.51.100.7", "request_id": "req-42"}
	first := postJSON(t, h, "/v1/policies/ip-blocks", "", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", first.Code, first.Body.String())
	}
	id1 := decodeBody(t, first)["policy_id"]

	second := postJSON(t, h, "/v1/policies/ip-blocks", "", body)
	if second.Code != http.StatusOK {
		t.Fatalf("replay: expected 200, got %d", second.Code)
	}
	b2 := decodeBody(t, second)
	if b2["policy_id"] != id1 {
		t.Fatalf("replay produced a different policy: %v vs %v", b2["policy_id"], id1)
	}
	if b2["idempotent"] != true {
		t.Fatalf("replay not flagged idempotent: %v", b2)
	}
	if len(node.proposed) != 1 {
		t.Fatalf("replay re-committed: %d proposes", len(node.proposed))
	}
}

// ---- cluster/leader introspection ----

func TestLeaderAndClusterEndpoints(t *testing.T) {
	node := &fakeNode{
		id: "n1", state: consensus.Follower, leader: "n2",
		peers: []string{"n1", "n2", "n3"},
	}
	api, _ := newTestAPI(t, node, "")
	h := api.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/leader", nil))
	if decodeBody(t, rec)["leader_id"] != "n2" {
		t.Fatal("leader endpoint wrong")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/cluster", nil))
	body := decodeBody(t, rec)
	if body["leader_id"] != "n2" || body["role"] != "follower" {
		t.Fatalf("cluster endpoint wrong: %v", body)
	}
	if peers, _ := body["peers"].([]any); len(peers) != 3 {
		t.Fatalf("peers wrong: %v", body["peers"])
	}
}

// Audit entries record the true outcome — an uncertain write must never be
// logged as committed (FR-031).
func TestAuditRecordsTrueOutcome(t *testing.T) {
	var buf bytes.Buffer
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1", err: consensus.ErrNoQuorum}
	fsm := consensus.NewFSM()
	node.fsm = fsm
	api := NewAPI(node, fsm, audit.New(&buf), "", nil)

	postJSON(t, api.Handler(), "/v1/policies/ip-blocks", "", map[string]any{"ip": "10.0.0.1"})

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("audit line not JSON: %v (%s)", err, buf.String())
	}
	if entry["outcome"] != "uncertain" {
		t.Fatalf("audit records %v for an unconfirmed write", entry["outcome"])
	}
	if entry["operation"] != "create_ip_block" || entry["node_id"] != "n1" {
		t.Fatalf("audit entry incomplete: %v", entry)
	}
}

// The storage interface round-trip used by the WAL hard-state tests must stay
// implementable by both real and mock storage (compile-time assertion).
var _ transport.Storage = (*transport.MockStorage)(nil)
var _ transport.Storage = (*transport.WAL)(nil)

// ---- diagnostics / node / audit / ui ----

func TestNodeEndpointReportsConsensusState(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1", peers: []string{"n1"}, term: 3, lastLog: 7, commit: 5}
	api, _ := newTestAPI(t, node, "")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/node", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["node_id"] != "n1" || m["role"] != "leader" || m["term"].(float64) != 3 {
		t.Fatalf("bad node payload: %+v", m)
	}
	if m["last_log_index"].(float64) != 7 || m["commit_index"].(float64) != 5 {
		t.Fatalf("bad log/commit: %+v", m)
	}
}

func TestDiagnosticsReportsQuorum(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1", peers: []string{"n1", "n2"}, term: 2, lastLog: 4, commit: 4}
	api, _ := newTestAPI(t, node, "")
	api.peerAddrs = map[string]string{"n2": "http://127.0.0.1:1"} // unreachable peer
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/diagnostics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["cluster_size"].(float64) != 2 {
		t.Fatalf("cluster size %v", m["cluster_size"])
	}
	if m["quorum"].(float64) != 2 {
		t.Fatalf("quorum %v", m["quorum"])
	}
	// self counts, unreachable peer does not
	if m["majority_commit_count"].(float64) != 1 {
		t.Fatalf("majority commit %v", m["majority_commit_count"])
	}
	if m["quorum_ok"] != false {
		t.Fatalf("expected quorum_ok false with one of two nodes")
	}
}

func TestAuditEndpointReturnsEntries(t *testing.T) {
	node := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1", peers: []string{"n1"}}
	api, _ := newTestAPI(t, node, "")
	api.audit.Record(audit.Entry{Actor: "admin", Operation: "create", Target: "10.0.0.5", Outcome: "committed", RaftIndex: 1})
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/audit?limit=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["count"].(float64) != 1 {
		t.Fatalf("count %v", m["count"])
	}
}

func TestUIInjectsDevFlag(t *testing.T) {
	t.Setenv("RAFTLITE_DEV_UI", "1")
	node := &fakeNode{id: "n1", state: consensus.Follower, peers: []string{"n1"}}
	api, _ := newTestAPI(t, node, "")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/ui", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "const DEV_UI = true;") {
		t.Fatalf("dev flag not injected")
	}
	if !strings.Contains(rec.Body.String(), "id=\"token\"") {
		t.Fatalf("token input missing")
	}
}

func TestFollowerForwardsCreateToLeader(t *testing.T) {
	leaderNode := &fakeNode{id: "n1", state: consensus.Leader, leader: "n1", peers: []string{"n1", "n2"}}
	followerNode := &fakeNode{id: "n2", state: consensus.Follower, leader: "n1", peers: []string{"n1", "n2"}}
	leaderAPI, _ := newTestAPI(t, leaderNode, "")
	followerAPI, _ := newTestAPI(t, followerNode, "")

	leaderSrv := httptest.NewServer(leaderAPI.Handler())
	defer leaderSrv.Close()
	followerSrv := httptest.NewServer(followerAPI.Handler())
	defer followerSrv.Close()

	followerAPI.SetPeerAdminAddrs(map[string]string{"n1": leaderSrv.URL})

	rec := postJSON(t, followerAPI.Handler(), "/v1/policies/ip-blocks", "", map[string]string{"ip": "10.0.0.9"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("forwarded create status %d: %s", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	if m["status"] != "committed" {
		t.Fatalf("forwarded create not committed: %+v", m)
	}
	if len(leaderNode.proposed) != 1 {
		t.Fatalf("leader did not receive proposal: %d", len(leaderNode.proposed))
	}
}

func TestFollowerWithoutPeerAddrsStillRedirects(t *testing.T) {
	node := &fakeNode{id: "n2", state: consensus.Follower, leader: "n1", peers: []string{"n1", "n2"}}
	api, _ := newTestAPI(t, node, "")
	rec := postJSON(t, api.Handler(), "/v1/policies/ip-blocks", "", map[string]string{"ip": "10.0.0.9"})
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status %d, want 307", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["error"] != "not_leader" {
		t.Fatalf("error %v", m["error"])
	}
	if !strings.Contains(m["message"].(string), "ADMIN_PEER_ADDRS") {
		t.Fatalf("missing ADMIN_PEER_ADDRS hint: %v", m["message"])
	}
}

func TestUICountersAndLabTab(t *testing.T) {
	t.Setenv("RAFTLITE_DEV_UI", "1")
	node := &fakeNode{id: "n1", state: consensus.Follower, peers: []string{"n1"}}
	api, _ := newTestAPI(t, node, "")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/ui", nil))
	body := rec.Body.String()
	for _, marker := range []string{"data-tab=\"testlab\"", "runLab()", "renderMetrics", "metricCards", "cleanupLab()"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("ui missing %q", marker)
		}
	}
}
