package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/policy"
)

// FailureOpts configures the failure-injection experiments. The dataset is
// split into thirds: baseline (before the fault), during (while the fault
// is active), and after (once it is healed).
type FailureOpts struct {
	Dataset    policy.Dataset
	RatePerSec int
}

// startCollector wires the receipt hub into a tracker: every receipt
// becomes a sample row and a live SSE event. It runs until the returned
// stop func is called — it deliberately does NOT exit when the tracker is
// momentarily complete, because failure experiments run in several phases
// and the tracker keeps growing between them.
func (r *Runner) startCollector(tracker *pendingTracker, runID int) func() {
	receipts, unsub := r.receipts.Subscribe()
	stopCollect := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case rc := <-receipts:
				row, all := tracker.record(rc, runID)
				if row.GatewayID == "" {
					continue // unknown IP or duplicate receipt for this gateway
				}
				r.emit(Event{Type: "sample", Data: map[string]any{
					"run_id": runID, "gateway_id": row.GatewayID,
					"ip": row.IP, "latency_ms": row.LatencyMs,
				}})
				if !all {
					continue
				}
				r.emit(Event{Type: "policy_complete", Data: map[string]any{
					"run_id": runID, "ip": row.IP,
				}})
			case <-stopCollect:
				return
			}
		}
	}()
	return func() {
		unsub()
		close(stopCollect)
		<-done
	}
}

// waitForComplete blocks until every tracked policy reached every gateway,
// the context is cancelled, or the timeout elapses.
func waitForComplete(ctx context.Context, tracker *pendingTracker, timeout time.Duration) {
	if tracker.added() == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for !tracker.complete() && ctx.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
}

// submitBatch submits policies at a fixed rate. Only successful
// submissions are added to the tracker (a failed submit never produces a
// receipt). Returns (succeeded, failed, succeededPolicies).
func (r *Runner) submitBatch(ctx context.Context, policies []policy.Policy, tracker *pendingTracker, runID, rate, startIndex int) (int, int, []policy.Policy) {
	var interval time.Duration
	if rate > 0 {
		interval = time.Second / time.Duration(rate)
	}
	ok, fail := 0, 0
	var succeeded []policy.Policy
	for i, p := range policies {
		if ctx.Err() != nil {
			break
		}
		t0 := time.Now()
		// Track BEFORE submitting: a gateway with a poll already in flight
		// can report the policy within milliseconds of the commit, and a
		// receipt that arrives before the policy is tracked would be
		// dropped as unknown and lost for the whole run.
		if tracker != nil {
			tracker.add(p.IP, t0)
		}
		if err := r.submitPolicy(ctx, p); err != nil {
			fail++
			if tracker != nil {
				tracker.remove(p.IP)
			}
			r.emit(Event{Type: "submit_failed", Data: map[string]any{
				"run_id": runID, "ip": p.IP, "error": err.Error(),
			}})
		} else {
			ok++
			succeeded = append(succeeded, p)
			r.emit(Event{Type: "policy_submitted", Data: map[string]any{
				"run_id": runID, "index": startIndex + i + 1, "ip": p.IP,
			}})
		}
		if interval > 0 {
			if d := time.Until(t0.Add(interval)); d > 0 {
				select {
				case <-ctx.Done():
				case <-time.After(d):
				}
			}
		}
	}
	return ok, fail, succeeded
}

func msSince(t time.Time) float64 {
	return round2(float64(time.Since(t).Microseconds()) / 1000)
}

// probeGateway hits a gateway's data path with a synthetic source IP and
// returns the HTTP status (429 = blocked, 200 = allowed).
func (r *Runner) probeGateway(gatewayID, ip string) (int, error) {
	url := r.env.GatewayURLs[gatewayID] + "/"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Forwarded-For", ip)
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, nil
}

// waitGatewaysConverged polls gateway states (excluding any named
// components) until each holds the full expected set, or timeout.
func (r *Runner) waitGatewaysConverged(expected map[string]string, timeout time.Duration, exclude ...string) {
	excluded := map[string]bool{}
	for _, e := range exclude {
		excluded[e] = true
	}
	deadline := time.Now().Add(timeout)
	for {
		all := true
		for _, id := range r.env.GatewayIDs {
			if excluded[id] {
				continue
			}
			st, err := r.fetchGatewayState(id)
			if err != nil || !holdsAll(st, expected) {
				all = false
				break
			}
		}
		if all || time.Now().After(deadline) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// splitDataset divides a run-scoped dataset into baseline / during / after
// thirds for failure experiments.
func splitDataset(ds policy.Dataset) (baseline, during, after []policy.Policy, err error) {
	n := len(ds.Policies)
	if n < 3 {
		return nil, nil, nil, fmt.Errorf("dataset too small (%d policies): failure experiments need at least 3", n)
	}
	third := n / 3
	return ds.Policies[:third], ds.Policies[third : 2*third], ds.Policies[2*third:], nil
}

// ---- Test 3: server failure ----

// RunServerFailure kills the backend write path mid-run, measures what
// happens to writes during the outage, then restarts it and measures
// recovery.
//
//   - Traditional: the single central server dies → writes fail outright
//     while it is down; gateways keep enforcing from cache.
//   - RaftLite: node1 dies → the adapter fails over to the surviving
//     majority, so writes keep succeeding (with an election blip if node1
//     was leader).
func (r *Runner) RunServerFailure(ctx context.Context, opts FailureOpts) (*ExperimentResult, error) {
	if !r.env.Running() {
		return nil, fmt.Errorf("environment not running")
	}
	runID := r.nextRunID()
	ds := transformDataset(opts.Dataset, runID)
	baseline, during, after, err := splitDataset(ds)
	if err != nil {
		return nil, err
	}
	rate := opts.RatePerSec
	if rate <= 0 {
		rate = 20
	}

	res := &ExperimentResult{
		RunID: runID, System: r.env.cfg.System, Experiment: "server_failure",
		Dataset: ds.Name, StartedAt: time.Now(), Stats: map[string]Stats{},
	}
	r.emit(Event{Type: "run_started", Data: map[string]any{
		"run_id": runID, "experiment": "server_failure", "system": r.env.cfg.System,
		"dataset": ds.Name, "policies": len(ds.Policies),
	}})

	tracker := &pendingTracker{gwCount: len(r.env.GatewayIDs), byIP: map[string]*pendingState{}}
	stopCollect := r.startCollector(tracker, runID)
	defer stopCollect()

	// Phase 1 — healthy baseline.
	baseOK, baseFail, _ := r.submitBatch(ctx, baseline, tracker, runID, rate, 0)
	tracker.setTotal(tracker.added())
	waitForComplete(ctx, tracker, 60*time.Second)

	// Phase 2 — kill the backend write path.
	comp := r.env.BackendComponentName()
	tKill := time.Now()
	if err := r.env.StopComponent(comp); err != nil {
		return res, fmt.Errorf("could not stop %s: %w", comp, err)
	}
	r.emit(Event{Type: "component_stopped", Data: map[string]any{
		"run_id": runID, "component": comp,
	}})

	// Gateways must keep enforcing from cache while the backend is gone.
	blockedStatus, blockedErr := r.probeGateway("G1", baseline[0].IP)
	allowedStatus, allowedErr := r.probeGateway("G1", "203.0.113.250")
	dataPathOK := blockedErr == nil && allowedErr == nil &&
		blockedStatus == http.StatusTooManyRequests && allowedStatus == http.StatusOK

	// Phase 3 — writes during the outage.
	duringOK, duringFail, _ := r.submitBatch(ctx, during, tracker, runID, rate, len(baseline))

	// Phase 4 — restart the backend and time the recovery. The outage
	// window is exactly kill → restart.
	tRestart := time.Now()
	outageMs := round2(float64(tRestart.Sub(tKill).Microseconds()) / 1000)
	restartErr := r.env.StartComponent(ctx, comp)
	recoveryMs := msSince(tRestart)
	if restartErr != nil {
		r.emit(Event{Type: "run_error", Data: map[string]any{
			"run_id": runID, "error": "restart failed: " + restartErr.Error(),
		}})
		return res, fmt.Errorf("restart of %s failed: %w", comp, restartErr)
	}
	r.emit(Event{Type: "component_started", Data: map[string]any{
		"run_id": runID, "component": comp, "recovery_ms": recoveryMs,
	}})

	// Phase 5 — writes after recovery, with propagation measured.
	afterOK, afterFail, _ := r.submitBatch(ctx, after, tracker, runID, rate, len(baseline)+len(during))
	tracker.setTotal(tracker.added())
	waitForComplete(ctx, tracker, 90*time.Second)

	res.Rows = tracker.snapshot()
	res.FinishedAt = time.Now()

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
		"component":                  comp,
		"baseline_succeeded":         baseOK,
		"baseline_failed":            baseFail,
		"during_outage_attempted":    len(during),
		"during_outage_succeeded":    duringOK,
		"during_outage_failed":       duringFail,
		"after_recovery_succeeded":   afterOK,
		"after_recovery_failed":      afterFail,
		"outage_ms":                  outageMs,
		"recovery_ms":                recoveryMs,
		"data_path_ok_during_outage": dataPathOK,
		"blocked_status_during_outage": blockedStatus,
		"policies_complete":          len(tracker.completeIPs()),
	}

	csvPath, err := r.writePropagationCSV(res)
	if err == nil {
		res.CSVPath = csvPath
	}
	if summaryPath, err := r.writeSummaryCSV(res, res.Extra); err == nil {
		res.SummaryPath = summaryPath
	}
	r.emit(Event{Type: "run_finished", Data: map[string]any{
		"run_id": runID, "experiment": "server_failure",
		"stats": res.Stats, "extra": res.Extra,
		"csv_path": res.CSVPath, "summary_path": res.SummaryPath,
	}})
	return res, nil
}

// ---- Test 4: network partition (minority isolation) ----

// RunPartition isolates one RaftLite node from the cluster — the
// single-host approximation of a network partition — and verifies that the
// surviving majority still accepts writes, then heals the node and
// measures how long it takes to catch up.
//
// Note: a true TCP-level partition needs network namespaces or an
// interposed proxy; on a single Windows host we isolate by removing the
// node process, which produces the same observable cluster semantics
// (minority gone → majority serves; healed node → log catch-up).
func (r *Runner) RunPartition(ctx context.Context, opts FailureOpts) (*ExperimentResult, error) {
	if r.env.cfg.System != "raftlite" {
		return nil, fmt.Errorf("the partition experiment applies to the raftlite system only (a single traditional server has no cluster to partition)")
	}
	if !r.env.Running() {
		return nil, fmt.Errorf("environment not running")
	}
	runID := r.nextRunID()
	ds := transformDataset(opts.Dataset, runID)
	baseline, during, after, err := splitDataset(ds)
	if err != nil {
		return nil, err
	}
	rate := opts.RatePerSec
	if rate <= 0 {
		rate = 20
	}

	res := &ExperimentResult{
		RunID: runID, System: r.env.cfg.System, Experiment: "partition",
		Dataset: ds.Name, StartedAt: time.Now(), Stats: map[string]Stats{},
	}
	r.emit(Event{Type: "run_started", Data: map[string]any{
		"run_id": runID, "experiment": "partition", "system": r.env.cfg.System,
		"dataset": ds.Name, "policies": len(ds.Policies),
	}})

	tracker := &pendingTracker{gwCount: len(r.env.GatewayIDs), byIP: map[string]*pendingState{}}
	stopCollect := r.startCollector(tracker, runID)
	defer stopCollect()

	// Phase 1 — healthy baseline on all three nodes.
	baseOK, baseFail, baseSucceeded := r.submitBatch(ctx, baseline, tracker, runID, rate, 0)
	tracker.setTotal(tracker.added())
	waitForComplete(ctx, tracker, 60*time.Second)

	// Phase 2 — isolate node3 (minority of a 3-node cluster).
	const isolated = "node3"
	if len(r.env.NodeURLs) < 3 {
		return res, fmt.Errorf("raftlite environment has no node URLs recorded")
	}
	tIsolate := time.Now()
	if err := r.env.StopComponent(isolated); err != nil {
		return res, fmt.Errorf("could not stop %s: %w", isolated, err)
	}
	r.emit(Event{Type: "component_stopped", Data: map[string]any{
		"run_id": runID, "component": isolated,
	}})

	// Enforcement must continue from cache during the partition.
	blockedStatus, blockedErr := r.probeGateway("G1", baseline[0].IP)
	dataPathOK := blockedErr == nil && blockedStatus == http.StatusTooManyRequests

	// Phase 3 — writes while node3 is isolated: the majority must serve.
	duringOK, duringFail, duringSucceeded := r.submitBatch(ctx, during, tracker, runID, rate, len(baseline))

	// Phase 4 — heal: bring node3 back and watch it catch up. The isolation
	// window is exactly isolate → heal.
	tHeal := time.Now()
	isolationMs := round2(float64(tHeal.Sub(tIsolate).Microseconds()) / 1000)
	if err := r.env.StartComponent(ctx, isolated); err != nil {
		return res, fmt.Errorf("could not restart %s: %w", isolated, err)
	}
	// Expected state on the healed node: every write that actually
	// succeeded while it was away (failed writes never reached the log).
	expected := map[string]string{}
	for _, p := range append(append([]policy.Policy{}, baseSucceeded...), duringSucceeded...) {
		expected[p.IP] = policy.NormalizeAction(p.Action)
	}
	catchupMs := waitForNodeCatchup(r.env.NodeURLs[2], expected, 60*time.Second)

	r.emit(Event{Type: "component_started", Data: map[string]any{
		"run_id": runID, "component": isolated, "catchup_ms": catchupMs,
	}})

	// Phase 5 — writes after healing, with propagation measured.
	afterOK, afterFail, _ := r.submitBatch(ctx, after, tracker, runID, rate, len(baseline)+len(during))
	tracker.setTotal(tracker.added())
	waitForComplete(ctx, tracker, 90*time.Second)

	res.Rows = tracker.snapshot()
	res.FinishedAt = time.Now()

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
		"isolated_component":        isolated,
		"baseline_succeeded":        baseOK,
		"baseline_failed":           baseFail,
		"while_isolated_attempted":  len(during),
		"while_isolated_succeeded":  duringOK,
		"while_isolated_failed":     duringFail,
		"after_heal_succeeded":      afterOK,
		"after_heal_failed":         afterFail,
		"isolation_ms":              isolationMs,
		"healed_node_catchup_ms":    catchupMs,
		"data_path_ok_during_cut":   dataPathOK,
		// The majority keeps serving during the cut. Killing the node that
		// happened to be leader still causes a brief election in which
		// writes fail cluster-wide — that window shows up in
		// while_isolated_failed above.
		"majority_available":        duringOK > 0,
		"isolated_write_success_pct": round2(100 * float64(duringOK) / float64(len(during))),
		"policies_complete":         len(tracker.completeIPs()),
	}

	csvPath, err := r.writePropagationCSV(res)
	if err == nil {
		res.CSVPath = csvPath
	}
	if summaryPath, err := r.writeSummaryCSV(res, res.Extra); err == nil {
		res.SummaryPath = summaryPath
	}
	r.emit(Event{Type: "run_finished", Data: map[string]any{
		"run_id": runID, "experiment": "partition",
		"stats": res.Stats, "extra": res.Extra,
		"csv_path": res.CSVPath, "summary_path": res.SummaryPath,
	}})
	return res, nil
}

// raftliteNativePolicy is RaftLite's own response shape (target/action/
// status), NOT the uniform gateway contract — the adapter is what
// translates between them, so any code talking to a node directly must
// decode this shape.
type raftliteNativePolicy struct {
	Target string `json:"target"`
	Action string `json:"action"`
	Status string `json:"status"`
}

// waitForNodeCatchup polls a RaftLite node directly (stale reads, so the
// check does not depend on leader forwarding) until it holds every
// expected IP. Returns the catch-up time in ms (0 if already caught up).
func waitForNodeCatchup(nodeURL string, expected map[string]string, timeout time.Duration) float64 {
	start := time.Now()
	deadline := start.Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, nodeURL+"/v1/policies?consistency=stale", nil)
		resp, err := client.Do(req)
		if err == nil {
			var lr struct {
				Policies []raftliteNativePolicy `json:"policies"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&lr)
			resp.Body.Close()
			got := map[string]bool{}
			for _, p := range lr.Policies {
				if p.Target != "" && p.Status != "deleted" {
					got[p.Target] = true
				}
			}
			complete := true
			for ip := range expected {
				if !got[ip] {
					complete = false
					break
				}
			}
			if complete {
				return msSince(start)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return msSince(start) // timed out — reported as the elapsed wait
}

// ---- Test 7: gateway recovery ----

// RunGatewayRecovery kills a gateway, submits policies while it is down
// (so it misses them), restarts it, and measures how long the restarted
// gateway takes to re-converge and start enforcing again.
func (r *Runner) RunGatewayRecovery(ctx context.Context, opts FailureOpts) (*ExperimentResult, error) {
	if !r.env.Running() {
		return nil, fmt.Errorf("environment not running")
	}
	runID := r.nextRunID()
	ds := transformDataset(opts.Dataset, runID)
	baseline, during, after, err := splitDataset(ds)
	if err != nil {
		return nil, err
	}
	rate := opts.RatePerSec
	if rate <= 0 {
		rate = 20
	}

	res := &ExperimentResult{
		RunID: runID, System: r.env.cfg.System, Experiment: "gateway_recovery",
		Dataset: ds.Name, StartedAt: time.Now(), Stats: map[string]Stats{},
	}
	r.emit(Event{Type: "run_started", Data: map[string]any{
		"run_id": runID, "experiment": "gateway_recovery", "system": r.env.cfg.System,
		"dataset": ds.Name, "policies": len(ds.Policies),
	}})

	// Phase 1 — healthy baseline on all five gateways.
	baseOK, baseFail, baseSucceeded := r.submitBatch(ctx, baseline, nil, runID, rate, 0)
	expected := map[string]string{}
	for _, p := range baseSucceeded {
		expected[p.IP] = policy.NormalizeAction(p.Action)
	}
	r.waitGatewaysConverged(expected, 60*time.Second)

	// Phase 2 — kill one gateway.
	const victim = "G3"
	tKill := time.Now()
	if err := r.env.StopComponent(victim); err != nil {
		return res, fmt.Errorf("could not stop %s: %w", victim, err)
	}
	r.emit(Event{Type: "component_stopped", Data: map[string]any{
		"run_id": runID, "component": victim,
	}})

	// Confirm it is really down.
	downConfirmed := true
	if req, err := http.NewRequest(http.MethodGet, r.env.GatewayURLs[victim]+"/healthz", nil); err == nil {
		if resp, err := r.client.Do(req); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				downConfirmed = false
			}
		}
	}

	// Phase 3 — policies submitted while the gateway is down: it will miss
	// every one of them. The other four must still converge.
	duringOK, duringFail, duringSucceeded := r.submitBatch(ctx, during, nil, runID, rate, len(baseline))
	for _, p := range duringSucceeded {
		expected[p.IP] = policy.NormalizeAction(p.Action)
	}
	r.waitGatewaysConverged(expected, 60*time.Second, victim)
	missed := len(duringSucceeded)

	// Phase 4 — restart the gateway and time its catch-up. The down window
	// is exactly kill → restart.
	tRestart := time.Now()
	downMs := round2(float64(tRestart.Sub(tKill).Microseconds()) / 1000)
	if err := r.env.StartComponent(ctx, victim); err != nil {
		return res, fmt.Errorf("could not restart %s: %w", victim, err)
	}
	// StartComponent waited for /healthz; now wait for full state.
	catchupStart := time.Now()
	deadline := catchupStart.Add(60 * time.Second)
	catchupOK := false
	for time.Now().Before(deadline) {
		st, err := r.fetchGatewayState(victim)
		if err == nil && holdsAll(st, expected) {
			catchupOK = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	catchupMs := msSince(catchupStart)

	// Phase 5 — verify it enforces again (blocked → 429, allowed → 200).
	blockedStatus, bErr := r.probeGateway(victim, baseline[0].IP)
	allowedStatus, aErr := r.probeGateway(victim, "203.0.113.250")
	enforcementOK := bErr == nil && aErr == nil &&
		blockedStatus == http.StatusTooManyRequests && allowedStatus == http.StatusOK

	// Phase 6 — writes after recovery, with propagation measured.
	tracker := &pendingTracker{gwCount: len(r.env.GatewayIDs), byIP: map[string]*pendingState{}}
	stopCollect := r.startCollector(tracker, runID)
	defer stopCollect()
	afterOK, afterFail, _ := r.submitBatch(ctx, after, tracker, runID, rate, len(baseline)+len(during))
	tracker.setTotal(tracker.added())
	waitForComplete(ctx, tracker, 90*time.Second)

	res.Rows = tracker.snapshot()
	res.FinishedAt = time.Now()

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
		"gateway":                  victim,
		"down_confirmed":           downConfirmed,
		"down_ms":                  downMs,
		"baseline_succeeded":       baseOK,
		"baseline_failed":          baseFail,
		"missed_while_down":        missed,
		"missed_submits_ok":        duringOK,
		"missed_submits_failed":    duringFail,
		"catchup_ms":               catchupMs,
		"caught_up":                catchupOK,
		"enforcement_ok_after":     enforcementOK,
		"blocked_status_after":     blockedStatus,
		"allowed_status_after":     allowedStatus,
		"after_recovery_succeeded": afterOK,
		"after_recovery_failed":    afterFail,
	}

	// Persist both the latency rows (phase-6 propagation) and the key
	// failure facts as a one-row summary.
	if csvPath, err := r.writePropagationCSV(res); err == nil {
		res.CSVPath = csvPath
	}
	if summaryPath, err := r.writeSummaryCSV(res, res.Extra); err == nil {
		res.SummaryPath = summaryPath
	}
	r.emit(Event{Type: "run_finished", Data: map[string]any{
		"run_id": runID, "experiment": "gateway_recovery",
		"stats": res.Stats, "extra": res.Extra,
		"csv_path": res.CSVPath, "summary_path": res.SummaryPath,
	}})
	return res, nil
}

// writeSummaryCSV writes a one-row-per-run summary CSV holding the key
// failure facts (outage durations, write failures, recovery/catch-up
// times). Columns are the sorted union of the fields given, so every
// failure experiment persists its extras without a fixed schema. The
// filename ends in -summary.csv so row-oriented analysis can skip it.
func (r *Runner) writeSummaryCSV(res *ExperimentResult, fields map[string]any) (string, error) {
	path := filepath.Join(r.resultsDir, "raw",
		fmt.Sprintf("%s-%s-run%d-%s-summary.csv", res.System, res.Experiment, res.RunID, res.StartedAt.Format("20060102-150405")))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(f, "run_id,"+joinComma(keys))
	vals := make([]string, len(keys))
	for i, k := range keys {
		vals[i] = fmt.Sprintf("%v", fields[k])
	}
	fmt.Fprintf(f, "%d,%s\n", res.RunID, joinComma(vals))
	return path, nil
}
