// Command adapter is the external adapter that connects the common
// gateway to RaftLite: it serves the uniform backend contract
// (GET/POST/DELETE /v1/policies) while translating to RaftLite's admin
// API. It is a fetch-through proxy — no extra polling staleness.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/adapter"
)

func main() {
	raftlite := flag.String("raftlite", "http://127.0.0.1:8081", "RaftLite base URL (any node)")
	listen := flag.String("listen", "127.0.0.1:8100", "listen address")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	a := adapter.New(*raftlite, log)
	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Info("adapter listening", "addr", *listen, "raftlite", *raftlite)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("adapter failed", "error", err)
		os.Exit(1)
	}
}
