ADR-0003: Deterministic Policy Expiration
Status: Accepted for the initial architecture
Date: 2026-10-11
Decision owners: RaftLite maintainers
Scope: Temporary policies, replicated state, and gateway enforcement
1. Context

RaftLite supports temporary security policies, such as blocking an IP address for 30 minutes.

A naive implementation might create a local timer on every node:

Node 1: Block expires at 12:30:00
Node 2: Block expires at 12:30:02
Node 3: Block expires at 12:30:05


Differences can arise from clock skew, scheduling delays, process pauses, and network interruptions.

If each node independently decides when an authoritative policy expires, the cluster can temporarily disagree about policy state.

RaftLite must distinguish between the authoritative policy lifecycle and the local enforcement of a policy.

2. Decision

Every temporary policy must contain an explicit expiration timestamp expressed in UTC.

The expiration timestamp is established when the policy is created and included in the committed policy command.

For example:

{
  "id": "pol_123",
  "type": "ip_block",
  "target": "203.0.113.50",
  "created_at": "2026-10-11T12:00:00Z",
  "expires_at": "2026-10-11T12:30:00Z",
  "reason": "credential abuse"
}


The state machine must apply the committed expiration metadata deterministically. It must not derive a new expiration timestamp independently on each replica.

A local timer may be used as an optimization to refresh an index or trigger maintenance, but it must not independently rewrite authoritative state.

3. Policy Semantics

RaftLite distinguishes two concepts:

Authoritative policy state: The policy and its expiration metadata recorded in committed state.
Effective policy decision: Whether a policy applies to a request at the evaluation time, under the documented expiration and clock rules.

A policy can remain in the authoritative history after its expiration timestamp has passed. It may be marked expired in a derived view or removed through a committed command.

Historical records should remain available for audit purposes according to the retention policy.

4. Expiration Rules

The initial implementation must follow these rules:

An expiration timestamp is assigned before the policy command is submitted for commitment.
All replicas apply the same timestamp from the committed command.
A policy with no expiration timestamp is permanent unless its policy type explicitly defines another lifecycle.
Expiration evaluation uses UTC timestamps and a documented clock policy.
Expiration must not require a new Raft election or an individual node's local timer to fire.
Expired policies must not become active again simply because a node restarts.
Gateways must apply the documented expiration rule to their installed policy projection.
Policy expiration and explicit revocation must remain distinguishable in audit history.
5. Clock Skew and Time Semantics

Raft does not synchronize physical clocks.

Consequently, storing an expiration timestamp in a committed command does not make all machines observe that timestamp at exactly the same physical instant.

RaftLite must document this limitation.

The initial implementation should:

Use UTC for serialized timestamps.
Synchronize host clocks using the deployment's time-synchronization service.
Validate that expiration timestamps are within permitted bounds.
Expose clock health where reliable clock monitoring is available.
Avoid using wall-clock timestamps to determine the ordering of committed commands.
Define a maximum acceptable clock-skew assumption for deployments that require bounded expiration behavior.

The precise boundary behavior must be explicit. For example, a policy is expired when the evaluation time is greater than or equal to its expiration timestamp, subject to the documented clock assumptions.

6. Gateway Behavior

Gateways evaluate temporary policies locally.

The gateway must not extend a policy's expiration simply because it loses connectivity to RaftLite.

If the gateway's clock is unreliable, it cannot safely claim exact time-based enforcement. The implementation must define its behavior for clock-health failures and report the condition operationally.

For a disconnected gateway, the enforcement contract must account for two separate conditions:

The gateway has the policy and can evaluate its expiration.
The gateway may have missed a newer policy update or revocation.

These are different failure modes and must be observable.

7. Explicit Expiration Commands

A future implementation may generate a replicated EXPIRE_POLICY command for cleanup, audit indexing, or lifecycle notifications.

Such a command must be processed through the normal Raft workflow.

It must not be required for the basic time-based enforcement rule if the policy's committed expiration timestamp already defines when it ceases to apply.

This prevents delayed cleanup commands from accidentally extending a temporary ban.

8. Consequences
Positive consequences
Replicas share the same committed expiration metadata.
Restart recovery does not reset policy durations.
Local timers cannot independently modify authoritative policy state.
Temporary bans have explicit, testable lifecycle semantics.
Audit records can distinguish creation, expiration, and revocation.
Negative consequences
Physical clock differences can still affect local enforcement timing.
Gateway synchronization and clock health must be monitored.
Cleanup and historical retention require separate lifecycle decisions.
The system must document timing guarantees rather than promise perfect instantaneous expiration.
9. Validation Plan
Test A: Replicated expiration metadata

Create a temporary policy and verify that all replicas store the same expiration timestamp.

Test B: Restart recovery

Restart a node before and after a policy's expiration timestamp.

Verify that recovery does not restart the duration or reactivate the policy.

Test C: Delayed cleanup

Delay or suppress an expiration-maintenance command.

Verify that the policy's effective status still follows its committed expiration timestamp.

Test D: Clock skew

Simulate different node and gateway clock offsets.

Verify that the system reports the limitation and follows the documented clock policy.

Test E: Gateway disconnection

Disconnect a gateway and allow a temporary policy to reach its expiration time.

Verify the documented local enforcement behavior and synchronization status.

10. Reconsideration Criteria

Revisit this decision if the product requires stricter expiration bounds than its clock assumptions can support, or if future policy types introduce time-dependent semantics that cannot be represented by a fixed expiration timestamp.

11. References
../overview.md — System architecture.
0001-use-raft-for-policy-consistency.md — Consensus and authoritative state.
0002-local-gateway-policy-cache.md — Local enforcement and synchronization.
12. Summary

Temporary policy expiration is defined by committed policy metadata and a documented time-evaluation contract.

Raft orders authoritative changes; it does not synchronize physical clocks.