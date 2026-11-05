package measure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/gateway"
	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/policy"
)

// Event is one SSE message streamed to the UI.
type Event struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// SampleRow is one measured propagation latency (one policy on one
// gateway). Rows are never discarded — slow results are data.
type SampleRow struct {
	RunID       int     `json:"run_id"`
	PolicyID    string  `json:"policy_id"`
	IP          string  `json:"ip"`
	GatewayID   string  `json:"gateway_id"`
	SubmitAtNs  int64   `json:"submit_at_ns"`
	AppliedAtNs int64   `json:"applied_at_ns"`
	LatencyMs   float64 `json:"latency_ms"`
}

// ExperimentResult is the complete output of one run.
type ExperimentResult struct {
	RunID       int              `json:"run_id"`
	System      string           `json:"system"`
	Experiment  string           `json:"experiment"`
	Dataset     string           `json:"dataset"`
	StartedAt   time.Time        `json:"started_at"`
	FinishedAt  time.Time        `json:"finished_at"`
	Stats       map[string]Stats `json:"stats"` // per gateway + "all"
	Extra       map[string]any   `json:"extra,omitempty"`
	Rows        []SampleRow      `json:"-"`
	CSVPath     string           `json:"csv_path,omitempty"`
	SummaryPath string           `json:"summary_path,omitempty"`
}

// pendingState tracks one submitted policy while its receipts arrive.
type pendingState struct {
	submitAt time.Time
	done     map[string]bool // gatewayID -> received
	rows     []SampleRow
}

// pendingTracker is the concurrency-safe registry of in-flight policies.
type pendingTracker struct {
	mu      sync.Mutex
	total   int
	gwCount int
	byIP    map[string]*pendingState
}

func newPendingTracker(total int) *pendingTracker {
	return &pendingTracker{total: total, byIP: map[string]*pendingState{}}
}

func (t *pendingTracker) add(ip string, submitAt time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byIP[ip] = &pendingState{submitAt: submitAt, done: map[string]bool{}}
}

// record files a receipt; returns the row and true when this completes
// the policy (all gateways have it).
func (t *pendingTracker) record(rc gateway.Receipt, runID int) (SampleRow, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.byIP[rc.IP]
	if !ok || p.done[rc.GatewayID] {
		return SampleRow{}, false
	}
	p.done[rc.GatewayID] = true
	row := SampleRow{
		RunID:       runID,
		PolicyID:    rc.PolicyID,
		IP:          rc.IP,
		GatewayID:   rc.GatewayID,
		SubmitAtNs:  p.submitAt.UnixNano(),
		AppliedAtNs: rc.AppliedAt,
		LatencyMs:   float64(rc.AppliedAt-p.submitAt.UnixNano()) / 1e6,
	}
	p.rows = append(p.rows, row)
	return row, len(p.done) >= t.gatewayCount()
}

// gatewayCount derives the fleet size from the first completed policy;
// set explicitly via setGatewayCount when the tracker is created.
func (t *pendingTracker) gatewayCount() int { return t.gwCount }

// snapshot returns a copy of all recorded rows.
func (t *pendingTracker) snapshot() []SampleRow {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []SampleRow
	for _, p := range t.byIP {
		out = append(out, p.rows...)
	}
	return out
}

// complete reports whether every tracked policy has been received by
// every gateway. A zero total (no successful submissions yet) is never
// complete, so collectors cannot exit early during submission.
func (t *pendingTracker) complete() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.total <= 0 || len(t.byIP) < t.total {
		return false
	}
	for _, p := range t.byIP {
		if len(p.done) < t.gwCount {
			return false
		}
	}
	return true
}

// added returns how many policies are currently tracked (successfully
// submitted ones).
func (t *pendingTracker) added() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byIP)
}

// setTotal records how many policies are expected to complete. Called once
// submission has finished, because only successful submissions are tracked.
func (t *pendingTracker) setTotal(n int) {
	t.mu.Lock()
	t.total = n
	t.mu.Unlock()
}

// remove drops a policy from tracking after its submit failed, so the run
// does not wait forever for receipts that can never arrive. Must be called
// before setTotal.
func (t *pendingTracker) remove(ip string) {
	t.mu.Lock()
	delete(t.byIP, ip)
	t.mu.Unlock()
}

// Runner executes experiments against a running environment.
type Runner struct {
	env        *Env
	receipts   *ReceiptHub
	resultsDir string
	emit       func(Event)
	nextRunID  func() int
	client     *http.Client
}

// NewRunner creates a runner bound to an environment.
func NewRunner(env *Env, receipts *ReceiptHub, resultsDir string, emit func(Event), nextRunID func() int) *Runner {
	return &Runner{
		env:        env,
		receipts:   receipts,
		resultsDir: resultsDir,
		emit:       emit,
		nextRunID:  nextRunID,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// ---- propagation (Test 1) ----

// PropagationOpts configures policy-propagation latency measurement.
type PropagationOpts struct {
	Dataset    policy.Dataset
	RatePerSec int           // submissions per second (0 = as fast as possible)
	MaxWait    time.Duration // after final submit, wait for stragglers (default 60s)
}

// RunPropagation submits every policy in order, records when each
// gateway applies it, and returns per-gateway latency statistics.
func (r *Runner) RunPropagation(ctx context.Context, opts PropagationOpts) (*ExperimentResult, error) {
	if !r.env.Running() {
		return nil, fmt.Errorf("environment not running")
	}
	maxWait := opts.MaxWait
	if maxWait <= 0 {
		maxWait = 60 * time.Second
	}
	runID := r.nextRunID()
	ds := transformDataset(opts.Dataset, runID)
	res := &ExperimentResult{
		RunID:      runID,
		System:     r.env.cfg.System,
		Experiment: "propagation",
		Dataset:    ds.Name,
		StartedAt:  time.Now(),
		Stats:      map[string]Stats{},
	}
	r.emit(Event{Type: "run_started", Data: map[string]any{
		"run_id": runID, "experiment": "propagation", "system": r.env.cfg.System,
		"dataset": ds.Name, "policies": len(ds.Policies),
	}})

	gwCount := len(r.env.GatewayIDs)
	tracker := &pendingTracker{gwCount: gwCount, byIP: map[string]*pendingState{}}

	receipts, unsub := r.receipts.Subscribe()
	defer unsub()

	// Collector: match receipts to in-flight policies by IP. It exits via
	// stopCollect (the receipt channel is hub-owned and never closed by
	// consumers).
	stopCollect := make(chan struct{})
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		for {
			select {
			case rc := <-receipts:
				row, done := tracker.record(rc, runID)
				if row.GatewayID == "" {
					continue // unknown IP or duplicate receipt for this gateway
				}
				r.emit(Event{Type: "sample", Data: map[string]any{
					"run_id": runID, "gateway_id": row.GatewayID,
					"ip": row.IP, "latency_ms": row.LatencyMs,
				}})
				if !done {
					continue
				}
				r.emit(Event{Type: "policy_complete", Data: map[string]any{
					"run_id": runID, "ip": row.IP,
				}})
				if tracker.complete() {
					return
				}
			case <-stopCollect:
				return
			}
		}
	}()

	// Submitter: fixed rate, records the submit timestamp per IP.
	var interval time.Duration
	if opts.RatePerSec > 0 {
		interval = time.Second / time.Duration(opts.RatePerSec)
	}
	var submitFailures int
	for i, p := range ds.Policies {
		if ctx.Err() != nil {
			break
		}
		submitAt := time.Now()
		// Track BEFORE submitting: a gateway with a poll already in flight
		// can report the policy within milliseconds of the commit, and a
		// receipt that arrives before the policy is tracked is dropped as
		// unknown and lost for the whole run. Failed submits are removed
		// again — they will never produce a receipt.
		tracker.add(p.IP, submitAt)
		if err := r.submitPolicy(ctx, p); err != nil {
			submitFailures++
			tracker.remove(p.IP)
			r.emit(Event{Type: "submit_failed", Data: map[string]any{
				"run_id": runID, "ip": p.IP, "error": err.Error(),
			}})
		} else {
			r.emit(Event{Type: "policy_submitted", Data: map[string]any{
				"run_id": runID, "index": i + 1, "total": len(ds.Policies), "ip": p.IP,
			}})
		}
		if interval > 0 {
			next := submitAt.Add(interval)
			if d := time.Until(next); d > 0 {
				select {
				case <-ctx.Done():
				case <-time.After(d):
				}
			}
		}
	}
	submitEnd := time.Now()
	tracker.setTotal(tracker.added())

	// Wait for stragglers: all policies on all gateways, bounded.
	if tracker.added() > 0 {
		deadline := submitEnd.Add(maxWait)
		for !tracker.complete() && ctx.Err() == nil && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	unsub()
	close(stopCollect)
	<-collectDone

	res.Rows = tracker.snapshot()
	res.FinishedAt = time.Now()

	// Aggregate per gateway + overall.
	perGW := map[string][]float64{}
	var all []float64
	for _, row := range res.Rows {
		perGW[row.GatewayID] = append(perGW[row.GatewayID], row.LatencyMs)
		all = append(all, row.LatencyMs)
	}
	for _, id := range r.env.GatewayIDs {
		res.Stats[id] = ComputeStats(perGW[id])
	}
	res.Stats["all"] = ComputeStats(all)
	res.Extra = map[string]any{
		"policies_submitted": len(ds.Policies),
		"submit_failures":    submitFailures,
		"policies_complete":  len(tracker.completeIPs()),
		"submit_duration_ms": round2(float64(submitEnd.Sub(res.StartedAt).Microseconds()) / 1000),
		"rate_per_sec":       opts.RatePerSec,
	}

	csvPath, err := r.writePropagationCSV(res)
	if err != nil {
		return res, fmt.Errorf("run completed but CSV write failed: %w", err)
	}
	res.CSVPath = csvPath

	r.emit(Event{Type: "run_finished", Data: map[string]any{
		"run_id": runID, "experiment": "propagation",
		"stats": res.Stats, "extra": res.Extra, "csv_path": res.CSVPath,
	}})
	return res, nil
}

// completeIPs returns the IPs that reached all gateways.
func (t *pendingTracker) completeIPs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for ip, p := range t.byIP {
		if len(p.done) >= t.gwCount {
			out = append(out, ip)
		}
	}
	return out
}

// ---- consistency (Test 2) ----

// ConsistencyOpts configures final-state correctness measurement.
type ConsistencyOpts struct {
	Dataset       policy.Dataset
	RatePerSec    int
	SettleTimeout time.Duration // max wait for all gateways to converge
}

// RunConsistency submits the dataset, waits for convergence, then
// compares every gateway's policy set against the expected set (matched
// by IP, since RaftLite assigns its own policy IDs).
func (r *Runner) RunConsistency(ctx context.Context, opts ConsistencyOpts) (*ExperimentResult, error) {
	if !r.env.Running() {
		return nil, fmt.Errorf("environment not running")
	}
	settle := opts.SettleTimeout
	if settle <= 0 {
		settle = 90 * time.Second
	}
	runID := r.nextRunID()
	ds := transformDataset(opts.Dataset, runID)
	res := &ExperimentResult{
		RunID:      runID,
		System:     r.env.cfg.System,
		Experiment: "consistency",
		Dataset:    ds.Name,
		StartedAt:  time.Now(),
		Stats:      map[string]Stats{},
	}
	r.emit(Event{Type: "run_started", Data: map[string]any{
		"run_id": runID, "experiment": "consistency", "system": r.env.cfg.System,
		"dataset": ds.Name, "policies": len(ds.Policies),
	}})

	var interval time.Duration
	if opts.RatePerSec > 0 {
		interval = time.Second / time.Duration(opts.RatePerSec)
	}
	var submitFailures int
	for i, p := range ds.Policies {
		if ctx.Err() != nil {
			break
		}
		if err := r.submitPolicy(ctx, p); err != nil {
			submitFailures++
			r.emit(Event{Type: "submit_failed", Data: map[string]any{
				"run_id": runID, "ip": p.IP, "error": err.Error(),
			}})
		}
		if i%100 == 0 {
			r.emit(Event{Type: "run_progress", Data: map[string]any{
				"run_id": runID, "submitted": i + 1, "total": len(ds.Policies),
			}})
		}
		if interval > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(interval):
			}
		}
	}
	finalSubmitAt := time.Now()

	expected := map[string]string{}
	for _, p := range ds.Policies {
		expected[p.IP] = policy.NormalizeAction(p.Action)
	}

	// Wait for every gateway to hold the full expected set (or timeout).
	deadline := finalSubmitAt.Add(settle)
	gwStates := map[string]gatewayState{}
	for {
		allFull := true
		for _, id := range r.env.GatewayIDs {
			st, err := r.fetchGatewayState(id)
			if err != nil {
				allFull = false
				continue
			}
			gwStates[id] = st
			if !holdsAll(st, expected) {
				allFull = false
			}
		}
		if allFull || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Compare final states.
	perGWCorrect := map[string]int{}
	perGWMissing := map[string]int{}
	perGWIncorrect := map[string]int{}
	perGWCatchup := map[string]float64{}
	gatewaysCorrect := 0
	for _, id := range r.env.GatewayIDs {
		st, ok := gwStates[id]
		if !ok {
			st, _ = r.fetchGatewayState(id)
		}
		correct, missing, incorrect := compareState(st, expected)
		perGWCorrect[id], perGWMissing[id], perGWIncorrect[id] = correct, missing, incorrect
		if missing == 0 && incorrect == 0 {
			gatewaysCorrect++
		}
		perGWCatchup[id] = round2(float64(time.Since(finalSubmitAt).Microseconds()) / 1000)
	}

	res.FinishedAt = time.Now()
	res.Extra = map[string]any{
		"policies_submitted":     len(ds.Policies),
		"submit_failures":        submitFailures,
		"expected_policies":      len(expected),
		"gateways_correct":       gatewaysCorrect,
		"gateway_count":          len(r.env.GatewayIDs),
		"per_gateway_correct":    perGWCorrect,
		"per_gateway_missing":    perGWMissing,
		"per_gateway_incorrect":  perGWIncorrect,
		"per_gateway_catchup_ms": perGWCatchup,
		"convergence_wait_ms":    round2(float64(time.Since(finalSubmitAt).Microseconds()) / 1000),
	}

	summaryPath, err := r.writeConsistencyCSV(res, perGWCorrect, perGWMissing, perGWIncorrect, perGWCatchup)
	if err == nil {
		res.SummaryPath = summaryPath
	}
	r.emit(Event{Type: "run_finished", Data: map[string]any{
		"run_id": runID, "experiment": "consistency",
		"extra": res.Extra, "summary_path": res.SummaryPath,
	}})
	return res, nil
}

// ---- helpers ----

type gatewayState struct {
	GatewayID string          `json:"gateway_id"`
	Revision  uint64          `json:"revision"`
	Policies  []policy.Policy `json:"policies"`
}

func (r *Runner) fetchGatewayState(id string) (gatewayState, error) {
	var st gatewayState
	resp, err := r.client.Get(r.env.GatewayURLs[id] + "/v1/gateway/state")
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

func holdsAll(st gatewayState, expected map[string]string) bool {
	got := map[string]string{}
	for _, p := range st.Policies {
		got[p.IP] = policy.NormalizeAction(p.Action)
	}
	for ip, action := range expected {
		if got[ip] != action {
			return false
		}
	}
	return true
}

func compareState(st gatewayState, expected map[string]string) (correct, missing, incorrect int) {
	got := map[string]string{}
	for _, p := range st.Policies {
		got[p.IP] = policy.NormalizeAction(p.Action)
	}
	for ip, want := range expected {
		switch got[ip] {
		case "":
			missing++
		case want:
			correct++
		default:
			incorrect++
		}
	}
	return correct, missing, incorrect
}

func (r *Runner) submitPolicy(ctx context.Context, p policy.Policy) error {
	payload, _ := json.Marshal(map[string]string{
		"id": p.ID, "ip": p.IP, "action": policy.NormalizeAction(p.Action),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.env.BackendURL+"/v1/policies", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("backend status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

func (r *Runner) writePropagationCSV(res *ExperimentResult) (string, error) {
	path := filepath.Join(r.resultsDir, "raw",
		fmt.Sprintf("%s-%s-run%d-%s.csv", res.System, res.Experiment, res.RunID, res.StartedAt.Format("20060102-150405")))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fmt.Fprintln(f, "run_id,policy_id,ip,gateway_id,submit_at_ns,applied_at_ns,latency_ms")
	for _, row := range res.Rows {
		fmt.Fprintf(f, "%d,%s,%s,%s,%d,%d,%.3f\n",
			row.RunID, row.PolicyID, row.IP, row.GatewayID,
			row.SubmitAtNs, row.AppliedAtNs, row.LatencyMs)
	}
	return path, nil
}

func (r *Runner) writeConsistencyCSV(res *ExperimentResult, correct, missing, incorrect map[string]int, catchup map[string]float64) (string, error) {
	path := filepath.Join(r.resultsDir, "raw",
		fmt.Sprintf("%s-consistency-run%d-%s.csv", res.System, res.RunID, res.StartedAt.Format("20060102-150405")))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fmt.Fprintln(f, "run_id,gateway_id,correct,missing,incorrect,catchup_ms")
	for _, id := range r.env.GatewayIDs {
		fmt.Fprintf(f, "%d,%s,%d,%d,%d,%.3f\n",
			res.RunID, id, correct[id], missing[id], incorrect[id], catchup[id])
	}
	return path, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// transformDataset rewrites a dataset so that every run uses fresh,
// run-scoped identities: policy IDs get an "-r<runID>" suffix and IPs are
// remapped into 100.<runID%256>.<idx/256>.<idx%256> (exact IPv4 only).
// Without this, re-running a dataset against a live environment would
// re-submit policies the gateways have already applied — their caches
// would not change, no receipts would fire, and the run would measure
// nothing. Both the submitted set and the expected set use the
// transformed values, so correctness comparisons are unaffected.
func transformDataset(ds policy.Dataset, runID int) policy.Dataset {
	out := policy.Dataset{
		Name:     fmt.Sprintf("%s#run%d", ds.Name, runID),
		Policies: make([]policy.Policy, len(ds.Policies)),
	}
	for i, p := range ds.Policies {
		id := p.ID
		if id == "" {
			id = fmt.Sprintf("P%05d", i)
		}
		out.Policies[i] = policy.Policy{
			ID:     fmt.Sprintf("%s-r%d", id, runID),
			IP:     fmt.Sprintf("100.%d.%d.%d", runID%256, i/256, i%256),
			Action: policy.NormalizeAction(p.Action),
		}
	}
	return out
}
