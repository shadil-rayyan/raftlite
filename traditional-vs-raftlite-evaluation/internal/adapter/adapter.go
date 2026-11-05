// Package adapter is the "external adapter" that connects the common
// gateway to RaftLite. It serves the exact same backend contract as the
// Traditional server (GET/POST/DELETE /v1/policies with policies +
// revision) while translating to RaftLite's own admin API.
//
// It is a fetch-through translating proxy: a gateway poll becomes a
// linearizable read on the RaftLite leader (same structural cost as a
// gateway poll hitting SQLite), so the adapter adds no polling staleness
// of its own and the comparison stays fair.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/policy"
)

// raftPolicy mirrors the subset of RaftLite's policy JSON we consume.
type raftPolicy struct {
	ID     string `json:"id"`
	Target string `json:"target"`
	Action string `json:"action"` // "deny" for block policies
	Status string `json:"status"`
}

type raftListResponse struct {
	Policies []raftPolicy `json:"policies"`
	Revision uint64       `json:"revision"`
}

// Adapter proxies a RaftLite cluster. It is given EVERY node URL and fails
// over between them, so killing one node does not take the control plane
// down — which is precisely what the server-failure experiment measures.
type Adapter struct {
	raftliteURLs []string
	client       *http.Client
	log          *slog.Logger
}

// New creates an adapter for one or more RaftLite base URLs, comma
// separated (e.g. "http://127.0.0.1:8081,http://127.0.0.1:8082,...").
// Any node works: RaftLite forwards or redirects reads/writes to its leader.
func New(raftliteURLs string, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.Default()
	}
	var urls []string
	for _, u := range strings.Split(raftliteURLs, ",") {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u != "" {
			urls = append(urls, u)
		}
	}
	return &Adapter{
		raftliteURLs: urls,
		// Generous timeout: during leader elections reads may wait.
		client: &http.Client{Timeout: 10 * time.Second},
		log:    log,
	}
}

// Handler returns the uniform backend contract.
func (a *Adapter) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "system": "raftlite-adapter"})
	})
	mux.HandleFunc("GET /v1/policies", a.handleList)
	mux.HandleFunc("POST /v1/policies", a.handleCreate)
	mux.HandleFunc("DELETE /v1/policies/{id}", a.handleDelete)
	return mux
}

func (a *Adapter) handleList(w http.ResponseWriter, r *http.Request) {
	upstream, err := a.do(r.Context(), http.MethodGet, "/v1/policies", nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "raftlite unreachable: " + err.Error()})
		return
	}
	if upstream.status != http.StatusOK {
		// Surface RaftLite's own errors (503 no leader, etc.) verbatim.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upstream.status)
		w.Write(upstream.body)
		return
	}
	var rl raftListResponse
	if err := json.Unmarshal(upstream.body, &rl); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "bad raftlite response: " + err.Error()})
		return
	}
	pols := make([]policy.Policy, 0, len(rl.Policies))
	for _, rp := range rl.Policies {
		if rp.Status != "" && rp.Status != "active" {
			continue // revoked/expired policies are not enforcement state
		}
		pols = append(pols, policy.Policy{
			ID:     rp.ID,
			IP:     rp.Target,
			Action: policy.NormalizeAction(rp.Action),
		})
	}
	policy.SortByID(pols)
	writeJSON(w, http.StatusOK, policy.ListResponse{Policies: pols, Revision: rl.Revision})
}

type createRequest struct {
	ID     string `json:"id"`
	IP     string `json:"ip"`
	Action string `json:"action"`
}

func (a *Adapter) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if req.IP == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ip is required"})
		return
	}
	if action := policy.NormalizeAction(req.Action); action != "" && action != "BLOCK" {
		// RaftLite's policy model is a block list; ALLOW is not
		// representable. Experiments therefore use BLOCK-only datasets.
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": "raftlite supports BLOCK policies only (got " + req.Action + ")",
		})
		return
	}
	payload, _ := json.Marshal(map[string]string{
		"ip":     req.IP,
		"reason": "evaluation experiment",
	})
	upstream, err := a.post(r.Context(), "/v1/policies/ip-blocks", payload)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "raftlite unreachable: " + err.Error()})
		return
	}
	var out struct {
		Status   string `json:"status"`
		PolicyID string `json:"policy_id"`
		Revision uint64 `json:"revision"`
	}
	_ = json.Unmarshal(upstream.body, &out)
	if upstream.status != http.StatusCreated && upstream.status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upstream.status)
		w.Write(upstream.body)
		return
	}
	// Keep the uniform response shape; report RaftLite's generated id
	// (RaftLite assigns policy IDs itself).
	writeJSON(w, http.StatusCreated, map[string]any{
		"status":    out.Status,
		"policy_id": out.PolicyID,
		"revision":  out.Revision,
		"submitted": req.ID,
	})
}

func (a *Adapter) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	upstream, err := a.do(r.Context(), http.MethodDelete, "/v1/policies/"+id, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "raftlite unreachable: " + err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(upstream.status)
	w.Write(upstream.body)
}

func (a *Adapter) post(ctx context.Context, path string, payload []byte) (*upstreamResponse, error) {
	return a.do(ctx, http.MethodPost, path, payload)
}

type upstreamResponse struct {
	status int
	body   []byte
}

func (a *Adapter) do(ctx context.Context, method, path string, payload []byte) (*upstreamResponse, error) {
	var lastErr error
	var lastTransient *upstreamResponse
	for _, base := range a.raftliteURLs {
		// Rebuild the body each attempt — readers are single-use.
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, body)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := a.client.Do(req)
		if err != nil {
			// Connection-level failure (node down / refusing): fail over.
			lastErr = fmt.Errorf("%s: %w", base, err)
			a.log.Debug("raftlite node unreachable, trying next", "node", base, "error", err)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// A node that is down, electing, or hasn't learned who the leader
		// is answers with a transient status. Another node may already be
		// the leader (or able to forward), so try it before surfacing the
		// error — this is the failover an LB would do in production.
		// 307/308 = "not_leader": RaftLite tells us to resend elsewhere
		// rather than issuing a Location we could follow.
		if isTransient(resp.StatusCode) {
			lastTransient = &upstreamResponse{status: resp.StatusCode, body: b}
			a.log.Debug("raftlite node not serving this request, trying next", "node", base, "status", resp.StatusCode)
			continue
		}
		return &upstreamResponse{status: resp.StatusCode, body: b}, nil
	}
	// Every node reported the same transient state: surface it verbatim so
	// a genuinely leaderless cluster still sees RaftLite's own error.
	if lastTransient != nil {
		return lastTransient, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no raftlite URLs configured")
	}
	return nil, lastErr
}

// isTransient reports whether an upstream status means "not this node right
// now" rather than a final answer: gateway-unavailable (503/504) or a
// not-leader redirect (307/308) from a node that cannot forward yet.
func isTransient(status int) bool {
	switch status {
	case http.StatusServiceUnavailable, http.StatusGatewayTimeout,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// String describes the adapter for logs.
func (a *Adapter) String() string { return fmt.Sprintf("adapter→%s", strings.Join(a.raftliteURLs, ",")) }
