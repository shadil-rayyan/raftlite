# Project overview

## Why this exists

A security-policy control plane can be built two ways:

1. **Traditional / centralized** — a single server owns the policy store. Gateways poll it,
   cache policies locally, and enforce them. Simple, fast to write, one point of failure.
2. **Consensus-based** — policy changes are committed through a replicated log (Raft), so every
   node agrees on the same policy state even when servers fail. More moving parts, stronger
   failure semantics.

RaftLite is the second kind: a hand-rolled Raft implementation that replicates IP/CIDR block
policies across a cluster. This evaluation answers the practical question:

> For the same gateway workload, how do the two architectures compare on propagation latency,
> correctness, and failure behaviour?

The comparison is only meaningful if everything except the backend is identical. That is the
central design rule of this project.

## Design rules

1. **One gateway, both systems.** The gateway code is a single implementation. It polls a
   backend URL for `{policies[], revision}`, caches the result in memory, and enforces IP
   blocking on its data path. Switching systems changes only `-backend`.
2. **Same datasets, same order.** Both systems receive the identical generated dataset in the
   identical submission order. Datasets are BLOCK-only exact IPs because RaftLite's policy model
   is a block list (ALLOW is not representable there and would break the comparison).
3. **Same measurement code.** One measurement engine runs the experiments, records receipts,
   computes statistics, and writes CSVs. There is no per-system measurement code path.
4. **Real processes.** Gateways, servers, and the RaftLite cluster run as separate OS processes
   (spawned by the measurement server), so the experiment is not an in-process simulation.
5. **Independent module, additive only.** This folder is a nested Go module inside the raftlite
   repository. It never modifies raftlite's own code, and the raftlite build ignores it.

## What is implemented now

| # | experiment | status |
|---|------------|--------|
| 1 | Policy propagation latency | **implemented & verified** |
| 2 | Consistency / correctness (final-state comparison) | **implemented & verified** |
| 3 | Server failure (kill the backend mid-run, restart it) | **implemented & verified** |
| 4 | Network partition (minority isolation, RaftLite only) | **implemented & verified** |
| 5 | HTTP request performance (RPS + latency distribution) | **implemented & verified** |
| 6 | Resource usage (CPU / memory / disk) | **implemented & verified** |
| 7 | Gateway recovery after outage | **implemented & verified** |

All seven are runnable from the dashboard and write CSVs to `results/raw/`. Notes:

- **Failure injection (3, 4, 7)** uses `Env.StopComponent` / `Env.StartComponent`: every
  component's spawn spec is recorded so a killed component restarts with identical
  configuration. These experiments split the dataset into three phases — baseline → while the
  fault is active → after it heals — track only successful submissions (a failed submit never
  produces a receipt), and write both latency rows and a one-row `*-summary.csv` holding the
  key failure facts (outage duration, writes failed, recovery/catch-up times).
- **Partition (4)** is a single-host approximation: isolating one RaftLite node process yields
  the same observable cluster semantics as a network partition of that node (minority gone →
  majority serves; healed node → log catch-up). True TCP-level partitioning would need network
  namespaces or an interposed proxy and is out of scope on a Windows host — this limitation is
  stated wherever the experiment is described.
- The RaftLite **adapter fails over** across all three node URLs on connection errors and on
  transient upstream statuses (503/504 = not ready, 307/308 = not-leader-and-can't-forward
  yet). That is deliberate: the server-failure experiment should measure the architecture
  (single server dies vs. cluster with a failover entry point), not a proxy gap.
- **HTTP performance (5)** preloads blocked policies, then drives the shared gateway data path
  with a blocked/allowed IP mix at a target RPS. **Resource usage (6)** runs a sustained
  submit workload while sampling each process once per second (PowerShell `Get-Process` for
  CPU/memory on Windows; directory size for disk).

## Experiment matrix (target)

- **Datasets:** 100 / 1000 / 10000 policies (all generated, all selectable in the UI).
- **Repeats:** run each experiment ≥ 10 times; the run history accumulates in the dashboard and
  every run writes its own CSV.
- **Main configuration:** 5 gateways, 1000 policies, 10 updates/sec (all configurable per run).
- **Statistics:** mean, median, min, max, stddev, P95, P99 — computed per gateway and overall.
- **Outputs:** raw CSVs (`results/raw/`), processed summaries (`results/`), graphs
  (`results/graphs/`, via `scripts/analyze.py`).

## Non-goals

- This is not a benchmark of RaftLite's own internals; it measures the *system* as a user sees it
  through the gateway.
- No latency or throughput *targets* are claimed anywhere in this project. Reported numbers come
  from runs on one machine and are labelled as such.
