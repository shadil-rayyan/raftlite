# RaftLite — Complete Project Guide

> Single-file orientation document: structure, implementation status, architecture,
> Raft details, scenarios, usage, and interview talking points. Trust code + CI
> over docs/README claims (see AGENTS.md).

---

## 1. What is RaftLite

RaftLite is a **self-hosted distributed control plane for security policies**
(IP/CIDR block rules) enforced by API gateways. Multiple gateway nodes share one
authoritative policy state replicated via a **hand-rolled Raft** state machine.
The product is the Go consensus core + gRPC peer transport + admin HTTP API.
Rust (telemetry drain) and Python (anomaly detector) are side pipelines,
prototype-grade and not wired into the main binary.

---

## 2. Why these languages

- **Go (primary)** — single static binary, strong concurrency primitives
  (goroutines/channels/mutexes), first-class gRPC/protobuf support, stdlib HTTP
  router, easy cross-compile for containers. Consensus needs deterministic
  concurrency + durable I/O; Go fits.
- **Rust (telemetry-drain)** — lock-free ring buffer + Arrow IPC batcher;
  chosen for zero-copy, memory safety, and throughput on the hot telemetry path.
  Sidecar only; not required for consensus.
- **Python (ai-engine)** — Isolation Forest anomaly detection via scikit-learn;
  ML ecosystem. Best-effort; only logs "would block", never calls Raft yet.

No external Raft library: `go.mod` pulls only gRPC + protobuf, by design, so
the implementation is auditable and dependency-light.

---

## 3. Repository structure

```
raftlite/
├── cmd/raftlite/main.go          # process entrypoint, env config, wiring
├── internal/
│   ├── consensus/                # hand-rolled Raft + FSM + membership + snapshot
│   │   ├── raft.go               # Node, election timer, propose, restore
│   │   ├── election.go           # startElection, winElection, vote handling
│   │   ├── replication.go        # AppendEntries, InstallSnapshot, commit
│   │   ├── membership.go         # single-server config changes
│   │   ├── fsm.go                # replicated policy state machine
│   │   ├── snapshot.go           # compaction + snapshot transfer
│   │   └── transport.go          # Transport interface
│   ├── transport/                # Clock/Storage/Network interfaces + WAL
│   │   ├── clock.go              # RealClock + MockClock (DST)
│   │   ├── storage.go            # Storage interface + MockStorage
│   │   ├── network.go            # MockNetwork
│   │   ├── wal.go                # segmented WAL + CRC + torn-tail recovery
│   │   └── dst.go                # deterministic simulation harness
│   ├── policy/policy.go          # policy engine, precedence, expiry, idempotency
│   ├── server/                   # admin API, gRPC server/client, UI
│   │   ├── api.go                # SRS §6.1 routes, auth, validation, outcomes
│   │   ├── grpc.go               # Raft + loopback gRPC services
│   │   ├── grpcclient.go         # peer transport, optional mTLS
│   │   └── ui.go                 # embedded dashboard
│   ├── gateway/                  # DEAD CODE: filter (SEC-007), pool, proxy (not wired)
│   ├── audit/audit.go            # JSON-lines audit (FR-031)
│   └── observability/metrics.go  # hand-rolled Prometheus text
├── api/                          # protos + generated Go + openapi.yaml
│   ├── raft.proto, loopback.proto
│   └── go/{raftpb,loopbackpb}/   # generated stubs
├── telemetry-drain/              # Rust sidecar (ring buffer, Arrow batcher)
├── ai-engine/                    # Python sidecar (Isolation Forest, stub loopback)
├── tests/e2e_test.go             # in-process + live-cluster e2e
├── scripts/                      # chaos/benchmark shell scripts
├── docs/                         # intended (largely aspirational) docs
├── docker-compose.yml
└── .github/workflows/            # test-bench.yml, security.yml
```

---

## 4. Status: done vs. remaining

### Done (consensus core is real)

- Elections: RequestVote RPCs sent, votes counted with deadline, durable
  term/vote before response (§5.4.2), step-down on higher term.
- Leader no-op entry (Figure-8), commit = majority + current-term rule.
- Replication: AppendEntries with conflict backtracking, follower commit
  `min(leaderCommit, lastNew)`.
- Restart recovery: hard state + snapshot + log tail; commit never inferred
  from log length (FR-023); torn WAL tails discarded (FR-022).
- Snapshots: atomic file, compaction at 1024 entries, InstallSnapshot fallback,
  load-on-restart.
- Hard state: CRC sidecar, atomic rename.
- Segmented WAL: 8 MiB rotation, O(segment) truncation.
- Single-server membership changes through normal quorum.
- Policy engine: IP/CIDR, precedence exact > longest prefix > ID, committed-
  timestamp expiry, idempotency keys, snapshot/restore.
- Admin API: SRS §6.1 routes, bearer auth, validation before log, outcome
  mapping committed/uncertain/not_leader/no_leader, 307 redirect, idempotent
  replay.
- Audit log, Prometheus metrics, gRPC peer transport with optional mTLS.
- DST harness: MockClock/Storage/Network, seeded rand simulation.
- In-process 3-node integration tests (election, replication, failover,
  minority safety, restart recovery).
- Gateway blocklist middleware with SEC-007 trusted-proxy fix (tests pass).

### Remaining / known gaps (grep `ponytail`)

- **Gateway is dead code**: proxy + sync.Pool allocators not wired into
  `main.go`; blocklist middleware mounted nowhere. Gateway changes don't affect
  the binary.
- **Telemetry write is a no-op stub** (`proxy.go::writeTelemetry`).
- **Python gRPC loopback is a stub**: logs "would block", never calls Raft.
- **Rust drain only listens on Unix socket**; no Go producer.
- **No joint consensus** (intentionally removed; only single-server changes).
- Docs/README describe a larger intended system; verify against code/CI.

---

## 5. How the whole system works

1. `main.go` opens segmented WAL (`transport.OpenWAL`), sets snapshot dir,
   creates FSM + Node, restores durable state.
2. If `PEER_ADDRS` provided, builds gRPC transport (optional TLS); else warns
   (single-node/dev only).
3. `node.Start()`: single voter elects itself immediately and appends a no-op;
   multi-node starts election timer.
4. gRPC server serves Raft RPCs + AI loopback on one port.
5. Admin HTTP API serves `/v1/*`, `/healthz`, `/readyz`, `/ui`, `/metrics`.
6. Writes: leader validates → proposes command → persists to WAL → broadcasts
   AppendEntries → on majority commits → applies to FSM → notifies waiters.
   Followers redirect (307) or return 503 no_leader.
7. Reads: any node serves from local FSM (eventually consistent on followers).
8. Recovery: node restarts, restores hard state + snapshot + log tail, applies
   committed prefix only.

---

## 6. Raft implementation details

- **State**: `Follower/Candidate/Leader`, term, votedFor, log, commitIndex,
  lastApplied, nextIndex/matchIndex maps.
- **Election**: randomized timeout 150–300ms; candidate persists term+vote, sends
  RequestVote with lastLogIndex/Term; wins on majority; steps down on higher term.
- **Log matching**: prevLogIndex/prevLogTerm consistency; conflict hints for
  fast backtracking; truncate conflicting suffix.
- **Commit**: majority storing entry AND entry from leader's current term
  (Figure-8). Followers commit to `min(leaderCommit, lastNew)`.
- **No-op**: new leader appends empty command in its term so prior-term committed
  prefix can commit.
- **Membership**: single-server changes (add/remove one voter) committed as
  CONFIG entries through normal quorum; removal shrinks quorum only after commit.
- **Snapshots**: threshold 1024; compact log to base entry; persist atomically;
  InstallSnapshot for lagging followers; load on restore.
- **Storage**: segmented WAL with CRC32 (IEEE) per record, fsync per append,
  atomic hard-state sidecar, torn-tail truncation on open.
- **DST**: injectable Clock/Storage/Network; seeded `math/rand` for
  reproducible simulation.

---

## 7. Scenarios implemented/tested

- Single-node immediate leadership + propose/commit.
- 3-node election & log replication.
- Leader failover after isolation (minority leader steps down).
- Minority quorum loss: writes return `ErrNoQuorum` (uncertain), never fake commit.
- Restart recovery: committed survives, uncommitted tail not applied (FR-023).
- Torn WAL tail discarded; appends after crash readable (FR-022).
- Segment rotation + cross-segment truncation.
- Snapshot compaction + InstallSnapshot catch-up.
- Membership add/remove with quorum semantics.
- Idempotent create replay (FR-016).
- Auth, validation-before-log, leader redirect (307), no-leader (503).
- Trusted proxy IP extraction (SEC-007).
- Deterministic convergence of replicas.

---

## 8. How to use

### Build & run (single node, dev)

```sh
go build ./...
go vet ./...
go test ./... -count=1 -short
```

Single node:
```sh
NODE_ID=node1 PEERS=node1 WAL_PATH=/tmp/raftlite.wal \
  RAFT_ADDR=:9090 RAFTLITE_ADDR=:8080 ./raftlite
```
With peers (need PEER_ADDRS):
```sh
PEER_ADDRS="node1=localhost:9091,node2=localhost:9092" ...
```

### API flow (user flow)

1. `GET /healthz` → liveness.
2. `GET /readyz` → role + write_ready.
3. `GET /v1/leader` → current leader.
4. Create block (on leader, with token if set):
   `POST /v1/policies/ip-blocks {"ip":"203.0.113.5","duration":"1h","reason":"abuse"}`
   → `{status:committed, policy_id, raft_index, revision}`.
5. Check: `GET /v1/blocked/203.0.113.5` → `{blocked:true, policy_id}`.
6. List: `GET /v1/policies` (or `?include_inactive=1`).
7. Revoke: `DELETE /v1/policies/{id}?reason=...`.
8. Follower write → `307` with `Location` to leader; no leader → `503 no_leader`.
9. Timeout → `504 outcome_uncertain` (retry same `request_id`).
10. Dashboard: `/ui`; metrics: `/metrics`.

### Cluster (docker-compose)

3 nodes (HTTP 8081-8083, gRPC 9091-9093) + Rust drain + Python engine.
`docker-compose up --build`. Chaos: `scripts/simulate_chaos.sh`. E2E needs
`INTEGRATION=1`.

---

## 9. Interview talking points (likely questions)

- **Why Raft over Paxos?** Easier to understand/implement; strong leader,
  log replication, membership; suitable for control plane with few nodes.
- **How is safety ensured?** Term/vote persisted before response (§5.4.2);
  commit requires majority + current-term (Figure-8); no commit inference
  from log length; uncommitted tails not applied.
- **How does leader election work?** Randomized timeouts, vote persistence,
  log up-to-date comparison, step-down on higher term.
- **How are conflicts resolved?** AppendEntries consistency check + conflict
  hints + truncate suffix; InstallSnapshot for lagging followers.
- **How do you handle crashes?** Hard state + snapshot + log tail; torn tail
  discarded; commit hint capped by log tail.
- **How do snapshots work?** Threshold compaction, atomic file, base entry,
  InstallSnapshot transfer.
- **Membership changes?** Single-server, committed via normal quorum; no joint
  consensus (removed due to safety).
- **Determinism of FSM?** No wall-clock in Apply; expiry evaluated at lookup
  against committed timestamp (ADR-0003).
- **Idempotency?** Replicated idem keys in policy state; replay returns
  original outcome.
- **Failure outcomes to clients?** committed / uncertain (504) / not_leader
  (307) / no_leader (503); never fake a commit.
- **Observability?** Hand-rolled Prometheus text, live sampling at scrape.
- **Security?** Bearer auth, validation before log, trusted-proxy IP
  extraction, optional mTLS, audit log.
- **Testing?** DST with mocks, real-clock cluster integration, race detector.
- **Limitations?** Gateway not wired, side pipelines stubs, no joint consensus,
  docs aspirational.

---

## 10. Key files to read first

- `cmd/raftlite/main.go` — wiring.
- `internal/consensus/raft.go` — Node core.
- `internal/consensus/election.go` — elections.
- `internal/consensus/replication.go` — replication/commit.
- `internal/transport/wal.go` — durable storage.
- `internal/policy/policy.go` — state machine.
- `internal/server/api.go` — admin API.
