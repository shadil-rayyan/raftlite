RaftLite — Software Requirements Specification (SRS)

Document ID: SRS-001
Version: 1.0
Status: Proposed
Product: RaftLite
Category: Distributed Security Policy Control Plane
Parent Document: Product Requirements

1. Introduction
1.1 Purpose

This document specifies the functional and non-functional requirements for RaftLite, a self-hosted security-policy control plane for distributed API gateways and reverse proxies.

The SRS defines the expected system behavior, interfaces, constraints, failure handling, security requirements, and verification criteria.

It serves as a contract between product design, implementation, testing, and evaluation.

The document describes the intended system. A requirement must not be considered implemented or verified merely because it appears here.

1.2 Scope

RaftLite provides a replicated authoritative policy state and a mechanism for distributing committed security policies to gateway consumers.

The initial system includes:

A multi-node control plane using the Raft consensus protocol.
Policy creation, retrieval, deletion, and lookup.
IP and CIDR blocking.
Temporary security policies.
Durable state and recovery.
Gateway-local policy evaluation.
Administrative APIs.
Audit records.
Health checks and operational metrics.
Automated correctness and failure testing.

Telemetry ingestion and anomaly detection are optional extensions. They must not bypass the authoritative policy-management path.

1.3 Intended Audience

This document is intended for:

Software developers.
Distributed-systems engineers.
Test engineers.
Platform and DevOps engineers.
Security engineers.
Project supervisors and evaluators.
Future maintainers of the repository.
1.4 Document Conventions

The following terms have specific meanings:

Shall: A mandatory requirement.
Should: A recommended behavior that may be deferred with justification.
May: An optional capability.
MVP: Minimum viable product.
Raft cluster: The group of voting nodes participating in consensus.
Quorum: The majority of configured voting members required for Raft commitment.
Committed policy: A policy command committed according to the Raft protocol.
Applied policy: A committed command that has been applied to the relevant state machine.
Gateway policy revision: The version or revision of policy state currently applied by a gateway.
Last-known-good policy: The most recent valid committed policy state available to a gateway.
1.5 References
Product Requirements
Documentation Index
Raft consensus protocol and the selected Raft library's documentation.
The API contract in api/openapi.yaml, when created.
Architecture and design documents maintained in docs/architecture/ and docs/design/.
2. Overall Description
2.1 Product Perspective

RaftLite is a distributed control plane that manages security policies for multiple gateway instances.

It separates authoritative policy management from request-time enforcement.

              Administrator
                    |
                    v
              Administrative API
                    |
                    v
           +--------------------+
           | RaftLite Cluster   |
           |                    |
           | Leader Election    |
           | Log Replication    |
           | Policy State       |
           | Durable Storage    |
           +---------+----------+
                     |
              Policy distribution
                     |
          +----------+----------+
          |          |          |
          v          v          v
       Gateway A  Gateway B  Gateway C
          |          |          |
          +----------+----------+
                     |
              Local evaluation
                     |
              Allow or deny


The Raft cluster is authoritative for committed policy changes. Gateways maintain local policy state for efficient enforcement.

Gateway synchronization is a separate protocol responsibility. Raft consensus alone does not guarantee that every gateway has immediately received a committed policy.

2.2 Product Functions

The system shall provide the following capabilities:

Manage IP-block policies.
Manage CIDR-block policies.
Support temporary policy expiration.
Maintain authoritative state through Raft.
Persist sufficient state for safe recovery.
Distribute committed policies to gateways.
Expose administrative and operational APIs.
Record policy-management activity.
Expose cluster health and relevant metrics.
Support reproducible testing of failures and recovery.
2.3 User Classes
User or actor	Responsibilities
Security administrator	Create, inspect, and revoke policies
Platform engineer	Configure and deploy the cluster
SRE	Monitor cluster health and diagnose failures
API gateway	Consume and enforce policy revisions
Anomaly detector	Optionally propose policy changes
Evaluator	Inspect implementation and verification evidence
2.4 Operating Environment

The MVP shall support a documented local development environment capable of running a multi-node cluster.

Docker Compose is the intended development deployment mechanism.

Future deployments may include:

Linux virtual machines.
Bare-metal servers.
Kubernetes.
Container platforms.

Support for a deployment environment shall not be claimed until its configuration and relevant tests are available.

2.5 Design Constraints
The control plane shall use a Raft-based consensus implementation.
Only committed commands shall modify authoritative policy state.
Gateway request processing shall not require a new Raft consensus round trip for every policy lookup.
Policy application shall be deterministic for the same committed command sequence.
Administrative writes shall not be reported as committed without the required consensus guarantees.
Persistence and recovery behavior shall respect the selected Raft implementation's requirements.
Security-sensitive interfaces shall have documented authentication and trust boundaries.
2.6 Assumptions and Dependencies

The system assumes:

Nodes can communicate over configured network interfaces.
Voting membership is explicitly configured.
The underlying storage can provide the durability guarantees required by the selected Raft implementation.
Gateway integrations can receive policy updates and maintain local state.
The deployment has a defined method for identifying client IP addresses.
Operators can securely configure credentials and certificates.

A violation of these assumptions must produce documented failure behavior rather than silent corruption or false success.

3. System Scope and Boundaries
3.1 Included in the MVP
Raft-based replicated control plane.
Policy state machine.
IP and CIDR block policies.
Temporary block policies.
Administrative APIs.
Gateway policy synchronization.
Local gateway enforcement.
Persistent state and recovery.
Basic audit records.
Health checks and metrics.
Integration, end-to-end, and failure tests.
3.2 Excluded from the MVP
Full web application firewall functionality.
Global DDoS mitigation.
General-purpose database functionality.
Full SIEM capabilities.
Autonomous machine-learning enforcement.
Multi-region consensus.
Advanced policy-language support.
Complete commercial dashboard.
Production certification.
3.3 System Boundary

RaftLite manages policy authority and distribution. The API gateway processes client requests and applies the policy revision available locally.

The MVP does not guarantee that all gateways enforce a new policy at precisely the same instant. It must expose or document propagation behavior and ensure that the authoritative state remains consistent.

4. Functional Requirements
4.1 Cluster and Consensus
FR-001 — Multi-node operation

The system shall support a cluster consisting of multiple configured Raft voting nodes.

Acceptance criteria:

A three-node development cluster can start successfully.
Each node has a distinct configured identity.
Nodes can discover or connect to their configured peers.
Cluster membership is not inferred from untrusted requests.
FR-002 — Leader election

The system shall elect a leader according to the selected Raft implementation.

Acceptance criteria:

A functioning cluster eventually elects one leader under the documented timing and network assumptions.
Leader status is observable.
Leader failure can trigger a subsequent election when quorum remains available.
FR-003 — Quorum-based commitment

The system shall commit policy commands only according to the safety and commitment rules of the selected Raft implementation.

Acceptance criteria:

A valid policy command is committed when the cluster can make progress and the applicable Raft commitment conditions are satisfied.
A node without quorum does not independently declare a new policy command committed.
Tests verify the expected behavior during quorum loss.
FR-004 — Ordered state-machine application

The system shall apply committed policy commands in the order required by the replicated state machine.

Acceptance criteria:

Each healthy state machine applies the same committed command sequence.
Identical initial state and committed command sequence produce equivalent policy state.
Conflicting policy operations follow documented semantics.
FR-005 — Leader failure handling

The system shall recover leadership after a leader failure when the remaining voting nodes can form a quorum and make progress.

Acceptance criteria:

A test can terminate the current leader.
The remaining cluster elects a replacement under the test's stated assumptions.
Previously committed policy state remains intact.
FR-006 — Follower failure handling

The system shall continue safe cluster operation when a follower fails, provided the remaining nodes can satisfy quorum requirements.

Acceptance criteria:

A three-node cluster can continue eligible writes after one follower becomes unavailable.
The failed follower can later recover and synchronize.
FR-007 — Quorum-loss behavior

The system shall not report unsafe writes as committed when quorum is unavailable.

Acceptance criteria:

A minority partition cannot independently commit a conflicting authoritative policy history.
The API communicates that the requested write could not be confirmed as committed.
The result distinguishes a known rejection from an operation whose outcome is uncertain due to communication failure.
4.2 Policy Management
FR-008 — Create IP-block policy

The system shall allow an authorized caller to request an IP-block policy.

Acceptance criteria:

Valid IPv4 and IPv6 addresses are accepted if both families are supported by the implementation.
Invalid addresses are rejected.
The resulting policy has a stable identifier.
The API returns the actual operation status.
FR-009 — Remove or revoke policy

The system shall support removal or revocation of an existing policy.

Acceptance criteria:

A valid policy identifier can be revoked.
An unknown identifier produces a documented response.
Revocation follows the authoritative command path.
The resulting change is reflected in policy state and distributed to consumers.
FR-010 — Create CIDR policy

The system shall support CIDR-based blocking for supported address families.

Acceptance criteria:

Valid network prefixes are accepted.
Invalid prefix lengths and malformed addresses are rejected.
Matching behavior is covered by automated tests.
FR-011 — Temporary policies

The system shall support policies with an expiration time or duration.

Acceptance criteria:

Invalid durations are rejected.
The authoritative state records sufficient information to determine expiration.
Expiration behavior is deterministic and documented.
Gateways eventually stop enforcing an expired policy according to the defined expiration and propagation semantics.
FR-012 — List policies

The system shall expose an API for retrieving policies visible in the authoritative state.

Acceptance criteria:

The response contains the documented policy fields.
Pagination or bounded response behavior is provided when needed.
Authorization is enforced.
Ordering and consistency semantics are documented.
FR-013 — Retrieve individual policy

The system shall expose an API for retrieving a policy by identifier.

Acceptance criteria:

Existing policies can be retrieved.
Missing policies produce a documented response.
Returned fields conform to the API contract.
FR-014 — Policy lookup

The system shall support evaluating whether an IP address matches an applicable committed policy.

Acceptance criteria:

Exact IP and supported CIDR matches are tested.
Nonmatching addresses are handled correctly.
Policy precedence is deterministic.
Lookup behavior is consistent with the documented semantics.
FR-015 — Input validation

The system shall validate policy requests before submitting them for commitment.

Acceptance criteria:

Invalid addresses, durations, identifiers, and required fields are rejected.
Rejected requests do not mutate authoritative policy state.
Errors provide sufficient information for callers to correct invalid requests without exposing secrets.
FR-016 — Idempotent administrative operations

The system shall support safe retries for policy-creation operations through a documented idempotency mechanism or equivalent request-deduplication behavior.

Acceptance criteria:

Repeating the same request with the same idempotency key does not unintentionally create multiple logical operations.
Reusing a key with an incompatible request produces a documented error.
Retry behavior remains correct when the original response is lost.
FR-017 — Policy versioning

The system shall associate policy state with a version, revision, or equivalent identifier sufficient to distinguish changes.

Acceptance criteria:

A policy change can be associated with the relevant committed revision.
Consumers can identify the revision they have applied.
Revision semantics are documented and testable.
4.3 Gateway Integration
FR-018 — Gateway policy synchronization

The system shall provide a defined mechanism for delivering committed policy updates to configured gateways or policy agents.

Acceptance criteria:

A gateway can obtain an initial policy state.
A gateway can receive subsequent updates.
A disconnected gateway can resume synchronization.
Delivery and applied-revision status are observable where supported.
FR-019 — Local policy evaluation

The gateway integration shall support policy evaluation using locally available policy state without a Raft consensus round trip for each request.

Acceptance criteria:

The request path uses a local policy representation.
A control-plane outage does not force every request to perform a remote consensus operation.
Lookup latency is measurable independently from policy-commit latency.
FR-020 — Gateway failure mode

The system shall document and support the configured behavior when a gateway cannot contact the control plane.

Acceptance criteria:

The behavior is explicit rather than silently defaulting to allow-all.
The last-known-good policy mode is supported by the MVP integration.
Operators can determine the configured mode.
The behavior during stale-policy conditions is documented.
FR-021 — Gateway revision reporting

The system shall expose sufficient information to identify the policy revision applied by a gateway or its policy agent.

Acceptance criteria:

An operator can identify the applied revision for a reporting consumer.
A stale or disconnected consumer can be distinguished from a synchronized consumer where reporting is available.
4.4 Persistence and Recovery
FR-022 — Durable Raft state

The system shall persist the Raft state required by the selected consensus implementation.

Acceptance criteria:

Required persistent metadata and log entries survive process restart under the documented storage assumptions.
Persistence failures are surfaced.
Storage writes and synchronization follow the selected library's requirements.
FR-023 — State recovery

The system shall recover its policy state after restart using the supported durable recovery mechanism.

Acceptance criteria:

Previously committed policy state can be reconstructed.
Uncommitted commands are not incorrectly treated as committed.
Recovery behavior is tested after abrupt process termination.
FR-024 — Snapshot support

The system shall support snapshots or the selected Raft implementation's equivalent state-compaction mechanism when required by the storage design.

Acceptance criteria:

A snapshot corresponds to a defined applied state or log position.
Recovery can restore the snapshot and process subsequent entries correctly.
Snapshot installation does not silently corrupt policy state.
FR-025 — Follower catch-up

The system shall allow a recovered or lagging follower to synchronize with the authoritative committed history.

Acceptance criteria:

A disconnected follower can reconnect.
The follower catches up using log replication, snapshot installation, or the supported recovery mechanism.
Its resulting policy state matches the authoritative state.
4.5 API and Operations
FR-026 — Versioned API

Administrative endpoints shall use a documented API version.

Acceptance criteria:

The MVP exposes its supported API version.
Request and response formats are documented.
Error responses follow a consistent contract.
FR-027 — Liveness endpoint

The system shall expose a liveness endpoint that indicates whether the process is alive and able to perform its basic runtime duties.

Acceptance criteria:

The endpoint responds under normal process operation.
Its semantics are distinct from cluster write readiness.
FR-028 — Readiness endpoint

The system shall expose readiness information that distinguishes process liveness from the ability to perform the endpoint's intended operation.

Acceptance criteria:

Readiness semantics are documented.
A node that cannot safely accept writes is not falsely reported as write-ready.
The endpoint does not imply that every API operation is available merely because the process is alive.
FR-029 — Cluster status

The system shall expose cluster status information appropriate for authorized operational use.

Acceptance criteria:

The interface identifies known node states and leadership information.
Relevant quorum or connectivity problems are reported where the implementation can determine them.
Unavailable information is not presented as confirmed health.
FR-030 — Operational metrics

The system shall expose metrics for relevant control-plane and policy operations.

The initial metrics should cover:

Request totals and errors.
Policy commits and rejections.
Leader changes and elections.
Replication progress or lag where available.
Commit and applied revisions.
Policy lookup and commit latency.
Policy counts.
Recovery and snapshot activity where available.

Acceptance criteria:

Metrics use documented names and types.
Metrics can be scraped in the development environment.
Labels do not expose unbounded or sensitive values unnecessarily.
FR-031 — Audit records

The system shall record security-relevant administrative policy operations.

Acceptance criteria:

Records identify the operation, target, timestamp, actor where available, and outcome.
Supplied reasons and request identifiers are recorded where appropriate.
Failed requests and uncertain outcomes are distinguishable from successful commits.
Audit records do not contain credentials or other unnecessary secrets.
4.6 Optional Telemetry and Anomaly Detection
FR-032 — Telemetry ingestion extension

If the telemetry component is enabled, it shall ingest supported gateway events using a documented event schema.

Acceptance criteria:

Valid events can be processed.
Malformed events are handled according to a documented error policy.
Ingestion failures do not silently corrupt authoritative policy state.
FR-033 — Anomaly proposal extension

If anomaly detection is enabled, the analytical component shall produce policy proposals rather than directly modifying authoritative policy state.

Acceptance criteria:

A proposal contains a target, rationale or supporting metadata, and proposed action.
A proposal must pass through the authorized policy-management path.
The configured approval policy is respected.
A proposal is not described as an active committed policy until it has actually been committed.

These requirements are optional for the core MVP unless explicitly included in the approved project scope.

5. Non-Functional Requirements

The following requirements define quality attributes. Where a numerical target is needed, the project must establish it through an explicit decision and verification method rather than inventing a performance guarantee.

NFR-001 — Consistency

The control plane shall preserve the safety guarantees of the selected Raft implementation for committed policy commands.

Verification: Consensus integration tests and failure-injection tests.

NFR-002 — Durability

Committed state shall survive supported process-restart scenarios under the documented storage and hardware assumptions.

Verification: Crash-recovery tests and persistence tests.

NFR-003 — Availability under node failure

A three-voter cluster should continue eligible policy writes after one voting node fails, provided the remaining nodes can form a quorum and make progress.

Verification: Leader and follower failure tests.

NFR-004 — Quorum safety

The system shall not independently commit conflicting policy histories in minority partitions.

Verification: Network-partition tests.

NFR-005 — Local lookup performance

Gateway policy lookup shall not require a new consensus round trip for each request.

Verification: Architecture review and gateway benchmarks.

A numerical latency target must be documented with its workload and test environment before being used as an acceptance threshold.

NFR-006 — Policy propagation observability

The system should expose enough revision and synchronization information to measure policy propagation.

Verification: Integration tests that create a policy and observe consumer revision progress.

NFR-007 — Security

Administrative and inter-node interfaces shall implement the authentication, authorization, encryption, and trust-boundary controls defined for the selected deployment.

Verification: Security tests and configuration review.

NFR-008 — Input robustness

Malformed requests and unsupported policy values shall be rejected without corrupting policy state.

Verification: Unit tests, integration tests, and fuzz testing where appropriate.

NFR-009 — Observability

The system shall provide structured operational logs, health endpoints, and relevant metrics.

Verification: Automated checks and operational walkthroughs.

NFR-010 — Recoverability

A failed node shall be able to recover using supported persistent-state and synchronization mechanisms.

Verification: Restart, snapshot, and follower catch-up tests.

NFR-011 — Deployability

A developer shall be able to start the documented MVP environment and execute the core tests using repository instructions.

Verification: A clean-environment deployment test.

NFR-012 — Maintainability

The implementation shall separate consensus, policy semantics, transport, persistence integration, gateway synchronization, and optional analytics into understandable components.

Verification: Code review and architecture review.

NFR-013 — Testability

Failure scenarios shall be reproducible where practical and produce evidence sufficient to diagnose a failed assertion.

Verification: Repeatable automated test runs and recorded scenario configuration.

NFR-014 — Resource visibility

Benchmarks shall report relevant CPU, memory, latency, and throughput measurements for the tested workload.

Verification: Reproducible benchmark runs.

NFR-015 — Time and expiration semantics

The system shall document the clock assumptions used for temporary policy expiration and shall not depend on independently mutating authoritative state through unrelated local timers.

Verification: Expiration tests covering delayed processing, restart, and clock-related edge cases supported by the design.

6. External Interface Requirements
6.1 Administrative HTTP API

The MVP should expose versioned endpoints similar to the following.

Method	Endpoint	Purpose
GET	/healthz	Process liveness
GET	/readyz	Readiness status
GET	/v1/leader	Leadership information
GET	/v1/cluster	Cluster status
GET	/v1/policies	List policies
POST	/v1/policies/ip-blocks	Create an IP block
GET	/v1/policies/{id}	Retrieve a policy
DELETE	/v1/policies/{id}	Revoke or delete a policy
GET	/v1/blocked/{ip}	Query blocking status

The final route names and request schemas shall be defined in the API contract.

The API must document authentication, authorization, error responses, idempotency, and consistency behavior.

6.2 Request and Response Format

The administrative API should use JSON over HTTP.

Requests shall be validated before policy commands are submitted for commitment.

Responses shall distinguish validation errors, authorization failures, confirmed commitment, and operations whose outcomes remain uncertain.

6.3 Internal Consensus Communication

Raft communication shall use the transport and message semantics required by the selected Raft implementation.

The implementation shall not modify consensus message handling in a way that violates the protocol's safety requirements.

6.4 Gateway Synchronization Interface

The gateway integration shall define:

Initial policy synchronization.
Incremental policy updates.
Revision identification.
Reconnection and catch-up.
Error handling.
Authentication.
Stale-policy behavior.

The specific transport, such as HTTP streaming or gRPC, is an architectural decision rather than a requirement to implement every option.

6.5 Telemetry Interface

If telemetry is enabled, its event schema and ingestion protocol shall be documented separately.

Telemetry transport failures must not cause uncommitted policy changes to be treated as authoritative.

7. Policy Data Requirements
7.1 Policy Fields

The initial policy model should support the following fields where applicable:

Field	Meaning
id	Stable policy identifier
type	Policy type
target	IP address, CIDR, or supported target
action	Enforceable action
created_at	Creation timestamp
expires_at	Optional expiration timestamp
reason	Human-readable rationale
source	Origin of the request
created_by	Actor or service identity
version	Policy revision or version
status	Defined policy lifecycle state

The final schema shall be defined in the policy-model design document.

7.2 Policy Precedence

The implementation shall define how conflicting rules are resolved.

The policy engine must not rely on unspecified map iteration order or other nondeterministic behavior to choose between conflicting policies.

7.3 Expiration

Expiration behavior shall specify:

Whether expiration is based on a committed timestamp or another defined representation.
How expiration is evaluated by the state machine and gateway.
What happens during control-plane disconnection.
How restart and recovery affect expiration.
How clock skew is handled.
7.4 Policy History

Policy changes shall be represented through the supported authoritative command path.

A rollback, if implemented, shall be a new policy operation rather than an untracked mutation of committed history.

8. Error Handling Requirements

The system shall distinguish, where applicable:

Invalid input.
Authentication failure.
Authorization failure.
Missing policy.
Duplicate or conflicting request.
No current leader.
Quorum unavailable.
Network timeout.
Storage failure.
Replication failure.
Snapshot or recovery failure.
Gateway synchronization failure.
Unknown operation outcome.

A client timeout does not prove that a policy command was rejected. If the operation may have committed before the response was lost, the API must provide a safe way to determine its outcome or retry idempotently.

Errors shall not expose internal secrets, credentials, or unnecessary sensitive information.

9. Security Requirements
SEC-001 — Administrative authentication

Policy-management endpoints shall require authentication in deployments that expose them beyond an explicitly isolated development environment.

SEC-002 — Authorization

The system shall restrict policy modifications to authorized actors.

SEC-003 — Inter-node authentication

Cluster communication shall use a documented mechanism for authenticating authorized members.

SEC-004 — Transport protection

Sensitive administrative and inter-node communication shall support encryption in the approved deployment configuration.

SEC-005 — Secret management

Credentials, private keys, and tokens shall not be hard-coded into the repository or logged in plaintext.

SEC-006 — Input validation

All externally supplied policy fields shall be validated before they affect authoritative state.

SEC-007 — Trusted client address

The gateway integration shall define how the client IP is obtained and how forwarded headers are trusted.

SEC-008 — Audit integrity

Audit records shall not silently describe rejected or uncertain operations as successfully committed.

SEC-009 — Secure failure behavior

A failure to contact the policy control plane shall not silently reset a gateway to allow-all behavior.

The gateway shall use its explicitly configured failure mode.

SEC-010 — Least privilege

Administrative, operational, and internal communication interfaces should expose only the permissions required by their intended actors.

10. Logging and Monitoring Requirements
10.1 Structured Logging

The system should emit structured logs for:

Startup and shutdown.
Leader election.
Leadership changes.
Policy command submission.
Policy commitment and application.
Replication problems.
Storage failures.
Snapshot activity.
Recovery.
Gateway synchronization.
Authorization failures.
Configuration errors.

Logs should include relevant identifiers and timestamps without exposing secrets.

10.2 Metrics

The implementation should expose appropriate metrics for:

Request count and errors.
Policy creation and revocation.
Commit latency.
Gateway lookup latency.
Leader changes.
Election activity.
Replication progress.
Applied policy revisions.
Policy count.
Snapshot and recovery activity.

Metric names and labels shall be documented.

10.3 Health Semantics

Liveness and readiness shall be distinct.

A process can be alive while the cluster lacks quorum. The health interface must not imply that the node can safely accept policy writes when it cannot.

11. Testing and Verification Requirements
11.1 Unit Tests

Unit tests shall cover:

IP and CIDR validation.
Policy matching.
Policy precedence.
Policy creation and revocation.
State-machine application.
Expiration semantics.
Idempotency.
Request validation.
Serialization and deserialization.
11.2 Integration Tests

Integration tests shall cover:

Multi-node startup.
Leader election.
Policy replication.
API behavior.
Persistence and restart.
Snapshot restoration.
Gateway synchronization.
Audit record generation.
11.3 Failure Tests

The test suite shall cover:

Leader termination.
Follower termination.
Loss of quorum.
Network partitions.
Delayed or dropped messages where the test harness supports them.
Storage errors where feasible.
Node restart.
Follower catch-up.
Gateway disconnection.
Uncertain client outcomes after timeouts.
11.4 End-to-End Tests

An end-to-end test shall demonstrate the core product workflow:

Start the policy cluster.
Start the gateway integration.
Create a block policy.
Confirm commitment.
Confirm gateway application.
Send a matching request.
Verify enforcement.
Fail a control-plane node.
Verify safe cluster behavior.
Restore the failed node.
Verify synchronization.
11.5 Performance Tests

Benchmarks shall measure policy lookup, policy commit, propagation delay, request overhead, throughput, resource usage, and recovery time as appropriate.

Each benchmark report shall state:

Hardware and operating system.
Software versions.
Cluster size.
Policy count.
Request workload.
Number of measurements.
Measurement tool.
Latency percentiles.
Relevant resource consumption.
11.6 Verification Evidence

Each implemented requirement should be traceable to a test, inspection, analysis, or documented manual verification procedure.

Passing unit tests alone does not establish distributed-system safety.

12. Acceptance Criteria

The MVP is ready for project-level acceptance when all mandatory MVP requirements have implementation evidence and the following scenarios pass.

ID	Scenario	Expected result
AC-001	Start a three-node cluster	Cluster starts and elects a leader
AC-002	Create a valid IP block	Command is committed and policy becomes authoritative
AC-003	Submit invalid policy input	Request is rejected without state mutation
AC-004	Query a blocked IP	The result matches committed policy state
AC-005	Enforce policy at a gateway	Gateway rejects matching traffic according to the configured response
AC-006	Terminate one voting node	Remaining majority can continue eligible writes
AC-007	Remove quorum	Unsafe writes are not reported as committed
AC-008	Restart a node	Committed state is recovered
AC-009	Reconnect a stale follower	Follower catches up with authoritative state
AC-010	Create a temporary policy	Expiration follows documented semantics
AC-011	Disconnect the control plane	Gateway follows its configured stale-policy behavior
AC-012	Inspect audit records	Policy operations and their outcomes are traceable
AC-013	Inspect operational metrics	Relevant health and policy metrics are available
AC-014	Run the documented test workflow	Tests execute reproducibly in the supported environment

A failed mandatory acceptance criterion blocks the corresponding claim of completion.

Security-sensitive release decisions require additional review beyond this functional acceptance table.

13. Requirements Traceability

Requirements shall be tracked in traceability.md.

Each requirement should map to:

An implementation component.
A design or interface contract.
One or more verification procedures.
A current implementation status.
Evidence supporting the status.

Recommended statuses:

Proposed.
Planned.
In progress.
Implemented.
Verified.
Deferred.
Retired.

A requirement is Verified only when the defined acceptance criteria have been satisfied and the evidence is recorded.

14. Limitations and Risks
14.1 Policy propagation delay

Raft commitment and gateway application are separate events. A gateway may temporarily enforce an older policy revision.

14.2 Loss of quorum

The cluster cannot safely commit new writes without the required quorum. Existing gateway policy state may remain usable.

14.3 Clock behavior

Temporary policies require a documented expiration model and consideration of clock differences.

14.4 Storage durability

Crash recovery depends on correct storage behavior and adherence to the selected Raft library's persistence requirements.

14.5 Incorrect policy decisions

A technically correct control plane can still distribute an incorrect security policy. Authorization, validation, auditability, and optional approval workflows reduce this risk but cannot eliminate it.

14.6 Scope limitations

The MVP is not a complete WAF or a substitute for managed security infrastructure, threat intelligence, or network-level DDoS protection.

15. Future Enhancements

Potential enhancements include:

Rate-limit policies.
Human approval workflows.
Anomaly-driven proposals.
Dry-run policy evaluation.
Policy rollback.
Advanced role-based access control.
Kubernetes packaging.
More gateway integrations.
Alerting and SIEM integration.
Multi-cluster management.

Each enhancement shall have a defined scope and verification criteria before implementation.

16. SRS Completion Criteria

This SRS is ready for implementation planning when:

The MVP scope has been approved.
Every mandatory requirement has a stable identifier.
External API contracts are defined or assigned for completion.
Policy precedence and expiration semantics are specified.
Gateway synchronization behavior is documented.
Failure and recovery expectations are explicit.
Security requirements have identified verification methods.
Measurable performance targets are approved where required.
Requirements are mapped to planned implementation and tests.
Unresolved assumptions are recorded rather than silently treated as facts.

Final principle: RaftLite shall be evaluated by the correctness and reliability of the security-policy workflow it delivers, not merely by whether it can elect a leader or replicate a log.