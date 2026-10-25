RaftLite Testing Strategy
1. Purpose

This document defines how RaftLite's correctness, reliability, security, and performance claims are verified.

The primary testing objective is to prove externally observable system behavior, particularly during distributed failures.

2. Testing Principles
Test protocol safety separately from performance.
Test failure recovery, not just successful startup.
Use deterministic inputs where possible.
Preserve reproducible test configurations.
Avoid relying exclusively on mocks for consensus behavior.
Test gateway enforcement independently from policy commitment.
Record benchmark methodology and environmental conditions.
Treat untested functionality as unverified.
3. Unit Tests

Unit tests should cover:

IP and CIDR validation.
Policy command validation.
State-machine transitions.
Policy creation and revocation.
Expiration evaluation.
Idempotency behavior.
Error handling.
Serialization and deserialization.
Configuration validation.

State-machine tests must verify that equivalent committed command sequences produce equivalent state.

4. Consensus Integration Tests

Test:

Initial cluster formation.
Leader election.
Policy replication.
Commit behavior.
Leader failure.
Follower failure.
Loss of quorum.
Recovery after restart.
Lagging follower catch-up.
Snapshot installation.

These tests must exercise the selected Raft implementation and persistent storage adapter.

5. API Integration Tests

Test:

Valid policy creation.
Invalid request rejection.
Authentication and authorization.
Policy retrieval.
Revocation.
Idempotent retries.
Correct error responses.
No-quorum behavior.
Commitment status reporting.
6. Gateway End-to-End Tests

Start the control plane and a real test gateway.

Verify that:

A committed policy reaches the gateway.
The gateway installs the correct revision.
Matching traffic is rejected according to the policy.
Nonmatching traffic follows the expected path.
A revocation reaches the gateway.
The gateway recovers after a synchronization interruption.
Control-plane failure does not unexpectedly clear installed policies.
7. Security Tests

Include:

Unauthenticated API requests.
Unauthorized mutations.
Malformed IP and CIDR values.
Oversized payloads.
Forged policy updates.
Untrusted forwarded headers.
Secret-leak checks.
Duplicate requests.
Invalid snapshot inputs.
8. Performance Tests

Measure independently:

Local policy lookup latency.
Policy commit latency.
Gateway throughput.
API throughput.
Policy propagation latency.
Memory consumption.
Disk usage and write latency.
Recovery time.

Report p50, p95, and p99 where applicable.

Every published result should include hardware, OS, software version, cluster size, workload, duration, sample count, and benchmark configuration.

9. Test Environments

Use separate environments for:

Fast unit tests.
Multi-node integration tests.
Gateway end-to-end tests.
Destructive failure-injection tests.
Performance benchmarks.

Tests that manipulate networking or persistent storage may require a controlled environment rather than ordinary CI workers.

10. Continuous Integration

A recommended CI pipeline is:

Formatting and static checks.
Unit tests.
API and state-machine tests.
Consensus integration tests.
Gateway end-to-end tests.
Security checks.
Selected failure tests.
Build and packaging validation.

Long-running chaos tests and performance benchmarks may run on a separate schedule.

11. Release Acceptance

A release must not be considered validated solely because unit tests pass.

Before release, verify the acceptance scenarios defined in the SRS, including leader failure, quorum loss, node recovery, policy synchronization, and gateway enforcement.

Document known limitations and failed or skipped tests.

12. Related Documents
Software Requirements Specification
Failure testing
Failure recovery