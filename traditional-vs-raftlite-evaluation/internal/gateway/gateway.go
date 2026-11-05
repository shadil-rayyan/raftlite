// Package gateway implements the single gateway application used in BOTH
// environments. It polls a backend (Traditional server or RaftLite
// adapter) for policies, keeps them in memory, enforces IP blocking on
// its data path, and reports receipt timestamps to the measurement
// collector. The only difference between environments is the BackendURL
// it is configured with.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/policy"
)

// Receipt is reported to the measurement collector whenever the gateway
// applies a policy it had not seen before. AppliedAt is the gateway's own
// wall-clock timestamp at apply time (all hosts share one clock in these
// experiments). Receipts are matched by IP: RaftLite assigns its own
// policy IDs, so the IP address is the common key across both systems.
type Receipt struct {
	GatewayID string `json:"gateway_id"`
	PolicyID  string `json:"policy_id"`
	IP        string `json:"ip"`
	Revision  uint64 `json:"revision"`
	AppliedAt int64  `json:"applied_at_ns"`
}

// Config configures one gateway process.
type Config struct {
	ID           string
	ListenAddr   string // e.g. "127.0.0.1:8110"
	BackendURL   string // base URL of Traditional server or RaftLite adapter
	PollInterval time.Duration
	ReceiptsURL  string // measurement collector; empty = no receipt reporting
	Logger       *slog.Logger
}

// Gateway holds the in-memory policy cache and serves the data path.
type Gateway struct {
	cfg    Config
	client *http.Client
	log    *slog.Logger

	mu        sync.RWMutex
	policies  []policy.Policy
	byIP      map[string]policy.Policy // exact-IP rules
	cidrs     []netip.Prefix           // CIDR rules
	revision  uint64
	applied   map[string]bool // policy IDs ever applied (receipt bookkeeping)
}

// New creates a gateway (not yet started).
func New(cfg Config) *Gateway {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Gateway{
		cfg:    cfg,
		client: &http.Client{Timeout: 5 * time.Second},
		log:    cfg.Logger,
		byIP:   map[string]policy.Policy{},
		applied: map[string]bool{},
	}
}

// Run polls the backend until ctx is cancelled.
func (g *Gateway) Run(ctx context.Context) {
	g.pollOnce(ctx) // sync immediately at startup
	t := time.NewTicker(g.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.pollOnce(ctx)
		}
	}
}

func (g *Gateway) pollOnce(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.BackendURL+"/v1/policies", nil)
	if err != nil {
		return
	}
	resp, err := g.client.Do(req)
	if err != nil {
		g.log.Debug("poll failed", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		g.log.Debug("poll status", "status", resp.StatusCode)
		return
	}
	var lr policy.ListResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return
	}
	g.applyList(lr)
}

// applyList installs a backend list if its revision is newer than the
// cached one, emits receipts for newly-applied policies, and reports the
// new revision.
func (g *Gateway) applyList(lr policy.ListResponse) {
	g.mu.Lock()
	if lr.Revision <= g.revision {
		g.mu.Unlock()
		return
	}
	added, removed := policy.Diff(g.policies, lr.Policies)
	// Rebuild the lookup structures.
	g.policies = lr.Policies
	g.byIP = map[string]policy.Policy{}
	g.cidrs = g.cidrs[:0]
	for _, p := range lr.Policies {
		if p.Action != "BLOCK" {
			continue
		}
		if pref, err := netip.ParsePrefix(p.IP); err == nil {
			g.cidrs = append(g.cidrs, pref)
			continue
		}
		if addr, err := netip.ParseAddr(p.IP); err == nil {
			g.byIP[addr.String()] = p
			continue
		}
	}
	byID := map[string]bool{}
	for _, p := range lr.Policies {
		byID[p.ID] = true
	}
	now := time.Now().UnixNano()
	var receipts []Receipt
	ipByID := map[string]string{}
	for _, p := range lr.Policies {
		ipByID[p.ID] = p.IP
	}
	for _, id := range added {
		if g.applied[id] {
			continue // re-added after removal: not a first application
		}
		g.applied[id] = true
		receipts = append(receipts, Receipt{
			GatewayID: g.cfg.ID,
			PolicyID:  id,
			IP:        ipByID[id],
			Revision:  lr.Revision,
			AppliedAt: now,
		})
	}
	_ = removed // removals do not generate receipts (see docs)
	g.revision = lr.Revision
	g.mu.Unlock()

	for _, rc := range receipts {
		g.reportReceipt(rc)
	}
	if len(receipts) > 0 {
		g.log.Info("applied policies",
			"revision", lr.Revision, "new", len(receipts), "total", len(lr.Policies))
	}
}

// reportReceipt POSTs a receipt to the collector, retrying briefly.
func (g *Gateway) reportReceipt(rc Receipt) {
	if g.cfg.ReceiptsURL == "" {
		return
	}
	body, _ := json.Marshal(rc)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest(http.MethodPost, g.cfg.ReceiptsURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := g.client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return
		}
		lastErr = fmt.Errorf("collector status %d", resp.StatusCode)
		time.Sleep(20 * time.Millisecond)
	}
	g.log.Warn("receipt delivery failed", "policy", rc.PolicyID, "error", lastErr)
}

// Handler returns the gateway's HTTP routes: the data path plus control
// endpoints used by experiments (state snapshot, health).
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		g.mu.RLock()
		rev := g.revision
		g.mu.RUnlock()
		writeJSON(w, map[string]any{"ok": true, "gateway_id": g.cfg.ID, "revision": rev})
	})
	// State snapshot: every policy the gateway currently holds, with its
	// revision. Correctness experiments read this from each gateway.
	mux.HandleFunc("GET /v1/gateway/state", func(w http.ResponseWriter, _ *http.Request) {
		g.mu.RLock()
		pols := append([]policy.Policy(nil), g.policies...)
		rev := g.revision
		g.mu.RUnlock()
		writeJSON(w, map[string]any{
			"gateway_id": g.cfg.ID,
			"revision":   rev,
			"policies":   pols,
		})
	})
	// Data path: every other request is evaluated against the local cache.
	mux.HandleFunc("/", g.handleDataPath)
	return mux
}

// handleDataPath applies the IP-blocking middleware. The client IP is
// taken from X-Forwarded-For, X-Real-IP, or the socket address — in that
// order — so load generators can present synthetic source IPs.
func (g *Gateway) handleDataPath(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	g.mu.RLock()
	blocked := false
	if p, ok := g.byIP[ip]; ok && p.Action == "BLOCK" {
		blocked = true
	} else {
		if addr, err := netip.ParseAddr(ip); err == nil {
			for _, pref := range g.cidrs {
				if pref.Contains(addr) {
					blocked = true
					break
				}
			}
		}
	}
	rev := g.revision
	g.mu.RUnlock()

	w.Header().Set("X-Gateway-Revision", fmt.Sprintf("%d", rev))
	if blocked {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintln(w, "blocked by policy")
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		if ip := net.ParseIP(v); ip != nil {
			return ip.String()
		}
		return v
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
