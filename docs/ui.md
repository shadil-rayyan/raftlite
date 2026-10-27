# RaftLite UI Guide

## Start the UI

Single-node dev mode:

```sh
RAFTLITE_DEV_UI=1 RAFTLITE_ADDR=:8080 RAFT_ADDR=:9090 PEERS=node1 \
  NODE_ID=node1 WAL_PATH=/tmp/raftlite.wal ./raftlite
```

Open http://localhost:8080/ui. The server redirects `/` to `/ui`.

For a 3-node cluster, run three nodes with `PEER_ADDRS` and
`ADMIN_PEER_ADDRS` set, then open any node's UI. Peer state is aggregated via
`/v1/diagnostics`.

## Main panels

- Overview: health, node, role, leader, term, revision, policy count, last log,
  commit, cluster topology, create IP block.
- Policies: list/search/revoke policies, expiry.
- IP Check: test an IP or CIDR against committed policy.
- Audit: recent audit entries (requires `RAFTLITE_AUDIT_LOG`).
- Metrics: raw Prometheus metrics.
- Diagnostics: cluster size, quorum, majority commit/log counts, JSON state.
- Dev Tests: visible only when `RAFTLITE_DEV_UI=1`.

## Dev self-test

The Dev Tests panel calls:
- `/healthz`
- `/v1/node`
- `/v1/diagnostics`
- `/v1/blocked/{ip}` for valid, IPv6, malformed, and CIDR inputs
- `/v1/audit`

Each check reports PASS/FAIL. Expected 400 responses for malformed IPs/CIDRs are
still PASS because the endpoint rejected invalid input correctly.

## Useful endpoints

- `/healthz`, `/readyz`
- `/v1/leader`, `/v1/cluster`, `/v1/node`
- `/v1/diagnostics`
- `/v1/policies`, `/v1/policies/ip-blocks`, `/v1/blocked/{ip}`
- `/v1/audit?limit=N`
- `/metrics`

## Production notes

- Set `RAFTLITE_ADMIN_TOKEN` and pass it in the UI token box.
- Disable `RAFTLITE_DEV_UI` in production.
- Audit entries are in-memory only unless `RAFTLITE_AUDIT_LOG` is set.
