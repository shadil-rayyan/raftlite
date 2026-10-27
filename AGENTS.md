# AGENTS.md

## What this repo is

RaftLite: a **security-policy control plane** for API gateways (IP/CIDR block
policies replicated via a hand-rolled Raft implementation). Polyglot:

- `cmd/raftlite/` + `internal/` — Go: Raft consensus, gRPC, HTTP API (the real product)
- Rust/Python sidecars temporarily removed from this branch; see `docs/rust-python-sidecars.md`.

The README and `docs/` describe **intended** behavior for a much larger system.
Trust the code and CI workflows over the docs. Consensus, storage, and the
admin API are real; the gateway sync protocol and the Rust/Python side
pipelines are still prototype-grade.

## Commands

There is **no Makefile** — ignore the README's `make dev-up`/`make help`
instructions. The executable source of truth is `.github/workflows/test-bench.yml`:

```sh
go build ./...
go vet ./...
go test ./... -count=1 -short          # what CI runs
go test ./... -race -count=1           # full local check before pushing
go test ./internal/transport/... -run TestDSTDeterminism -v   # DST harness (CI job)
go test ./internal/consensus/... -run TestDSTDeterministicElection
go test ./internal/consensus/ -run 'TestCluster|TestMembership' -v

```

Go is **not installed** on this Windows machine; verify with `go version` before
assuming any `go` command works. Shell scripts (`scripts/*.sh`) are bash + Docker
+ curl and assume a Linux-ish environment.

## Testing quirks

- `tests/e2e_test.go::TestE2EBlockRequest` is skipped unless `INTEGRATION=1` and
  a live cluster is on `localhost:8081` (via `docker-compose up --build`).
  `TestE2ESingleNode` runs hermetically.
- Consensus/DST tests inject `transport.MockClock`, `MockStorage`, `MockNetwork`
  (seeded `math/rand`) instead of real time/disk/network — see
  `internal/transport/dst.go`. Keep new consensus code behind these interfaces.
- **`internal/consensus/cluster_test.go` uses `transport.RealClock`** (real
  election timeouts, ~100-200ms) and an in-process `localTransport`. It is the
  only suite that exercises real message flow between nodes; don't convert it
  to mocks.
- Single test: `go test ./internal/consensus/ -run TestLogReplication -v`.

## Architecture gotchas

- **`internal/gateway/` is dead code**: the reverse proxy and sync.Pool
  allocators are NOT wired into `cmd/raftlite/main.go`. The blocklist
  middleware exists (with the SEC-007 trusted-proxy fix) but is mounted
  nowhere. Don't assume gateway changes affect the binary.
- The Raft implementation in `internal/consensus/` is **hand-rolled**; `go.mod`
  pulls in only gRPC/protobuf. There is no external Raft library.
- **Membership is single-server-per-entry** (`membership.go`), committed
  through the normal quorum. The old joint-consensus code was removed: it
  committed config transitions without a quorum.
- **Leaders append a no-op entry** in their term (Figure-8 rule). Tests that
  assert log indexes must account for index 1 being the no-op on a single-node
  cluster.
- **`Storage` hard state**: `<wal>.hs` sidecar (CRC, atomic rename) holds
  term/vote/commit. `restore()` uses the commit index as a *hint* capped by
  the log tail; it never infers commitment from log length (FR-023).
- **WAL is segmented** (`<path>`, `<path>.000001`, ...) with 8 MiB rotation;
  `OpenWAL` truncates torn tails so appends after a crash stay readable.
- **Snapshots** live in `SNAPSHOT_DIR` (defaults to `<dirname WAL>/snapshots`);
  empty dir = memory-only snapshots (unit tests). Set via
  `consensus.SetSnapshotDir`.
- `StartGRPCServer` serves Raft RPC and the AI loopback on the same port; it
  takes `credentials.TransportCredentials` (nil = dev plaintext + warning).
- Metrics (`internal/observability`) are **hand-rolled Prometheus text
  format** — deliberately no client_golang dependency; `/metrics` samples live
  node/FSM state at scrape time.
- Admin API lives in `internal/server/api.go` (SRS §6.1); `cmd/raftlite/main.go`
  adds deprecated v0.1 aliases (`/block`, `/blocked/{ip}`, `/leader`) on top.
- Env vars: `NODE_ID`, `RAFT_ADDR`, `RAFTLITE_ADDR`, `PEERS`, `PEER_ADDRS`
  (id=host:port pairs — **without it the node runs without peer RPCs and can
  never elect**), `ADMIN_PEER_ADDRS` (id=admin-URL pairs for 307 redirects),
  `WAL_PATH`, `SNAPSHOT_DIR`, `RAFTLITE_ADMIN_TOKEN`, `RAFTLITE_AUDIT_LOG`,
  `RAFT_TLS_CERT`/`RAFT_TLS_KEY`/`RAFT_TLS_CA` (inter-node mTLS).
- Protobufs: `api/raft.proto` + `api/loopback.proto`; `api/openapi.yaml` is
  the admin API contract. Generated Go is committed under
  `api/go/{raftpb,loopbackpb}/` (protoc 5.28.2, protoc-gen-go 1.36.11).
  If you change a `.proto`, regenerate those — nothing in the repo does this
  automatically.
- Telemetry write is a no-op stub (`proxy.go::writeTelemetry`); the Rust/Python
  sidecars are temporarily removed (see `docs/rust-python-sidecars.md`). The gRPC
  loopback service remains so a future AI engine can propose blocks. Stubs are
  marked with `// ponytail:` comments — grep for `ponytail` to find every known
  gap.

## docker-compose

3 Go nodes (ports 8081-8083 HTTP, 9091-9093 gRPC), pinned to CPUs 0-2 —
**Linux-only assumptions**; expect failures on Windows/macOS Docker Desktop.
`scripts/simulate_chaos.sh` kills the leader container and polls `/leader`;
`scripts/benchmark.sh` needs `ghz` or `wrk` installed.

## Docs

- `docs/README.md` is the docs index; `docs/architecture/decisions/` holds ADRs.
- Requirement IDs (`FR-xxx`, `NFR-xxx`, `UC-xxx`, `ADR-xxx`) are stable — don't
  reuse a retired ID.
- Behavior/interface changes should update the relevant doc + test in the same
  change. Distinguish "implemented" from "verified" in any status claims.
- Never claim latency/throughput targets (p99 < 1.8ms, 25k RPS) — those appear
  in an old superseded spec and are not backed by benchmark results.
