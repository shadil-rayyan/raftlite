ADR-0004: Durable Acknowledgement and Linearizable Read Semantics
Status: Accepted
Date: 2026-11-02
Decision owners: RaftLite maintainers
Scope: RaftLite control plane (consensus, storage, admin API)

1. Context

RaftLite replicates security policies that gateways enforce. Two classes of
defects in the pre-Milestone-1 implementation threatened the control plane's
trustworthiness:

Durability gaps. The follower's AppendEntries path mutated in-memory state
before (or without) a successful durable write, so a storage failure could
produce an acknowledgement for an entry that was never persisted. Votes could
be granted before term and vote were fsynced, allowing a double vote after a
crash-restart. Snapshot compaction could discard WAL entries before the
snapshot file itself was durable, and an interrupted WAL compaction could
leave a hole that the restore path would accept.

Read staleness. Policy reads (list, get, decision lookup) were served from
any node's local FSM with no freshness guarantee. A follower several entries
behind could answer a decision lookup with pre-block state — a security
control gap presented as an ordinary 200 response. There was no way to ask
for a linearizable read, and no documented way to accept a stale one.

Both classes are silent: neither produces an error the operator can see
without dedicated tests and observability.

2. Decision

2.1 Durable-first acknowledgement (storage before memory)

AppendEntries: perform the storage operation first; mutate the in-memory log
only after it succeeds. On any storage failure the follower responds
Success=false without advancing commitIndex and without applying, and leaves
ConflictIndex/ConflictTerm unset so the leader treats the failure as
transient — no nextIndex rewind, retry on the next heartbeat from the same
position (disk and memory still agree).

Votes: persist term and self-vote before any RequestVote is sent; a
persistence failure refuses the vote or aborts the campaign. A term bump
observed from a peer is persisted even when no vote is granted.

Snapshots: write and fsync the snapshot file before any memory or WAL
compaction. Installation on the follower is ordered write file → truncate →
FSM restore (all-or-nothing) → adopt indexes → persist hard state; any
failure means no acknowledgement and no mutation.

WAL compaction: CompactPrefix writes a new base file via temp → fsync →
rename over the old base, then removes stale rotated segments. A crash at
any point leaves either the old full log or the new base plus stale
rotated segments, which are pruned at open. Restore drops holes and
duplicates (contiguity guard); append rolls back partial frames when a
write or sync fails.

Storage health (storage_healthy / storage_error on /v1/cluster, events
storage_error/storage_recovered, metric raftlite_storage_errors_total) is
observability only. Every safety-critical persist already checks its own
result; the flag exists so operators can see why a node refuses votes or
acknowledgements, and it never gates behaviour on its own.

2.2 Linearizable reads with an explicit stale opt-in

Consensus provides ReadIndex: the leader records its commitIndex, sends a
heartbeat round whose send goroutines carry the round's generation — so
only responses to requests sent after the recording can confirm it — and
requires quorum acknowledgement at the recorded term before the read is
served once applied state covers the barrier. A stale in-flight ack from
before the recording must not count: the sending node may have accepted a
higher-term leader's commits afterwards, which would break the quorum
intersection argument. Rounds fail immediately when the node steps down or
its term changes.

API behaviour for GET /v1/policies, GET /v1/policies/{id} and
GET /v1/blocked/{ip}:

- Default: linearizable. On the leader the read is confirmed via
  ReadIndex. A follower forwards the request to the leader once (loop-safe
  via X-Raftlite-Forwarded), falls back to a 307 pointing at the leader's
  admin address when it cannot proxy, and otherwise responds 503.
- ?consistency=stale: serves the node's local FSM without a leader round
  trip — the documented, caller-chosen eventual view.
- No reachable leader, or leadership that cannot be confirmed: 503. A
  linearizable read is never silently downgraded to a stale one.

Node-local views are unchanged and documented as such: /v1/cluster/events
serves the local node's event log, and /v1/cluster, /v1/node and
/v1/diagnostics report the serving node's own state.

3. Requirements Addressed

Leader-only writes with follower forwarding (FR-007). Recovery that never
infers commitment from log length (FR-022, FR-023). Observability of storage
and node health (FR-030). Auditability of administrative actions (FR-031).
Validation before entering the log (FR-015). The read-consistency behaviour
described above is specified in api/openapi.yaml and docs/api/api-contract.md.

4. Alternatives Considered

4.1 Serve local reads on every node (the previous behaviour)

Advantages: no extra round trip; simplest possible code.

Disadvantages: a lagging follower answers enforcement-relevant lookups with
stale state, and the caller cannot tell. For a security policy plane the
failure is silent and security-relevant.

Assessment: rejected as the default. It remains available only through the
explicit ?consistency=stale opt-in, where the caller accepts the lag.

4.2 Leader lease / clock-based read leases

Advantages: reads without a quorum round trip per request.

Disadvantages: safety depends on bounded clock drift between hosts;
RaftLite targets commodity deployments with no clock discipline.

Assessment: rejected; the heartbeat-round ReadIndex confirmation needs no
clock assumptions.

4.3 Confirm leadership by committing a no-op entry per read

Advantages: reuses the ordinary commit path; correlation is exact (the log
index identifies the round).

Disadvantages: a log write, fsync, and quorum exchange per read — as
expensive as a write, defeating the purpose of a read barrier.

Assessment: rejected; the heartbeat round reuses the existing heartbeat
channel at negligible cost.

4.4 Acknowledge before persisting (pipelined follower acks)

Advantages: lower replication latency.

Disadvantages: a crash between acknowledgement and fsync loses an entry the
leader counted as replicated; after restart the follower's log is shorter
than what was acked, breaking the durability contract.

Assessment: rejected; correctness over latency. Group commit (batching
fsyncs to recover the latency) is explicitly deferred.

4.5 Heartbeat confirmation without request/response correlation

Advantages: no plumbing changes to the send path.

Disadvantages: an ack to a pre-capture heartbeat may arrive after the read
was recorded; its sender may, in between, have accepted a higher-term
leader's commits — the quorum intersection argument no longer holds.

Assessment: rejected; the generation tag on send goroutines is the minimal
correlation that makes the proof work without a protocol change.

5. Verification (implemented and tested)

Durability: internal/consensus/durability_test.go (8 tests — transient
NACKs, vote refusal on storage failure, snapshot-write-blocks-compaction,
install-snapshot nack, hintless NACK handling, uncommitted-tail recovery,
contiguity guard) and internal/transport/wal_durability_test.go (6 tests —
append/sync rollback, compaction round-trip, interrupted-compaction prune,
empty compaction).

Crash and partition behaviour: internal/consensus/partition_test.go (2 —
minority leader cannot confirm commits, majority failover preserves
committed entries) and tests/multiproc_test.go (3 real processes: hard
leader kill, re-election, restart and catch-up; gated on INTEGRATION=1).

Read consistency: internal/consensus/readindex_test.go (2 — quorum
confirmation succeeds on a healthy leader, fails while isolated and for a
deposed leader) and internal/server/readgate_test.go (7 — leader confirms
via ReadIndex, confirmation failure returns 503, stale opt-in skips
confirmation, follower forwards instead of serving its lagging FSM,
no-leader and unreachable-leader 503s, redirect fallback).

The full suite passes under -race; the deterministic-simulation harness and
the live multi-node smoke (13 checks) pass.

6. Consequences

Followers pay a forwarding hop for linearizable reads; deployments that
want local reads must opt in per request. Reads fail with 503 during
quorum loss instead of degrading silently — callers must treat 503 as
retryable. Storage failures surface as refusals (votes/acks) plus the
health flag, so an unhealthy node is visible rather than quietly corrupting
state. Group commit, read leases, and any cross-request read batching are
explicitly out of scope for this decision.
