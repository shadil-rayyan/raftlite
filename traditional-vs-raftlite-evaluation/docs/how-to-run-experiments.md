# How to run experiments

## 0. One-time setup

```powershell
cd raftlite\traditional-vs-raftlite-evaluation
.\scripts\build.ps1        # builds gateway, traditional, adapter, measure, gen + raftlite.exe
.\scripts\gen-datasets.ps1 # regenerates datasets/policies-{100,1000,10000}.json (optional)
.\scripts\start.ps1        # starts the measurement server and opens the dashboard
```

Dashboard: **http://127.0.0.1:8095**

## 1. Start an environment

In the **Environment** panel choose a system and press **Start environment**.

- *Traditional* → spawns `traditional` + 5 gateways (a few seconds).
- *RaftLite* → spawns `node1..node3` (real RaftLite processes), the `adapter`, and the same 5
  gateways; the server waits until the cluster has a leader and serves policy reads.

Only one environment runs at a time. The dashboard shows backend URL, components, and gateways.

## 2. Run an experiment

In the **Experiment** panel:

- **Experiment** — one of the seven:
  - *Propagation latency* — submits at the chosen rate and measures per-gateway apply latency.
  - *Consistency (correctness)* — submits the full dataset, waits for convergence, and compares
    every gateway's final state to the expected set.
  - *Server failure* — baseline writes, then kills the backend write path (the traditional
    server / raftlite `node1`), keeps writing during the outage, restarts it, and measures
    failures during the outage plus recovery time. The dataset is split into three phases
    (baseline / during / after).
  - *Network partition* (RaftLite only) — isolates `node3` (single-host approximation of a
    partition), verifies the surviving majority still accepts writes, heals the node, and
    measures its log catch-up. On the traditional system it is rejected with a clear error —
    a single server has no cluster to partition.
  - *Gateway recovery* — kills `G3`, submits policies while it is down (so it misses them),
    restarts it, and measures re-convergence and that enforcement works again.
  - *HTTP request performance* — here **rate = target RPS**: preloads 50 blocked policies,
    then sends a blocked/allowed IP mix through the gateway data path and records the request
    latency distribution and enforcement correctness.
  - *Resource usage* — runs a sustained workload for 20 s (rate = submit rate) while sampling
    CPU, memory, and disk usage of every component once per second.
- **Dataset** — `policies-100.json`, `policies-1000.json`, or `policies-10000.json`.
- **Submit rate** — policies per second (the professor's main config is 10/s).

Press **Run experiment**. You will see:

- live per-gateway latency lines on the chart,
- submitted / fully-propagated counters and a progress bar,
- any submit failures as toasts.

When it finishes, the **Latest result** tab shows n / mean / median / min / max / stddev / P95 /
P99 per gateway and overall, plus experiment-specific extras (consistency: correct/missing/
incorrect per gateway; propagation: complete policies, submit failures).

**Cancel** stops the current run (partial data is still recorded in the CSV).

## 3. Repeat ≥ 10 times

Run the same experiment again and again — each run gets its own `run_id` and its own CSV.
The run history panel lists every run this session; clicking one re-opens its stats.

Two things make repeats safe:

- Every run uses **run-scoped identities** (IDs get an `-r<run>` suffix, IPs are remapped into
  `100.<run>.<hi>.<lo>`), so re-submitting the same dataset never collides with a previous run's
  policies — gateways always emit fresh receipts.
- Results are appended, never overwritten: `results/raw/<system>-<experiment>-run<N>-<ts>.csv`.

## 4. Build the comparison

Run the *same* experiment + dataset on both systems (start Traditional → run → stop → start
RaftLite → run). The **Comparison** tab shows the latest run of each system side by side
(overall stats plus consistency counts).

## CLI alternative (no browser)

Everything the dashboard does is plain HTTP:

```powershell
# start / stop an environment
Invoke-WebRequest -Method Post -ContentType application/json -Body '{"system":"traditional"}' http://127.0.0.1:8095/api/env/start
Invoke-WebRequest -Method Post http://127.0.0.1:8095/api/env/stop

# run an experiment
Invoke-WebRequest -Method Post -ContentType application/json `
  -Body '{"experiment":"propagation","dataset":"policies-100.json","rate":10}' `
  http://127.0.0.1:8095/api/runs

# poll results
Invoke-WebRequest http://127.0.0.1:8095/api/runs

# live event stream (SSE)
curl.exe -N http://127.0.0.1:8095/api/stream
```

Endpoints: `GET /api/status`, `POST /api/env/start|stop`, `GET /api/experiments`,
`GET /api/datasets`, `POST /api/runs`, `POST /api/runs/cancel`, `GET /api/runs[/{id}]`,
`GET /api/stream`, `POST /receipts`, `GET /results/*` (CSV downloads).

## 5. Post-process and graph

```powershell
python scripts\analyze.py
```

Reads every CSV in `results/raw/`, recomputes aggregates per system/experiment, writes
`results/summary.csv`, and (if `matplotlib` is available) renders comparison graphs into
`results/graphs/`.

## Experiment matrix status

| # | experiment | status |
|---|------------|--------|
| 1 | propagation latency | **ready** |
| 2 | consistency / correctness | **ready** |
| 3 | server failure | **ready** |
| 4 | network partition (RaftLite only) | **ready** (single-host approximation: node isolation) |
| 5 | HTTP request performance | **ready** |
| 6 | resource usage | **ready** |
| 7 | gateway recovery | **ready** |

Every experiment writes `results/raw/<system>-<experiment>-run<N>-<timestamp>.csv`. The three
failure experiments additionally write a `...-summary.csv` (one row: outage duration, writes
succeeded/failed, recovery/catch-up times, enforcement checks) — `scripts/analyze.py` skips
those summary files when aggregating latency rows. See `docs/project-overview.md` for how each
experiment is implemented and where its approximations are.
