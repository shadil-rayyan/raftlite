# RaftLite UI Guide

## Build

```sh
go build -o raftlite ./cmd/raftlite
```

## Run: single node (dev)

```sh
RAFTLITE_DEV_UI=1 \
RAFTLITE_ADDR=:8080 \
RAFT_ADDR=:9090 \
PEERS=node1 \
NODE_ID=node1 \
WAL_PATH=/tmp/raftlite.wal \
./raftlite
```

Open http://localhost:8080/ui (`/` redirects to `/ui`).

## Run: 3-node cluster (dev, local processes)

Run each node in its own shell:

```sh
# node1
RAFTLITE_DEV_UI=1 NODE_ID=node1 RAFT_ADDR=:9090 RAFTLITE_ADDR=:8080 \
PEERS=node1,node2,node3 \
PEER_ADDRS=node1=localhost:9090,node2=localhost:9091,node3=localhost:9092 \
ADMIN_PEER_ADDRS=node1=http://localhost:8080,node2=http://localhost:8081,node3=http://localhost:8082 \
WAL_PATH=/tmp/n1.wal ./raftlite

# node2
RAFTLITE_DEV_UI=1 NODE_ID=node2 RAFT_ADDR=:9091 RAFTLITE_ADDR=:8081 \
PEERS=node1,node2,node3 \
PEER_ADDRS=node1=localhost:9090,node2=localhost:9091,node3=localhost:9092 \
ADMIN_PEER_ADDRS=node1=http://localhost:8080,node2=http://localhost:8081,node3=http://localhost:8082 \
WAL_PATH=/tmp/n2.wal ./raftlite

# node3
RAFTLITE_DEV_UI=1 NODE_ID=node3 RAFT_ADDR=:9092 RAFTLITE_ADDR=:8082 \
PEERS=node1,node2,node3 \
PEER_ADDRS=node1=localhost:9090,node2=localhost:9091,node3=localhost:9092 \
ADMIN_PEER_ADDRS=node1=http://localhost:8080,node2=http://localhost:8081,node3=http://localhost:8082 \
WAL_PATH=/tmp/n3.wal ./raftlite
```

Open http://localhost:8080/ui. The Diagnostics panel aggregates peer state via
`ADMIN_PEER_ADDRS`; without it the UI shows only the local node.

## Run: Docker Compose

```sh
docker compose up --build -d
```

UIs: http://localhost:8081/ui, http://localhost:8082/ui, http://localhost:8083/ui.
Compose is Linux-only (CPU pinning, `/dev/shm`).

## Environment options

| Variable | Purpose | Default |
| --- | --- | --- |
| `NODE_ID` | This node's identifier | `node1` |
| `RAFT_ADDR` | gRPC listen address (consensus RPC) | `:9090` |
| `RAFTLITE_ADDR` | Admin HTTP/UI listen address | `:8080` |
| `PEERS` | Comma-separated voter IDs | `node1,node2,node3` |
| `PEER_ADDRS` | `id=host:port` pairs for peer gRPC; omitting it means no peer RPCs and the node can never elect (single-node only) | empty |
| `ADMIN_PEER_ADDRS` | `id=http-url` pairs used for 307 redirects and UI diagnostics aggregation | empty |
| `WAL_PATH` | Write-ahead log path | `/tmp/raftlite.wal` |
| `SNAPSHOT_DIR` | Snapshot directory (defaults next to WAL) | `<WAL dir>/snapshots` |
| `RAFTLITE_ADMIN_TOKEN` | Bearer token for mutating admin calls; empty disables auth (dev only) | empty |
| `RAFTLITE_AUDIT_LOG` | Audit JSON-lines path; empty disables audit | empty |
| `RAFTLITE_DEV_UI` | `1` shows the Dev Tests tab | unset |
| `RAFT_TLS_CERT` / `RAFT_TLS_KEY` / `RAFT_TLS_CA` | Inter-node mTLS material; all empty = plaintext (dev only) | empty |

## UI panels

- **Overview**: health, node, role, leader, term, revision, policy count, last
  log index, commit index, cluster topology table, create IP block form.
- **Policies**: list, filter by target/status, revoke, expiry.
- **IP Check**: test an IP against committed policy (exact IP; malformed input
  returns the API's validation error).
- **Audit**: recent audit entries; requires `RAFTLITE_AUDIT_LOG`.
- **Metrics**: raw Prometheus text from `/metrics`.
- **Diagnostics**: cluster size, quorum, majority commit/log counts, full JSON
  diagnostics payload, per-node reachability.
- **Dev Tests**: visible only with `RAFTLITE_DEV_UI=1`.

## Dev self-test

Click **Dev Tests → Run Self-Test**. It exercises:

1. `/healthz` — liveness.
2. `/v1/node` — node identity, role, term, last log, commit.
3. `/v1/diagnostics` — quorum and majority-log checks.
4. `/v1/blocked/{ip}` for `10.0.0.5`, `2001:db8::1`, `not-an-ip`, `10.0.0.0/8`,
   `256.1.1.1` — valid, IPv6, malformed, and CIDR inputs. Expected 400s still
   count as PASS because the endpoint correctly rejected invalid input.
5. `/v1/audit` — endpoint availability.

Each check renders PASS/FAIL with the raw response detail.

## Test Lab (dev only)

**Test Lab** runs full scenarios against the live cluster and reports each step.
Use **Run All Scenarios** or individual buttons. **Random IP** fills the target
box with an address from `203.0.113.0/24` (documentation range, safe to block).
**Cleanup Lab Policies** revokes every policy the lab created.

| Scenario | What it verifies |
| --- | --- |
| Liveness | `/healthz` and `/readyz` respond correctly |
| Leader / Quorum | diagnostics reports quorum, majority log, known leader, node identity |
| Create Block | create commits, revision advances, IP reports blocked |
| Idempotency | replaying the same `request_id` returns `idempotent: true` |
| Revoke | create, revoke, then the IP reports unblocked |
| Validation | malformed IP, out-of-range octet, unspecified/multicast, CIDR, and negative duration are all rejected with 400 |
| Audit | audit entry count grows after a write |
| Metrics | `/metrics` is reachable and contains `raftlite_commit_index` |
| Follower Forwarding | a create on a follower is forwarded to the leader and commits |

## Create IP block arguments

`POST /v1/policies/ip-blocks` accepts:

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `ip` | string | yes | Valid IPv4/IPv6 unicast address; CIDR is rejected here |
| `reason` | string | no | Free text, recorded in audit |
| `duration` | string | no | Go duration (`1h`, `30m`); empty = permanent; max 8760h |
| `request_id` | string | no | Idempotency key; replay returns the original outcome |
| `created_by` | string | no | Actor label recorded in audit |

## Troubleshooting

- **Create returns `not_leader` (307)**: you are on a follower and
  `ADMIN_PEER_ADDRS` is not set on that node, so it cannot forward. Set it and
  restart, or POST to the leader directly.
- **Audit panel is empty**: writes are recorded in memory only for the current
  process; set `RAFTLITE_AUDIT_LOG` for a durable file. Entries are lost on
  restart when no file is configured.
- **`applied_index` is 0 but `commit_index` is not**: no policy has been
  committed yet — the no-op entry commits but does not advance the FSM revision.
- **Diagnostics shows a peer unreachable**: that node is down or
  `ADMIN_PEER_ADDRS` is missing for it on the node serving the UI.

## Useful endpoints

- `/healthz`, `/readyz`
- `/v1/leader`, `/v1/cluster`, `/v1/node`
- `/v1/diagnostics`
- `/v1/policies`, `/v1/policies/ip-blocks`, `/v1/policies/{id}`, `/v1/blocked/{ip}`
- `/v1/audit?limit=N`
- `/metrics`

## Production notes

- Set `RAFTLITE_ADMIN_TOKEN` and paste it into the UI token box.
- Leave `RAFTLITE_DEV_UI` unset in production.
- Enable `RAFTLITE_AUDIT_LOG` for a durable audit trail.
- Set `RAFT_TLS_*` for any shared network.
- Point `ADMIN_PEER_ADDRS` at each node's admin URL so the UI shows the full
  cluster, not just the local node.
