RaftLite Failure Testing and Deterministic Simulation
1. Purpose

RaftLite must demonstrate that its consistency and recovery claims hold under failures, not merely during normal execution.

This document defines the failure scenarios and invariants that the test harness should validate.

2. Safety Invariants

The test suite must verify the following properties:

Invariant 1 — No unsafe commitment

A minority partition must not independently commit conflicting policy history.

Invariant 2 — Committed history is preserved

A node recovery procedure must not silently discard or contradict valid committed state.

Invariant 3 — Deterministic state-machine application

Replicas applying the same committed command sequence must reach equivalent logical policy state.

Invariant 4 — Invalid updates are not installed

Gateways must not treat malformed or incomplete policy updates as valid completed installations.

Invariant 5 — No false success

An API request must not be reported as committed merely because it was received or queued.

Invariant 6 — Recoverable lag

A node or gateway that falls behind must either catch up using the supported protocol or report that recovery is incomplete.

3. Failure Scenarios
Scenario A — Leader crash
Start three nodes.
Commit a policy.
Stop the leader.
Wait for a new leader.
Commit another policy.
Restart the failed node.
Verify convergence.
Scenario B — Follower crash
Start a three-node cluster.
Stop one follower.
Commit policies through the remaining majority.
Restart the follower.
Verify catch-up.
Scenario C — Loss of quorum
Start three nodes.
Isolate two nodes from the remaining voter.
Verify the minority cannot commit independent writes.
Restore connectivity.
Verify the cluster recovers safely.
Scenario D — Delayed and dropped messages

Inject message delays and packet loss in a controlled test environment.

Verify that the cluster preserves safety even if progress slows or leadership changes.

Scenario E — Restart during persistence

Terminate a node at controlled points during normal operation.

Restart it and verify recovery according to the persistence contract.

Do not assume that a process kill reproduces every possible power-loss or storage-failure scenario.

Scenario F — Snapshot recovery
Generate enough log entries to trigger a snapshot.
Start or recover a lagging node.
Verify snapshot installation.
Verify replay of later entries.
Compare the resulting logical policy state.
Scenario G — Gateway synchronization interruption
Synchronize a gateway.
Disconnect it.
Commit several policy changes.
Reconnect it.
Verify catch-up or full-state synchronization.
Scenario H — Temporary-policy expiration
Commit a temporary block.
Restart a node before expiration.
Restart or disconnect a gateway where appropriate.
Verify that the duration is not restarted.
Verify documented expiration behavior.
4. Deterministic Simulation

Where the implementation supports a deterministic simulation harness, it should control:

Message delivery order.
Message delay.
Message loss.
Node crashes and restarts.
Network partitions.
Event scheduling.

A fixed seed, such as 12345, should reproduce the same simulated event sequence when the simulator's implementation and configuration remain unchanged.

Record the seed, configuration, software version, and event trace for failing scenarios.

A deterministic simulator is not a substitute for integration tests using real processes, operating-system networking, and persistent storage.

5. Property-Based Testing

Where practical, generate sequences of operations and failures.

Example operations:

Create policy.
Revoke policy.
Restart node.
Delay message.
Drop message.
Partition network.
Restore connectivity.
Install snapshot.

After each sequence, check the defined safety invariants.

Keep failing seeds and operation traces so the test can be reproduced.

6. Failure Injection Safety

Destructive tests must run in isolated environments.

They must not share production storage, credentials, or network resources.

Every test should have a bounded duration and a cleanup procedure that does not accidentally delete valuable persistent state.

7. Test Evidence

For each failure scenario, retain:

Test identifier.
Software version.
Cluster topology.
Failure-injection configuration.
Event trace or logs.
Expected result.
Actual result.
Pass/fail status.
Reproduction instructions.
8. Acceptance Criteria

The failure-testing milestone is complete when the required scenarios run reproducibly, verify their expected outcomes, and fail automatically if a safety invariant is violated.

A test that merely observes a leader change without checking policy commitment and recovery is insufficient.

9. Related Documents
Testing strategy
Architecture overview
Failure recovery runbook
Software Requirements Specification