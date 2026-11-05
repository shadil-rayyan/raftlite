// Command traditional is the centralized policy server: one process, one
// SQLite file, a monotonic revision counter. This is the "Approach 1"
// backend in the comparison.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/traditional"
)

func main() {
	dbPath := flag.String("db", "policies.db", "SQLite database file")
	listen := flag.String("listen", "127.0.0.1:8100", "listen address")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	srv, err := traditional.Open(*dbPath, log)
	if err != nil {
		log.Error("open database failed", "error", err)
		os.Exit(1)
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Info("traditional server listening", "addr", *listen, "db", *dbPath)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("server failed", "error", err)
		os.Exit(1)
	}
}
