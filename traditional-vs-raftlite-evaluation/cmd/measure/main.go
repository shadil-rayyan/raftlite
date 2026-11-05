// Command measure is the measurement server: it hosts the dashboard,
// starts/stops experimental environments (spawning the real
// traditional/gateway/adapter/raftlite processes), runs experiments,
// collects gateway receipt timestamps over HTTP, and streams live events
// to the browser over SSE.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/measure"
)

func binName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func main() {
	listen := flag.String("listen", ":8095", "dashboard/API listen address")
	datasets := flag.String("datasets", "datasets", "datasets directory")
	results := flag.String("results", "results", "results directory (CSVs land in results/raw)")
	work := flag.String("work", "work", "scratch root for environment processes")
	raftliteBin := flag.String("raftlite-bin", os.Getenv("RAFTLITE_BIN"), "path to the raftlite binary (raftlite system only)")
	poll := flag.Duration("poll", 500*time.Millisecond, "gateway poll interval")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Component binaries live next to this executable unless overridden.
	self, err := os.Executable()
	binDir := filepath.Dir(self)
	if err == nil && binDir == "" {
		binDir = "."
	}
	// Default the raftlite binary to a copy sitting next to the others,
	// so scripts only need to place it in bin/ (or set RAFTLITE_BIN).
	if *raftliteBin == "" {
		candidate := filepath.Join(binDir, binName("raftlite"))
		if _, statErr := os.Stat(candidate); statErr == nil {
			*raftliteBin = candidate
		}
	}

	srv := measure.NewServer(measure.ServerConfig{
		ListenAddr:   *listen,
		ResultsDir:   *results,
		DatasetsDir:  *datasets,
		BinDir:       binDir,
		RaftLiteBin:  *raftliteBin,
		WorkRoot:     *work,
		BackendPort:  8100,
		GatewayPorts: []int{8110, 8111, 8112, 8113, 8114},
		// RaftLite cluster: 3 nodes (HTTP admin + gRPC raft).
		RaftHTTPPorts: []int{8081, 8082, 8083},
		RaftGRPCPorts: []int{9091, 9092, 9093},
		PollInterval:  *poll,
	}, log)

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Info("measurement server listening",
		"addr", *listen, "datasets", *datasets, "results", *results, "bindir", binDir)
	if *raftliteBin != "" {
		log.Info("raftlite binary", "path", *raftliteBin)
	} else {
		log.Warn("RAFTLITE_BIN not set — the raftlite environment cannot start until it is")
	}
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("server failed", "error", err)
		os.Exit(1)
	}
}
