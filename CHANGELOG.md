# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] — 2026-07-03

### Added

- **Raft consensus core** — leader election, log replication, joint consensus
  transitions, blocklist FSM with snapshot support.
- **HTTP gateway** — reverse proxy, in-memory blocklist filter, `sync.Pool`
  allocators with zero heap allocation on the validation path.
- **gRPC inter-node server** — RaftService for log replication and elections,
  LoopbackService for AI-engine blocklist updates.
- **WAL** — flat-file write-ahead log with CRC integrity checks.
- **Transport layer** — deterministic simulation testing (DST) harness with
  configurable packet drops, clock drift, disk stalls, and node crashes.
- **Rust telemetry drain** — lock-free ring buffer ingestion over Unix domain
  sockets, zero-copy Apache Arrow IPC batcher writing to `/dev/shm`.
- **Python AI engine** — streaming Isolation Forest anomaly detection consuming
  memory-mapped Arrow dataframes, gRPC loopback to Raft leader.
- **Docker Compose topology** — 3-node Go cluster + Rust drain + Python engine,
  each pinned to dedicated CPU cores.
- **CI/CD** — automated test, build, lint, and security scanning workflows
  (gosec, govulncheck, clippy, cargo audit, bandit, pip-audit).
- **Monitoring** — Prometheus metrics (`/metrics`) and a pre-built Grafana
  dashboard for RPS, leader elections, and blocklist size.
- **Chaos script** — automated leader-kill simulation under load.
- **Benchmark script** — `ghz`/`wrk` load testing pipeline.
- **Protobuf contracts** — `api/raft.proto` (inter-node Raft RPC) and
  `api/loopback.proto` (AI-engine loopback).
- **Arrow schema** — cross-language IPC layout for Rust→Python telemetry.

[0.1.0]: https://github.com/shadil-rayyan/distributed_RaftLite/releases/tag/v0.1.0
