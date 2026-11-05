// Command simgw runs a standalone simulated Envoy gateway: a real reverse
// proxy data plane that enforces the security-policy snapshot it receives
// from a RaftLite control plane, plus an Envoy-style admin interface.
//
// It mocks the entire standalone-Envoy deployment model:
//
//   - pull-based configuration (like xDS): the gateway polls the control
//     plane for the current revision, validates it, atomically swaps its
//     local enforcement set, then reports accepted/rejected — acceptance
//     and enforcement are separate questions (FR-018, FR-019);
//   - local policy evaluation: requests never touch the control plane
//     (FR-019); when the control plane is unreachable the gateway keeps
//     enforcing its last-known-good snapshot — never allow-all (FR-020);
//   - revision reporting + liveness: every poll and report proves the
//     gateway is alive; silence beyond the control plane's heartbeat
//     window classifies it offline (FR-021);
//   - enforcement verification: the control plane probes this gateway's
//     data path with the policy target as client address (honored only
//     from explicitly trusted probe sources, SEC-007) and expects 429.
//
// Configuration (env):
//
//	SIMGW_ID               stable gateway identity              (default simgw-1)
//	SIMGW_NAME             display name                          (default edge-sim-1)
//	SIMGW_LISTEN           data-plane (proxy) listen address     (default :18080)
//	SIMGW_ADMIN            admin listen address                  (default :19901)
//	SIMGW_UPSTREAM         upstream base URL to proxy to         (required)
//	SIMGW_CONTROL_PLANE    RaftLite admin base URL               (required)
//	SIMGW_TOKEN            bearer token (RAFTLITE_ADMIN_TOKEN)   (dev: empty = no auth)
//	SIMGW_PROXY_URL        externally reachable data-path URL    (default http://127.0.0.1:18080)
//	SIMGW_TRUST_PROBE_SRC  comma-separated CIDRs trusted as      (default 127.0.0.1/32)
//	                       proxy hops for X-Forwarded-For
//	SIMGW_APPLY_DELAY_MS   simulated config-apply delay          (default 0)
//	SIMGW_FAIL_MODE        "" | reject | timeout | offline       (default "")
//	SIMGW_POLL_MS          config poll interval                  (default 1000)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shadil/raftlite/internal/fleet"
	"github.com/shadil/raftlite/internal/gateway"
	"github.com/shadil/raftlite/internal/policy"
)

// simgwConfig is the process configuration, resolved from the environment.
type simgwConfig struct {
	ID           string
	Name         string
	ListenAddr   string
	AdminAddr    string
	Upstream     string
	ControlPlane string
	Token        string
	ProxyURL     string
	TrustProbes  []netip.Prefix
	ApplyDelay   time.Duration
	FailMode     string // "" | reject | timeout | offline
	PollInterval time.Duration
}

func configFromEnv() (simgwConfig, error) {
	cfg := simgwConfig{
		ID:           env("SIMGW_ID", "simgw-1"),
		Name:         env("SIMGW_NAME", "edge-sim-1"),
		ListenAddr:   env("SIMGW_LISTEN", ":18080"),
		AdminAddr:    env("SIMGW_ADMIN", ":19901"),
		Upstream:     os.Getenv("SIMGW_UPSTREAM"),
		ControlPlane: strings.TrimRight(os.Getenv("SIMGW_CONTROL_PLANE"), "/"),
		Token:        os.Getenv("SIMGW_TOKEN"),
		ProxyURL:     env("SIMGW_PROXY_URL", "http://127.0.0.1:18080"),
		FailMode:     os.Getenv("SIMGW_FAIL_MODE"),
	}
	trust := env("SIMGW_TRUST_PROBE_SRC", "127.0.0.1/32")
	for _, part := range strings.Split(trust, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pre, err := netip.ParsePrefix(part)
		if err != nil {
			return cfg, fmt.Errorf("SIMGW_TRUST_PROBE_SRC %q: %w", part, err)
		}
		cfg.TrustProbes = append(cfg.TrustProbes, pre)
	}
	if ms, err := strconv.Atoi(env("SIMGW_APPLY_DELAY_MS", "0")); err == nil && ms >= 0 {
		cfg.ApplyDelay = time.Duration(ms) * time.Millisecond
	}
	if ms, err := strconv.Atoi(env("SIMGW_POLL_MS", "1000")); err == nil && ms >= 100 {
		cfg.PollInterval = time.Duration(ms) * time.Millisecond
	}
	switch cfg.FailMode {
	case "", "reject", "timeout", "offline":
	default:
		return cfg, fmt.Errorf("SIMGW_FAIL_MODE %q: want reject, timeout, or offline", cfg.FailMode)
	}
	if cfg.Upstream == "" {
		return cfg, fmt.Errorf("SIMGW_UPSTREAM is required")
	}
	if cfg.ControlPlane == "" {
		return cfg, fmt.Errorf("SIMGW_CONTROL_PLANE is required")
	}
	return cfg, nil
}

// policySet is the gateway's local enforcement state: the applied config
// snapshot, evaluated without any control-plane round trip (FR-019).
type policySet struct {
	mu   sync.RWMutex
	rev  uint64
	pols []fleet.PolicyView
}

// IsBlocked implements gateway.BlocklistChecker against the applied set.
func (s *policySet) IsBlocked(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.pols {
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

// apply atomically swaps in a new revision's policies.
func (s *policySet) apply(rev uint64, pols []fleet.PolicyView) {
	cp := make([]fleet.PolicyView, len(pols))
	copy(cp, pols)
	s.mu.Lock()
	s.rev = rev
	s.pols = cp
	s.mu.Unlock()
}

func (s *policySet) snapshot() (uint64, []fleet.PolicyView) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]fleet.PolicyView, len(s.pols))
	copy(cp, s.pols)
	return s.rev, cp
}

// app is the standalone gateway process.
type app struct {
	cfg    simgwConfig
	set    *policySet
	client *http.Client
	log    *slog.Logger
	proxy  http.Handler // data plane: enforcement middleware + reverse proxy

	mu           sync.Mutex
	registered   bool
	attemptedRev uint64 // last revision this gateway decided about
	lastSyncErr  string
	startedAt    time.Time
}

func newApp(cfg simgwConfig, log *slog.Logger) (*app, error) {
	up, err := url.Parse(cfg.Upstream)
	if err != nil {
		return nil, fmt.Errorf("upstream URL: %w", err)
	}
	a := &app{
		cfg:       cfg,
		set:       &policySet{},
		client:    &http.Client{Timeout: 5 * time.Second},
		log:       log,
		startedAt: time.Now(),
	}
	// Data path: blocklist enforcement (SEC-007 trust boundary) in front of
	// the reverse proxy. Blocked sources get 429; everything else forwards.
	rp := httputil.NewSingleHostReverseProxy(up)
	a.proxy = gateway.MiddlewareTrusted(a.set, cfg.TrustProbes, rp)
	return a, nil
}

// ---- control-plane client ----

func (a *app) api(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(payload)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.ControlPlane+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// register announces this gateway to the control plane. Idempotent: a
// restart re-registers the same identity (200 = already existed).
func (a *app) register(ctx context.Context) error {
	req := map[string]string{
		"id":           a.cfg.ID,
		"name":         a.cfg.Name,
		"adapter_type": fleet.AdapterEnvoySim,
		"proxy_url":    a.cfg.ProxyURL,
	}
	status, body, err := a.api(ctx, http.MethodPost, "/v1/fleet/gateways", req)
	if err != nil {
		return err
	}
	if status == http.StatusCreated || status == http.StatusOK {
		a.mu.Lock()
		a.registered = true
		a.mu.Unlock()
		a.log.Info("registered with control plane", "id", a.cfg.ID, "status", status)
		return nil
	}
	return fmt.Errorf("register: control plane returned %d %s", status, body)
}

// report sends an ACK/NACK for one revision.
func (a *app) report(ctx context.Context, rev uint64, status string, evErr *fleet.EventError) error {
	req := map[string]any{"revision": rev, "status": status}
	if evErr != nil {
		req["error"] = evErr
	}
	code, body, err := a.api(ctx, http.MethodPost, "/v1/fleet/gateways/"+a.cfg.ID+"/report", req)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("report: control plane returned %d %s", code, body)
	}
	return nil
}

// syncOnce performs one configuration poll. It applies new revisions
// according to the configured fail mode:
//
//	(default) apply + report accepted
//	reject    do NOT apply; report rejected (a rejected config is not running)
//	timeout   apply, but never report — the control plane classifies timeout
//	offline   do not poll at all; keep enforcing last-known-good (FR-020)
func (a *app) syncOnce(ctx context.Context) error {
	a.mu.Lock()
	registered := a.registered
	a.mu.Unlock()
	if !registered {
		if err := a.register(ctx); err != nil {
			return err
		}
	}

	if a.cfg.FailMode == "offline" {
		return nil // FR-020: explicit mode, last-known-good enforced, no polling
	}

	status, body, err := a.api(ctx, http.MethodGet, "/v1/fleet/gateways/"+a.cfg.ID+"/config", nil)
	if err != nil {
		a.setSyncErr(err.Error())
		return err
	}
	if status != http.StatusOK {
		// Edge case: fleet state is leader-owned and in-memory. After a
		// leader failover the new leader starts with an empty fleet and
		// answers 404 — clear the sticky flag so the next sync
		// re-registers this identity (idempotent) and rejoins the fleet.
		if status == http.StatusNotFound {
			a.mu.Lock()
			a.registered = false
			a.mu.Unlock()
		}
		err := fmt.Errorf("config poll: control plane returned %d %s", status, body)
		a.setSyncErr(err.Error())
		return err
	}
	var snap struct {
		Revision uint64             `json:"revision"`
		Policies []fleet.PolicyView `json:"policies"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		a.setSyncErr("config poll: bad JSON: " + err.Error())
		return err
	}
	a.clearSyncErr()

	a.mu.Lock()
	behind := snap.Revision > a.attemptedRev
	a.mu.Unlock()
	if !behind {
		return nil // nothing new; this poll still counted as a heartbeat
	}

	if a.cfg.ApplyDelay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.cfg.ApplyDelay):
		}
	}

	switch a.cfg.FailMode {
	case "reject":
		evErr := &fleet.EventError{Code: "INVALID_CONFIGURATION", Message: "The simulated Envoy rejected this configuration."}
		if err := a.report(ctx, snap.Revision, fleet.RollRejected, evErr); err != nil {
			return err // not attempted-rev: retried next poll
		}
		a.markAttempted(snap.Revision)
		a.log.Warn("rejected configuration (not applied)", "revision", snap.Revision)
	case "timeout":
		// Apply but never report: the control plane's delivery timer
		// classifies it as timeout, which is not a rejection.
		a.set.apply(snap.Revision, snap.Policies)
		a.markAttempted(snap.Revision)
		a.log.Warn("applied configuration but not reporting (timeout scenario)", "revision", snap.Revision)
	default:
		a.set.apply(snap.Revision, snap.Policies)
		if err := a.report(ctx, snap.Revision, fleet.RollAccepted, nil); err != nil {
			// Applied locally but unreported: retried next poll so the
			// control plane never guesses acceptance.
			a.setSyncErr(err.Error())
			return err
		}
		a.markAttempted(snap.Revision)
		a.log.Info("configuration applied", "revision", snap.Revision, "policies", len(snap.Policies))
	}
	return nil
}

func (a *app) markAttempted(rev uint64) {
	a.mu.Lock()
	a.attemptedRev = rev
	a.mu.Unlock()
}

func (a *app) setSyncErr(msg string) {
	a.mu.Lock()
	a.lastSyncErr = msg
	a.mu.Unlock()
}

func (a *app) clearSyncErr() {
	a.mu.Lock()
	a.lastSyncErr = ""
	a.mu.Unlock()
}

// ---- admin interface (Envoy-style) ----

func (a *app) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		rev, pols := a.set.snapshot()
		a.mu.Lock()
		st := map[string]any{
			"id":               a.cfg.ID,
			"name":             a.cfg.Name,
			"role":             "simulated-envoy data plane",
			"adapter_type":     fleet.AdapterEnvoySim,
			"applied_revision": rev,
			"policy_count":     len(pols),
			"fail_mode":        a.cfg.FailMode,
			"registered":       a.registered,
			"last_sync_error":  a.lastSyncErr,
			"uptime_seconds":   int(time.Since(a.startedAt).Seconds()),
			"upstream":         a.cfg.Upstream,
			"control_plane":    a.cfg.ControlPlane,
		}
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("GET /policies", func(w http.ResponseWriter, _ *http.Request) {
		rev, pols := a.set.snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"revision": rev, "policies": pols})
	})
	return mux
}

// ---- process wiring ----

func (a *app) Run(ctx context.Context) error {
	proxySrv := &http.Server{Addr: a.cfg.ListenAddr, Handler: a.proxy, ReadHeaderTimeout: 5 * time.Second}
	adminSrv := &http.Server{Addr: a.cfg.AdminAddr, Handler: a.adminHandler(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 2)
	go func() {
		a.log.Info("data plane listening", "addr", a.cfg.ListenAddr, "upstream", a.cfg.Upstream)
		if err := proxySrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	go func() {
		a.log.Info("admin listening", "addr", a.cfg.AdminAddr)
		if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = proxySrv.Shutdown(shutdownCtx)
			_ = adminSrv.Shutdown(shutdownCtx)
			return nil
		case err := <-errCh:
			return err
		case <-ticker.C:
			if err := a.syncOnce(ctx); err != nil {
				// A control-plane outage never resets enforcement (FR-020):
				// the last-known-good set stays loaded and blocking.
				a.log.Warn("sync failed; enforcing last-known-good", "error", err)
			}
		}
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := configFromEnv()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	a, err := newApp(cfg, logger)
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil {
		logger.Error("simgw failed", "error", err)
		os.Exit(1)
	}
	logger.Info("simgw stopped")
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
