# Gateway Simulation & Rollout Manager

RaftLite can publish policy revisions to a **fleet of gateways** and track
each delivery independently. Two gateway kinds exist:

- **mock** — the in-process simulator: fakes delivery delays, rejects, and
  timeouts. Fast and hermetic; the default for tests.
- **envoy-sim** (`cmd/simgw`) — a **standalone simulated Envoy**: a real
  reverse-proxy process that pulls configuration, enforces it on live
  traffic, and whose enforcement the control plane actively probes.

**Simulated acceptance is never enforcement verification** — a gateway
accepting a revision says nothing about real network behavior. For
envoy-sim gateways the control plane verifies enforcement separately by
probing the gateway's data path (see "Enforcement verification" below);
the mock adapter never advances verification at all.

## Architecture

```
policy commit (Raft) ──► API (leader) ──► fleet.Manager ──► Adapter interface
                                             │                  │
                                             │            MockAdapter (simulates
                                             │            delays, rejects, timeouts)
                                             ▼
                                   rollout + gateway state + event history

envoy-sim pull loop (per gateway process):
  cmd/simgw ──GET /v1/fleet/gateways/{id}/config──► fleet.Manager (leader)
  cmd/simgw ──POST .../report (accepted|rejected)─► fleet.Manager (leader)
  control plane ──probe through gateway data path──► 429/200 ⇒ verification
```

- `internal/fleet` — gateway registry, rollout engine, event model,
  pull-config serving, report ingestion, verification records. All
  policy-management business logic lives here; adapters stay dumb.
- `Adapter` is the integration boundary. `MockAdapter` implements it
  today; a future real-Envoy adapter (xDS via
  `envoyproxy/go-control-plane`) implements the same interface without
  touching the manager.
- Fleet state is **in-memory on the leader**. Followers forward every
  `/v1/fleet/*` request to the leader. State is lost on leader restart — a
  deliberate simplification for the simulation era; replicating fleet state
  through Raft is the upgrade path. envoy-sim gateways re-register on boot
  **and whenever a config poll answers 404** (the new leader's empty fleet
  after a failover), so a leader restart re-creates their registry entries
  idempotently and the gateway catches up on missed revisions.

## Data model

`Gateway` keeps desired and reported state **separate**:

| Field | Meaning |
|---|---|
| `id`, `name`, `description`, `adapter_type` | stable identity (`mock` or `envoy-sim`) |
| `proxy_url` | envoy-sim only: the data-path base URL the control plane probes |
| `connection_status` | `online` / `offline` / `reconnecting` |
| `desired_revision` | revision RaftLite wants the gateway to use |
| `reported_accepted_revision` | revision the gateway reported accepting |
| `last_rejected_revision`, `last_error` | most recent rejection details |
| `last_heartbeat_at`, `last_update_at` | liveness and config-event timestamps |
| `verification` | `not_tested` / `verified` / `failed` — enforcement verification, **only** advanced by probes |
| `created_at`, `updated_at` | identity/history timestamps |

Gateways retain identity, reported revision, and history across
offline/reconnect cycles. A gateway is never marked up to date merely
because it reconnected — reconciliation is scheduled and its outcome is
reported like any other delivery.

Liveness differs by kind: mock gateways get simulated heartbeats (they
have no process behind them). envoy-sim gateways get **no fabricated
liveness** — every config poll and report proves the process is alive, and
silence beyond the heartbeat window (default 15s) classifies the gateway
offline with a `heartbeat_lost` event.

`ConfigSnapshot` is the gateway-independent configuration at a revision
(policy ID, name, type, target, action, status). It is deliberately free of
Envoy/xDS resource formats; a future real-Envoy adapter translates it into
xDS resources itself.

## The simulated Envoy gateway (`cmd/simgw`)

A standalone binary that mocks the entire standalone-Envoy deployment:

- **Data path** (`SIMGW_LISTEN`, default `:18080`): real reverse proxy in
  front of `SIMGW_UPSTREAM`. The blocklist middleware evaluates the
  **applied snapshot locally** — no control-plane round trip per request
  (FR-019). Client addresses come from the TCP peer only; forwarded
  headers are honored solely from explicitly trusted probe sources
  (`SIMGW_TRUST_PROBE_SRC`, SEC-007).
- **Admin interface** (`SIMGW_ADMIN`, default `:19901`, Envoy-style):
  `GET /status` (applied revision, policy count, fail mode, last sync
  error, uptime), `GET /policies`, `GET /healthz`.
- **Pull sync** (xDS-like): polls `GET /v1/fleet/gateways/{id}/config`,
  applies a new revision atomically after an optional simulated apply
  delay (`SIMGW_APPLY_DELAY_MS`), then reports `accepted`/`rejected` via
  `POST /v1/fleet/gateways/{id}/report`. A poll never pre-marks anything.
- **Fail modes** (`SIMGW_FAIL_MODE`, simulation controls):
  - `reject` — do not apply; report a structured rejection. A rejected
    configuration is never running.
  - `timeout` — apply locally but never report; the control plane's
    delivery timer classifies the rollout result as timeout (not a
    rejection).
  - `offline` — stop polling entirely; keep enforcing last-known-good
    (FR-020 — never a silent allow-all).
- **Control-plane outage** (any mode): the last-known-good snapshot stays
  loaded and blocking; the outage is surfaced in `/status`.

## Enforcement verification ("is the policy achieved?")

Acceptance and enforcement are separate questions. After the leader sees
an envoy-sim gateway report `accepted` for the current revision, the
verifier loop (`internal/server/verify.go`, every 5s) probes the gateway's
**data path**:

1. **Negative control first** — a TEST-NET address no policy covers must
   get 200 through the proxy. If it does not, the gateway is not proxying
   as configured; the gateway is marked `failed` with `PROBE_FAILED` and
   no per-policy conclusion is drawn.
2. **Per policy** — a request carrying the policy's target as client
   address (via `X-Forwarded-For`, honored only because the gateway trusts
   the probe source as a proxy hop) must get **429**. 429 ⇒ the policy is
   `verified` (achieved) on that gateway; anything else ⇒ `failed` with
   `ENFORCEMENT_MISSING`.

Probe outcomes are the **only** producer of verification state: they set
the per-policy record, roll up into the gateway-level `verification`
field, and emit `verification` events. Publishing a new revision resets
verification to `not_tested` — the question changed. Mock gateways are
never probed and stay `not_tested`.

## Rollout lifecycle

Per-gateway result states: `pending → queued → sending → accepted | rejected
| timeout | offline | superseded`.

1. An operator creates/revokes a policy; Raft commits it as an immutable
   revision.
2. The API (leader) calls `fleet.Manager.OnRevision(rev, policyID, snapshot)`.
3. The manager creates a rollout, advances `desired_revision` for every
   target gateway (including offline ones), and schedules async delivery for
   online gateways. Mock gateways get the adapter's simulated delivery;
   envoy-sim gateways wait for their next poll + report (the delivery timer
   classifies silence as timeout).
4. Results, gateway state, and the event history are updated; the
   verifier loop then probes enforcement for envoy-sim gateways; the
   dashboard reflects it on the next poll.

Guarantees:

- **Per-gateway independence** — one gateway's rejection never marks the
  fleet rollout rejected.
- **Stale-response guard** — a result for a revision older than the
  gateway's current `desired_revision` is recorded as `superseded` and never
  overwrites newer state (tested out-of-order).
- **Timeout ≠ rejection** — a delivery that produces no result within the
  delivery window (default 5s) is classified `timeout` and sets no error on
  the gateway. A late acceptance after a timeout still advances the
  gateway's reported revision.
- **Publishing never pre-marks acceptance** — `reported_accepted_revision`
  only moves when a result arrives.
- **Acceptance never pre-marks verification** — `verification` only moves
  on probe outcomes (tested).

## API (versioned, bearer-authenticated like the rest of `/v1`)

| Endpoint | Purpose |
|---|---|
| `GET /v1/fleet/gateways` | list gateways + summary (total/online/offline/needs_attention) |
| `GET /v1/fleet/gateways/{id}` | gateway detail + recent events |
| `GET /v1/fleet/gateways/{id}/events` | one gateway's event history |
| `GET /v1/fleet/gateways/{id}/config` | envoy-sim pull: snapshot at the current revision (a poll is a heartbeat) |
| `POST /v1/fleet/gateways/{id}/report` | envoy-sim ACK/NACK: `{revision, status, error?}` |
| `GET /v1/fleet/policies/{id}/status` | **is the policy achieved?** — per-gateway applied revision + probe result, with a summary (total/applied/verified/failed/not_tested/offline) |
| `GET /v1/fleet/rollouts` | rollouts, newest first |
| `GET /v1/fleet/rollouts/{id}` | one rollout with per-gateway results |
| `GET /v1/fleet/events` | recent fleet-wide events |
| `POST /v1/fleet/gateways` | create a gateway: mock (`name`, `description`, `delay_ms`) or envoy-sim (`id`, `name`, `adapter_type: "envoy-sim"`, `proxy_url` — idempotent on re-registration) |
| `POST /v1/fleet/reconcile` | manually re-publish the current revision to gateways behind it |

Rollouts are also triggered **automatically** on every committed policy
create/revoke on the leader.

## Dev-only simulator controls

Registered **only** when the node runs with `RAFTLITE_DEV_UI=1`, and always
bearer-authenticated. They manipulate **mock** gateways only; envoy-sim
gateways have their own process-level knobs (`SIMGW_*` env). Never enable
the dev flag in production.

| Endpoint | Effect |
|---|---|
| `POST /dev/mock-gateways` | create (`name`, `delay_ms`) |
| `DELETE /dev/mock-gateways/{id}` | delete (409 while a delivery is in flight) |
| `POST /dev/mock-gateways/{id}/scenarios/offline` | mark offline (retains state) |
| `POST /dev/mock-gateways/{id}/scenarios/online` | reconnect + reconcile if behind |
| `POST /dev/mock-gateways/{id}/scenarios/set-delay` | `{"delay_ms": N}` |
| `POST /dev/mock-gateways/{id}/scenarios/reject-next` | structured rejection on next delivery |
| `POST /dev/mock-gateways/{id}/scenarios/accept-next` | force-accept next delivery |
| `POST /dev/mock-gateways/{id}/scenarios/timeout-next` | never respond; manager classifies timeout |
| `POST /dev/mock-gateways/{id}/scenarios/reset` | clear one-shot flags |

Rejection payload shape (from the simulator):

```json
{
  "gateway_id": "gw-003",
  "revision": 12,
  "status": "rejected",
  "error": {"code": "INVALID_CONFIGURATION", "message": "The simulated gateway rejected this configuration."}
}
```

## Event history

Every notable occurrence is appended to a bounded in-memory ring (1000
events) — history is preserved, not just latest state: `gateway_created`,
`gateway_offline`, `gateway_reconnected`, `policy_published`,
`rollout_created`, `delivery_scheduled`, `config_accepted`,
`config_rejected`, `delivery_timed_out`, `rollout_superseded`,
`gateway_deleted`, `verification`. Events carry ID, timestamp,
gateway/policy/revision/rollout references, result, structured error, and
metadata — generic enough for real Envoy instances later.

## Dashboard

The **Gateways (Sim)** tab shows fleet cards, the current rollout with a
progress bar and per-gateway results, a clickable gateway table (adapter
kind, desired vs reported revisions, applied/stale/rejected/offline
badges, verification badge), a **Policy Enforcement Status** card (per
active policy, per gateway: achieved / not achieved / not tested / offline
from probe results), a gateway detail view with recent events, the fleet
event feed, and — in dev mode — the simulator controls.

## docker-compose

The compose file runs a full loop: 3 raftlite nodes, an `echo` upstream,
and one `simgw` gateway (`:18080` data path, `:19901` admin) that
registers with node1, pulls revisions, and is probe-verified. Like the rest
of the compose file this is Linux-oriented. The gateway trusts the docker
bridge range as a probe hop — simulation only, never a production trust
configuration.

```sh
docker-compose up --build
curl -s http://localhost:19901/status | jq .          # gateway view
curl -s http://localhost:8081/v1/fleet/gateways | jq . # control-plane view
```

## Future real Envoy adapter (not implemented)

The boundary is ready for it; when it lands it should:

- use `envoyproxy/go-control-plane` (never hand-rolled xDS),
- map Envoy node identities to RaftLite gateway IDs,
- translate `ConfigSnapshot` into the appropriate xDS resources (one policy
  revision may map to multiple resources — one ACK does not prove all
  resources were accepted),
- track resource versions/nonces, process ACKs and NACKs, preserve
  Envoy-specific diagnostics,
- handle disconnect/reconnect and reconcile lagging gateways,
- report configuration acceptance **separately** from enforcement
  verification (`verification` stays its own field).

`cmd/simgw` is the behavioral reference for that adapter: pull, apply,
report, enforce locally, be probe-verifiable. The mock adapter remains
available for automated tests.
