ADR-0001: Use Raft for Security-Policy Consistency
Status: Accepted for the initial architecture
Date: 2026-10-11
Decision owners: RaftLite maintainers
Scope: Distributed policy control plane
1. Context

RaftLite manages security policies consumed by multiple API gateways.

Examples include:

Blocking an abusive IP address.
Temporarily banning a suspicious client.
Allowing or denying traffic from a CIDR range.
Managing rate-limit policies.

When multiple control-plane nodes maintain policy state, concurrent updates, node failures, and network partitions can cause inconsistent results unless the system defines how updates become authoritative.

Consider three control-plane nodes:

Node 1: BLOCK 203.0.113.50
Node 2: UNBLOCK 203.0.113.50
Node 3: Previous policy state


Without an agreed ordering of updates, different replicas could apply conflicting changes.

RaftLite therefore requires a mechanism that establishes an authoritative, ordered sequence of policy commands and preserves safety when individual nodes fail.

2. Decision

RaftLite will use the Raft consensus protocol to replicate policy commands across a cluster of voting nodes.

The initial deployment target is three voting nodes.

A policy command must pass through the consensus workflow before it becomes authoritative committed state.

The implementation must preserve Raft's safety guarantees and must not independently accept conflicting committed histories on different sides of a network partition.

The policy state machine will apply committed commands deterministically.

Gateways will consume committed policy state through a separate synchronization mechanism and enforce it locally.

3. Requirements Addressed

This decision supports the following product requirements:

Consistent ordering of policy updates.
Leader election after node failure.
Majority-based commitment.
Prevention of conflicting committed histories.
Recovery of restarted nodes.
Durable policy state.
A clear distinction between authoritative state and gateway-local policy projections.

The detailed, testable requirements belong in the SRS.

4. Alternatives Considered
4.1 Single policy server

Description: Run one server that owns all policy state.

Advantages:

Simple architecture.
Low operational complexity.
Easy to implement and debug.

Disadvantages:

The server is a single point of failure.
Policy writes become unavailable when the server is down.
Recovery and failover require additional mechanisms.

Assessment: Suitable for a prototype with no high-availability requirement, but insufficient for RaftLite's intended fault-tolerant control plane.

4.2 Central relational database

Description: Store policies in a relational database such as PostgreSQL.

Advantages:

Mature transactions and persistence.
Familiar operational tooling.
Strong query capabilities.
Existing backup and recovery mechanisms.

Disadvantages:

High availability requires its own deployment and failover design.
Policy-specific semantics and gateway synchronization still need implementation.
A separate database may increase the operational footprint.

Assessment: A legitimate alternative. If a target deployment already operates a reliable highly available database, using it may be more economical than introducing a custom consensus layer.

4.3 Redis

Description: Store shared policy state in Redis.

Advantages:

Fast key-value access.
Useful data structures.
Established ecosystem.
Suitable for many shared-state and caching workloads.

Disadvantages:

The complete durability and consistency guarantees depend on the chosen topology and configuration.
High availability and failover semantics still need to be evaluated.
Policy distribution, auditability, and gateway synchronization remain application responsibilities.

Assessment: A reasonable option for deployments already standardized on Redis. It is not categorically inferior to RaftLite.

4.4 etcd

Description: Use etcd as the distributed coordination and storage layer.

Advantages:

Mature distributed key-value store.
Raft-based consistency.
Established cluster and operational mechanisms.
Avoids implementing a consensus protocol from scratch.

Disadvantages:

Introduces an external infrastructure dependency.
Policy validation, gateway integration, policy lifecycle, and enforcement still require application code.
Requires operators to understand and maintain another service.

Assessment: A strong alternative and likely the better engineering choice when general-purpose distributed coordination is already available.

RaftLite must not claim that implementing its own Raft layer is inherently safer or more reliable than using etcd.

4.5 Independent policy files on each gateway

Description: Store a separate policy configuration on each gateway.

Advantages:

Simple deployment.
No distributed control plane.
Local policy evaluation.

Disadvantages:

Updates can drift across gateways.
Distribution and rollback require external orchestration.
Concurrent updates may produce conflicting configurations.

Assessment: Appropriate for small static deployments, but not sufficient for RaftLite's dynamic shared-policy use case without additional coordination.

5. Consequences
Positive consequences
Policy updates have a defined authoritative order.
A three-node cluster can continue committing writes after one voter fails, provided the remaining two can communicate and satisfy the protocol's requirements.
Committed policy history can be recovered using persistent state and snapshots.
Policy management has explicit leader, quorum, and recovery semantics.
Failure behavior can be validated through automated integration tests and fault injection.
Negative consequences
The implementation is more complex than a single server or database-backed API.
Writes depend on quorum availability.
Leader elections can temporarily interrupt writes.
Operators must manage persistent storage, network connectivity, and cluster membership.
Incorrect consensus, storage, or recovery logic can cause severe correctness failures.
The project must invest in extensive failure testing and protocol validation.
6. Safety and Availability Trade-Off

RaftLite prioritizes consistency and safety over accepting unsafe writes when quorum is unavailable.

For a three-voter cluster:

Available voting nodes	Quorum available?	New writes
3	Yes	May commit
2	Yes	May commit if protocol conditions hold
1	No	Cannot commit
0	No	Cannot commit

These statements describe voting-node availability, not arbitrary network connectivity. Actual progress depends on whether a majority can communicate and satisfy Raft's protocol requirements.

When quorum is unavailable, the system must not report new policy changes as committed.

Gateways should normally continue enforcing their last known committed local policy projection, subject to configured enforcement rules.

7. Implementation Constraints

The implementation must satisfy the following constraints:

Use a well-tested Raft library where practical rather than writing a new consensus implementation without a compelling reason.
Persist protocol-required state according to the selected library's durability contract.
Apply only committed commands to authoritative policy state.
Keep state-machine transitions deterministic.
Ensure retries cannot unintentionally create duplicate operations.
Support restart recovery and snapshot-based catch-up.
Separate control-plane commitment from gateway synchronization.
Expose quorum, leader, replication, and recovery status.
Test leader failures, follower failures, network partitions, restarts, and storage errors.
Never claim fault tolerance based only on a successful normal-operation demonstration.
8. Validation Plan

This decision will be validated through automated tests.

Test A: Normal replication
Start three Raft nodes.
Submit a policy update.
Verify the update is committed.
Verify the policy state converges across the nodes.

Expected result: The committed policy is represented consistently across healthy replicas.

Test B: Leader failure
Start a three-node cluster.
Commit a policy.
Stop the current leader.
Wait for a new leader election.
Submit another policy update.

Expected result: The cluster elects a new leader and can commit the new update when a majority is available.

Test C: Loss of quorum
Start a three-node cluster.
Isolate two nodes from the remaining node.
Attempt a policy write on the isolated minority side.

Expected result: The minority side cannot commit a new authoritative policy change.

Test D: Restart recovery
Commit multiple policies.
Stop one node.
Restart the node using its persistent storage.
Allow it to catch up.

Expected result: The restarted node recovers and converges with the authoritative cluster state.

Test E: Conflicting commands
Submit competing updates affecting the same policy.
Verify that committed commands have an agreed order.
Verify that every replica applies the committed sequence deterministically.

Expected result: Replicas converge on the same logical policy state.

9. Reconsideration Criteria

Revisit this decision if:

The target deployment already provides suitable highly available coordination infrastructure.
Operating a custom Raft-based control plane creates excessive maintenance costs.
The system's consistency requirements can be met more simply by an existing datastore.
The project cannot adequately validate consensus correctness.
Operational measurements show that the architecture does not meet its stated requirements.

A future migration to an external coordination system must preserve the documented policy semantics and explicitly redefine durability, consistency, and failure guarantees.

10. References
Raft: In Search of an Understandable Consensus Algorithm
The Raft Consensus Algorithm
etcd Documentation
PostgreSQL Documentation
11. Summary

Raft is selected because RaftLite requires an explicit, replicated ordering of authoritative policy changes across multiple control-plane nodes.

This decision does not imply that RaftLite should replace mature distributed databases or coordination systems in every deployment.

The decision is justified only if the project delivers reliable policy semantics, gateway integration, persistence, observability, and demonstrable recovery behavior around the consensus layer.