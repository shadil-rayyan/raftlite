package measure

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EnvConfig configures one experimental environment.
type EnvConfig struct {
	System        string        // "traditional" | "raftlite"
	RaftLiteBin   string        // path to the raftlite binary (raftlite system only)
	WorkDir       string        // scratch dir for DBs, WALs, logs
	BackendPort   int           // traditional server / raftlite adapter
	GatewayPorts  []int         // one per gateway
	RaftHTTPPorts []int         // raftlite HTTP admin ports (raftlite only)
	RaftGRPCPorts []int         // raftlite gRPC ports (raftlite only)
	ReceiptsURL   string        // measurement collector endpoint
	PollInterval  time.Duration // gateway poll interval (default 500ms)
	BinDir        string        // where traditional/gateway/adapter binaries live
}

// componentSpec records exactly how a component was spawned so failure and
// recovery experiments can restart it with identical configuration.
type componentSpec struct {
	bin  string
	args []string
	env  []string
}

// Env is a running environment: the backend plus the gateway fleet.
type Env struct {
	cfg      EnvConfig
	log      *slog.Logger
	mu       sync.Mutex
	cmds     map[string]*exec.Cmd   // component name -> live process
	specs    map[string]componentSpec // component name -> how to (re)spawn it
	logFiles map[string]*os.File    // component name -> its log handle
	running  bool

	BackendURL  string            // what gateways poll (uniform contract)
	NodeURLs    []string          // raftlite node admin URLs (raftlite only)
	GatewayIDs  []string          // e.g. ["G1",...]
	GatewayURLs map[string]string // gateway id -> base URL
	StartedAt   time.Time
}

// binName returns the platform-appropriate binary name.
func binName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// NewEnv creates a stopped environment description.
func NewEnv(cfg EnvConfig, log *slog.Logger) *Env {
	if log == nil {
		log = slog.Default()
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.BackendPort == 0 {
		cfg.BackendPort = 8100
	}
	if len(cfg.GatewayPorts) == 0 {
		cfg.GatewayPorts = []int{8110, 8111, 8112, 8113, 8114}
	}
	e := &Env{
		cfg:         cfg,
		log:         log,
		cmds:        map[string]*exec.Cmd{},
		specs:       map[string]componentSpec{},
		logFiles:    map[string]*os.File{},
		BackendURL:  fmt.Sprintf("http://127.0.0.1:%d", cfg.BackendPort),
		GatewayURLs: map[string]string{},
	}
	for i, p := range cfg.GatewayPorts {
		id := fmt.Sprintf("G%d", i+1)
		e.GatewayIDs = append(e.GatewayIDs, id)
		e.GatewayURLs[id] = fmt.Sprintf("http://127.0.0.1:%d", p)
	}
	return e
}

// Running reports whether the environment is up.
func (e *Env) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// Components returns the names of live components.
func (e *Env) Components() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.cmds))
	for name := range e.cmds {
		out = append(out, name)
	}
	return out
}

// ComponentRunning reports whether a named component is currently alive.
func (e *Env) ComponentRunning(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.cmds[name]
	return ok
}

// ComponentPIDs maps live component names to OS process IDs (used by the
// resource-usage sampler).
func (e *Env) ComponentPIDs() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int, len(e.cmds))
	for name, cmd := range e.cmds {
		if cmd.Process != nil {
			out[name] = cmd.Process.Pid
		}
	}
	return out
}

// BackendComponentName returns the component that owns the backend write
// path: the single traditional server, or — for RaftLite — the node the
// adapter prefers (node1). Killing it is the "server failure" scenario.
func (e *Env) BackendComponentName() string {
	if e.cfg.System == "raftlite" {
		return "node1"
	}
	return "traditional"
}

// spawnLocked starts a component from its spec, records the spec for later
// restarts, and keeps its log handle. Caller holds e.mu.
func (e *Env) spawnLocked(name, bin string, args, envVars []string) error {
	logsDir := filepath.Join(e.cfg.WorkDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = e.cfg.WorkDir
	cmd.Env = append(os.Environ(), envVars...)
	setupProc(cmd)
	// Replacing a previous log handle (component restart): close it first
	// so repeated restarts do not leak handles.
	if old, ok := e.logFiles[name]; ok {
		old.Close()
		delete(e.logFiles, name)
	}
	f, err := os.Create(filepath.Join(logsDir, name+".log"))
	if err != nil {
		return err
	}
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		f.Close()
		return fmt.Errorf("start %s: %w", name, err)
	}
	e.cmds[name] = cmd
	e.specs[name] = componentSpec{bin: bin, args: args, env: envVars}
	e.logFiles[name] = f
	e.log.Info("started component", "name", name, "pid", cmd.Process.Pid, "bin", bin)
	return nil
}

// Start spawns every component and waits until the backend and all
// gateways answer /healthz.
func (e *Env) Start(ctx context.Context) (err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return fmt.Errorf("environment already running")
	}
	// Absolute WorkDir: children run with cmd.Dir set to it, so every path
	// we pass them (DB, WAL, snapshots) must not be relative to the measure
	// server's own CWD or they would resolve inside it.
	if abs, absErr := filepath.Abs(e.cfg.WorkDir); absErr == nil {
		e.cfg.WorkDir = abs
	}
	// On any failure, kill whatever spawned and release log handles.
	defer func() {
		if err != nil {
			e.killAllLocked()
		}
	}()
	if err := os.MkdirAll(e.cfg.WorkDir, 0o755); err != nil {
		return err
	}

	switch e.cfg.System {
	case "traditional":
		bin := filepath.Join(e.cfg.BinDir, binName("traditional"))
		if err := e.spawnLocked("traditional", bin, []string{
			"-db", filepath.Join(e.cfg.WorkDir, "policies.db"),
			"-listen", fmt.Sprintf("127.0.0.1:%d", e.cfg.BackendPort),
		}, nil); err != nil {
			return err
		}
		e.BackendURL = fmt.Sprintf("http://127.0.0.1:%d", e.cfg.BackendPort)

	case "raftlite":
		if e.cfg.RaftLiteBin == "" {
			return fmt.Errorf("raftlite system requires RaftLiteBin (path to the raftlite binary)")
		}
		n := len(e.cfg.RaftHTTPPorts)
		if n == 0 {
			return fmt.Errorf("raftlite system requires RaftHTTPPorts")
		}
		var ids, peerAddrs, adminAddrs []string
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("node%d", i+1)
			ids = append(ids, id)
			peerAddrs = append(peerAddrs, fmt.Sprintf("%s=127.0.0.1:%d", id, e.cfg.RaftGRPCPorts[i]))
			adminAddrs = append(adminAddrs, fmt.Sprintf("%s=http://127.0.0.1:%d", id, e.cfg.RaftHTTPPorts[i]))
			e.NodeURLs = append(e.NodeURLs, fmt.Sprintf("http://127.0.0.1:%d", e.cfg.RaftHTTPPorts[i]))
		}
		for i := 0; i < n; i++ {
			id := ids[i]
			nodeDir := filepath.Join(e.cfg.WorkDir, id)
			if err := os.MkdirAll(nodeDir, 0o755); err != nil {
				return err
			}
			vars := []string{
				"NODE_ID=" + id,
				fmt.Sprintf("RAFT_ADDR=127.0.0.1:%d", e.cfg.RaftGRPCPorts[i]),
				fmt.Sprintf("RAFTLITE_ADDR=127.0.0.1:%d", e.cfg.RaftHTTPPorts[i]),
				"PEERS=" + joinComma(ids),
				"PEER_ADDRS=" + joinComma(peerAddrs),
				"ADMIN_PEER_ADDRS=" + joinComma(adminAddrs),
				"ADMIN_INTERNAL_PEER_ADDRS=" + joinComma(adminAddrs),
				"WAL_PATH=" + filepath.Join(nodeDir, id+".wal"),
				"SNAPSHOT_DIR=" + filepath.Join(nodeDir, "snapshots"),
			}
			if err := e.spawnLocked(id, e.cfg.RaftLiteBin, nil, vars); err != nil {
				return err
			}
		}
		// The adapter presents RaftLite under the uniform contract. It is
		// given EVERY node URL so it can fail over when a node dies — that
		// is what makes the RaftLite environment survive server failure.
		adapterBin := filepath.Join(e.cfg.BinDir, binName("adapter"))
		if err := e.spawnLocked("adapter", adapterBin, []string{
			"-raftlite", joinCommaHTTP(adminAddrs),
			"-listen", fmt.Sprintf("127.0.0.1:%d", e.cfg.BackendPort),
		}, nil); err != nil {
			return err
		}
		e.BackendURL = fmt.Sprintf("http://127.0.0.1:%d", e.cfg.BackendPort)

	default:
		return fmt.Errorf("unknown system %q", e.cfg.System)
	}

	// Gateway fleet: identical for both systems — only the backend URL
	// differs, which is the whole point of the comparison.
	gwBin := filepath.Join(e.cfg.BinDir, binName("gateway"))
	for i, port := range e.cfg.GatewayPorts {
		id := fmt.Sprintf("G%d", i+1)
		if err := e.spawnLocked(id, gwBin, []string{
			"-id", id,
			"-listen", fmt.Sprintf("127.0.0.1:%d", port),
			"-backend", e.BackendURL,
			"-poll", e.cfg.PollInterval.String(),
			"-receipts", e.cfg.ReceiptsURL,
		}, nil); err != nil {
			return err
		}
	}

	// Wait for health: backend first, then every gateway.
	healthCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := waitHealthy(healthCtx, e.BackendURL+"/healthz"); err != nil {
		return fmt.Errorf("backend never healthy: %w", err)
	}
	// For RaftLite the cluster must also have elected a leader and answer
	// policy reads through the adapter before the environment is ready.
	if e.cfg.System == "raftlite" {
		if err := waitBackendReady(healthCtx, e.BackendURL+"/v1/policies"); err != nil {
			return fmt.Errorf("raftlite never ready for policy reads: %w", err)
		}
	}
	for _, id := range e.GatewayIDs {
		if err := waitHealthy(healthCtx, e.GatewayURLs[id]+"/healthz"); err != nil {
			return fmt.Errorf("gateway %s never healthy: %w", id, err)
		}
	}

	e.running = true
	e.StartedAt = time.Now()
	e.log.Info("environment ready", "system", e.cfg.System, "backend", e.BackendURL)
	return nil
}

// StartComponent (re)starts one component from its recorded spec and waits
// until it is healthy again. Used by server-failure and gateway-recovery
// experiments. The rest of the environment keeps running throughout.
func (e *Env) StartComponent(ctx context.Context, name string) error {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return fmt.Errorf("environment not running")
	}
	spec, ok := e.specs[name]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("unknown component %q", name)
	}
	if _, live := e.cmds[name]; live {
		e.mu.Unlock()
		return fmt.Errorf("component %q already running", name)
	}
	if err := e.spawnLocked(name, spec.bin, spec.args, spec.env); err != nil {
		e.mu.Unlock()
		return err
	}
	e.mu.Unlock()

	// Health wait happens outside the lock so the rest of the environment
	// stays responsive while a component boots.
	waitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if gwURL, isGateway := e.GatewayURLs[name]; isGateway {
		return waitHealthy(waitCtx, gwURL+"/healthz")
	}
	// A restarted raftlite node must answer on ITS OWN endpoint: waiting on
	// the adapter would succeed immediately via the surviving majority and
	// understate recovery time.
	if idx, ok := e.nodeIndex(name); ok {
		return waitHealthy(waitCtx, e.NodeURLs[idx]+"/healthz")
	}
	// Backend-side components: the uniform contract must answer again.
	return waitBackendReady(waitCtx, e.BackendURL+"/v1/policies")
}

// Stop kills every component and closes log handles.
func (e *Env) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.killAllLocked()
	e.running = false
	e.log.Info("environment stopped", "system", e.cfg.System)
}

// killAllLocked kills all spawned processes and closes log handles.
// Caller holds e.mu.
func (e *Env) killAllLocked() {
	for name, cmd := range e.cmds {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		e.log.Info("stopped component", "name", name)
	}
	e.cmds = map[string]*exec.Cmd{}
	for name, f := range e.logFiles {
		f.Close()
		delete(e.logFiles, name)
	}
}

// StopComponent kills one component by name (failure experiments). The
// component's spec is retained so it can be started again later.
func (e *Env) StopComponent(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cmd, ok := e.cmds[name]
	if !ok {
		return fmt.Errorf("component %q not running", name)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	delete(e.cmds, name)
	if f, ok := e.logFiles[name]; ok {
		f.Close()
		delete(e.logFiles, name)
	}
	e.log.Info("stopped component", "name", name)
	return nil
}

func waitHealthy(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// waitBackendReady polls a real data endpoint (not just /healthz) until it
// answers 200 — used for RaftLite so a restarted component is not declared
// ready before the cluster serves policy reads again.
func waitBackendReady(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// nodeIndex maps a component name like "node2" to its index in NodeURLs.
func (e *Env) nodeIndex(name string) (int, bool) {
	if !strings.HasPrefix(name, "node") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(name, "node"))
	if err != nil || n < 1 || n > len(e.NodeURLs) {
		return 0, false
	}
	return n - 1, true
}

func joinComma(parts []string) string {
	return strings.Join(parts, ",")
}

// joinCommaHTTP extracts just the URL part of "id=http://host:port" pairs
// and joins them for the adapter's -raftlite flag.
func joinCommaHTTP(pairs []string) string {
	urls := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if i := strings.Index(p, "="); i >= 0 {
			urls = append(urls, p[i+1:])
		}
	}
	return strings.Join(urls, ",")
}
