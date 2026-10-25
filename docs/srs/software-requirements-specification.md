RaftLite — Software Requirements Specification (SRS)
Document ID: RL-SRS-001
Version: 1.0
Status: Proposed baseline
Last updated: 2026-10-11
1. Introduction
1.1 Purpose

This document specifies the functional and non-functional requirements for RaftLite, a self-hosted security-policy control plane for distributed API gateways.

It defines the expected system behavior, constraints, interfaces, operational requirements, and acceptance criteria.

This document is the baseline for implementation planning, testing, and evaluation. Requirements marked as planned are not evidence that the corresponding functionality has already been implemented.

1.2 Scope

RaftLite manages security policies across multiple API gateways using a Raft-based replicated control plane.

The initial release focuses on:

IP block and unblock policies.
Temporary IP blocks.
Three-node consensus.
Durable state and restart recovery.
Local gateway policy enforcement.
Policy synchronization.
Administrative APIs.
Health monitoring, metrics, and audit events.
Automated failure testing.

CIDR policies, rate limiting, anomaly detection, approval workflows, and advanced dashboards are subsequent extensions unless explicitly included in an implementation milestone.

1.3 Product objective

The product objective is to maintain one authoritative committed security-policy history and distribute that policy state to gateway instances for local enforcement.

1.4 Definitions
Term	Definition
Raft	Consensus protocol used to replicate an ordered log
Leader	Raft node coordinating normal write replication
Follower	Raft node replicating the leader's log
Quorum	Majority of voting members required for relevant consensus decisions
Committed entry	Log entry committed according to Raft's protocol rules
State machine	Deterministic component applying committed commands
Policy projection	Local representation of policy state used by a consumer
Gateway	HTTP reverse proxy or API gateway enforcing policies
WAL	Write-ahead log or equivalent durable log storage
Snapshot	Persisted representation of state at a known log position
RPO	Recovery point objective
RTO	Recovery time objective
2. Product Overview
2.1 Intended users
Platform engineer: Deploys and operates the policy cluster.
Security engineer: Creates, revokes, and investigates policies.
SRE: Monitors cluster health and performs recovery.
API gateway: Consumes policies and enforces them against requests.
Authorized automation: Submits policy requests through the documented API.
2.2 Operating environment

The initial development environment should support Docker Compose and a three-node test cluster.

The software should use Go for the control plane, Rust for the optional telemetry drain, and Python for the optional anomaly-detection engine.

These language choices are implementation decisions, not externally observable product requirements.

2.3 System boundaries

RaftLite is responsible for authoritative policy management, replication, persistence, policy distribution, and operational visibility.

The gateway is responsible for request processing and local policy evaluation.

External identity providers, load balancers, managed WAF services, and general-purpose databases are outside the initial product boundary unless explicitly integrated.

3. Assumptions and Constraints
The initial cluster has three configured Raft voting members.
Each voting node has access to storage that satisfies the selected Raft implementation's durability requirements.
Nodes communicate through authenticated network connections in a secured deployment.
Gateways have a documented mechanism for receiving committed policy state.
Gateway-local state is not the authoritative source of policy truth.
The system cannot guarantee new write commitment without quorum.
Time-based enforcement depends on documented clock assumptions.
Performance targets must be established through reproducible benchmarks rather than assumed from architecture alone.
4. External Interfaces
4.1 Administrative HTTP API

The API should use versioned paths under /v1/.

Proposed endpoints:

Method	Endpoint	Purpose
GET	/healthz	Process liveness
GET	/readyz	Readiness status
GET	/v1/leader	Leader information
GET	/v1/cluster	Cluster status
GET	/v1/policies	List policies
POST	/v1/policies	Create a policy
GET	/v1/policies/{id}	Retrieve a policy
DELETE	/v1/policies/{id}	Revoke or remove a policy
GET	/v1/decisions/{ip}	Query a policy decision

These endpoints are proposed interfaces. The final API specification must define request schemas, response schemas, error codes, authentication, pagination, and consistency guarantees.

4.2 Inter-node interface

The consensus transport must carry the messages required by the selected Raft implementation.

The implementation must not expose unauthenticated consensus operations to untrusted clients.

4.3 Gateway synchronization interface

The synchronization protocol must provide an ordered update stream or equivalent revision-based mechanism.

It must support initial synchronization, incremental updates, reconnection, and recovery from missed updates.

4.4 Observability interface

Each node should expose metrics compatible with the selected monitoring stack and structured logs suitable for operational investigation.

5. Functional Requirements

Each requirement has a stable identifier so tests, issues, and implementation work can reference it.

5.1 Consensus and cluster management

FR-001 — Multiple nodes

The system shall support a cluster containing multiple Raft nodes.

Acceptance: A test deployment starts three nodes and reports their identities and roles.

FR-002 — Leader election

The system shall elect a leader according to the Raft protocol.

Acceptance: After stopping the leader, a remaining majority can elect a new leader within the configured test timeout.

FR-003 — Ordered replication

The system shall replicate policy commands through the Raft log.

Acceptance: Committed commands are applied in the agreed log order.

FR-004 — Quorum-based commitment

The system shall only report a write as committed when the selected Raft implementation confirms commitment.

Acceptance: A minority partition cannot commit an independent conflicting write.

FR-005 — No-quorum behavior

The system shall not acknowledge a new policy write as committed when quorum is unavailable.

Acceptance: A write attempted against an isolated minority returns an appropriate error or remains pending according to the documented API contract.

FR-006 — Cluster status

The system shall expose node identity, role, leader information, and relevant health state.

Acceptance: An operator can distinguish a healthy follower from a node that cannot participate in the cluster.

5.2 Policy management

FR-007 — Create IP block

The system shall support creating an IP-block policy.

Acceptance: A valid request creates a policy that becomes authoritative only after commitment.

FR-008 — Revoke policy

The system shall support revoking an existing policy.

Acceptance: The revocation is represented by an ordered command and propagated to policy consumers.

FR-009 — Validate policy input

The system shall validate IP addresses, required fields, policy types, and supported duration formats.

Acceptance: Invalid addresses and malformed requests are rejected without changing authoritative state.

FR-010 — List policies

The system shall expose a policy-listing operation.

Acceptance: Authorized callers can retrieve policies and their lifecycle metadata.

FR-011 — Temporary policy

The system shall support policies with an explicit expiration timestamp.

Acceptance: A temporary policy does not remain effective beyond its documented expiration boundary, subject to the configured clock assumptions.

FR-012 — Policy identity

Each policy shall have a unique identifier.

Acceptance: A policy can be retrieved, audited, and revoked using its identifier.

FR-013 — Idempotent requests

The system shall support a documented idempotency mechanism for retryable policy-creation requests.

Acceptance: Retrying a request with the same valid idempotency key does not unintentionally create duplicate logical operations.

FR-014 — Audit metadata

The system shall associate policy changes with actor, reason, source, timestamp, request identifier, and outcome where applicable.

Acceptance: An operator can trace a policy change to its originating request.

5.3 Persistence and recovery

FR-015 — Durable protocol state

The system shall persist the state required by the selected Raft implementation.

Acceptance: A node restart does not violate the library's documented safety and durability requirements.

FR-016 — Restart recovery

The system shall restore state from persistent data.

Acceptance: A restarted node recovers its durable state and reconciles with the current cluster.

FR-017 — Snapshots

The system shall support snapshot-based state recovery if required by the selected implementation and configured for the MVP.

Acceptance: A node can restore a valid snapshot and process subsequent committed entries.

FR-018 — Follower catch-up

The system shall support a recovered or lagging node catching up with authoritative state.

Acceptance: A restarted node converges with the cluster without manually reconstructing individual policies.

5.4 Gateway enforcement

FR-019 — Local policy evaluation

The gateway integration shall evaluate supported policies using local policy state.

Acceptance: A normal request does not require a new Raft consensus operation.

FR-020 — Policy synchronization

The gateway integration shall receive committed policy updates and track its installed revision.

Acceptance: A test verifies that a committed policy reaches the gateway and becomes enforceable.

FR-021 — Synchronization recovery

The gateway integration shall recover from interrupted policy synchronization.

Acceptance: A disconnected gateway catches up or performs a full-state synchronization after reconnecting.

FR-022 — Control-plane outage

The gateway shall follow its configured enforcement behavior when control-plane connectivity is lost.

Acceptance: During a simulated outage, the gateway follows the documented last-known-good behavior and reports degraded synchronization health.

FR-023 — Trusted client identity

The gateway shall use a documented client-IP extraction policy.

Acceptance: Untrusted forwarded headers cannot override the configured trusted-proxy behavior.

5.5 Operations and observability

FR-024 — Liveness

The system shall expose a liveness endpoint.

Acceptance: The endpoint reports whether the process is alive according to its documented semantics.

FR-025 — Readiness

The system shall expose readiness information separately from liveness.

Acceptance: A process can be alive but not ready for the operation being evaluated.

FR-026 — Metrics

The system shall expose metrics for requests, policy operations, leader changes, and relevant replication state.

Acceptance: Metrics are available to the configured monitoring system.

FR-027 — Structured logs

The system shall emit structured logs for important policy and cluster events.

Acceptance: An operator can correlate a failed policy operation with its associated logs.

FR-028 — Audit history

The system shall record security-policy changes and their outcomes.

Acceptance: A test can retrieve or inspect the audit record associated with a committed policy operation.

6. Non-Functional Requirements
NFR-001 — Consistency

The system shall preserve the safety guarantees of its selected Raft implementation and deterministic state machine.

Verification: Consensus integration tests and partition tests.

NFR-002 — Availability

The control plane should continue accepting writes when a majority of voting members can communicate and the Raft protocol permits progress.

Verification: Leader and follower failure tests.

NFR-003 — Durability

Committed state shall survive the failures covered by the documented storage and deployment assumptions.

Verification: Process-crash and restart-recovery tests. Host or storage-loss guarantees require separate validation.

NFR-004 — Performance

Policy evaluation shall be local for supported gateway policy types.

Verification: Gateway benchmarks reporting p50, p95, and p99 latency.

Numerical targets must be established and justified before release.

NFR-005 — Security

Administrative and inter-node communication shall support authentication and encryption for secured deployments.

Verification: Authentication, authorization, transport-security, and negative tests.

NFR-006 — Observability

Operators shall be able to identify leadership, quorum loss, replication lag, and gateway synchronization problems.

Verification: Operational tests and dashboard or metrics inspection.

NFR-007 — Recoverability

A failed node shall be able to restore its durable state and reconcile with the cluster.

Verification: Restart, snapshot, and catch-up tests.

NFR-008 — Deployability

A developer shall be able to start the documented development cluster using the repository's deployment instructions.

Verification: Automated or documented clean-environment deployment test.

NFR-009 — Maintainability

Consensus, policy semantics, transport, persistence, and gateway integration should have explicit interfaces and automated tests.

Verification: Code review and package-level test coverage.

NFR-010 — Auditability

Security-sensitive changes shall be attributable to an actor or authorized automation source.

Verification: Audit-event inspection.

7. Data Requirements
7.1 Policy entity

A policy should contain:

Field	Purpose
id	Stable policy identifier
type	Policy type
target	IP, CIDR, or other supported target
action	Allow, deny, or another supported action
created_at	Creation timestamp
expires_at	Optional expiration timestamp
reason	Explanation for the policy
source	Originating subsystem
created_by	Actor or automation identity
version	Policy revision or equivalent version
status	Derived or explicitly modeled lifecycle state

The implementation must define which fields are authoritative, which are derived, and how updates and revocations are represented in the replicated command model.

7.2 Durability

Policy state and protocol-required persistent state must use storage appropriate to the selected Raft implementation.

The repository must document backup, recovery, snapshot retention, and data-loss assumptions before making production durability claims.

7.3 Audit retention

Audit retention must be configurable or explicitly documented. Sensitive data should not be retained indefinitely without a defined operational reason.

8. Security Requirements
SEC-001: Authenticate administrative callers.
SEC-002: Authorize policy-management operations.
SEC-003: Authenticate and protect inter-node communication.
SEC-004: Validate and normalize policy targets.
SEC-005: Protect secrets using an appropriate secret-management mechanism.
SEC-006: Record security-sensitive policy changes.
SEC-007: Enforce request-size and resource-consumption limits.
SEC-008: Do not trust forwarded client-IP headers without a trusted-proxy policy.
SEC-009: Do not expose administrative or consensus endpoints unnecessarily.
SEC-010: Document and test gateway behavior during control-plane failure.
9. Testing Requirements

The project shall include:

Unit tests for policy validation and state transitions.
Consensus integration tests.
API integration tests.
Gateway end-to-end tests.
Leader-failure and follower-failure tests.
Network-partition tests.
Restart and recovery tests.
Temporary-policy expiration tests.
Idempotency tests.
Authentication and authorization tests.
Reproducible performance benchmarks.

Tests must assert externally observable behavior rather than merely checking that internal functions execute.

10. Acceptance Criteria for the MVP

The MVP is acceptable when the following scenario passes automatically:

Start three control-plane nodes.
Verify cluster formation and leadership.
Start a gateway.
Create an IP-block policy.
Verify commitment and gateway synchronization.
Verify the gateway enforces the policy locally.
Stop the leader.
Verify a new leader is elected when quorum remains.
Commit another policy.
Restart the failed node.
Verify it catches up.
Remove a policy and verify propagation.
Test a temporary policy's expiration behavior.
Simulate loss of quorum and verify unsafe writes are not reported as committed.
Inspect metrics and audit events.

All results must be reproducible and backed by automated tests or recorded test evidence.

11. Requirements Traceability

Requirements should be linked to implementation and validation artifacts.

Recommended traceability:

Requirement group	Implementation area	Validation
FR-001–FR-006	internal/consensus/	Consensus integration tests
FR-007–FR-014	internal/policy/, internal/server/	Policy and API tests
FR-015–FR-018	internal/storage/, consensus storage adapter	Recovery tests
FR-019–FR-023	internal/gateway/	Gateway end-to-end tests
FR-024–FR-028	Server, audit, and observability packages	Operational tests
NFR-001–NFR-010	Cross-cutting	Reliability, security, and performance tests

These paths describe the intended repository layout. Update the traceability table if the implementation uses a different structure.

12. Limitations

This specification does not establish that RaftLite is production-ready.

The following require implementation-specific decisions and evidence:

Exact latency and throughput targets.
Gateway propagation-time targets.
RPO and RTO targets.
Clock-skew bounds.
Supported cluster-membership changes.
Backup and disaster-recovery procedures.
Security certification or compliance requirements.
Maximum supported policy-set size.
13. Change Management

Changes to requirements must preserve stable identifiers where possible.

When a requirement changes, maintainers should update its implementation references, tests, and dependent architecture documents.

Requirements that affect consistency, durability, security, or externally visible API behavior should receive explicit review before implementation.

14. Related Documents
../../README.md — Product overview.
../architecture/overview.md — System architecture.
../architecture/decisions/0001-use-raft-for-policy-consistency.md — Consensus decision.
../architecture/decisions/0002-local-gateway-policy-cache.md — Gateway policy projection.
../architecture/decisions/0003-deterministic-policy-expiration.md — Temporary policy expiration.
../threat-model/threat-model.md — Security threats and mitigations, when created.