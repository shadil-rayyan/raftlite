package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/policy"
)

// ---- Test 5: HTTP request performance ----

// PerfOpts configures the gateway data-path load test.
type PerfOpts struct {
	Dataset         policy.Dataset // preloaded so some source IPs are blocked
	Requests        int            // total requests to send
	Concurrency     int            // parallel clients
	TargetRPS       int            // pacing; 0 = unpaced
	BlockedFraction float64        // share of requests using a blocked IP
}

// httpRow is one measured request.
type httpRow struct {
	RunID     int     `json:"run_id"`
	GatewayID string  `json:"gateway_id"`
	IP        string  `json:"ip"`
	Status    int     `json:"status"`
	LatencyMs float64 `json:"latency_ms"`
}

// RunHTTPPerformance drives the gateway data path (the identical code in
// both systems) with a mix of blocked and allowed source IPs and records
// the request latency distribution. Because enforcement code is shared,
// differences here reflect cache state, not backend architecture.
func (r *Runner) RunHTTPPerformance(ctx context.Context, opts PerfOpts) (*ExperimentResult, error) {
	if !r.env.Running() {
		return nil, fmt.Errorf("environment not running")
	}
	if opts.Requests <= 0 {
		opts.Requests = 2000
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 20
	}
	if opts.BlockedFraction <= 0 {
		opts.BlockedFraction = 0.3
	}

	runID := r.nextRunID()
	ds := transformDataset(opts.Dataset, runID)
	res := &ExperimentResult{
		RunID: runID, System: r.env.cfg.System, Experiment: "http_performance",
		Dataset: ds.Name, StartedAt: time.Now(), Stats: map[string]Stats{},
	}
	r.emit(Event{Type: "run_started", Data: map[string]any{
		"run_id": runID, "experiment": "http_performance", "system": r.env.cfg.System,
		"dataset": ds.Name, "requests": opts.Requests,
	}})

	// Preload blocked policies and wait until every gateway enforces them.
	preload := ds.Policies
	if len(preload) > 50 {
		preload = preload[:50]
	}
	expected := map[string]string{}
	for _, p := range preload {
		expected[p.IP] = policy.NormalizeAction(p.Action)
	}
	preOK, _, _ := r.submitBatch(ctx, preload, nil, runID, 50, 0)
	r.waitGatewaysConverged(expected, 60*time.Second)
	if len(expected) == 0 {
		return nil, fmt.Errorf("dataset empty: nothing to preload")
	}

	// IP pools: blocked ones come from the preload; allowed ones live in a
	// range no dataset uses (198.18.0.0/15, benchmarking space).
	blockedIPs := make([]string, 0, len(expected))
	for ip := range expected {
		blockedIPs = append(blockedIPs, ip)
	}
	allowedIPs := make([]string, 0, opts.Requests)
	for i := 0; i < opts.Requests; i++ {
		allowedIPs = append(allowedIPs, fmt.Sprintf("198.18.%d.%d", i/256%256, i%256))
	}

	// Paced producer + worker pool.
	type job struct {
		gw  string
		ip  string
		idx int
	}
	jobs := make(chan job, opts.Concurrency*2)
	rows := make([]httpRow, 0, opts.Requests)
	var rowsMu sync.Mutex

	var wg sync.WaitGroup
	for w := 0; w < opts.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				url := r.env.GatewayURLs[j.gw] + "/"
				req, err := http.NewRequest(http.MethodGet, url, nil)
				if err != nil {
					continue
				}
				req.Header.Set("X-Forwarded-For", j.ip)
				t0 := time.Now()
				resp, err := r.client.Do(req)
				lat := float64(time.Since(t0).Microseconds()) / 1000
				status := 0
				if err == nil {
					status = resp.StatusCode
					resp.Body.Close()
				}
				rowsMu.Lock()
				rows = append(rows, httpRow{
					RunID: runID, GatewayID: j.gw, IP: j.ip,
					Status: status, LatencyMs: round2(lat),
				})
				rowsMu.Unlock()
			}
		}()
	}

	gwIDs := r.env.GatewayIDs
	var interval time.Duration
	if opts.TargetRPS > 0 {
		interval = time.Second / time.Duration(opts.TargetRPS)
	}
	tStart := time.Now()
	blockedSent, allowedSent, errorsSent := 0, 0, 0
	for i := 0; i < opts.Requests && ctx.Err() == nil; i++ {
		var ip string
		isBlocked := float64(i%100)/100 < opts.BlockedFraction
		if isBlocked {
			ip = blockedIPs[i%len(blockedIPs)]
			blockedSent++
		} else {
			ip = allowedIPs[i%len(allowedIPs)]
			allowedSent++
		}
		jobs <- job{gw: gwIDs[i%len(gwIDs)], ip: ip, idx: i}
		if interval > 0 {
			if d := time.Until(tStart.Add(time.Duration(i+1) * interval)); d > 0 {
				select {
				case <-ctx.Done():
				case <-time.After(d):
				}
			}
		}
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(tStart)

	// Classify outcomes: 429 on a blocked IP and 200 on an allowed IP are
	// both *correct* enforcement; anything else is an anomaly.
	blockedOK, allowedOK := 0, 0
	perGW := map[string][]float64{}
	var all []float64
	for _, row := range rows {
		perGW[row.GatewayID] = append(perGW[row.GatewayID], row.LatencyMs)
		all = append(all, row.LatencyMs)
		if row.Status == http.StatusTooManyRequests {
			blockedOK++
		} else if row.Status == http.StatusOK {
			allowedOK++
		} else {
			errorsSent++
		}
	}
	for _, id := range r.env.GatewayIDs {
		res.Stats[id] = ComputeStats(perGW[id])
	}
	res.Stats["all"] = ComputeStats(all)
	res.FinishedAt = time.Now()

	res.Extra = map[string]any{
		"requests_sent":       len(rows),
		"target_rps":          opts.TargetRPS,
		"rps_achieved":        round2(float64(len(rows)) / elapsed.Seconds()),
		"elapsed_ms":          msSince(tStart),
		"concurrency":         opts.Concurrency,
		"blocked_requests":    blockedSent,
		"allowed_requests":    allowedSent,
		"blocked_correct":     blockedOK,  // 429 = correctly enforced
		"allowed_correct":     allowedOK,  // 200 = correctly passed
		"unexpected_status":   errorsSent,
		"preload_policies":    preOK,
		"enforcement_correct": errorsSent == 0 && blockedOK == blockedSent && allowedOK == allowedSent,
	}

	// Persist rows.
	path := filepath.Join(r.resultsDir, "raw",
		fmt.Sprintf("%s-http_performance-run%d-%s.csv", res.System, runID, res.StartedAt.Format("20060102-150405")))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		if f, err := os.Create(path); err == nil {
			fmt.Fprintln(f, "run_id,gateway_id,ip,status,latency_ms")
			for _, row := range rows {
				fmt.Fprintf(f, "%d,%s,%s,%d,%.3f\n", row.RunID, row.GatewayID, row.IP, row.Status, row.LatencyMs)
			}
			f.Close()
			res.CSVPath = path
		}
	}

	r.emit(Event{Type: "run_finished", Data: map[string]any{
		"run_id": runID, "experiment": "http_performance",
		"stats": res.Stats, "extra": res.Extra, "csv_path": res.CSVPath,
	}})
	return res, nil
}

// ---- Test 6: resource usage ----

// ResourceOpts configures the resource sampler.
type ResourceOpts struct {
	Dataset    policy.Dataset
	RatePerSec int
	Duration   time.Duration // sampling window (default 20s)
}

type resourceRow struct {
	RunID     int     `json:"run_id"`
	TS        int64   `json:"ts_ns"`
	Component string  `json:"component"`
	PID       int     `json:"pid"`
	CPUDeltaS float64 `json:"cpu_delta_s"`
	MemBytes  int64   `json:"mem_bytes"`
	DiskBytes int64   `json:"disk_bytes"`
}

// RunResourceUsage runs a sustained policy workload while sampling CPU,
// memory and disk usage of every component once per second.
//
// CPU/memory sampling uses PowerShell's Get-Process (Windows). If that is
// unavailable the run still records disk usage and says so in extras.
func (r *Runner) RunResourceUsage(ctx context.Context, opts ResourceOpts) (*ExperimentResult, error) {
	if !r.env.Running() {
		return nil, fmt.Errorf("environment not running")
	}
	runID := r.nextRunID()
	ds := transformDataset(opts.Dataset, runID)
	if len(ds.Policies) == 0 {
		return nil, fmt.Errorf("dataset empty")
	}
	if opts.RatePerSec <= 0 {
		opts.RatePerSec = 20
	}
	if opts.Duration <= 0 {
		opts.Duration = 20 * time.Second
	}

	res := &ExperimentResult{
		RunID: runID, System: r.env.cfg.System, Experiment: "resource_usage",
		Dataset: ds.Name, StartedAt: time.Now(), Stats: map[string]Stats{},
	}
	r.emit(Event{Type: "run_started", Data: map[string]any{
		"run_id": runID, "experiment": "resource_usage", "system": r.env.cfg.System,
		"dataset": ds.Name, "duration_s": opts.Duration.Seconds(),
	}})

	// Sustained workload: keep submitting for the whole window (repeating
	// the dataset once exhausted — upserts still exercise the write path).
	workCtx, stopWork := context.WithTimeout(ctx, opts.Duration)
	workDone := make(chan struct{})
	go func() {
		defer close(workDone)
		i := 0
		for workCtx.Err() == nil {
			p := ds.Policies[i%len(ds.Policies)]
			_ = r.submitPolicy(workCtx, p)
			i++
			select {
			case <-workCtx.Done():
			case <-time.After(time.Second / time.Duration(opts.RatePerSec)):
			}
		}
	}()

	// Sampler.
	var rows []resourceRow
	prevCPU := map[int]float64{}
	sampleInterval := time.Second
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	powerShellOK := runtime.GOOS == "windows"
	samples := 0
	deadline := time.Now().Add(opts.Duration)

collect:
	for {
		select {
		case <-ctx.Done():
			break collect
		case <-ticker.C:
			if time.Now().After(deadline) {
				break collect
			}
			pids := r.env.ComponentPIDs()
			diskBytes := dirSize(r.env.cfg.WorkDir)
			procInfo := sampleProcesses(pids) // empty if PowerShell unavailable
			if len(pids) > 0 && len(procInfo) == 0 {
				powerShellOK = false
			}
			ts := time.Now().UnixNano()
			for name, pid := range pids {
				row := resourceRow{
					RunID: runID, TS: ts, Component: name, PID: pid,
					MemBytes: procInfo[pid].mem, DiskBytes: diskBytes,
				}
				if cpu, ok := procInfo[pid]; ok && cpu.seen {
					if prev, ok := prevCPU[pid]; ok && cpu.cpu >= prev {
						row.CPUDeltaS = round2(cpu.cpu - prev)
					}
					prevCPU[pid] = cpu.cpu
				}
				rows = append(rows, row)
			}
			samples++
		}
	}
	stopWork()
	<-workDone

	if samples == 0 {
		return nil, fmt.Errorf("no samples collected")
	}
	res.FinishedAt = time.Now()

	// Aggregate per component.
	type agg struct {
		samples  int
		peakCPU  float64
		sumCPU   float64
		peakMem  int64
		sumMem   int64
	}
	byComp := map[string]*agg{}
	for _, row := range rows {
		a := byComp[row.Component]
		if a == nil {
			a = &agg{}
			byComp[row.Component] = a
		}
		a.samples++
		a.sumCPU += row.CPUDeltaS
		if row.CPUDeltaS > a.peakCPU {
			a.peakCPU = row.CPUDeltaS
		}
		a.sumMem += row.MemBytes
		if row.MemBytes > a.peakMem {
			a.peakMem = row.MemBytes
		}
	}
	summary := map[string]map[string]float64{}
	for name, a := range byComp {
		summary[name] = map[string]float64{
			"samples":        float64(a.samples),
			"avg_cpu_cores":  round2(a.sumCPU / float64(a.samples)),
			"peak_cpu_cores": round2(a.peakCPU),
			"avg_mem_mb":     round2(float64(a.sumMem) / float64(a.samples) / 1024 / 1024),
			"peak_mem_mb":    round2(float64(a.peakMem) / 1024 / 1024),
		}
	}
	res.Extra = map[string]any{
		"window_seconds":      opts.Duration.Seconds(),
		"samples":             samples,
		"submit_rate":         opts.RatePerSec,
		"cpu_mem_sampled":     powerShellOK,
		"peak_disk_mb":        round2(float64(peakDisk(rows)) / 1024 / 1024),
		"per_component":       summary,
	}

	// CSV rows.
	path := filepath.Join(r.resultsDir, "raw",
		fmt.Sprintf("%s-resource_usage-run%d-%s.csv", res.System, runID, res.StartedAt.Format("20060102-150405")))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		if f, err := os.Create(path); err == nil {
			fmt.Fprintln(f, "run_id,ts_ns,component,pid,cpu_delta_s,mem_bytes,disk_bytes")
			for _, row := range rows {
				fmt.Fprintf(f, "%d,%d,%s,%d,%.3f,%d,%d\n",
					row.RunID, row.TS, row.Component, row.PID,
					row.CPUDeltaS, row.MemBytes, row.DiskBytes)
			}
			f.Close()
			res.CSVPath = path
		}
	}

	if !powerShellOK {
		res.Extra["note"] = "CPU/memory sampling unavailable on this platform; disk usage only"
	}
	r.emit(Event{Type: "run_finished", Data: map[string]any{
		"run_id": runID, "experiment": "resource_usage",
		"extra": res.Extra, "csv_path": res.CSVPath,
	}})
	return res, nil
}

// procSample is one process's instantaneous counters.
type procSample struct {
	seen bool
	cpu  float64 // cumulative CPU seconds
	mem  int64   // working set bytes
}

// sampleProcesses reads CPU/memory for the given PIDs via PowerShell.
// Returns an empty map when PowerShell is unavailable or returns nothing.
func sampleProcesses(pids map[string]int) map[int]procSample {
	out := map[int]procSample{}
	if len(pids) == 0 || runtime.GOOS != "windows" {
		return out
	}
	idList := make([]string, 0, len(pids))
	for _, pid := range pids {
		idList = append(idList, fmt.Sprintf("%d", pid))
	}
	script := fmt.Sprintf(
		"Get-Process -Id %s -ErrorAction SilentlyContinue | Select-Object Id,CPU,WorkingSet64 | ConvertTo-Json -Compress",
		joinComma(idList))
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	outBytes, err := cmd.Output()
	if err != nil {
		return out
	}
	var procs []struct {
		ID          int      `json:"Id"`
		CPU         *float64 `json:"CPU"`
		WorkingSet64 int64   `json:"WorkingSet64"`
	}
	if err := json.Unmarshal(outBytes, &procs); err != nil {
		// Single-object response when only one process matched.
		var one struct {
			ID          int      `json:"Id"`
			CPU         *float64 `json:"CPU"`
			WorkingSet64 int64   `json:"WorkingSet64"`
		}
		if err2 := json.Unmarshal(outBytes, &one); err2 != nil {
			return out
		}
		procs = []struct {
			ID          int      `json:"Id"`
			CPU         *float64 `json:"CPU"`
			WorkingSet64 int64   `json:"WorkingSet64"`
		}{one}
	}
	for _, p := range procs {
		s := procSample{seen: true, mem: p.WorkingSet64}
		if p.CPU != nil {
			s.cpu = *p.CPU
		}
		out[p.ID] = s
	}
	return out
}

// dirSize sums the size of every file under dir (WAL / snapshot / DB
// growth is the disk signal we care about).
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func peakDisk(rows []resourceRow) int64 {
	var peak int64
	for _, row := range rows {
		if row.DiskBytes > peak {
			peak = row.DiskBytes
		}
	}
	return peak
}
