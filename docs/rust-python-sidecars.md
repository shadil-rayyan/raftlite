# Rust and Python Sidecars (Temporarily Removed)

Status: temporarily removed from the `dev` branch. Go control plane, consensus,
storage, admin API, and gRPC loopback service are unchanged.

## What was removed

- `telemetry-drain/` (Rust): lock-free ring buffer, Arrow IPC batcher, Unix
  socket IPC listener, Dockerfile.
- `ai-engine/` (Python): Isolation Forest anomaly detector, shared-memory
  reader, gRPC loopback proposal client, Dockerfile, requirements.
- Docker Compose services `telemetry-drain` and `ai-engine`.
- CI steps for Rust build/test/clippy/audit and Python compile/lint/audit.

## What remains in Go

- `internal/server/grpc.go` `LoopbackServer.AddBlock`: authoritative proposal
  path for an AI engine; proposals still go through Raft.
- `internal/gateway/proxy.go` `writeTelemetry`: no-op stub; no Go producer yet.
- Generated protobufs `api/go/loopbackpb` and `api/loopback.proto`.

## Why removed

The sidecars were prototype-grade, not wired into the main binary, and added
CI/Docker complexity. This branch focuses on the Go consensus core.

## Reintroduction plan

1. Define the telemetry/proposal contract (protobuf or HTTP) and auth.
2. Rust drain: ring buffer, Arrow batching, UDS listener, backpressure,
   validation, tests.
3. Go producer: implement `writeTelemetry` with a bounded, non-blocking queue.
4. Python engine: feature extraction, Isolation Forest, gRPC loopback proposals,
   approval workflow.
5. Restore Docker Compose services and CI build/test/lint/audit steps.
6. Add integration and chaos tests for telemetry loss and proposal flow.

## Acceptance criteria

- Sidecars start via `docker-compose up --build`.
- Go telemetry write is non-blocking and bounded.
- AI proposals pass through Raft and are auditable.
- Rust/Python CI steps pass.
- No change to consensus correctness or commit semantics.

## References

- Removed from `dev` on 2026-10-27.
- Historical implementation exists in git history (commits `f39f114`,
  `ebf4902`).