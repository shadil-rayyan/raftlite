// Fleet endpoints: gateway registry, rollouts, and dev-only simulator
// controls. State lives in the leader's in-memory fleet.Manager; followers
// forward every fleet request to the leader (same machinery as writes).
//
// Dev controls are registered only when the node was started with
// RAFTLITE_DEV_UI=1 and always require authentication. They manipulate
// simulated gateways only and must never be enabled in production.
package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/fleet"
)

// SetFleetManager attaches the fleet manager (leader-owned, in-memory).
func (a *API) SetFleetManager(m *fleet.Manager) { a.fleet = m }

// SetDevMode enables the /dev/mock-gateways control routes. Dev only.
func (a *API) SetDevMode(dev bool) { a.dev = dev }

func (a *API) registerFleetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/fleet/gateways", a.handleFleetGateways)
	mux.HandleFunc("GET /v1/fleet/gateways/{id}", a.handleFleetGateway)
	mux.HandleFunc("GET /v1/fleet/gateways/{id}/events", a.handleFleetGatewayEvents)
	mux.HandleFunc("GET /v1/fleet/rollouts", a.handleFleetRollouts)
	mux.HandleFunc("GET /v1/fleet/rollouts/{id}", a.handleFleetRollout)
	mux.HandleFunc("GET /v1/fleet/events", a.handleFleetEvents)
	mux.HandleFunc("POST /v1/fleet/gateways", a.handleCreateGateway)
	mux.HandleFunc("POST /v1/fleet/reconcile", a.handleFleetReconcile)
	if a.dev {
		mux.HandleFunc("POST /dev/mock-gateways", a.handleDevCreateGateway)
		mux.HandleFunc("DELETE /dev/mock-gateways/{id}", a.handleDevDeleteGateway)
	mux.HandleFunc("POST /dev/mock-gateways/{id}/scenarios/{action}", a.handleDevScenario)
}
}

// fleetGate checks role and forwards to the leader when this node is a
// follower. Returns handled=true when the response is already written.
func (a *API) fleetGate(w http.ResponseWriter, r *http.Request) bool {
	if a.fleet == nil {
		writeErr(w, apiError{http.StatusServiceUnavailable, "fleet_unavailable", "fleet manager not configured on this node"})
		return true
	}
	if a.node.State() == consensus.Leader {
		return false
	}
	leader := a.node.LeaderID()
	if leader == "" {
		writeErr(w, apiError{http.StatusServiceUnavailable, "no_leader", "no elected leader; fleet state is unavailable"})
		return true
	}
	base, ok := a.peerAddrs[leader]
	if !ok || leader == a.node.ID() {
		writeErr(w, apiError{http.StatusTemporaryRedirect, "not_leader",
			"fleet state lives on the leader " + leader + "; set ADMIN_PEER_ADDRS to enable forwarding"})
		return true
	}
	// Capture the body so it survives both forwarding and local fallback.
	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if a.forwardToLeader(r.Context(), w, r, raw, base) {
		return true
	}
	writeErr(w, apiError{http.StatusBadGateway, "forward_failed", "could not reach the leader's fleet API"})
	return true
}

// triggerFleetRollout fires after a committed policy change on the leader.
// The rollout manager owns everything from here: desired revisions, async
// delivery, and per-gateway outcomes.
func (a *API) triggerFleetRollout(policyID string) {
	if a.fleet == nil || a.node.State() != consensus.Leader {
		return
	}
	rev := a.fsm.Revision()
	a.fleet.OnRevision(rev, policyID, a.configSnapshot(rev))
}

func (a *API) configSnapshot(rev uint64) fleet.ConfigSnapshot {
	now := time.Now()
	pols := a.fsm.List(now, false)
	out := make([]fleet.PolicyView, 0, len(pols))
	for _, p := range pols {
		pv := fleet.PolicyView{
			ID:     p.ID,
			Name:   p.Name,
			Type:   string(p.Type),
			Target: p.Target,
			Action: p.Action,
			Status: string(p.Status),
		}
		if p.ExpiresAt != nil {
			u := p.ExpiresAt.Unix()
			pv.Expires = &u
		}
		out = append(out, pv)
	}
	return fleet.ConfigSnapshot{Revision: rev, Policies: out}
}

func fleetSummary(gws []fleet.Gateway) map[string]any {
	total, online, offline, attention := len(gws), 0, 0, 0
	for _, g := range gws {
		switch g.ConnectionStatus {
		case fleet.StatusOnline:
			online++
		case fleet.StatusOffline:
			offline++
		}
		if g.ConnectionStatus != fleet.StatusOnline || g.DesiredRevision != g.ReportedAcceptedRevision {
			attention++
		}
	}
	return map[string]any{"total": total, "online": online, "offline": offline, "needs_attention": attention}
}

// ---- fleet read/write endpoints ----

func (a *API) handleFleetGateways(w http.ResponseWriter, r *http.Request) {
	if _, aerr := a.authenticate(r); aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	gws := a.fleet.List()
	writeJSON(w, http.StatusOK, map[string]any{
		"gateways": gws,
		"summary":  fleetSummary(gws),
		"revision": a.fleet.CurrentRevision(),
	})
}

func (a *API) handleFleetGateway(w http.ResponseWriter, r *http.Request) {
	if _, aerr := a.authenticate(r); aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	id := r.PathValue("id")
	gw, ok := a.fleet.Get(id)
	if !ok {
		writeErr(w, apiError{http.StatusNotFound, "not_found", "no such gateway"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"gateway": gw,
		"events":  a.fleet.Events(50, id),
	})
}

func (a *API) handleFleetGatewayEvents(w http.ResponseWriter, r *http.Request) {
	if _, aerr := a.authenticate(r); aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	limit := queryLimit(r, 100)
	writeJSON(w, http.StatusOK, map[string]any{
		"events": a.fleet.Events(limit, r.PathValue("id")),
	})
}

func (a *API) handleFleetRollouts(w http.ResponseWriter, r *http.Request) {
	if _, aerr := a.authenticate(r); aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rollouts": a.fleet.Rollouts()})
}

func (a *API) handleFleetRollout(w http.ResponseWriter, r *http.Request) {
	if _, aerr := a.authenticate(r); aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	ro, ok := a.fleet.Rollout(r.PathValue("id"))
	if !ok {
		writeErr(w, apiError{http.StatusNotFound, "not_found", "no such rollout"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rollout": ro})
}

type createGatewayRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	DelayMs     int    `json:"delay_ms,omitempty"`
}

func (a *API) handleCreateGateway(w http.ResponseWriter, r *http.Request) {
	actor, aerr := a.authenticate(r)
	if aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	a.createGateway(w, r, actor)
}

func (a *API) createGateway(w http.ResponseWriter, r *http.Request, actor string) {
	var req createGatewayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, apiError{http.StatusBadRequest, "invalid_json", "request body must be valid JSON"})
		return
	}
	if req.Name == "" {
		writeErr(w, apiError{http.StatusBadRequest, "invalid_name", "name is required"})
		return
	}
	delay := time.Duration(req.DelayMs) * time.Millisecond
	gw, err := a.fleet.CreateGateway(req.Name, req.Description, delay)
	if err != nil {
		writeErr(w, apiError{http.StatusBadRequest, "invalid_gateway", err.Error()})
		return
	}
	a.auditEvent(actor, "create_gateway", gw.ID, req.Description, "", "committed", 0, "")
	writeJSON(w, http.StatusCreated, map[string]any{"gateway": gw})
}

func (a *API) handleFleetReconcile(w http.ResponseWriter, r *http.Request) {
	actor, aerr := a.authenticate(r)
	if aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	ro, err := a.fleet.Reconcile()
	if err != nil {
		writeErr(w, apiError{http.StatusConflict, "reconcile_failed", err.Error()})
		return
	}
	a.auditEvent(actor, "reconcile_fleet", ro.ID, "", "", "committed", 0, "")
	writeJSON(w, http.StatusCreated, map[string]any{"rollout": ro})
}

// ---- dev-only simulator controls ----

func (a *API) devGate(w http.ResponseWriter, r *http.Request) bool {
	if _, aerr := a.authenticate(r); aerr.Code != "" {
		writeErr(w, aerr)
		return true
	}
	if a.fleetGate(w, r) {
		return true
	}
	return false
}

func (a *API) handleDevCreateGateway(w http.ResponseWriter, r *http.Request) {
	actor, aerr := a.authenticate(r)
	if aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	a.createGateway(w, r, actor)
}

func (a *API) handleDevDeleteGateway(w http.ResponseWriter, r *http.Request) {
	actor, aerr := a.authenticate(r)
	if aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	if err := a.fleet.DeleteGateway(r.PathValue("id")); err != nil {
		status := http.StatusNotFound
		if err == fleet.ErrInFlight {
			status = http.StatusConflict
		}
		writeErr(w, apiError{status, "delete_failed", err.Error()})
		return
	}
	a.auditEvent(actor, "delete_gateway", r.PathValue("id"), "", "", "committed", 0, "")
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (a *API) handleDevScenario(w http.ResponseWriter, r *http.Request) {
	actor, aerr := a.authenticate(r)
	if aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	id, action := r.PathValue("id"), r.PathValue("action")
	var err error
	switch action {
	case "offline":
		err = a.fleet.SetOffline(id)
	case "online":
		err = a.fleet.SetOnline(id)
	case "set-delay":
		var req struct {
			DelayMs int `json:"delay_ms"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.DelayMs < 0 {
			writeErr(w, apiError{http.StatusBadRequest, "invalid_delay", "delay_ms must be a non-negative integer"})
			return
		}
		err = a.fleet.SetDelay(id, time.Duration(req.DelayMs)*time.Millisecond)
	case "reject-next", "accept-next", "timeout-next", "reset":
		err = a.fleet.Scenario(id, action)
	default:
		writeErr(w, apiError{http.StatusNotFound, "unknown_scenario", "unknown scenario " + action})
		return
	}
	if err != nil {
		status := http.StatusBadRequest
		if err == fleet.ErrNotFound {
			status = http.StatusNotFound
		}
		writeErr(w, apiError{status, "scenario_failed", err.Error()})
		return
	}
	a.auditEvent(actor, "gateway_scenario", id, action, "", "committed", 0, "")
	gw, _ := a.fleet.Get(id)
	writeJSON(w, http.StatusOK, map[string]any{"gateway": gw})
}

func (a *API) handleFleetEvents(w http.ResponseWriter, r *http.Request) {
	if _, aerr := a.authenticate(r); aerr.Code != "" {
		writeErr(w, aerr)
		return
	}
	if a.fleetGate(w, r) {
		return
	}
	limit := queryLimit(r, 100)
	writeJSON(w, http.StatusOK, map[string]any{"events": a.fleet.Events(limit, "")})
}

func queryLimit(r *http.Request, def int) int {
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 1000 {
			return n
		}
	}
	return def
}
