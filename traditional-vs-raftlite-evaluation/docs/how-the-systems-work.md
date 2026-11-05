# How the systems work

Both environments share the same shape. Only the box in the middle differs.

```
                 ┌──────────────────────────────────────────┐
                 │            measurement server            │
                 │  (orchestrator + collector + dashboard)  │
                 └───────┬───────────────────┬──────────────┘
                         │ spawns            │ receives receipts
                         ▼                   │
   ┌─────────────────────────────┐           │
   │  gateway fleet (G1..G5)     │───────────┘
   │  - polls backend /v1/policies
   │  - caches {policies, revision}
   │  - blocks matching source IPs
   │  - POSTs receipt timestamps │
   └───────────┬─────────────────┘
               │ GET /v1/policies  (identical contract)
               ▼
   ┌─────────────────────────────┐        ┌─────────────────────────────┐
   │  TRADITIONAL                │        │  RAFTLITE                   │
   │  central server (Go+SQLite) │        │  adapter (REST proxy)       │
   │  monotonic revision counter │        │       │                      │
   └─────────────────────────────┘        │       ▼                      │
                                          │  3-node RaftLite cluster     │
                                          │  node1/2/3 (real consensus)  │
                                          └─────────────────────────────┘
```

## The common backend contract

Both backends answer exactly this shape:

```
GET  /v1/policies  →  {"policies": [{"id","ip","action"}], "revision": N}
POST /v1/policies  →  {"status","policy_id","revision"}
DELETE /v1/policies/{id}
GET  /healthz
```

`revision` is a monotonically increasing counter that changes on every write. Gateways use it to
detect change cheaply: same revision → nothing to do.

## Traditional environment

- `traditional` — one Go process. Policies live in a SQLite file (`modernc.org/sqlite`, pure Go,
  no CGO). Every write bumps a revision counter inside the same transaction, so the list and the
  revision a reader sees are always consistent.
- Failure model: one process. If it stops, new writes fail and gateways keep enforcing the last
  cached policy set (they never silently drop policies).

## RaftLite environment

- `node1`, `node2`, `node3` — the **real RaftLite binary** (built from the parent repository).
  Policy writes are committed through the replicated log with quorum acknowledgement; policy
  reads on the leader are served through a linearizable read path (ReadIndex), and followers
  forward or redirect read requests to the leader. WAL, snapshots, and hard-state persistence are
  RaftLite's own.
- `adapter` — a small translating proxy. It presents the uniform contract above while talking to
  RaftLite's admin API underneath:
  - `GET /v1/policies` → fetch-through (no extra caching), normalising RaftLite's policy shape
    (its `target`/`action`/`status` fields) into `{id, ip, action}`.
  - `POST /v1/policies` → `POST /v1/policies/ip-blocks` (RaftLite is a block list, so ALLOW is
    rejected with 501 — which is why shared datasets are BLOCK-only).
  - Because it fetches through on every request, the adapter adds **no polling staleness** of its
    own: a gateway poll costs the same on both systems (one backend read).
- Failure model: any node can fail; the cluster elects a new leader and continues. The adapter
  follows RaftLite's forwarding/redirect behaviour, so it can point at any node.

## The gateway (identical in both)

One implementation, configured with `-backend`:

1. Polls `GET {backend}/v1/policies` every `-poll` (default 500 ms).
2. Diffs the new list against its cache by policy ID; installs additions/removals.
3. Rebuilds lookup structures: exact-IP map + CIDR prefix list (both enforced).
4. For every policy it applies for the first time, POSTs a receipt to the collector:
   `{gateway_id, policy_id, ip, revision, applied_at_ns}`.
5. On the data path: parse the client IP (`X-Forwarded-For` → `X-Real-IP` → socket), match
   against the cache, respond `429` if blocked else `200`, and echo the cache revision in
   `X-Gateway-Revision`.

Control endpoints (used by experiments):

- `GET /healthz` → `{ok, gateway_id, revision}`
- `GET /v1/gateway/state` → `{gateway_id, revision, policies[]}` — the correctness check reads
  this from every gateway and compares it against the expected set.

## Ports (defaults)

| component | port |
|-----------|------|
| measurement server (dashboard, API, SSE, receipts) | 8095 |
| backend (traditional server / raftlite adapter) | 8100 |
| gateways G1..G5 | 8110–8114 |
| RaftLite node1..3 HTTP admin | 8081–8083 |
| RaftLite node1..3 gRPC raft | 9091–9093 |

All on `127.0.0.1` for single-machine runs.

## Environment variables (RaftLite nodes)

The measurement server sets these when spawning `raftlite`: `NODE_ID`, `RAFT_ADDR`,
`RAFTLITE_ADDR`, `PEERS`, `PEER_ADDRS`, `ADMIN_PEER_ADDRS`, `ADMIN_INTERNAL_PEER_ADDRS`,
`WAL_PATH`, `SNAPSHOT_DIR`. No admin token is set for these runs (dev plaintext).
