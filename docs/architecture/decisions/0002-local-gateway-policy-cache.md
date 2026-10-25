ADR-0002: Enforce Policies Using a Local Gateway Projection
Status: Accepted for the initial architecture
Date: 2026-10-11
Decision owners: RaftLite maintainers
Scope: Gateway policy evaluation and synchronization
1. Context

RaftLite manages security policies through a replicated control plane, while API gateways enforce those policies against incoming requests.

The system must balance three requirements:

Consistent, authoritative policy management.
Low-latency request processing.
Continued enforcement during temporary control-plane outages.

A naive implementation might ask the Raft leader to evaluate every incoming HTTP request.

For example:

Incoming request
      |
      v
API Gateway
      |
      v
RaftLite leader
      |
      v
Policy lookup
      |
      v
Allow / Deny


This design introduces a network round trip into the request path. It also makes gateway availability dependent on the control plane for every request.

A leader election, network interruption, or quorum loss could therefore disrupt ordinary request processing even when the gateway already has sufficient policy information to make a decision.

RaftLite needs to separate authoritative policy updates from routine request enforcement.

2. Decision

API gateways will evaluate security policies using a local, in-memory policy projection.

The Raft cluster remains the authoritative source of committed policy state. Gateways receive policy updates through a separate synchronization mechanism and maintain a local projection suitable for efficient request evaluation.

Normal request processing must not require a new Raft consensus operation.

The initial implementation will prioritize local IP-block lookup and define explicit behavior for synchronization failures.

The gateway must expose its last successfully installed policy revision or equivalent synchronization position.

3. Architectural Model
flowchart TD
    Admin[Administrator]
    API[RaftLite Administrative API]
    Raft[Raft Consensus Cluster]
    State[Committed Policy State]
    Sync[Policy Synchronization]
    Cache[Gateway Local Policy Projection]
    Request[Incoming HTTP Request]
    Eval[Local Policy Evaluation]
    Decision[Allow or Deny]
    Telemetry[Gateway Telemetry]

    Admin --> API
    API --> Raft
    Raft --> State
    State --> Sync
    Sync --> Cache
    Request --> Eval
    Cache --> Eval
    Eval --> Decision
    Eval --> Telemetry


The policy synchronization mechanism may use streaming updates, polling, or another explicitly defined transport. The initial implementation must choose and document one mechanism rather than treating synchronization as an implicit capability of Raft.

4. Alternatives Considered
4.1 Query the leader for every request

Advantages:

Centralized evaluation.
No need to maintain a complete policy projection in every gateway.
Policy changes can be observed without distributing the entire policy set, provided the query reaches a sufficiently up-to-date authoritative service.

Disadvantages:

Adds network latency to the request path.
Increases control-plane request volume.
Couples request availability to control-plane connectivity.
Makes leader elections and quorum loss operationally disruptive.
Can create a bottleneck under high traffic.

Decision: Rejected for ordinary request enforcement.

A centralized decision API may still be useful for administrative queries or specialized policy evaluations that cannot be performed locally.

4.2 Local policy projection

Advantages:

Low-latency policy evaluation.
No consensus operation per HTTP request.
Reduced control-plane request load.
Continued enforcement using installed policy state during temporary disconnection.

Disadvantages:

Gateways may temporarily have different policy revisions.
Synchronization and recovery require additional engineering.
Memory usage grows with the policy set.
Operators need visibility into stale or disconnected gateways.

Decision: Selected.

4.3 Shared external cache

Advantages:

Multiple gateways can access common policy data.
May simplify distribution in an existing infrastructure environment.
Can reduce duplicated storage in some deployments.

Disadvantages:

Introduces a network dependency into lookups unless gateways also cache locally.
Requires separate availability, consistency, and recovery decisions.
Does not remove the need to distribute authoritative updates.

Decision: Not required for the initial implementation. A shared cache may be evaluated later if operational evidence justifies it.

5. Policy Synchronization Contract

The synchronization mechanism must define how a gateway moves from one committed policy revision to another.

At minimum, the contract must provide:

An identifiable policy revision, committed log index, or equivalent ordered position.
A way to deliver policy changes.
A way to detect missing or out-of-order updates.
A full-state synchronization mechanism for new or recovered gateways.
A way to report the last successfully installed revision.
A defined response to invalid, incomplete, or interrupted updates.

A gateway must not claim that it is fully synchronized merely because its connection to a control-plane node is healthy.

5.1 Initial synchronization

When a gateway starts without a valid local projection, it must obtain a consistent policy snapshot and its corresponding revision.

Updates that occur during snapshot transfer must not be lost.

One valid approach is:

Obtain a snapshot associated with a known committed revision.
Install the snapshot atomically.
Apply all subsequent updates in revision order.
Report the installed revision.

The implementation must ensure that snapshot installation and update replay do not introduce gaps.

5.2 Incremental updates

Once synchronized, the gateway may receive incremental policy changes.

Each update must be associated with an ordering mechanism.

The gateway must reject, buffer, or recover from unexpected gaps according to the synchronization protocol.

Duplicate delivery must not corrupt the local projection.

5.3 Reconnection

If a gateway disconnects, it must determine whether it can safely resume from its last installed revision.

If the required update history is no longer available, the gateway must perform a full-state synchronization.

5.4 Atomic policy installation

A policy update must not leave the gateway's local policy projection in a partially applied state.

Where multiple related changes must take effect together, the implementation must install them atomically or define explicit intermediate-state semantics.

6. Consistency Guarantees

RaftLite must distinguish control-plane consistency from gateway synchronization.

Raft establishes the authoritative committed order of policy commands.

The gateway projection reflects the subset of that committed history that the gateway has successfully installed.

Therefore, a newly committed policy is not necessarily enforced instantaneously by every gateway.

The product must document this propagation behavior honestly.

6.1 Required guarantees
Only committed control-plane state is eligible for authoritative distribution.
Each gateway applies updates according to the defined revision order.
A gateway must not silently skip required updates.
A gateway must expose its synchronization position.
A recovered gateway must reconcile with authoritative state.
A gateway must not interpret a synchronization failure as permission to allow all traffic.
6.2 Fleet-wide enforcement acknowledgment

If an administrative operation requires confirmation that every registered gateway has installed a policy, the control plane must track gateway acknowledgments.

The API must distinguish:

Policy accepted.
Policy committed.
Policy distributed.
Policy installed on a particular gateway.
Policy installed on all gateways required by a deployment's enforcement policy.

A committed policy alone does not prove fleet-wide installation.

7. Behavior During Control-Plane Failure

When the control plane becomes unavailable, a gateway with a valid local policy projection can continue enforcing the policies it has already installed.

The initial product should support a documented last-known-good enforcement mode.

In this mode:

Existing installed policies remain available for local evaluation.
New policy changes cannot be assumed to have arrived.
The gateway reports degraded synchronization health.
The gateway exposes the age or revision of its installed policy state.
Recovery triggers synchronization before the gateway reports itself fully synchronized again.

The gateway must not automatically clear its policy projection because the control plane is unavailable.

7.1 Stale policy risk

Continuing with a last-known-good projection protects against a sudden loss of control-plane connectivity, but it does not guarantee that the projection contains the latest security decisions.

For example, an IP that should now be blocked may remain unblocked on a disconnected gateway.

The product must expose this risk rather than promising both instantaneous global consistency and unrestricted disconnected operation.

7.2 Enforcement modes

The architecture may eventually support:

Last-known-good: Continue using the last valid installed policy projection.
Fail-closed: Reject requests when a required policy decision cannot be safely evaluated.
Fail-open: Allow requests when policy evaluation cannot be completed.

Fail-open must be an explicit configuration, never an accidental consequence of a control-plane error.

The initial release should implement and test last-known-good behavior first. Other modes require explicit requirements and security review.

8. Performance Requirements

The architecture is intended to keep ordinary policy evaluation local.

The implementation should measure:

Policy lookup latency at p50, p95, and p99.
Gateway throughput.
CPU and memory usage.
Policy update propagation latency.
Time required for full synchronization.
Time required for reconnection and catch-up.
Behavior under large policy sets and sustained updates.

The project must not claim a particular latency or throughput target without specifying and running a reproducible benchmark.

The local projection should use a data structure appropriate to the supported policy types. Initial IP-block lookup can use a hash-based structure, while CIDR policies require an appropriate prefix-matching structure.

9. Security Considerations

Local policy enforcement introduces security-sensitive implementation requirements.

Client IP identification

The gateway must use a clearly defined source of client identity.

Forwarded headers must only be trusted when they are supplied by configured trusted proxies.

Policy update authenticity

Gateways must accept policy updates only from authenticated, authorized sources.

Projection integrity

The synchronization mechanism must detect incomplete or invalid updates and avoid applying corrupted state.

Resource limits

Policy synchronization must enforce sensible limits on payload sizes, policy counts, and update rates.

Auditability

The system should retain enough information to associate an installed policy revision with its originating committed change.

Sensitive data must not be unnecessarily exposed through metrics or logs.

10. Testing Strategy
Test A: Local enforcement
Start the control-plane cluster and a gateway.
Install a committed IP-block policy.
Send requests from the blocked test IP through the gateway.
Verify the configured rejection behavior.

Expected result: The gateway enforces the installed policy without making a consensus request for each HTTP request.

Test B: Control-plane outage
Synchronize a gateway.
Stop or isolate the control-plane nodes.
Send requests that exercise an installed policy.
Inspect gateway synchronization health.

Expected result: The gateway continues using its valid last-known-good projection and reports degraded synchronization.

Test C: Missed updates
Disconnect a gateway.
Commit several policy changes.
Reconnect the gateway.
Verify incremental catch-up or full-state synchronization.

Expected result: The gateway reaches the authoritative revision without silently skipping updates.

Test D: Duplicate updates
Deliver the same update more than once.
Inspect the local projection.

Expected result: Duplicate delivery does not corrupt policy state or create duplicate effective rules.

Test E: Snapshot and update race
Begin full synchronization.
Commit additional policy changes during synchronization.
Complete snapshot installation and replay updates.

Expected result: The gateway does not lose committed changes that occur during synchronization.

Test F: Partial installation
Simulate an invalid or interrupted policy update.
Inspect the active local projection.

Expected result: The gateway does not expose a partially applied update as a valid completed installation.

11. Consequences
Positive consequences
Ordinary requests do not depend on Raft leader availability.
Local policy evaluation minimizes network overhead.
The control plane can focus on policy management and replication.
Gateways can continue enforcing installed policies during temporary disconnection.
Synchronization health becomes an explicit operational concern.
Negative consequences
Policy propagation is eventually reflected at each gateway rather than instantaneous.
Synchronization requires revision tracking and recovery logic.
Stale policy state creates a security risk during disconnection.
Memory usage is distributed across gateway instances.
Testing must cover reconnects, missed updates, snapshots, and partial installation.
12. Reconsideration Criteria

Revisit this decision if:

The policy set becomes too large for gateway-local memory.
Some policy types require centralized evaluation.
The synchronization protocol cannot provide adequate recovery guarantees.
Security requirements demand stronger enforcement acknowledgment semantics.
Measurements show that local evaluation is not an acceptable implementation for a particular gateway integration.

Any alternative must explicitly document its latency, consistency, availability, and failure-mode trade-offs.

13. References
../overview.md — RaftLite system architecture.
0001-use-raft-for-policy-consistency.md — ADR-0001: Use Raft for policy consistency.
../../srs/README.md — SRS documentation entry point, if present.
14. Summary

RaftLite uses Raft to establish authoritative committed policy state and local gateway projections to enforce that state efficiently.

The consensus layer guarantees an ordered committed history; the synchronization layer distributes that history; and the gateway enforces its installed projection.

The key distinction is that a committed policy is authoritative, but it is not necessarily installed on every gateway at the instant of commitment.