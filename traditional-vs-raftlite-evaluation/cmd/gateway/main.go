// Command gateway is the single gateway application used in both
// environments. It polls its configured backend for policies and
// enforces them on its data path; the only difference between
// environments is -backend.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/gateway"
)

func main() {
	id := flag.String("id", "G1", "gateway id")
	listen := flag.String("listen", "127.0.0.1:8110", "listen address")
	backend := flag.String("backend", "http://127.0.0.1:8100", "backend base URL (traditional server or raftlite adapter)")
	poll := flag.Duration("poll", 500*time.Millisecond, "backend poll interval")
	receipts := flag.String("receipts", "", "measurement collector receipts URL (optional)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	gw := gateway.New(gateway.Config{
		ID:           *id,
		ListenAddr:   *listen,
		BackendURL:   *backend,
		PollInterval: *poll,
		ReceiptsURL:  *receipts,
		Logger:       log,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go gw.Run(ctx)

	srv := &http.Server{Addr: *listen, Handler: gw.Handler()}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shCtx)
	}()

	log.Info("gateway listening", "id", *id, "addr", *listen, "backend", *backend, "poll", poll.String())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("gateway failed", "error", err)
		os.Exit(1)
	}
}
