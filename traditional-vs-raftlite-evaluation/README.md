# Traditional vs RaftLite — Evaluation

An independent, self-contained experiment suite that compares two policy-management
architectures for API-gateway IP/CIDR block policies:

- **Traditional** — one centralized policy server backed by a local SQLite file.
- **RaftLite** — the real RaftLite consensus cluster (3 nodes) behind a small REST adapter.

Both use **exactly the same gateway implementation**, **the same datasets**, and **the same
measurement code**. The only difference is the backend each gateway polls. That is deliberate:
any measured difference comes from the policy-management architecture, not from gateway logic.

This folder lives **inside** the raftlite repository but is a **separate Go module** (`go.mod`
in this directory), so it never affects the RaftLite product build: `go build ./...` at the
repository root ignores this nested module entirely.

## Layout

```
traditional-vs-raftlite-evaluation/
├── cmd/
│   ├── gateway/      # the shared gateway (poll + cache + IP blocking + receipts)
│   ├── traditional/  # centralized policy server (Go + SQLite)
│   ├── adapter/      # RaftLite → uniform REST contract (fetch-through proxy)
│   ├── measure/      # measurement server: orchestrator + collector + SSE + dashboard
│   └── gen/          # deterministic dataset generator (100 / 1000 / 10000)
├── internal/
│   ├── policy/       # shared policy model + backend contract
│   ├── gateway/      # gateway implementation
│   ├── traditional/  # central server implementation
│   ├── adapter/      # RaftLite adapter implementation
│   ├── measure/      # experiment runners, stats, env orchestrator, HTTP/SSE server
│   └── ui/           # embedded single-page dashboard (vanilla JS + canvas)
├── datasets/         # generated policy datasets (BLOCK-only, unique exact IPs)
├── docs/             # this documentation set
├── scripts/          # build / start / dataset / analysis helpers
├── bin/              # local binaries (gitignored)
├── work/             # per-start scratch (process logs, WALs; gitignored)
└── results/          # raw CSVs (results/raw/) + processed outputs
```

## Quickstart (Windows PowerShell)

```powershell
cd raftlite\traditional-vs-raftlite-evaluation

# 1. build every component (including the raftlite binary from the parent repo)
.\scripts\build.ps1

# 2. generate the shared datasets (already committed, but re-runnable)
.\scripts\gen-datasets.ps1

# 3. start the measurement server and open the dashboard
.\scripts\start.ps1
# → http://127.0.0.1:8095
```

In the dashboard:

1. Pick a system (Traditional or RaftLite) and **Start environment**.
2. Pick an experiment and a dataset (100 / 1000 / 10000 policies) and a submit rate.
3. **Run experiment** — watch live per-gateway latencies and the progress bar.
4. Read the result table (mean / median / min / max / stddev / P95 / P99) and the raw CSV path.
5. Switch systems and repeat to build a comparison (the **Comparison** tab shows the latest run
   of each system side by side).

To stop everything: **Stop** in the dashboard, then close the measurement server.

## What is measured

The gateway reports a **receipt** — its own wall-clock timestamp the moment it applies a policy
it has not seen before. The measurement server timestamps the submission just before it POSTs
the policy to the backend. Propagation latency for one policy on one gateway is:

```
latency = gateway_applied_at − submit_at
```

Same host, same clock, so this is an end-to-end propagation number that includes the backend
write (SQLite commit or RaftLite consensus) **and** the gateway's poll interval (500 ms by
default — this dominates the observed distribution and is why medians sit near half the poll
interval). Full details: `docs/measurement-explanation.md`.

## Observed results (runs of 2026-11-03, single machine)

Configuration: 5 gateways, 100-policy dataset, 20 submissions/sec, 500 ms gateway poll.

**Propagation latency** (all runs pooled; `results/summary.csv` has per-run and per-gateway rows):

| system     | samples | mean (ms) | median (ms) | P95 (ms) | P99 (ms) |
|------------|---------|-----------|-------------|----------|----------|
| Traditional| 1000    | 251.0     | 251.8       | 476.6    | 495.5    |
| RaftLite   | 1000    | 269.6     | 268.3       | 493.3    | 512.9    |

**Consistency:** both systems **1000/1000 correct, 0 missing, 0 incorrect** across all runs.

**Failure behaviour** (final verified runs):

| scenario | Traditional | RaftLite |
|----------|-------------|----------|
| writes during a 1.7 s backend outage | 0/33 succeeded — one server is a hard outage | 33/33 succeeded — adapter failed over to the surviving majority |
| data path during the outage | still enforcing from cache (blocked IP → 429) | same |
| restart → backend answering again | 257 ms | 157 ms (restarted node's own `/healthz`) |
| writes immediately after recovery | 34/34 | 34/34 |
| writes while one node is isolated (partition) | n/a — no cluster | 33/33; majority available |
| healed node's log catch-up | n/a | 411 ms |
| gateway killed ≈2 s, missed 33 policies | re-converged in 8 ms, enforcement restored | re-converged in 1 ms, enforcement restored |
| HTTP data-path load, 2000 req @ 500 RPS | mean 0.01 ms, 0 unexpected statuses | mean 0.02 ms, 0 unexpected statuses |
| resource usage, 20 s window | server peak 0.13 cores / 19 MB; gateways ≈15 MB | node1 peak 0.45 cores / 24 MB; other nodes ≈20 MB; adapter 17 MB; disk 0.48 MB |

These are single-machine runs, not benchmark claims. Both architectures propagate correctly;
the latency difference sits on top of the shared 500 ms poll interval, and the meaningful
signal in the failure rows is behavioural (outage vs. failover), not the timing. Treat the
numbers as indicative only.

## Documentation

| file | contents |
|------|----------|
| `docs/project-overview.md` | why this exists, scope, experiment matrix |
| `docs/how-the-systems-work.md` | the two architectures, the adapter, the gateway, ports |
| `docs/how-to-run-experiments.md` | step-by-step runs (UI + CLI), repeats, datasets |
| `docs/measurement-explanation.md` | receipt model, timestamps, stats, CSV formats |
