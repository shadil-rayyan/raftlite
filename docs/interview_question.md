# RaftLite — Interview Questions

Every question someone can reasonably ask about this project, grouped by topic,
with answers grounded in the **actual code** (not the aspirational docs). Where
the implementation is incomplete, the answer says so — that honesty is itself an
interview asset.

Legend: ✅ implemented and tested · 🟡 implemented but partial/stub · ⬜ planned, not built.

---

## 1. Product and problem

**Q1. What is RaftLite?**
A self-hosted security-policy control plane for API gateway fleets. It keeps one
authoritative set of block policies (IP, CIDR, temporary bans) replicated across
a small Raft cluster so every gateway enforces the same rules. Raft is the
mechanism; the product outcome is "one policy, consistently enforced everywhere,
with explicit behavior during failures" (PRD §21). ✅

**Q2. What problem does it solve?**
Three gateways behind a load balancer with independent blocklists diverge: a
blocked attacker still reaches the gateways that missed the update (PRD §2.1).
RaftLite makes policy state authoritative and replicated; gateways evaluate
against locally cached committed state. ✅

**Q3. Who is the target user?**
Primary operator: platform/SRE engineers running self-hosted gateways. Primary
policy user: security engineers who create/revoke/audit blocks (PRD §4–5).
Explicit non-targets: enterprises wanting a commercial WAF, DDoS mitigation, or
SIEM (PRD §4.3).

**Q4. What is RaftLite *not*?**
Not a WAF, not a database, not a rate limiter, not a general consensus
platform, not multi-region, not an identity provider (PRD §9).

**Q5. Why not just use etcd/Consul/Redis?**
Honest answer: for many teams, those *are* the better choice (PRD §7.4). The
differentiation is a policy-specific workflow — policy model, gateway
distribution, audit semantics, and enforcement behavior integrated in one small
binary — rather than assembling them yourself. RaftLite should not be chosen
"because it uses Raft" (PRD §7.6).

**Q6. What's the difference between committed and propagated?**
Committed = the Raft log entry reached quorum and is authoritative. Applied = the
state machine has executed it. Propagated = a gateway received it. Enforced = the
gateway is filtering traffic with it. A Raft commit proves nothing about
propagation — that's a separate protocol with its own status reporting (PRD §13.1,
SRS FR-018/FR-021). ⬜ (revision tracking is designed, gateway sync not built)

**Q7. What does the MVP include vs defer?**
Include: 3-node cluster, IP/CIDR/temporary policies, admin API, durable
recovery, audit, metrics, failure tests (PRD §10.1). Defer: rate limiting,
anomaly-driven enforcement, approval workflows, RBAC, Kubernetes operator,
dry-run/rollback (PRD §10.2).

---

## 8. Raft consensus internals

**Q8. How does leader election work here?**
Standard Raft: randomized election timeout → candidate increments term, votes
for itself, requests votes from peers; majority → leader (election.go). A vote
is granted only if the candidate's log is at least as up-to-date (last
term/index comparison) — this guarantees a new leader already has all committed
entries. 🟡 (logic present; before the production pass the RPC send-path was a
stub, which is why the cluster couldn't actually elect anyone — see Q21)

**Q9. What's the voting rule, exactly?**
One vote per term, persisted before responding (§5.4.2 durability). Candidate
wins with votes > N/2. A follower resets its election timer only on valid
leader contact or granting a vote. 🟡

**Q10. How does log replication work?**
Leader sends AppendEntries with prevLogIndex/prevLogTerm for consistency
checking; a follower rejects if its log doesn't contain that exact
index/term pair, returning conflict hints so the leader can back up (the
conflict-index optimization from the Raft paper §5.3) — implemented in
`HandleAppendEntries`/`HandleAppendEntriesResponse` with nextIndex backtracking. 🟡

**Q11. When is an entry committed?**
When the leader's matchIndex shows a majority of nodes have it *and* the entry
is from the leader's current term (the Figure-8 rule — `updateCommitIndex`
skips entries from older terms). Counting older-term entries alone can elect a
leader whose tail isn't actually committed. 🟡

**Q12. What happens when a node restarts with an uncommitted tail?**
A new leader overwrites it via the conflict/backtrack path: AppendEntries with
mismatched term truncates the follower's log and replaces it (replication.go
truncate branch). This is why blindly treating the WAL tail as committed — the
pre-production behavior — was unsafe. 🟡

**Q13. Why is persisting currentTerm and votedFor critical?**
Without it, a node that crashes and restarts can grant a *second* vote in the
same term, enabling two leaders in one term — a safety violation (Raft §5.4.2).
The production design adds a hard-state block persisted with fsync before any
vote response. 🟡 (added in the production pass; the prototype only persisted
log entries)

**Q14. What is the Figure-8 scenario and how do you prevent it?**
A leader from an old term can have a committed-looking tail that a later
leader overwrites, if commit counting ever counts old-term entries. Prevention:
only commit by counting entries from the current term; old-term entries commit
indirectly once a current-term entry above them commits. `updateCommitIndex`
implements exactly this. 🟡

**Q15. Explain snapshots and log compaction.**
Periodically the state machine serializes its full state with the last included
index/term; the log prefix below that point is discarded; a lagging follower
too far behind gets InstallSnapshot over RPC instead of log entries
(snapshot.go, proto InstallSnapshotRequest). 🟡 (design + RPC shape exist;
persistence was a stub — production pass makes it real)

**Q16. What happens when a follower is behind the compaction point?**
Leader sends InstallSnapshot; follower restores FSM state, replaces its log
with the snapshot's last-included index as the new base, and resumes normal
replication from there (FR-025). ⬜→🟡 in the production pass.

**Q17. How does membership change work?**
Single-server changes (per the Raft dissertation §4.1): add/remove exactly one
voter per committed config entry; a new node catches up before it counts toward
quorum. Simpler and safer than joint consensus; a two-node change can't be
safely committed without it. The prototype's joint-consensus code committed
config entries *without a quorum* — unsafe — so the production pass replaced it
with single-server changes. 🟡

**Q18. Why did you drop the prototype's joint consensus?**
`ProposeJointConfig` set `commitIndex` directly instead of waiting for old AND
new majority quorums, and it applied both config entries synchronously. That's
unsafe and untested. Single-server changes cover the actual need (scaling one
node at a time) with a fraction of the risk. (joint.go → membership.go)

**Q19. What's your quorum math?**
3 voters: tolerate 1 failure. 5: tolerate 2. Writes need majority of *voters*;
minority partitions must reject writes rather than commit (FR-007, NFR-004).
A 2-of-3 cluster that loses 2 nodes stops accepting writes by design.

**Q20. Does a client timeout mean the write failed?**
No. A timeout after the leader appended the entry may hide a successful
commit. The API distinguishes "rejected" from "unknown outcome", and supports
idempotency keys so clients can retry safely and recover the original result
(SRS §8, FR-016). 🟡 (in the production API design)

---

## 21. Codebase specifics

**Q21. The cluster couldn't actually run multi-node before — why?**
Honest answer: the transport was stubbed. `startElection` built vote requests
but never sent them; `broadcastAppendEntries` did the same; the gRPC *server*
existed but no gRPC *client* was ever wired (marked with `ponytail:` comments).
Single-node worked because it elected itself. This is exactly the trap of
"tests pass but the system doesn't work" — unit tests called the `Handle*`
methods directly, bypassing the missing network. The production pass adds the
`Transport` interface + gRPC client and fixes election/replication to use it.

**Q22. Why hand-roll Raft instead of using hashicorp/raft?**
The repo predates that decision and exists partly as a learning artifact. For
production credibility the honest position is: a battle-tested library is the
default recommendation; the hand-rolled implementation is kept because the
project's purpose includes demonstrating consensus mechanics, and it's now
covered by DST + failure tests. Any use beyond that requires the full test
evidence in `docs/testing/`.

**Q23. What is the WAL format?**
Flat file: 24-byte header (magic "RLTE", last index, last term) + length-prefixed
records, each with CRC32-IEEE over the payload (wal.go). Entries are fsynced on
append; a torn tail record is detected by CRC/length and discarded on scan.
🟡→ the production pass replaces whole-file rewrite truncation with segmented
WAL (O(1) truncation, crash-safe tail drop).

**Q24. Why is whole-file rewrite on truncation a problem?**
`Truncate` re-serialized every entry — O(n) per conflict resolution, and a crash
mid-rewrite could lose the whole log. Segmented WAL confines rewrites to the
last segment and makes truncation a metadata/boundary operation.

**Q25. What does the FSM apply?**
Prototype: bare `map[string]bool` with `"add:1.2.3.4"` / `"del:..."` string
commands. Production: structured commands — create-ip-block, create-cidr-block,
create-temp-block, revoke — with stable policy IDs, CIDR matching, deterministic
precedence, and committed-timestamp expiration (policy model per SRS §7).

**Q26. How is deterministic application guaranteed?**
Same initial state + same committed sequence ⇒ same final state (FR-004). That
requires: no map-iteration-order decisions (SRS §7.2), expiration evaluated
against timestamps *committed in the log*, never local timers mutating state
(NFR-015), and no wall-clock reads inside Apply.

**Q27. What is the DST harness?**
Deterministic Simulation Testing (systemd-style): all randomness comes from one
seed; MockNetwork injects drops/partitions, MockClock injects drift, MockStorage
injects stalls/failures (dst.go). Same seed ⇒ same interleaving, so a found
failure is exactly reproducible. `TestDSTDeterminism` and
`TestDSTDeterministicElection` run it; CI runs it as a separate job.

**Q28. What did DST find / what's it good for here?**
It's the cheapest way to exercise election/replication races without flaky
real-time sleeps: crashes every N steps, clock drift, packet drops with one
reproducible seed. The production pass extends it to drive the full node over
the transport interface, plus a linearizability check on the propose/apply
history.

**Q29. Why are there three languages?**
Go: the control plane (consensus, API, storage) — concurrency + one static
binary. Rust: the telemetry drain, where zero-GC, lock-free ingestion matters.
Python: anomaly detection, where the ML ecosystem matters. Each language earns
its place on the critical path it sits on; the Rust/Python parts are explicitly
best-effort side pipelines, not in the consensus path (README §7).

**Q30. Are the Rust and Python components real?**
Rust: ring buffer + Arrow batcher code exists; Go→Rust telemetry write is a
no-op stub. Python: reads shared-memory frames and logs "would block" — the
gRPC loopback was never generated. Both are marked `ponytail:`. Per the
product docs, anomaly detection must *propose* through the admin path, never
mutate policy state directly (FR-033).

**Q31. Why is `internal/gateway` dead code?**
The prototype's reverse proxy, blocklist middleware and sync.Pool allocators
were built in Phase 4 but never wired into `cmd/raftlite/main.go`, which uses
its own inline mux. The production pass wires the filter as in-process
enforcement middleware (optional, off by default) so the code has one owner.

**Q32. What's the XFF security bug?**
`extractIP` trusts the first `X-Forwarded-For` entry unconditionally, so any
client can spoof its IP and bypass an IP block. Production fix: a trusted-proxy
config — only honor forwarded headers from configured proxy addresses, else use
`RemoteAddr` (SEC-007).

**Q33. What are the "ponytail" comments?**
The repo's marker for known stubs/TODOs. `grep -rn ponytail` lists every place
the code admits it doesn't do what it claims. Good interview move: mention
that the codebase *labels its own gaps* rather than hiding them.

**Q34. How do nodes find each other?**
`PEERS` env var (comma-separated IDs) + per-node `RAFT_ADDR`; the gRPC client
pool dials each peer. Membership isn't inferred from requests (FR-001) —
anti-attack property.

---

## 35. API and policy model

**Q35. What does the admin API look like?**
Versioned per SRS §6.1: `POST /v1/policies/ip-blocks`, `GET /v1/policies`,
`GET /v1/policies/{id}`, `DELETE /v1/policies/{id}`, `GET /v1/blocked/{ip}`,
`GET /v1/cluster`, `GET /v1/leader`, `/healthz`, `/readyz`. Writes return the
true outcome: accepted/committed/unknown with raft index + revision.

**Q36. How do writes reach the leader?**
A follower receiving a write either proxies it to the current leader or returns
307 + leader address; it never fakes a commit (FR-007). The prototype returned
a generic 500 "not the leader".

**Q37. What's the policy schema?**
id, type (ip/cidr/temp), target, action, created_at, expires_at, reason,
created_by, version, status (SRS §7.1). Stable IDs so revocation and audit are
unambiguous.

**Q38. How does expiration work without clocks diverging?**
A temporary policy carries an absolute `expires_at` timestamp decided at
commit time. All nodes and gateways evaluate "expired?" by comparing that
committed timestamp against now — no local timer mutates authoritative state
(NFR-015, ADR-0003). Clock-skew assumptions are documented rather than hidden.

**Q39. What happens when a temporary policy expires mid-outage?**
State machine semantics are pure functions of the log + time comparison: any
node evaluating the same log at/after the timestamp agrees the policy is dead.
Gateways stop enforcing it at the same committed timestamp even if the
"expiry" was never separately propagated.

**Q40. How is precedence resolved?**
Deterministically: longest-prefix match for CIDR, exact match over prefix,
documented tie-breakers — never map iteration order (SRS §7.2). 🔧 specified,
implemented in the production policy engine.

**Q41. How are idempotency keys handled?**
Client supplies a key; the leader stores key→result in replicated state;
retries return the original outcome; reusing a key with a different payload is
a 409-class documented error (FR-016).

**Q42. What's in an audit record?**
Actor (from auth), operation, target, reason, request ID, timestamp, raft
index, and the *actual* outcome — rejected/uncertain never reads as committed
(FR-031, SEC-008). JSON lines, no secrets.

**Q43. How does /readyz differ from /healthz?**
Liveness = process is running. Readiness = this node can safely serve its
intended function (e.g. has recovered state, knows leadership status) — a node
must not report write-ready when it can't safely accept writes (FR-027/028).
The prototype returned `{"status":"ok"}` and the raw node state, conflating the
two.

**Q44. What validation happens before a write is proposed?**
netip parsing of IPs/CIDRs, duration bounds, required fields — rejected
requests never touch the log (FR-015), and errors are actionable without
leaking internals (SEC-006).

---

## 45. Storage and durability

**Q45. What must survive a restart?**
Hard state (term, votedFor) + log entries + latest snapshot = enough to
reconstruct committed policy state and rejoin safely (FR-022/023).

**Q46. What was wrong with prototype restart recovery?**
`restoreFromStorage` set `commitIndex = len(log)-1`, blindly applying an
uncommitted tail — an uncommitted write could silently become "policy". The
production recovery sets commitIndex from the snapshot's last-included index
and only advances through normal consensus rules (FR-023: "uncommitted commands
are not incorrectly treated as committed").

**Q47. How is crash-safety achieved in storage?**
Append-only segmented WAL, CRC per record, fsync policy explicit, atomic
snapshot writes (temp file → fsync → rename), recovery scans segments in order
and truncates a torn tail. Never rewrite the whole log.

**Q48. What about disk failures mid-write?**
Append errors surface immediately; a node that can't persist required state
must stop acting as though its guarantees are intact (architecture overview) —
it steps down rather than serving possibly-volatile commits.

**Q49. What is hard state and where does it live?**
{currentTerm, votedFor, commitIndex} — small, written with fsync *before* the
vote response leaves the process. Stored alongside the WAL (dedicated file) so
log and metadata recovery stay independent.

**Q50. Why CRC and not a hash?**
CRC32-IEEE catches torn/rotated bits at negligible cost per record; it's not
adversarial integrity (that's TLS/auth's job) — right tool, right threat.

---

## 51. Security

**Q51. How are admin requests authenticated?**
Bearer token (env/file-provisioned), constant-time comparison, on every
mutating endpoint; operational endpoints restricted per least privilege
(SEC-001/002/010). Real deployments should front this with TLS.

**Q52. How is inter-node traffic protected?**
mTLS via Go stdlib crypto/tls: per-node cert/key + shared CA, plaintext only
when explicitly configured for local dev (SEC-003/004). Membership isn't
inferred from untrusted input (FR-001).

**Q53. Secrets handling?**
No credentials in the repo or logs; tokens/certs via env or mounted files
(SEC-005). Audit records exclude secrets by construction.

**Q54. Trust boundary for client IPs?**
Trusted-proxy configuration decides whether XFF is honored; untrusted peers'
headers are discarded (SEC-007) — the fix for the spoofing bug in Q32.

**Q55. What if the control plane goes down — do gateways fail open?**
Only if explicitly configured. Default is last-known-good: keep enforcing the
last committed revision, with staleness visible to operators (FR-020,
SEC-009). Fail-open must be a deliberate, documented choice.

**Q56. Is this production-ready?**
The docs' own answer: no deployment is production-ready because TLS or auth
exists (PRD §14); it needs the full failure-test evidence, security review and
operational hardening. The honest claim is "prototype with a defined path to
hardening", backed by DST + integration + failure suites.

---

## 57. Testing and verification

**Q57. What's your test strategy?**
Four layers (docs/testing/strategy.md): unit (policy, WAL, FSM determinism),
consensus integration (3/5-node elections, replication, partitions, restarts,
snapshots, membership), DST with seeds, and end-to-end through the HTTP API.
CI runs build → vet → tests (-race) → security scans.

**Q58. How do you test a distributed system without flaky sleeps?**
Inject a clock. MockClock + seeded randomness make elections deterministic;
real-time sleeps only appear in the opt-in integration tests (`INTEGRATION=1`).

**Q59. How do you prove writes aren't lost?**
Crash-recovery tests: propose → kill before commit confirmation → restart →
assert committed entries reappear and uncommitted ones don't (FR-023), plus a
linearizability check over recorded histories in the extended suite.

**Q60. What does `-count=1` do in CI?**
Disables Go's test result caching — CI must always actually run tests.

**Q61. What's gated behind INTEGRATION=1?**
Tests needing a live cluster on localhost:8081 (Docker). Default runs stay
hermetic; the gate keeps `go test ./...` fast and environment-free.

**Q62. How would you test quorum loss?**
Partition 2 of 3 nodes away; assert the majority side commits and the minority
side's writes fail with "quorum unavailable" — never "committed" (NFR-004).
Then heal and assert convergence.

**Q63. How is a regression in Raft safety caught?**
Layered: property tests on WAL recovery, seeded DST interleavings, membership
change tests, and CI on every PR with the race detector. Any reported failure
reproduces from its seed.

---

## 64. Performance and operations

**Q64. What are the performance claims?**
None as guarantees. The PRD forbids advertising latency targets until
reproducible benchmarks support them (PRD §16). What's architecturally true:
gateway policy lookups are local — no consensus round trip per request (NFR-005).

**Q65. Where are the bottlenecks?**
Per-write: fsync on WAL append (the price of durability), then quorum RTT.
Per-lookup: in-memory policy matching — CIDR lookup cost is the thing to
optimize with a trie if policy counts grow.

**Q66. What metrics exist?**
Prometheus: elections, commits, propose/commit latency histograms, replication
lag per peer, applied revision, WAL/snapshot activity, policy counts, RPC
errors (FR-030). The prototype's `/metrics` returned hardcoded zeros — a good
example of "an endpoint existing ≠ being real".

**Q67. How do you diagnose "no leader"?**
`/v1/cluster` shows per-node state/term/reachability; metrics show election
count and last-leader age; troubleshooting doc walks quorum loss vs network vs
storage stalls (docs/operations/troubleshooting.md).

**Q68. How do you upgrade/recover a node?**
Restart uses WAL+snapshot recovery; a node far behind gets InstallSnapshot;
worst case: wipe data dir and rejoin as a learner → snapshot catch-up
(operations/failure-recovery.md).

**Q69. What does docker-compose give you?**
3 Go nodes + Rust drain + Python engine, CPU-pinned, WAL volumes, /dev/shm —
but it assumes Linux (network_mode: host, cpuset); on Windows/macOS Docker
Desktop expect failures. Chaos script kills the leader and verifies re-election.

---

## 70. Design decisions (ADR-level)

**Q70. Why Raft for policy consistency?**
ADR-0001: replicating small, low-rate policy commands is exactly what Raft is
for; the alternative (external DB) adds an operational dependency the target
customer may not want.

**Q71. Why gateway-local caches instead of checking the cluster per request?**
ADR-0002: consensus RTT per request would destroy latency budgets; local
enforcement with revision tracking gives fast lookups while keeping the cluster
authoritative. Trade-off: bounded propagation delay, made visible via revisions.

**Q72. Why committed-timestamp expiration?**
ADR-0003: local timers would make each node mutate state independently →
divergence. Committed absolute timestamps keep Apply deterministic and make
expiry behavior identical across nodes and gateways.

**Q73. Why single-server membership changes?**
Simpler safety story (one voter change per commit, catch-up before counting);
joint consensus in the prototype was implemented unsafely and would need
old+new quorum logic that's hard to get right and rarely needed (Q17–18).

**Q74. Why structured commands instead of raw strings in the log?**
Versioned command schema = evolvable state machine, replayable logs across
versions, and audit-friendly entries. The prototype's `"add:1.2.3.4"` strings
couldn't carry TTLs, IDs, or actors.

---

## 75. Honest limitations

**Q75. What are the biggest risks?**
(1) Hand-rolled consensus — mitigated by DST + failure suites, but a library
remains safer. (2) No formal verification. (3) Gateway sync protocol designed
but not built. (4) No multi-region story. (5) Security review pending.

**Q76. What would you do first with more time?**
Gateway sync (FR-018…021) to close the product loop, then linearizability
checking in CI, then mTLS default-on, then a real CIDR trie, then Kubernetes
packaging.

**Q77. If this were real infra, would you hand-roll Raft?**
No. You'd take hashicorp/raft or etcd's Raft, and keep the policy model, audit,
gateway protocol, and operational tooling — the actual product — on top. The
value is the control plane around consensus, not consensus itself (PRD §21).

**Q78. What did building this teach you / name a hard bug class?**
Great closing question material: the "unit tests pass, system doesn't work"
class (Q21) — stubbed transport with handler-level tests; the uncommitted-tail
restart bug (Q46); and timer-driven state divergence (Q38). Each maps to a
specific Raft rule, which is the point.

---

*Answers reflect the repository state after the production hardening pass.
Update this file when behavior changes — its value depends on matching the code.*
