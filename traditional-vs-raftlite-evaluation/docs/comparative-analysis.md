# Chapter 5 — Experimental Evaluation: Traditional (Centralized) vs RaftLite (Consensus)

## 5.1 Experimental Setup and Evaluation Metrics

### 5.1.1 Environment

Single host, all components as separate OS processes spawned by the measurement server on `127.0.0.1`. Both systems share the identical gateway implementation, datasets, and measurement code; only the backend differs.

| Component | Port |
|---|---|
| Measurement server (dashboard, API, SSE, receipts) | 8095 |
| Backend (traditional server / RaftLite adapter) | 8100 |
| Gateways G1..G5 | 8110–8114 |
| RaftLite nodes 1..3 HTTP admin | 8081–8083 |
| RaftLite nodes 1..3 gRPC Raft | 9091–9093 |

- Gateways: 5, poll interval 500 ms (default).
- RaftLite cluster: 3 nodes, quorum = 2.
- Datasets: `policies-{100,1000,10000}.json`, BLOCK-only exact IPs.
- Run-scoped identities: policy IDs suffixed `-r<run>`, IPs remapped per run, so repeats never collide.

### 5.1.2 Backend configurations

- **Traditional** — one Go process, policies in a SQLite file (pure-Go driver, no CGO). Every write bumps a monotonic revision counter in the same transaction, so readers see a consistent list+revision.
- **RaftLite** — real 3-node Raft cluster; writes committed through the replicated log with quorum ack; reads via linearizable read (ReadIndex); followers forward/redirect to leader. WAL, snapshots, hard-state persistence are RaftLite's own. A small adapter presents the uniform REST contract (`{policies[], revision}`) and fetches through with no extra caching.

### 5.1.3 Measurement model

For one policy observed by one gateway:

```
latency = gateway_applied_at − submit_at   (same host clock, no sync correction)
```

Covers backend write (SQLite commit **or** Raft consensus commit) + gateway's next poll + cache rebuild. Receipts are emitted once per policy per gateway on first apply and matched by IP (RaftLite assigns its own IDs).

### 5.1.4 Experiments and metrics

| # | Experiment | Primary metric |
|---|---|---|
| 1 | Policy propagation latency | latency distribution (mean/median/min/max/stddev/P95/P99) |
| 2 | Consistency/correctness | correct / missing / incorrect counts, catch-up ms |
| 3 | Server failure | writes succeeded/failed during outage, recovery ms |
| 4 | Network partition (RaftLite only) | writes during isolation, healed-node catch-up ms |
| 5 | HTTP request performance | data-path latency, unexpected statuses |
| 6 | Resource usage | CPU cores, memory, disk per component |
| 7 | Gateway recovery | re-convergence ms, enforcement restored |

Statistics: nearest-rank percentiles, sample stddev (n−1), no sample discarded.

## 5.2 Results (measured, single machine, 2026-11-03)

### 5.2.1 Propagation latency (100-policy dataset, 5 gateways, all runs pooled)

| System | n | mean ms | median ms | min ms | max ms | stddev ms | P95 ms | P99 ms |
|---|---|---|---|---|---|---|---|---|
| Traditional | 1000 | 251.0 | 251.8 | 4.2 | 501.2 | 144.7 | 476.6 | 495.5 |
| RaftLite | 1000 | 269.6 | 268.3 | 11.4 | 533.5 | 142.9 | 493.3 | 512.9 |

### 5.2.2 Consistency (2 runs × 5 gateways × 100 policies)

| System | correct | missing | incorrect |
|---|---|---|---|
| Traditional | 1000 | 0 | 0 |
| RaftLite | 1000 | 0 | 0 |

### 5.2.3 Server failure (backend killed mid-run, ~1.7 s outage)

| Metric | Traditional | RaftLite |
|---|---|---|
| Writes during outage | 0 / 33 | 33 / 33 |
| Data path during outage | enforced from cache (429) | enforced from cache (429) |
| Recovery (backend answering) | 257 ms | 157 ms (restarted node `/healthz`) |
| Writes after recovery | 34 / 34 | 34 / 34 (run 15) |
| Policies complete | 67 / 100 | 100 / 100 |

### 5.2.4 Network partition (RaftLite only, node3 isolated ~1.7 s)

| Metric | Value |
|---|---|
| Writes while isolated | 33 / 33 (100%) |
| Majority available | yes |
| Data path during cut | ok (429) |
| Healed node catch-up | 411 ms |

### 5.2.5 HTTP data-path performance (2000 req @ 500 RPS, 5 gateways)

| System | n | mean ms | P95 ms | P99 ms | unexpected statuses |
|---|---|---|---|---|---|
| Traditional | 4000 | 0.01 | 0.00 | 0.51 | 0 |
| RaftLite | 4000 | 0.02 | 0.00 | 0.54 | 0 |

### 5.2.6 Resource usage (20 s sustained workload)

| Component | Peak CPU (cores) | Peak memory (MB) | Peak disk (MB) |
|---|---|---|---|
| Traditional server | 0.13 | 19.06 | 0.14 |
| Traditional gateways (each) | ≤0.06 | ~15.8 | 0.14 |
| RaftLite node1 | 0.45 | 24.13 | 0.48 |
| RaftLite node2 / node3 | 0.16 | 20.95 / 19.77 | 0.48 |
| RaftLite adapter | 0.08 | 23.03 | 0.48 |
| RaftLite gateways (each) | ≤0.05 | ~15.5 | 0.48 |

### 5.2.7 Gateway recovery (G3 killed ~2 s, missed 33 policies)

| System | Re-convergence | Enforcement restored |
|---|---|---|
| Traditional | 8 ms | yes |
| RaftLite | 1 ms | yes |

## 5.3 Comparative Analysis and Discussion

**Findings.** On the shared propagation workload both architectures are correct (1000/1000, 0 missing, 0 incorrect) and their latency distributions are nearly identical (medians ~252 ms vs ~268 ms, on top of the shared 500 ms poll interval). The ~18 ms mean gap is the cost of Raft consensus + leader read; it is small relative to the poll term, and medians sit near half the poll interval as expected. HTTP data-path latency is effectively zero on both (0.01–0.02 ms mean), so enforcement performance is not a differentiator.

**Benefits of RaftLite.** The decisive difference is failure behaviour: a single backend is a hard outage (0/33 writes succeed), while the cluster keeps serving writes through a majority with adapter failover (33/33), and heals faster (157 ms vs 257 ms). Partitions are handled by majority availability with log catch-up (411 ms). RaftLite also provides durable, replicated policy state (WAL + snapshots + hard state) instead of one SQLite file.

**Limitations / trade-offs.** RaftLite costs more resources (node1 peaks 0.45 cores / 24 MB vs 0.13 cores / 19 MB; ~0.48 MB disk vs 0.14 MB) and more operational complexity (3 nodes, leader election, membership). Propagation latency is slightly higher and heavier-tailed under failure (P99 1418 ms in server_failure vs 498 ms traditional, because in-flight writes queue behind recovery). Partition experiment is a single-host node-isolation approximation, not a true network partition. All numbers are single-machine, indicative only; no throughput/latency targets are claimed.

**When to choose.** Traditional fits single-site, low-ops, availability-tolerant deployments; RaftLite fits when policy writes must survive node failure and partitions without manual intervention. The gateway layer is identical, so enforcement behaviour and data-path latency are unchanged by the choice.
", "filePath">
/home/shadil/MTech/shadil/raftlite/traditional-vs-raftlite-evaluation/docs/comparative-analysis.md