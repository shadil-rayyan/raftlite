# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] — Unreleased

Scope: consensus + storage + admin API turned from prototype into a working
control plane for API-gateway security policies. The gateway sync protocol and
the Rust/Python side pipelines are intentionally **unchanged** and still
best-effort.

### Fixed

- **Elections now actually happen** — `RequestVote` RPCs were defined but
  never sent; candidates now broadcast real votes, count responses with a
  deadline, durably persist term/vote before responding (Raft §5.4.2), and
  step down on higher terms.
- **Leader no-op entry** — a new leader appends a no-op in its term so a
  committed prefix from earlier terms can commit (Figure-8 rule).
- **Commit is a majority + current-term rule** (Figure 8) instead of
  "log length"; followers commit to `min(leaderCommit, lastNew)`.
- **Restart recovery** — commit index is re-established from durable hard
  state and snapshots, never inferred from log length (FR-023); uncommitted
  tails are not applied. Torn WAL tails are discarded on open so appends after
  a crash remain readable (FR-022).
- **Snapshots are real** — atomic temp+fsync+rename files, log compaction at a
  configurable threshold, `InstallSnapshot` fallback for lagging followers,
  load-on-restart. The previous implementation never persisted or compacted.
- **Hard state (term/vote/commit) is durable** — CRC-protected sidecar file,
  written atomically before any vote response.
- **Election-timer goroutine leak** — replaced per-reset goroutines with a
  single timer loop (`resetTimerCh`).
- **Unsafe joint consensus removed** — replaced by single-server membership
  changes committed through the normal quorum (Raft dissertation §4.1); the
  old code committed configuration transitions without a quorum.
- **Client-IP trust (SEC-007)** — the gateway middleware no longer trusts
  `X-Forwarded-For`/`X-Real-IP` unconditionally; forwarded headers are honored
  only from configured trusted-proxy CIDRs, walking right-to-left past trusted
  hops.

### Added

- **Policy engine (`internal/policy`)** — SRS §7.1 model (id, type, target,
  action, created_at, expires_at, reason, created_by, version, status), CIDR
  blocks (FR-010), deterministic precedence exact > longest-prefix > ID
  (§7.2), committed-timestamp expiry (NFR-015, ADR-0003), replicated
  idempotency keys (FR-016), JSON snapshot/restore.
- **Admin API (`internal/server`)** — SRS §6.1 routes: `/v1/policies/ip-blocks`,
  `/v1/policies/{id}`, `/v1/policies`, `/v1/blocked/{ip}`, `/v1/cluster`,
  `/v1/leader`, `/healthz` (liveness), `/readyz` (write-readiness, FR-028).
  Bearer-token auth on writes (SEC-001), validation before the log (FR-015),
  outcome mapping committed / 504 `outcome_uncertain` / 307 `not_leader` /
  503 `no_leader` (PRD §13.1, FR-007), 307 carries the leader's Location.
- **Audit log (`internal/audit`)** — JSON-lines records of every administrative
  operation with its *true* outcome (FR-031, SEC-008); uncertain writes are
  never recorded as committed.
- **Prometheus metrics (`internal/observability`)** — text-format exposition
  driven by live consensus state: elections, term, commit/applied index,
  active policies, WAL size, propose→commit latency histogram, RPC errors
  (FR-030). Hand-rolled to keep the binary dependency-free.
- **gRPC peer transport (`internal/server/grpcclient.go`)** — pooled per-peer
  connections, optional inter-node TLS (`RAFT_TLS_CERT/KEY/CA`), explicit
  insecure fallback for development (SEC-003/004).
- **Segmented WAL** — base path + `.NNNNNN` segments, 8 MiB rotation,
  O(segment) truncation, format-compatible record framing.
- **Tests** — policy-engine suite, WAL hard-state/torn-tail/rotation suite,
  admin-API contract suite, SEC-007 XFF regression tests, and an in-process
  3-node integration suite (`cluster_test.go`): election, replication,
  failover after leader isolation, minority-leader safety, restart recovery.

### Changed

- `FSM.Apply(data, revision)` now takes the log revision; snapshots are raw
  `[]byte` envelopes.
- `Storage` interface gained `LoadHardState`/`SaveHardState`.
- `StartGRPCServer` takes `credentials.TransportCredentials` (nil = dev
  plaintext with a warning).
- `docker-compose.yml` passes `PEER_ADDRS`/`ADMIN_PEER_ADDRS`; the v0.1
  `/block`, `/blocked/{ip}`, `/leader` endpoints are kept as deprecated
  aliases in `cmd/raftlite/main.go`.

### Known gaps (unchanged)

- The gateway reverse proxy is still not wired into `cmd/raftlite/main.go`.
- Telemetry write to the Rust drain (`proxy.go::writeTelemetry`) and the
  Python gRPC loopback are still stubs (marked `ponytail:`).
- `/metrics` claims in dashboards are not validated by load tests; no
  latency/throughput targets are asserted anywhere.

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
