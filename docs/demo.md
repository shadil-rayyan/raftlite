# RaftLite Demo Runbook

A step-by-step script for demonstrating how RaftLite works end to end: a
3-node Raft cluster, Raft-conscious consensus logs in the dashboard, block /
unblock an IP, three simulated Envoy gateways receiving the policy, and the
failure-injection moments (leader kill, gateway offline) that prove the
system reacts correctly.

Everything below runs from the repository root. **The compose cluster is
Linux-only** (CPU pinning, `/dev/shm`); on macOS/Windows use Docker Desktop
with a Linux VM and expect `cpuset` to be ignored or adjusted.

## 1. Start the fleet

```sh
docker compose up --build -d
```

This starts:

| Service | Ports (host) | Role |
|---|---|---|
| `node1..node3` | 8081-8083 HTTP, 9091-9093 gRPC | 3-node Raft cluster |
| `simgw-1` | 18080 data, 19901 admin | simulated Envoy → polls **node1** |
| `simgw-2` | 18081 data, 19902 admin | simulated Envoy → polls **node2** (follower → forwarded) |
| `simgw-3` | 18082 data, 19903 admin | simulated Envoy → polls **node3** (follower → forwarded) |
| `echo` | — | upstream the gateways proxy to |

The gateways deliberately poll **different** nodes: `simgw-2` and `simgw-3`
talk to followers, which forward fleet requests to the leader over the
in-network admin URLs (`ADMIN_INTERNAL_PEER_ADDRS`). That exercises the
three-node communication path on every config pull.

Wait a few seconds for the election:

```sh
curl -s localhost:8081/leader     # → {"leader_id":"nodeN", ...}
```

## 2. Open the dashboard

<http://localhost:8081/ui> (any of 8081/8082/8083 works — every node serves
the UI from its own local view).

What to point at, in demo order:

1. **Header**: node id, `role`, `leader`, `term` — who is the leader right now.
2. **Cluster Topology** (Overview): all three nodes with role, leader, term,
   last log, commit index, and reachability.
3. **Raft Consensus Events** (Overview): the Raft-conscious log —
   `election_started`, `leader_elected`, `leader_observed`, `term_bumped`,
   `vote_granted`, `snapshot_taken`, `state_restored` — each with time, term,
   and detail. This is the "who became leader, when, in which term" view.
4. **Gateways tab**: three gateways with adapter, connection status,
   desired/reported revision, state, verification badge, and last error.
5. **Policy Enforcement Status card**: every policy × every gateway with the
   probe result (`verified` / `failed` / `not_tested`).
6. **Audit tab**: committed/rejected/uncertain outcomes with Raft index.

Each node records its own local event view — reload the UI on a follower
after a leader kill to see the same story from that node's perspective.

## 3. Block an IP

In the UI **Overview → Create IP Block** (or curl):

```sh
curl -s -X POST localhost:8081/v1/policies/ip-blocks \
  -H 'Content-Type: application/json' \
  -d '{"ip":"203.0.113.10","reason":"demo","duration":"1h"}'
```

Then watch, in order:

1. **Revision** increments; the new policy appears in **Policies**.
2. **Gateways → Current Rollout** shows the rollout to `simgw-1..3`;
   desired revision climbs, gateways pull config (their log streams show the
   `config_accepted` event — the "gateway received the logs" moment).
3. **Policy Enforcement Status** flips from `not_tested` to `verified` — the
   control plane *probed* each gateway's data path and got the expected
   `429` for the blocked target (and `200` for the negative control).
   `not_tested` never means "fine"; it means no probe has spoken yet.

## 4. Prove it on the data path

The gateways are real reverse proxies: they pull config, apply the
blocklist, and forward to `echo`.

```sh
# Blocked source IP → 429 from every gateway
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Forwarded-For: 203.0.113.10' localhost:18080/
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Forwarded-For: 203.0.113.10' localhost:18081/
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Forwarded-For: 203.0.113.10' localhost:18082/

# Clean source → 200 (proxied to echo)
curl -s -H 'X-Forwarded-For: 198.51.100.7' localhost:18080/
```

Unblock by revoking the policy in **Policies → Revoke** and the next probe
cycle returns `200` again for the previously blocked target.

## 5. Edge case: kill the leader

```sh
docker compose kill node1     # or whichever node /leader reports
```

Then observe:

1. The UI served by a **surviving node** (e.g. <http://localhost:8082/ui>)
   shows `term_bumped` / new `election_started` / `leader_elected` events in
   the Raft log; the header and topology show the new leader and higher term.
2. Writes still work: create another block against any surviving node —
   followers forward to the new leader (`ADMIN_INTERNAL_PEER_ADDRS`).
3. Gateways keep polling; `simgw-1` (pointed at the dead node) logs
   control-plane errors but keeps enforcing last-known-good config
   (FR-020) while `simgw-2/3` keep receiving updates. Because fleet state
   is leader-owned and in-memory, the new leader starts with an empty
   fleet: gateways answer `404` there for one poll, then **re-register
   their idempotent identity and catch up** on the revision they missed
   (`TestSyncReregistersAfterLeaderFailover`).

Bring it back:

```sh
docker compose up -d node1
```

The restarted node records `state_restored` in its event log (WAL + hard
state recovery) and rejoins as a follower.

## 6. Edge case: gateway offline / failure modes

```sh
docker compose stop simgw-3
```

Within the heartbeat timeout `simgw-3` flips to `offline` (event
`heartbeat_lost`) in the Gateways tab; rollouts to it stop succeeding.
Restart with `docker compose start simgw-3` — it re-registers (idempotent),
pulls the current revision, reports acceptance, and the enforcement probes
re-verify it.

For protocol-level failures, run a gateway with `SIMGW_FAIL_MODE`:

| `SIMGW_FAIL_MODE` | Effect |
|---|---|
| `reject` | gateway rejects the next config apply → rollout `rejected` |
| `timeout` | gateway never reports → rollout stalls at `sending` |
| `offline` | gateway stops heartbeating → detected `offline` |

Example:

```sh
docker compose run --rm -e SIMGW_FAIL_MODE=reject -e SIMGW_ID=simgw-x \
  -e SIMGW_NAME=edge-x -e SIMGW_LISTEN=:18080 -e SIMGW_ADMIN=:19901 \
  -e SIMGW_CONTROL_PLANE=http://node1:8080 -e SIMGW_PROXY_URL=http://simgw-x:18080 \
  simgw-1
```

The **dev-only** Gateways tab controls (`reject-next`, `offline`, …) drive
the *mock* adapter, which never advances verification — useful for showing
the difference between fake acceptance and probe-verified enforcement.

## 7. What each demo moment maps to

| Demo moment | Where to show it |
|---|---|
| Who is the leader / which term | UI header, Cluster Topology |
| Raft-conscious logs (elections, term bumps, snapshots) | Raft Consensus Events card, `GET /v1/cluster/events` |
| 3 nodes communicating | Topology reachability, follower→leader forwarding, gateways polling followers |
| Block / add an IP | Create IP Block card, Policies tab, audit entries |
| Gateway received the config | Gateways table (desired vs reported revision), gateway detail events |
| Is the policy achieved? | Policy Enforcement Status card, gateway detail enforcement table — probe `verified` only |
| Leader failover | kill a node → new election events on surviving UIs |
| Gateway failure | stop a container → `offline`; `SIMGW_FAIL_MODE` → rejected/stalled rollouts |

## 8. Tests behind the demo

```sh
go test ./... -count=1 -short          # CI suite
go test ./internal/consensus/ -run 'TestClusterRaftEvents|TestEventLog' -v
go test ./internal/server/ -run 'TestClusterEventsEndpoint|TestThreeNodeUI|TestUIRaftEvents' -v
go test ./internal/server/ -run 'TestVerifyFleetOnceProbeLifecycle' -v
go test ./internal/fleet/ -run 'TestEnforcementFor|TestVerificationOnlyFromProbes' -v
go test ./cmd/simgw/ -run 'TestSyncReregistersAfterLeaderFailover' -v
```

- `TestClusterRaftEvents` — real 3-node election records
  `leader_elected` / `leader_observed`.
- `TestClusterEventsEndpoint` — every node serves its own event view with a
  valid envelope.
- `TestThreeNodeUIAndDiagnostics` — all three nodes serve the dashboard and
  replicate a committed policy.
- `TestUIRaftEventsAndEnforcement` — dashboard contains the Raft log and
  enforcement surfaces.
- `TestVerifyFleetOnceProbeLifecycle` — probe outcomes are the only thing
  that advances verification.
- `TestEnforcementFor` — per-gateway enforcement view (`not_tested` until a
  probe speaks; stale revisions never count).
- `TestSyncReregistersAfterLeaderFailover` — gateways self-heal after a
  leader failover: `404` on the empty fleet clears the sticky registration
  flag, the next sync re-registers, and the missed revision is caught up.

## 9. Teardown

```sh
docker compose down -v
```
