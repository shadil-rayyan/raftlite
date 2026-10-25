// Package server exposes the RaftLite administrative API (SRS §6.1).
//
// Design rules enforced here:
//   - Only the leader commits writes; followers forward or redirect (FR-007).
//   - Responses distinguish accepted / committed / uncertain outcomes
//     (PRD §13.1); a timeout is never reported as a rejection.
//   - Every mutating request is authenticated (SEC-001) and audited (FR-031).
//   - All inputs are validated before entering the log (FR-015, SEC-006).
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/shadil/raftlite/internal/audit"
	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/policy"
)

// NodeView is the subset of consensus.Node the API needs. Lets tests
// substitute a fake without standing up consensus.
type NodeView interface {
	ID() string
	State() consensus.NodeState
	LeaderID() string
	Peers() []string
	ProposeAndWait(ctx context.Context, data []byte) (uint64, error)
}

// API bundles handlers and dependencies.
type API struct {
	node    NodeView
	fsm     *consensus.FSM
	audit   *audit.Logger
	log     *slog.Logger
	token     string              // admin bearer token; empty = auth disabled (dev only)
	peerAddrs map[string]string   // peer ID -> admin base URL, for 307 redirects
	client    *http.Client

	// idempotency records request_id -> outcome for safe retries (FR-016).
	mu   sync.Mutex
	idem map[string]idemRecord
}

type idemRecord struct {
	Outcome   string
	RaftIndex uint64
	PolicyID  string
}

// NewAPI constructs the administrative API. token == "" disables auth and
// logs a warning — acceptable only in isolated development environments
// (SEC-001).
func NewAPI(node NodeView, fsm *consensus.FSM, auditLog *audit.Logger, token string, log *slog.Logger) *API {
	if log == nil {
		log = slog.Default()
	}
	if token == "" {
		log.Warn("admin API authentication is DISABLED — set RAFTLITE_ADMIN_TOKEN for any shared environment")
	}
	return &API{
		node:    node,
		fsm:     fsm,
		audit:   auditLog,
		log:     log,
		token:   token,
		client:  &http.Client{Timeout: 5 * time.Second},
		idem:    make(map[string]idemRecord),
	}
}

// Handler returns the versioned route table (SRS §6.1).
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("GET /readyz", a.handleReadyz)
	mux.HandleFunc("GET /v1/leader", a.handleLeader)
	mux.HandleFunc("GET /v1/cluster", a.handleCluster)
	mux.HandleFunc("GET /v1/policies", a.handleListPolicies)
	mux.HandleFunc("POST /v1/policies/ip-blocks", a.handleCreateIPBlock)
	mux.HandleFunc("GET /v1/policies/{id}", a.handleGetPolicy)
	mux.HandleFunc("DELETE /v1/policies/{id}", a.handleRevokePolicy)
	mux.HandleFunc("GET /v1/blocked/{ip}", a.handleBlocked)
	mux.HandleFunc("GET /ui", a.handleUI)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui", http.StatusFound)
	})
	return mux
}

// ---- shared helpers ----

type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e apiError) Error() string { return e.Code + ": " + e.Message }

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeErr(w http.ResponseWriter, e apiError) {
	writeJSON(w, e.Status, map[string]string{"error": e.Code, "message": e.Message})
}

// writeErrRedirect behaves like writeErr but attaches a Location header so
// clients (and curl -L) can follow a307 to the leader.
func writeErrRedirect(w http.ResponseWriter, e apiError, location string) {
	if location != "" {
		w.Header().Set("Location", location)
	}
	writeErr(w, e)
}

func (a *API) authenticate(r *http.Request) (string, apiError) {
	if a.token == "" {
		return "anonymous", apiError{}
	}
	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		return "", apiError{http.StatusUnauthorized, "unauthenticated", "missing bearer token"}
	}
	got := strings.TrimPrefix(authz, prefix)
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
		return "", apiError{http.StatusUnauthorized, "unauthenticated", "invalid token"}
	}
	return "token-admin", apiError{}
}

func (a *API) auditEvent(actor, op, target, reason, reqID, outcome string, index uint64, errStr string) {
	if a.audit == nil {
		return
	}
	a.audit.Record(audit.Entry{
		Timestamp: time.Now().UTC(),
		Actor:     actor,
		Operation: op,
		Target:    target,
		Reason:    reason,
		RequestID: reqID,
		Outcome:   outcome,
		RaftIndex: index,
		Error:     errStr,
		NodeID:    a.node.ID(),
	})
}

// ---- operational endpoints ----

func (a *API) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	// Liveness only: the process is up. Distinct from write readiness.
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	// Readiness: can this node safely serve its role right now? A follower
	// is ready for reads; only a leader is write-ready. Never claim
	// write-readiness the node cannot honor (FR-028).
	st := a.node.State()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ready",
		"node_id":     a.node.ID(),
		"role":        st.String(),
		"write_ready": st == consensus.Leader,
	})
}

func (a *API) handleLeader(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"leader_id": a.node.LeaderID(),
		"node_id":   a.node.ID(),
	})
}

func (a *API) handleCluster(w http.ResponseWriter, _ *http.Request) {
	state := a.node.State()
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":   a.node.ID(),
		"role":      state.String(),
		"leader_id": a.node.LeaderID(),
		"peers":     a.node.Peers(),
		"revision":  a.fsm.Revision(),
	})
}

// ---- policy endpoints ----

type createBlockRequest struct {
	IP          string `json:"ip"`
	Duration    string `json:"duration,omitempty"` // e.g. "1h", "30m"; empty = permanent
	Reason      string `json:"reason,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
}

func (a *API) handleCreateIPBlock(w http.ResponseWriter, r *http.Request) {
	actor, aerr := a.authenticate(r)
	if aerr.Code != "" {
		writeErr(w, aerr)
		return
	}

	var req createBlockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apiError{http.StatusBadRequest, "invalid_json", "request body must be valid JSON"})
		return
	}

	// Validation before anything touches the log (FR-015).
	addr, err := netip.ParseAddr(req.IP)
	if err != nil {
		writeErr(w, apiError{http.StatusBadRequest, "invalid_ip", "ip must be a valid IPv4 or IPv6 address"})
		return
	}
	if addr.IsUnspecified() || addr.IsMulticast() {
		writeErr(w, apiError{http.StatusBadRequest, "invalid_ip", "ip must be a unicast address"})
		return
	}

	var expiresAt *time.Time
	if req.Duration != "" {
		d, err := time.ParseDuration(req.Duration)
		if err != nil || d <= 0 || d > 365*24*time.Hour {
			writeErr(w, apiError{http.StatusBadRequest, "invalid_duration", "duration must be a positive Go duration up to 8760h"})
			return
		}
		t := time.Now().Add(d).UTC().Truncate(time.Second)
		expiresAt = &t
	}

	// Idempotency (FR-016): replay the original outcome for a known key.
	if req.RequestID != "" {
		a.mu.Lock()
		if rec, ok := a.idem[req.RequestID]; ok {
			a.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{
				"status":     rec.Outcome,
				"request_id": req.RequestID,
				"raft_index": rec.RaftIndex,
				"policy_id":  rec.PolicyID,
				"idempotent": true,
			})
			return
		}
		a.mu.Unlock()
	}

	id, err := policy.NewID()
	if err != nil {
		writeErr(w, apiError{http.StatusInternalServerError, "internal", "could not generate policy id"})
		return
	}
	cmd := policy.Command{
		Op:        policy.OpCreate,
		ID:        id,
		Type:      policy.TypeIPBlock,
		Target:    addr.String(),
		Reason:    req.Reason,
		CreatedBy: req.CreatedBy,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		ExpiresAt: expiresAt,
		IdemKey:   req.RequestID,
	}
	payload, _ := json.Marshal(cmd)

	outcome, index, aerr, loc := a.propose(r.Context(), r, payload, actor, "create_ip_block", addr.String(), req.Reason, req.RequestID)
	if aerr.Code != "" {
		a.auditEvent(actor, "create_ip_block", addr.String(), req.Reason, req.RequestID, outcome, index, aerr.Message)
		writeErrRedirect(w, aerr, loc)
		return
	}

	a.mu.Lock()
	if req.RequestID != "" {
		a.idem[req.RequestID] = idemRecord{Outcome: "committed", RaftIndex: index, PolicyID: id}
	}
	a.mu.Unlock()

	a.auditEvent(actor, "create_ip_block", addr.String(), req.Reason, req.RequestID, "committed", index, "")
	writeJSON(w, http.StatusCreated, map[string]any{
		"status":     "committed",
		"policy_id":  id,
		"raft_index": index,
		"revision":   a.fsm.Revision(),
	})
}

func (a *API) handleRevokePolicy(w http.ResponseWriter, r *http.Request) {
	actor, aerr := a.authenticate(r)
	if aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	id := r.PathValue("id")
	if id == "" || len(id) > 128 {
		writeErr(w, apiError{http.StatusBadRequest, "invalid_id", "policy id is required"})
		return
	}
	reason := r.URL.Query().Get("reason")
	reqID := r.Header.Get("Idempotency-Key")

	// Unknown id on the leader's authoritative view: documented response,
	// no state change (FR-009). Followers may lag — propose() redirects
	// them instead of serving a stale 404.
	if a.node.State() == consensus.Leader {
		if _, exists := a.fsm.Get(id); !exists {
			writeErr(w, apiError{http.StatusNotFound, "not_found", "no such policy"})
			return
		}
	}

	cmd := policy.Command{Op: policy.OpRevoke, ID: id, IdemKey: reqID}
	payload, _ := json.Marshal(cmd)

	outcome, index, aerr, loc := a.propose(r.Context(), r, payload, actor, "revoke_policy", id, reason, reqID)
	if aerr.Code != "" {
		a.auditEvent(actor, "revoke_policy", id, reason, reqID, outcome, index, aerr.Message)
		writeErrRedirect(w, aerr, loc)
		return
	}
	a.auditEvent(actor, "revoke_policy", id, reason, reqID, "committed", index, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "committed",
		"policy_id":  id,
		"raft_index": index,
		"revision":   a.fsm.Revision(),
	})
}

func (a *API) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	if _, aerr := a.authenticate(r); aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	includeInactive := r.URL.Query().Get("include_inactive") == "1"
	pols := a.fsm.List(time.Now(), includeInactive)
	out := make([]map[string]any, 0, len(pols))
	for _, p := range pols {
		out = append(out, policyJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policies": out,
		"revision": a.fsm.Revision(),
	})
}

func (a *API) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	if _, aerr := a.authenticate(r); aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	id := r.PathValue("id")
	p, ok := a.fsm.Get(id)
	if !ok {
		writeErr(w, apiError{http.StatusNotFound, "not_found", "no such policy"})
		return
	}
	writeJSON(w, http.StatusOK, policyJSON(p))
}

func (a *API) handleBlocked(w http.ResponseWriter, r *http.Request) {
	if _, aerr := a.authenticate(r); aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	ipStr := r.PathValue("ip")
	addr, err := netip.ParseAddr(ipStr)
	if err != nil {
		writeErr(w, apiError{http.StatusBadRequest, "invalid_ip", "path segment must be an IP address"})
		return
	}
	dec := a.fsm.Lookup(addr.String(), time.Now())
	resp := map[string]any{
		"ip":       addr.String(),
		"blocked":  dec.Blocked,
		"revision": a.fsm.Revision(),
	}
	if dec.Policy != nil {
		resp["policy_id"] = dec.Policy.ID
	}
	writeJSON(w, http.StatusOK, resp)
}

func policyJSON(p *policy.Policy) map[string]any {
	m := map[string]any{
		"id":         p.ID,
		"type":       string(p.Type),
		"target":     p.Target,
		"action":     p.Action,
		"status":     string(p.Status),
		"created_at": p.CreatedAt,
		"version":    p.Version,
	}
	if p.ExpiresAt != nil {
		m["expires_at"] = p.ExpiresAt
	}
	if p.Reason != "" {
		m["reason"] = p.Reason
	}
	if p.CreatedBy != "" {
		m["created_by"] = p.CreatedBy
	}
	return m
}

// propose commits a command through consensus and maps errors to API
// outcomes. Returns a coarse outcome for audit: committed | uncertain |
// rejected, plus a redirect location when the leader is known (307).
func (a *API) propose(ctx context.Context, r *http.Request, payload []byte, actor, op, target, reason, reqID string) (string, uint64, apiError, string) {
	if a.node.State() != consensus.Leader {
		// FR-007: never pretend the write succeeded. Point the client at
		// the leader; the retry replays the same request (and request_id).
		leader := a.node.LeaderID()
		if leader == "" {
			return "rejected", 0, apiError{http.StatusServiceUnavailable, "no_leader", "no elected leader; retry shortly"}, ""
		}
		loc := ""
		if base, ok := a.peerAddrs[leader]; ok && leader != a.node.ID() {
			loc = base + r.URL.RequestURI()
		}
		return "rejected", 0, apiError{
			http.StatusTemporaryRedirect, "not_leader",
			"leader is " + leader + "; resend the request to that node",
		}, loc
	}

	// Bound commit wait: the client should treat timeout as uncertain.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	index, err := a.node.ProposeAndWait(ctx, payload)
	switch {
	case err == nil:
		return "committed", index, apiError{}, ""
	case errors.Is(err, consensus.ErrNoQuorum):
		// FR-007/§8: the command may still commit later. Report uncertain,
		// give the caller the idempotency path to recover the outcome.
		return "uncertain", index, apiError{
			http.StatusGatewayTimeout, "outcome_uncertain",
			"commit not confirmed before timeout; retry with the same request_id to recover the outcome",
		}, ""
	case errors.Is(err, consensus.ErrNotLeader):
		return "rejected", 0, apiError{http.StatusTemporaryRedirect, "not_leader", "leadership changed; retry"}, ""
	default:
		return "rejected", 0, apiError{http.StatusInternalServerError, "storage_error", "write failed: " + err.Error()}, ""
	}
}

// SetPeerAdminAddrs maps peer IDs to their admin API base URLs so redirects
// can point at the leader's exact address (FR-007).
func (a *API) SetPeerAdminAddrs(addrs map[string]string) {
	a.peerAddrs = addrs
}
