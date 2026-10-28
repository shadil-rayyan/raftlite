# Gateway Simulation & Rollout Manager

RaftLite can publish policy revisions to a **fleet of simulated gateways**
and track each delivery independently. This is a simulation harness: it
exercises the async rollout lifecycle, per-gateway outcomes, and dashboard
reporting. **Simulated acceptance is not enforcement verification** — a mock
gateway accepting a revision says nothing about real network behavior.

## Architecture

```
policy commit (Raft) ──► API (leader) ──► fleet.Manager ──► Adapter interface
                                             │                  │
                                             │            MockAdapter (simulates
                                             │            delays, rejects, timeouts)
                                             ▼
                                   rollout + gateway state + event history
```

- `internal/fleet` — gateway registry, rollout engine, event model. All
  policy-management business logic lives here; adapters stay dumb.
- `Adapter` is the integration boundary. `MockAdapter` implements it today;
  a future Envoy adapter (xDS via `envoyproxy/go-control-plane`) implements
  the same interface without touching the manager.
- Fleet state is **in-memory on the leader**. Followers forward every
  `/v1/fleet/*` request to the leader. State is lost on leader restart — a
  deliberate simplification for the simulation era; replicating fleet state
  through Raft is the upgrade path.

## Data model

`Gateway` keeps desired and reported state **separate**:

| Field | Meaning |
|---|---|
| `id`, `name`, `description`, `adapter_type` | stable identity (`adapter_type` = `mock`) |
| `connection_status` | `online` / `offline` / `reconnecting` |
| `desired_revision` | revision RaftLite wants the gateway to use |
| `reported_accepted_revision` | revision the gateway reported accepting |
| `last_rejected_revision`, `last_error` | most recent rejection details |
| `last_heartbeat_at`, `last_update_at` | liveness and config-event timestamps |
| `verification` | `not_tested` / `verified` / `failed` / `unknown` — enforcement verification, **never** advanced by the mock adapter |
| `created_at`, `updated_at` | identity/history timestamps |

Gateways retain identity, reported revision, and history across
offline/reconnect cycles. A gateway is never marked up to date merely
because it reconnected — reconciliation is scheduled and its outcome is
reported like any other delivery.

`ConfigSnapshot` is the gateway-independent configuration at a revision
(policy ID, name, type, target, action, status). It is deliberately free of
Envoy/xDS resource formats; the future Envoy adapter translates it into xDS
resources itself.

## Rollout lifecycle

Per-gateway result states: `pending → queued → sending → accepted | rejected
| timeout | offline | superseded`.

1. An operator creates/revokes a policy; Raft commits it as an immutable
   revision.
2. The API (leader) calls `fleet.Manager.OnRevision(rev, policyID, snapshot)`.
3. The manager creates a rollout, advances `desired_revision` for every
   target gateway (including offline ones), and schedules async delivery for
   online gateways.
4. Each gateway's adapter simulates delivery after its configured delay and
   reports a normalized `Result`.
5. The manager updates gateway state, rollout results, and the event
   history; the dashboard reflects it on the next poll.

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

## API (versioned, bearer-authenticated like the rest of `/v1`)

| Endpoint | Purpose |
|---|---|
| `GET /v1/fleet/gateways` | list gateways + summary (total/online/offline/needs_attention) |
| `GET /v1/fleet/gateways/{id}` | gateway detail + recent events |
| `GET /v1/fleet/gateways/{id}/events` | one gateway's event history |
| `GET /v1/fleet/rollouts` | rollouts, newest first |
| `GET /v1/fleet/rollouts/{id}` | one rollout with per-gateway results |
| `GET /v1/fleet/events` | recent fleet-wide events |
| `POST /v1/fleet/gateways` | create a simulated gateway (`name`, `description`, `delay_ms`) |
| `POST /v1/fleet/reconcile` | manually re-publish the current revision to gateways behind it |

Rollouts are also triggered **automatically** on every committed policy
create/revoke on the leader.

## Dev-only simulator controls

Registered **only** when the node runs with `RAFTLITE_DEV_UI=1`, and always
bearer-authenticated. They manipulate simulated gateways only. Never enable
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
`gateway_deleted`. Events carry ID, timestamp, gateway/policy/revision/
rollout references, result, structured error, and metadata — generic enough
for real Envoy instances later.

## Dashboard

The **Gateways (Sim)** tab shows fleet cards, the current rollout with a
progress bar and per-gateway results, a clickable gateway table (desired vs
reported revisions, applied/stale/rejected/offline badges, verification
state), a gateway detail view with recent events, the fleet event feed, and
— in dev mode — the simulator controls. Everything is labeled **SIMULATED**.

## Future Envoy adapter (not implemented)

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

The mock adapter remains available for automated tests after that.
