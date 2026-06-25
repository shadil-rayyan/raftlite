package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/server"
	"github.com/shadil/raftlite/internal/transport"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	nodeID := env("NODE_ID", "node1")
	raftAddr := env("RAFT_ADDR", ":9090")
	httpAddr := env("RAFTLITE_ADDR", ":8080")
	peersRaw := env("PEERS", "node1,node2,node3")
	peers := splitComma(peersRaw)

	storage, err := transport.OpenWAL(env("WAL_PATH", "/tmp/raftlite.wal"))
	if err != nil {
		logger.Error("open WAL", "error", err)
		os.Exit(1)
	}
	defer storage.Close()

	fsm := consensus.NewFSM()
	node := consensus.NewNode(nodeID, peers, transport.RealClock{}, storage, fsm)
	node.Start()

	grpcSrv, lis, err := server.StartGRPCServer(raftAddr, node)
	if err != nil {
		logger.Error("gRPC server", "error", err)
		os.Exit(1)
	}
	defer grpcSrv.GracefulStop()
	logger.Info("gRPC listening", "addr", lis.Addr())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", jsonHandler(map[string]string{"status": "ok"}))
	mux.HandleFunc("GET /readyz", jsonHandler(map[string]string{"status": node.State().String()}))
	mux.HandleFunc("GET /leader", jsonHandler(map[string]string{"leader_id": node.LeaderID()}))
	mux.HandleFunc("POST /block", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IP string `json:"ip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		idx, err := node.Propose([]byte("add:" + body.IP))
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"acked": true, "index": idx})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "# HELP raftlite_requests_total Total requests processed\n")
		fmt.Fprintf(w, "# TYPE raftlite_requests_total counter\n")
		fmt.Fprintf(w, "raftlite_requests_total 0\n")
		fmt.Fprintf(w, "# HELP raftlite_elections_total Total leader elections\n")
		fmt.Fprintf(w, "# TYPE raftlite_elections_total counter\n")
		fmt.Fprintf(w, "raftlite_elections_total 0\n")
		fmt.Fprintf(w, "# HELP raftlite_blocklist_size Current blocklist size\n")
		fmt.Fprintf(w, "# TYPE raftlite_blocklist_size gauge\n")
		fmt.Fprintf(w, "raftlite_blocklist_size 0\n")
	})
	mux.HandleFunc("GET /blocked/{ip}", func(w http.ResponseWriter, r *http.Request) {
		ip := r.PathValue("ip")
		blocked := fsm.IsBlocked(ip)
		json.NewEncoder(w).Encode(map[string]any{"ip": ip, "blocked": blocked})
	})

	httpServer := &http.Server{
		Addr:              httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("raftlite starting", "http_addr", httpAddr, "raft_addr", raftAddr, "node_id", nodeID)
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
	logger.Info("raftlite stopped")
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
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func jsonHandler(payload any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payload)
	}
}
