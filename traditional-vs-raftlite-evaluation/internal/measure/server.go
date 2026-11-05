package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/gateway"
	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/policy"
	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/ui"
)

// ServerConfig configures the measurement server (API + SSE + receipts
// collector + embedded dashboard).
type ServerConfig struct {
	ListenAddr    string // e.g. ":8095"
	ResultsDir    string // results root (raw/ inside)
	DatasetsDir   string // generated datasets
	BinDir        string // where traditional/gateway/adapter binaries live
	RaftLiteBin   string // path to raftlite binary (raftlite system only)
	WorkRoot      string // scratch root for per-start environment dirs
	BackendPort   int
	GatewayPorts  []int
	RaftHTTPPorts []int
	RaftGRPCPorts []int
	PollInterval  time.Duration
}

// Server is the single service behind the dashboard: it starts/stops
// environments, runs experiments, collects gateway receipts, and streams
// live events over SSE.
type Server struct {
	cfg ServerConfig
	log *slog.Logger
	hub *ReceiptHub

	mu         sync.Mutex
	env        *Env
	runActive  bool
	runCancel  context.CancelFunc
	results    []*ExperimentResult // newest last
	nextRunID  int
	envMessage string // last start/stop error, if any

	sseMu     sync.Mutex
	sseNextID int
	sseSubs   map[int]chan Event
}

// NewServer creates the measurement server (not yet listening).
func NewServer(cfg ServerConfig, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8095"
	}
	if cfg.ResultsDir == "" {
		cfg.ResultsDir = "results"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	return &Server{
		cfg:      cfg,
		log:      log,
		hub:      NewReceiptHub(),
		nextRunID: 1,
		sseSubs:  map[int]chan Event{},
	}
}

// Handler returns all HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Dashboard (embedded single page).
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(ui.IndexHTML)
	})

	// Receipt collector (gateways POST here).
	mux.HandleFunc("POST /receipts", s.hub.HandlePost)

	// Live event stream.
	mux.HandleFunc("GET /api/stream", s.handleStream)

	// Status + environment control.
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/env/start", s.handleEnvStart)
	mux.HandleFunc("POST /api/env/stop", s.handleEnvStop)

	// Experiments, datasets, runs.
	mux.HandleFunc("GET /api/experiments", s.handleExperiments)
	mux.HandleFunc("GET /api/datasets", s.handleDatasets)
	mux.HandleFunc("POST /api/runs", s.handleRunStart)
	mux.HandleFunc("POST /api/runs/cancel", s.handleRunCancel)
	mux.HandleFunc("GET /api/runs", s.handleRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.handleRunGet)

	// CSV downloads.
	mux.Handle("GET /results/", http.StripPrefix("/results/",
		http.FileServer(http.Dir(s.cfg.ResultsDir))))

	return mux
}

// ---- status ----

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	status := map[string]any{
		"system":         "",
		"running":        false,
		"backend_url":    "",
		"gateways":       []any{},
		"components":     []string{},
		"run_active":     s.runActive,
		"env_message":    s.envMessage,
		"runs":           len(s.results),
	}
	if s.env != nil {
		status["system"] = s.env.cfg.System
		status["running"] = s.env.Running()
		status["backend_url"] = s.env.BackendURL
		status["components"] = s.env.Components()
		gws := []map[string]string{}
		for _, id := range s.env.GatewayIDs {
			gws = append(gws, map[string]string{"id": id, "url": s.env.GatewayURLs[id]})
		}
		status["gateways"] = gws
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleEnvStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		System string `json:"system"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.System != "traditional" && req.System != "raftlite") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "system must be \"traditional\" or \"raftlite\""})
		return
	}

	s.mu.Lock()
	if s.env != nil && s.env.Running() {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "environment already running; stop it first"})
		return
	}
	if s.runActive {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "a run is active; cancel it first"})
		return
	}
	s.envMessage = ""
	s.mu.Unlock()

	workDir := filepath.Join(s.cfg.WorkRoot, fmt.Sprintf("%s-%d", req.System, time.Now().Unix()))
	env := NewEnv(EnvConfig{
		System:        req.System,
		RaftLiteBin:   s.cfg.RaftLiteBin,
		WorkDir:       workDir,
		BackendPort:   s.cfg.BackendPort,
		GatewayPorts:  s.cfg.GatewayPorts,
		RaftHTTPPorts: s.cfg.RaftHTTPPorts,
		RaftGRPCPorts: s.cfg.RaftGRPCPorts,
		ReceiptsURL:   "http://127.0.0.1" + listenPort(s.cfg.ListenAddr) + "/receipts",
		PollInterval:  s.cfg.PollInterval,
		BinDir:        s.cfg.BinDir,
	}, s.log)

	if err := env.Start(r.Context()); err != nil {
		s.setEnvMessage(err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
	s.broadcast(Event{Type: "env_status", Data: map[string]any{
		"system": req.System, "running": true, "backend_url": env.BackendURL,
	}})
	writeJSON(w, http.StatusOK, map[string]any{
		"system": req.System, "running": true, "backend_url": env.BackendURL,
		"gateways": len(env.GatewayIDs),
	})
}

func (s *Server) handleEnvStop(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	if s.runActive {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "cancel the active run first"})
		return
	}
	env := s.env
	s.env = nil
	s.mu.Unlock()
	if env != nil {
		env.Stop()
	}
	s.broadcast(Event{Type: "env_status", Data: map[string]any{"running": false}})
	writeJSON(w, http.StatusOK, map[string]any{"running": false})
}

func (s *Server) setEnvMessage(msg string) {
	s.mu.Lock()
	s.envMessage = msg
	s.mu.Unlock()
	s.broadcast(Event{Type: "env_status", Data: map[string]any{"running": false, "error": msg}})
}

// ---- experiments & datasets ----

func (s *Server) handleExperiments(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []map[string]string{
		{
			"id":   "propagation",
			"name": "Policy propagation latency",
			"desc": "Submit policies at a fixed rate; measure the time from submission until each gateway applies it. Reports mean/median/min/max/stddev/P95/P99 per gateway and overall.",
		},
		{
			"id":   "consistency",
			"name": "Consistency (correctness)",
			"desc": "Submit the full dataset, wait for convergence, then verify every gateway holds exactly the expected policy set (matched by IP). Reports correct/missing/incorrect counts per gateway.",
		},
		{
			"id":   "server_failure",
			"name": "Server failure",
			"desc": "Baseline writes, then kill the backend write path (traditional server / raftlite node1), keep writing during the outage, restart it, and measure failures during the outage plus recovery time. Gateways must keep enforcing from cache throughout.",
		},
		{
			"id":   "partition",
			"name": "Network partition (raftlite only)",
			"desc": "Isolate node3 from the 3-node cluster (single-host approximation of a partition), verify the surviving majority still accepts writes, heal the node and measure its log catch-up. Not applicable to the single traditional server.",
		},
		{
			"id":   "gateway_recovery",
			"name": "Gateway recovery",
			"desc": "Kill one gateway, submit policies while it is down (so it misses them), restart it and measure how long it takes to re-converge and start enforcing again.",
		},
		{
			"id":   "http_performance",
			"name": "HTTP request performance",
			"desc": "Load the gateway data path with a mix of blocked and allowed source IPs (rate = target RPS) and record the request latency distribution plus enforcement correctness.",
		},
		{
			"id":   "resource_usage",
			"name": "Resource usage",
			"desc": "Run a sustained policy workload for a fixed window while sampling CPU, memory and disk usage of every component once per second.",
		},
	})
}

func (s *Server) handleDatasets(w http.ResponseWriter, _ *http.Request) {
	entries, err := os.ReadDir(s.cfg.DatasetsDir)
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	out := []map[string]any{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ds, err := policy.LoadDataset(filepath.Join(s.cfg.DatasetsDir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"file": e.Name(), "name": ds.Name, "policies": len(ds.Policies),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["file"].(string) < out[j]["file"].(string) })
	writeJSON(w, http.StatusOK, out)
}

// ---- runs ----

func (s *Server) handleRunStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Experiment string `json:"experiment"`
		Dataset    string `json:"dataset"`
		Rate       int    `json:"rate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if req.Experiment != "propagation" && req.Experiment != "consistency" &&
		req.Experiment != "server_failure" && req.Experiment != "partition" &&
		req.Experiment != "gateway_recovery" && req.Experiment != "http_performance" &&
		req.Experiment != "resource_usage" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown experiment"})
		return
	}
	if req.Dataset == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "dataset required"})
		return
	}
	// Prevent path traversal: datasets are files inside DatasetsDir.
	if strings.ContainsAny(req.Dataset, `/\`) || strings.Contains(req.Dataset, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid dataset name"})
		return
	}
	ds, err := policy.LoadDataset(filepath.Join(s.cfg.DatasetsDir, req.Dataset))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "dataset not found: " + err.Error()})
		return
	}

	s.mu.Lock()
	if s.runActive {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "a run is already active"})
		return
	}
	if s.env == nil || !s.env.Running() {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "environment not running; start it first"})
		return
	}
	env := s.env
	runID := s.nextRunID
	s.nextRunID++
	s.runActive = true
	ctx, cancel := context.WithCancel(context.Background())
	s.runCancel = cancel
	s.mu.Unlock()

	runner := NewRunner(env, s.hub, s.cfg.ResultsDir, s.broadcast, func() int { return runID })
	if req.Rate <= 0 {
		req.Rate = 10
	}

	go func() {
		defer func() {
			s.mu.Lock()
			s.runActive = false
			s.runCancel = nil
			s.mu.Unlock()
			cancel()
		}()
		var res *ExperimentResult
		var runErr error
		switch req.Experiment {
		case "propagation":
			res, runErr = runner.RunPropagation(ctx, PropagationOpts{
				Dataset: *ds, RatePerSec: req.Rate,
			})
		case "consistency":
			res, runErr = runner.RunConsistency(ctx, ConsistencyOpts{
				Dataset: *ds, RatePerSec: req.Rate,
			})
		case "server_failure":
			res, runErr = runner.RunServerFailure(ctx, FailureOpts{
				Dataset: *ds, RatePerSec: req.Rate,
			})
		case "partition":
			res, runErr = runner.RunPartition(ctx, FailureOpts{
				Dataset: *ds, RatePerSec: req.Rate,
			})
		case "gateway_recovery":
			res, runErr = runner.RunGatewayRecovery(ctx, FailureOpts{
				Dataset: *ds, RatePerSec: req.Rate,
			})
		case "http_performance":
			// rate = target RPS; defaults chosen for a single machine.
			res, runErr = runner.RunHTTPPerformance(ctx, PerfOpts{
				Dataset: *ds, Requests: 2000, Concurrency: 20, TargetRPS: req.Rate,
			})
		case "resource_usage":
			res, runErr = runner.RunResourceUsage(ctx, ResourceOpts{
				Dataset: *ds, RatePerSec: req.Rate, Duration: 20 * time.Second,
			})
		}
		if runErr != nil {
			s.broadcast(Event{Type: "run_error", Data: map[string]any{
				"run_id": runID, "experiment": req.Experiment, "error": runErr.Error(),
			}})
			return
		}
		s.mu.Lock()
		s.results = append(s.results, res)
		s.mu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID, "experiment": req.Experiment})
}

func (s *Server) handleRunCancel(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	if !s.runActive || s.runCancel == nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "no active run"})
		return
	}
	cancel := s.runCancel
	s.mu.Unlock()
	cancel()
	writeJSON(w, http.StatusOK, map[string]any{"cancelling": true})
}

func (s *Server) handleRuns(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := make([]map[string]any, 0, len(s.results))
	for _, res := range s.results {
		out = append(out, summarizeResult(res))
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRunGet(w http.ResponseWriter, r *http.Request) {
	var id int
	fmt.Sscanf(r.PathValue("id"), "%d", &id)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, res := range s.results {
		if res.RunID == id {
			full := summarizeResult(res)
			full["stats"] = res.Stats
			full["extra"] = res.Extra
			writeJSON(w, http.StatusOK, full)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "run not found"})
}

// summarizeResult is the JSON shape used by list/detail endpoints.
func summarizeResult(res *ExperimentResult) map[string]any {
	return map[string]any{
		"run_id":      res.RunID,
		"system":      res.System,
		"experiment":  res.Experiment,
		"dataset":     res.Dataset,
		"started_at":  res.StartedAt,
		"finished_at": res.FinishedAt,
		"duration_ms": round2(float64(res.FinishedAt.Sub(res.StartedAt).Microseconds()) / 1000),
		"csv_path":    res.CSVPath,
		"summary":     res.SummaryPath,
		"stats":       res.Stats,
		"extra":       res.Extra,
	}
}

// ---- SSE ----

func (s *Server) subscribeSSE() (<-chan Event, func()) {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	s.sseNextID++
	id := s.sseNextID
	ch := make(chan Event, 512)
	s.sseSubs[id] = ch
	return ch, func() {
		s.sseMu.Lock()
		delete(s.sseSubs, id)
		s.sseMu.Unlock()
	}
}

// broadcast sends an event to every SSE subscriber (non-blocking: slow
// consumers drop events rather than stall experiments).
func (s *Server) broadcast(ev Event) {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	for _, ch := range s.sseSubs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.subscribeSSE()
	defer unsub()

	// Immediate snapshot so the UI can render before the next event.
	hello, _ := json.Marshal(map[string]any{"type": "hello"})
	fmt.Fprintf(w, "data: %s\n\n", hello)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-time.After(15 * time.Second):
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// ---- misc ----

// listenPort extracts ":PORT" from a listen address for URL building.
func listenPort(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return addr
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ReceiptFromJSON is exported for tests that fabricate gateway receipts.
func ReceiptFromJSON(b []byte) (gateway.Receipt, error) {
	var rc gateway.Receipt
	err := json.Unmarshal(b, &rc)
	return rc, err
}
