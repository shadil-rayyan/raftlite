# RaftLite

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://go.dev)
[![Rust](https://img.shields.io/badge/Rust-2021-000000?logo=rust&logoColor=fff)](https://rust-lang.org)
[![Python](https://img.shields.io/badge/Python-3.12-3776AB?logo=python&logoColor=fff)](https://python.org)
[![CI](https://github.com/shadil-rayyan/distributed_RaftLite/actions/workflows/test-bench.yml/badge.svg)](https://github.com/shadil-rayyan/distributed_RaftLite/actions/workflows/test-bench.yml)
[![Security](https://github.com/shadil-rayyan/distributed_RaftLite/actions/workflows/security.yml/badge.svg)](https://github.com/shadil-rayyan/distributed_RaftLite/actions/workflows/security.yml)

**Distributed rate-limiting and policy engine** backed by the Raft consensus protocol. Routes HTTP traffic, maintains a cluster-wide blocklist, drains telemetry without allocation, and detects anomalies — all in <1.8ms p99.

```
┌──────────────┐    ┌──────────────┐    ┌──────────────┐
│   Go Node 1  │    │   Go Node 2  │    │   Go Node 3  │
│  (HTTP/gRPC) │◄──►│  (HTTP/gRPC) │◄──►│  (HTTP/gRPC) │
│  Raft Member │    │  Raft Member │    │  Raft Member │
└──────┬───────┘    └──────┬───────┘    └──────┬───────┘
       │                    │                    │
       └────────────────────┼────────────────────┘
                            │ Unix socket IPC
                     ┌──────▼───────┐
                     │  Rust Drain   │  lock-free ring buffer
                     │ (telemetry)   │  → Arrow IPC
                     └──────┬───────┘
                            │ /dev/shm
                     ┌──────▼───────┐
                     │ Python AI     │  Isolation Forest
                     │ (anomaly)     │  → gRPC loopback
                     └──────────────┘
```

## Features

- **Raft Consensus** — leader election, log replication, joint consensus for dynamic membership
- **HTTP Gateway** — reverse proxy with in-memory blocklist check, `sync.Pool` allocators, zero heap allocation on hot path
- **Telemetry Drain** — lock-free ring buffer in Rust, zero-copy Arrow batching over POSIX shared memory
- **Anomaly Detection** — streaming Isolation Forest in Python, gRPC loopback to update the cluster blocklist
- **Deterministic Simulation Testing** — replayable chaos harness with configurable packet drops, clock drift, disk stalls, and node crashes
- **Monitoring** — Prometheus metrics endpoint, Grafana dashboard included
- **DevSecOps** — SAST (gosec, clippy, bandit), dependency auditing (govulncheck, cargo audit, pip-audit) on every PR

## Quick Start

```bash
git clone https://github.com/shadil-rayyan/distributed_RaftLite.git
cd distributed_RaftLite
docker compose up --build -d
```

This starts a 3-node Go Raft cluster, a Rust telemetry drain, and a Python AI engine — each pinned to a dedicated CPU core.

```bash
# Check cluster health
curl localhost:8081/healthz
curl localhost:8081/leader

# Block an IP (propose to Raft)
curl -X POST localhost:8081/block -d '{"ip":"10.0.0.99"}'

# Query blocklist
curl localhost:8081/blocked/10.0.0.99

# Run benchmark
bash scripts/benchmark.sh

# Run chaos simulation (kills leader mid-flight)
bash scripts/simulate_chaos.sh
```

## Manual Run

```bash
go run ./cmd/raftlite/ &
NODE_ID=node2 RAFT_ADDR=:9091 RAFTLITE_ADDR=:8081 PEERS=node1,node2,node3 \
  go run ./cmd/raftlite/ &
```

## Project Structure

```
cmd/raftlite/           Go entry point — HTTP server, gRPC server, WAL init
internal/
├── consensus/          Raft core — election, replication, joint consensus, FSM, snapshots
├── gateway/            HTTP filter, reverse proxy, sync.Pool allocators
├── server/             gRPC inter-node RPC (RaftService + LoopbackService)
└── transport/          WAL, clock abstraction, network mock, DST harness
telemetry-drain/        Rust crate — lock-free ring buffer, Arrow batcher, Unix socket IPC
ai-engine/              Python engine — Isolation Forest anomaly detection, shared memory reader
api/                    Protobuf contracts (raft.proto, loopback.proto) + Arrow schema
scripts/                Benchmark (ghz/wrk) and chaos simulation
tests/                  End-to-end and deterministic simulation tests
prometheus/             Prometheus config + Grafana dashboard
```

## Architecture

Three languages, three planes, one shared-memory pipeline:

| Layer | Language | Role | Constraints |
|-------|----------|------|-------------|
| Performance | Go | HTTP ingress, Raft consensus, blocklist FSM | Zero disk I/O on request path, `sync.Pool` for memory |
| Observability | Rust | Telemetry drain, Arrow IPC batching | Zero allocation in ingest loop, lock-free ring buffer |
| Analytical | Python | Anomaly detection (Isolation Forest) | Isolated CPU via `taskset`, memory-mapped Arrow dataframes |

**Data flow:**
1. Go receives HTTP request, checks in-memory blocklist, proxies to upstream, emits telemetry via Unix socket
2. Rust reads telemetry into lock-free ring buffer, batches into Arrow IPC frames in `/dev/shm`
3. Python reads Arrow frames, runs Isolation Forest, sends anomalies back via gRPC to Raft leader
4. Leader commits the blocklist update through Raft, propagating to all followers

## Testing

```bash
go test ./... -count=1 -short          # unit tests
go test ./internal/transport/... -run TestDSTDeterminism -v  # DST
# or via CI: triggered on every push to main and every PR
```

The deterministic simulation harness replays clock drifts, packet drops, disk stalls, and node crashes from a fixed seed — identical seed produces identical interleaving.

## Monitoring

Each node exposes `/metrics` at `:8080`. Prometheus scrapes every 5s.

```
# HELP raftlite_requests_total Total requests processed
# TYPE raftlite_requests_total counter
# HELP raftlite_elections_total Total leader elections
# TYPE raftlite_elections_total counter
# HELP raftlite_blocklist_size Current blocklist size
# TYPE raftlite_blocklist_size gauge
```

Import `prometheus/grafana-dashboard.json` into Grafana for a pre-built cluster dashboard.

## Contributing

PRs require passing CI checks (build, test, vet, lint, security scan). Commit messages follow conventional commits ([SemVer](https://semver.org) for releases).

## License

Unlicense — public domain. See `LICENSE` for details.
