// Command raftlite runs a RaftLite node: the consensus core, the gRPC peer
// transport, and the versioned administrative API.
//
// Configuration (env):
//
//	NODE_ID              this node's identifier            (default node1)
//	RAFT_ADDR            gRPC listen address               (default :9090)
//	RAFTLITE_ADDR        admin HTTP listen address         (default :8080)
//	PEERS                comma-separated voter IDs         (default node1,node2,node3)
//	PEER_ADDRS           id=host:port pairs for the gRPC transport
//	ADMIN_PEER_ADDRS     id=http-base-url pairs for 307 redirects
//	WAL_PATH             write-ahead log path              (default /tmp/raftlite.wal)
//	SNAPSHOT_DIR         snapshot directory                (default dirname(WAL_PATH)/snapshots)
//	RAFTLITE_ADMIN_TOKEN bearer token for mutating admin calls; empty disables auth (dev only)
//	RAFTLITE_AUDIT_LOG   audit JSON-lines path; empty disables audit
//	RAFT_TLS_CERT / RAFT_TLS_KEY / RAFT_TLS_CA
//	                     inter-node mTLS material; all empty = plaintext (dev only)
package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shadil/raftlite/internal/audit"
	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/observability"
	"github.com/shadil/raftlite/internal/policy"
	"github.com/shadil/raftlite/internal/server"
	"github.com/shadil/raftlite/internal/transport"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	nodeID := env("NODE_ID", "node1")
	raftAddr := env("RAFT_ADDR", ":9090")
	httpAddr := env("RAFTLITE_ADDR", ":8080")
	peers := splitComma(env("PEERS", "node1,node2,node3"))
	walPath := env("WAL_PATH", "/tmp/raftlite.wal")
	snapshotDir := env("SNAPSHOT_DIR", filepath.Join(filepath.Dir(walPath), "snapshots"))
	adminToken := os.Getenv("RAFTLITE_ADMIN_TOKEN")

	// ---- storage: segmented WAL with durable hard state (term/vote) ----
	storage, err := transport.OpenWAL(walPath)
	if err != nil {
		logger.Error("open WAL", "error", err)
		os.Exit(1)
	}
	defer storage.Close()

	if err := consensus.SetSnapshotDir(snapshotDir); err != nil {
		logger.Error("snapshot dir", "dir", snapshotDir, "error", err)
		os.Exit(1)
	}

	fsm := consensus.NewFSM()
	node := consensus.NewNode(nodeID, peers, transport.RealClock{}, storage, fsm)

	// ---- observability ----
	metrics := observability.NewRegistry(nodeID)
	node.SetMetrics(metrics)

	// ---- inter-node transport: gRPC client with TLS when configured ----
	peerAddrs := parsePeerAddrs(os.Getenv("PEER_ADDRS"))
	tlsCfg := server.TLSConfig{
		CAFile:   os.Getenv("RAFT_TLS_CA"),
		CertFile: os.Getenv("RAFT_TLS_CERT"),
		KeyFile:  os.Getenv("RAFT_TLS_KEY"),
	}
	if len(peerAddrs) > 0 {
		tr, err := server.NewGRPCTransport(peerAddrs, &tlsCfg)
		if err != nil {
			logger.Error("peer transport", "error", err)
			os.Exit(1)
		}
		defer tr.Close()
		node.SetTransport(tr)
	} else {
		logger.Warn("PEER_ADDRS not set — running without peer transport (single-node/dev only)")
	}

	node.Start()
	defer node.Stop()

	if os.Getenv("RAFTLITE_DEV_UI") == "1" {
		go seedDemoData(logger, node, fsm)
	}

	// ---- gRPC server (Raft RPCs + AI loopback on one listener) ----
	creds, err := server.LoadServerCredentials(tlsCfg)
	if err != nil {
		logger.Error("TLS credentials", "error", err)
		os.Exit(1)
	}
	if creds == nil {
		logger.Warn("inter-node TLS is DISABLED — plaintext Raft RPC; set RAFT_TLS_* for any shared network (SEC-003)")
	}
	grpcSrv, lis, err := server.StartGRPCServer(raftAddr, node, creds)
	if err != nil {
		logger.Error("gRPC server", "error", err)
		os.Exit(1)
	}
	defer grpcSrv.GracefulStop()
	logger.Info("gRPC listening", "addr", lis.Addr())

	// ---- audit log ----
	auditLog, err := openAudit(logger)
	if err != nil {
		logger.Error("audit log", "error", err)
		os.Exit(1)
	}
	defer auditLog.Close()

	// ---- admin API ----
	api := server.NewAPI(node, fsm, auditLog, adminToken, logger)
	api.SetPeerAdminAddrs(parsePeerAddrs(os.Getenv("ADMIN_PEER_ADDRS")))

	mux := http.NewServeMux()
	mux.Handle("/", api.Handler())

	// Real metrics from live consensus state (FR-030).
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		syncMetrics(metrics, node, fsm, storage)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		metrics.WriteMetrics(w)
	})

	// Legacy v0.1 endpoints kept for the compose cluster and integration
	// tests; they delegate to the same consensus path as /v1.
	mux.HandleFunc("GET /leader", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"leader_id": node.LeaderID()})
	})
	mux.HandleFunc("POST /block", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IP string `json:"ip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		cmd := legacyBlockCommand(body.IP)
		idx, err := node.ProposeAndWait(r.Context(), cmd)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"acked": true, "index": idx})
	})
	mux.HandleFunc("GET /blocked/{ip}", func(w http.ResponseWriter, r *http.Request) {
		ip := r.PathValue("ip")
		writeJSON(w, map[string]any{"ip": ip, "blocked": fsm.IsBlocked(ip)})
	})

	httpServer := &http.Server{
		Addr:              httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("raftlite starting",
			"http_addr", httpAddr, "raft_addr", raftAddr,
			"node_id", nodeID, "peers", strings.Join(peers, ","))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	node.Stop()
	httpServer.Shutdown(shutdownCtx)
	grpcSrv.GracefulStop()
	logger.Info("raftlite stopped")
}

// syncMetrics samples consensus state into the metric registry at scrape
// time, so gauges always reflect the current node without a background
// sampler goroutine.
func syncMetrics(m *observability.Registry, node *consensus.Node, fsm *consensus.FSM, st transport.Storage) {
	m.SetLeader(node.State() == consensus.Leader)
	m.SetTerm(node.CurrentTerm())
	m.SetCommitIndex(node.GetCommitIndex())
	m.SetAppliedIndex(fsm.Revision())
	m.SetPolicies(int64(fsm.CountActive(time.Now())))
	if w, ok := st.(*transport.WAL); ok {
		if size, err := w.Size(); err == nil {
			m.SetWALSizeBytes(size)
		}
	}
}

func openAudit(logger *slog.Logger) (*audit.Logger, error) {
	path := audit.DefaultPath()
	if path == "" {
		logger.Warn("audit logging is disabled — set RAFTLITE_AUDIT_LOG (FR-031); recent entries are in-memory only")
		return audit.New(io.Discard), nil
	}
	l, err := audit.OpenFile(path)
	if err != nil {
		return nil, err
	}
	logger.Info("audit log enabled", "path", path)
	return l, nil
}

// legacyBlockCommand builds the structured create command for the v0.1
// POST /block endpoint so both entry points share one policy model.
func legacyBlockCommand(ip string) []byte {
	id, err := policy.NewID()
	if err != nil {
		return []byte("add:" + ip) // deterministic fallback to legacy parsing
	}
	cmd := policy.Command{
		Op:        policy.OpCreate,
		ID:        id,
		Type:      policy.TypeIPBlock,
		Target:    ip,
		Reason:    "legacy /block endpoint",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	payload, _ := json.Marshal(cmd)
	return payload
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parsePeerAddrs parses "id=host:port,id=host:port" pairs.
func parsePeerAddrs(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range splitComma(s) {
		if idx := strings.Index(pair, "="); idx > 0 {
			out[strings.TrimSpace(pair[:idx])] = strings.TrimSpace(pair[idx+1:])
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
}

// seedDemoData populates a fresh cluster with a few demo policies so the UI
// has something to show on first open. Dev only (RAFTLITE_DEV_UI=1); it runs
// on whichever node wins the election and only when the FSM is empty, so
// restarts and multi-node clusters do not duplicate data.
func seedDemoData(logger *slog.Logger, node *consensus.Node, fsm *consensus.FSM) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if node.State() == consensus.Leader {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if node.State() != consensus.Leader {
		return
	}
	if fsm.Revision() != 0 {
		return // data already exists (restart or another leader seeded)
	}

	ttl := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	type seed struct {
		target string
		cidr   bool
		reason string
		exp    *time.Time
	}
	seeds := []seed{
		{target: "203.0.113.10", reason: "demo: repeated auth failures"},
		{target: "203.0.113.11", reason: "demo: scraper abuse"},
		{target: "198.51.100.0/24", cidr: true, reason: "demo: blocked test range"},
		{target: "192.0.2.44", reason: "demo: temporary block (24h)", exp: &ttl},
	}
	for _, sd := range seeds {
		id, err := policy.NewID()
		if err != nil {
			logger.Warn("demo seed id", "error", err)
			return
		}
		typ := policy.TypeIPBlock
		if sd.cidr {
			typ = policy.TypeCIDR
		}
		cmd := policy.Command{
			Op:        policy.OpCreate,
			ID:        id,
			Type:      typ,
			Target:    sd.target,
			Reason:    sd.reason,
			CreatedBy: "demo-seed",
			CreatedAt: time.Now().UTC().Truncate(time.Second),
			ExpiresAt: sd.exp,
		}
		payload, _ := json.Marshal(cmd)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = node.ProposeAndWait(ctx, payload)
		cancel()
		if err != nil {
			logger.Warn("demo seed propose", "target", sd.target, "error", err)
			return
		}
	}
	logger.Info("demo seed data created", "policies", len(seeds))
}
