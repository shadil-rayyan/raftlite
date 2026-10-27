RaftLite — System Architecture
1. Purpose

This document describes the high-level architecture of RaftLite, a self-hosted, highly available security-policy control plane for distributed API gateways.

It defines the responsibilities of each component, the boundaries between components, the flow of policy updates, and the system's behavior during failures.

This document is the architectural reference for implementation. Detailed requirements belong in the Software Requirements Specification (SRS), while individual protocols and interfaces should be documented separately.

2. Architectural Goals

RaftLite is designed around the following goals:

Consistency: Policy changes must be committed through a consensus mechanism before becoming authoritative cluster state.
Fault tolerance: The control plane must tolerate individual node failures while a voting majority remains available.
Low-latency enforcement: Gateways should evaluate requests using locally available policy data rather than contacting the Raft leader for every request.
Durability: Committed state must survive process restarts and recover through persistent logs and snapshots.
Security: Administrative operations and inter-node communication must be authenticated and protected.
Observability: Operators must be able to inspect cluster health, policy changes, replication progress, and failures.
Modularity: Telemetry processing and anomaly detection must remain separate from the consensus and policy-authority components.
3. Architectural Principles
3.1 Single authoritative policy state

The committed Raft-replicated state is the authoritative source of security policies.

Gateways maintain local policy projections for enforcement. These projections are caches or materialized views, not independent authorities.

Telemetry and anomaly-detection components may generate policy proposals but must not modify authoritative state directly.

3.2 Control plane and data plane separation

The control plane manages policies and maintains their consistent replicated state.

The data plane evaluates incoming requests against locally available policies.

This separation allows gateways to continue enforcing their last known committed policy state during temporary control-plane failures, subject to the configured enforcement mode.

3.3 Consensus before authoritative activation

A policy update must not be reported as committed until the Raft implementation confirms commitment according to the protocol's safety rules.

A successful HTTP request, a locally appended log entry, or a response from an individual follower is not sufficient evidence of commitment.

3.4 Deterministic state transitions

Every committed command must produce the same logical state transition on every node applying that command.

The state machine must not depend on local wall-clock time, random values, network responses, or nondeterministic external services.

4. High-Level Architecture
flowchart TB
    Admin[Security Engineer / Operator]
    Gateway[API Gateway Fleet]

    subgraph CP[RaftLite Control Plane]
        API[Administrative API]
        Leader[Raft Leader]
        F1[Raft Follower]
        F2[Raft Follower]
        SM[Policy State Machine]
        WAL[Durable Raft Storage]
    end

    subgraph DP[Gateway Data Plane]
        Cache[Local Policy Projection]
        Enforce[Request Enforcement]
    end

    Telemetry[Telemetry Drain]
    ML[Anomaly Detection]
    Metrics[Metrics and Monitoring]

    Admin --> API
    API --> Leader
    Leader <--> F1
    Leader <--> F2
    Leader --> SM
    F1 --> SM
    F2 --> SM

    Leader --> WAL
    F1 --> WAL
    F2 --> WAL

    SM --> Cache
    Cache --> Enforce
    Gateway --> Telemetry
    Telemetry --> ML
    ML --> API

    CP --> Metrics
    Gateway --> Metrics


The diagram represents logical responsibilities rather than a claim that every connection or transport has already been implemented. The concrete transport, gateway adapter, and deployment topology must be defined by the implementation.

5. Component Responsibilities
5.1 RaftLite control-plane node

Each control-plane node participates in the Raft cluster.

Responsibilities include:

Maintaining the node's Raft term, voting state, and log.
Participating in leader election.
Replicating log entries.
Determining commitment according to Raft rules.
Applying committed commands to the policy state machine.
Persisting required state.
Recovering after process restart.
Exposing health and operational information.

A node may be a leader, follower, or candidate depending on the current Raft state.

Only a valid leader may coordinate normal writes, and successful commitment still requires the appropriate quorum and protocol conditions.

5.2 Administrative API

The administrative API provides versioned endpoints for policy management and cluster inspection.

Initial responsibilities:

Create a policy.
Revoke or delete a policy.
List policies.
Query a policy decision.
Inspect cluster membership and leader information.
Report operation status.
Enforce authentication, authorization, validation, and idempotency.

The API must distinguish between a request being accepted, a command being replicated, and a command being committed.

5.3 Raft consensus layer

The consensus layer is responsible for agreement on the ordered sequence of commands.

It handles:

Leader election.
Log replication.
Terms and voting.
Commit-index advancement.
Log consistency checks.
Recovery and catch-up.
Snapshot coordination.
Controlled membership changes when implemented.

The consensus implementation must preserve Raft safety properties. Application code must not bypass the consensus layer to modify authoritative policy state.

5.4 Policy state machine

The policy state machine applies committed commands to the authoritative logical policy state.

Initial command types may include:

CREATE_IP_BLOCK
REMOVE_POLICY
CREATE_CIDR_BLOCK
CREATE_IP_ALLOW
SET_RATE_LIMIT

The state machine validates command semantics and applies deterministic transitions.

For example:

Previous state + committed command = New state


A policy command must have well-defined behavior when repeated, rejected, revoked, or superseded.

Expiration and time-dependent policy semantics must be designed explicitly so that replicas do not independently make conflicting authoritative decisions.

5.5 Persistent storage

Each Raft node maintains durable state required for protocol correctness and recovery.

Depending on the chosen Raft implementation, this may include:

Persistent Raft log entries.
Current term and vote.
Snapshot data.
Snapshot metadata.
Commit and applied indices where required by the implementation.

The storage layer must honor the durability guarantees expected by the consensus implementation.

Snapshots must contain sufficient state and metadata to restore the policy state machine at a known log index.

5.6 Gateway adapter

The gateway adapter connects RaftLite's committed policy state to the request-processing layer.

Its responsibilities include:

Receiving committed policy updates.
Maintaining a local policy projection.
Tracking synchronization progress.
Applying policy updates safely.
Exposing policy decisions to the gateway.
Reporting synchronization health.

The initial implementation should define one concrete gateway integration before attempting to support multiple gateway products.

5.7 Gateway enforcement

The gateway evaluates requests against its local policy projection.

A typical request path is:

Identify the effective client IP using a trusted proxy configuration.
Normalize the IP address.
Evaluate applicable IP, CIDR, allow, deny, and rate-limit rules.
Apply the configured enforcement behavior.
Emit appropriate telemetry.

Client IP extraction is security-sensitive. Forwarded headers must not be trusted indiscriminately.

The gateway must never interpret a failed control-plane connection as permission to allow all traffic unless an explicitly configured fail-open policy requires that behavior.

5.8 Rust telemetry drain (temporarily removed)

The Rust telemetry drain is temporarily removed from this branch; see
`docs/rust-python-sidecars.md`. The Go loopback service remains so a future
engine can propose blocks. Telemetry ingestion must not block or bypass the
authoritative policy-commit path.

5.9 Python anomaly-detection engine (temporarily removed)

The Python anomaly-detection engine is temporarily removed from this branch; see
`docs/rust-python-sidecars.md`. It may analyze telemetry and propose policies
through the Go loopback service, but must never directly modify authoritative
state.

It may use statistical rules or an Isolation Forest model to produce findings such as elevated request rates or abnormal authentication failures.

Its output is a policy proposal containing relevant evidence and a suggested action.

The proposal must pass through the administrative policy workflow. Depending on configuration, it may require human approval before submission for commitment.

Anomaly detection must never directly modify authoritative policy state or gateway enforcement state.

5.10 Observability

The observability layer collects operational information from the control plane and gateway fleet.

It should expose:

Node health and Raft role.
Current term and leader changes.
Commit and applied indices.
Replication lag.
Policy operation results.
Policy commit latency.
Gateway synchronization status.
Policy lookup latency.
Audit events.
Telemetry ingestion health.

Metrics, logs, and traces must avoid unnecessarily exposing sensitive information.

6. Policy Update Flow

A policy update follows this logical sequence:

An administrator or authorized automation submits a policy request.
The API authenticates the caller and validates the request.
The API forwards the command to the current leader if necessary.
The leader coordinates replication through Raft.
The command becomes committed only when the protocol's commitment conditions are satisfied.
Nodes apply committed commands to their policy state machines.
The gateway synchronization mechanism propagates the committed policy to gateway-local projections.
Gateways apply the policy to subsequent requests once the update is installed locally.
The system records the operation outcome and relevant audit information.

The API contract must define what its response guarantees. A committed control-plane update does not automatically prove that every gateway has already installed the update.

If immediate fleet-wide enforcement is required, the system must track gateway acknowledgments and define the required acknowledgment policy.

7. Request Enforcement Flow

The request path is intentionally independent of a new Raft consensus operation.

Incoming request
      |
      v
Gateway extracts client IP
      |
      v
Normalize and validate IP
      |
      v
Evaluate local committed-policy projection
      |
      +---- Deny ----> Reject request
      |
      +---- Allow ---> Continue request processing
      |
      v
Emit telemetry


The local projection should support efficient IP and CIDR matching. Rate limiting requires additional state and synchronization semantics and should be implemented only after the initial IP-blocking workflow is reliable.

8. Failure Behavior
8.1 Leader failure

If the leader fails, the remaining nodes may elect a new leader if a majority of voting members can communicate.

Committed policies must remain intact.

Writes may temporarily fail or be delayed during election.

8.2 Follower failure

The cluster may continue accepting writes while the remaining available voters can form a majority.

A recovered follower must reconcile its log and state with the current cluster.

8.3 Loss of quorum

Without a majority, the cluster must not commit new writes.

Nodes must not independently accept conflicting policy changes and later claim both histories were authoritative.

8.4 Gateway disconnection

A gateway may continue enforcing its last known committed policy projection according to the configured enforcement mode.

The system should expose the age and synchronization status of that projection so operators can evaluate the associated risk.

8.5 Storage failure

A node that cannot safely persist protocol-required state must not continue as though its durability guarantees were intact.

The implementation must define how storage errors affect node readiness, write acceptance, recovery, and alerts.

9. Consistency and Gateway Synchronization

Raft consensus governs the authoritative control-plane state. It does not, by itself, guarantee instantaneous installation of every policy at every gateway.

RaftLite must define a separate synchronization contract for gateway consumers.

The initial design should provide:

A monotonically ordered policy revision or equivalent committed log index.
A mechanism for distributing policy changes.
A way to detect missing updates.
A full-state synchronization mechanism for reconnecting gateways.
An acknowledgment or status mechanism.
A clearly defined behavior when a gateway falls behind.

If gateways can miss updates, they must not silently claim to have the latest policy revision.

10. Temporary Policies

Temporary policies require explicit semantics for expiration.

A policy should contain an expiration timestamp or equivalent duration-based metadata.

The effective expiration rule must be consistent across replicas and gateways.

The implementation must define:

Whether expiration is evaluated at the control plane, gateway, or both.
How expiration is represented in replicated state.
How clock skew is handled.
How stale gateway projections behave.
Whether an expired policy requires an explicit replicated removal command.

The state machine must not depend on independently firing local timers to establish conflicting authoritative state.

11. Security Boundaries

The architecture includes three primary trust boundaries:

Administrative boundary: Human operators and automation interacting with the management API.
Consensus boundary: Control-plane nodes communicating over the Raft transport.
Gateway boundary: Policy distribution and enforcement across gateway instances.

Security controls should include, as appropriate:

TLS for inter-node and administrative traffic.
Authentication and role-based authorization.
Input validation and IP/CIDR normalization.
Audit logging.
Secret management.
Trusted-proxy configuration.
Protection against unauthorized policy distribution.
Limits on request sizes and resource consumption.

The threat model must identify which controls are implemented in the MVP and which remain future work.

12. Deployment Topology

The recommended initial topology consists of three Raft voting nodes distributed across independent failure domains where practical.

Three voters require two votes for a majority and can tolerate one unavailable voter.

The nodes should use persistent storage and authenticated communication.

A development environment may use Docker Compose to run the cluster on one machine. This validates application behavior but does not demonstrate resilience to physical host or availability-zone failure.

A production deployment must consider independent hosts, network isolation, storage durability, backups, monitoring, certificate management, and upgrade procedures.

13. Architectural Scope
MVP

The MVP should prioritize:

A three-node Raft cluster.
Correct leader election and log replication.
Durable state and restart recovery.
IP block and unblock policies.
Temporary blocks with documented expiration semantics.
A versioned administrative API.
One gateway integration.
Local gateway policy enforcement.
Health endpoints, metrics, and audit events.
Automated failure and recovery tests.
Future extensions

After the core system is validated, later releases may add:

CIDR and rate-limit policies.
Additional gateway integrations.
Anomaly-detection proposals.
Human approval workflows.
Dry-run and rollback.
Role-based access control.
Kubernetes deployment support.
Advanced dashboards and alerting.

These features must not weaken the core consistency, durability, and security guarantees.

14. Architecture Decision Records

Significant design decisions should be recorded in docs/architecture/decisions/.

Each decision record should include:

Context and problem.
Considered alternatives.
Chosen approach.
Reasons for the choice.
Consequences and trade-offs.
Status of the decision.

Important decisions include the Raft library, storage implementation, gateway integration, policy synchronization protocol, expiration semantics, and enforcement behavior during control-plane outages.

15. Implementation Status

This document describes the intended architecture, not proof that every component exists.

Implementation status must be established from the source code, automated tests, and deployment artifacts.

Do not describe a feature as production-ready merely because its architecture is documented.

The project should distinguish clearly between:

Implemented and tested.
Implemented but not adequately validated.
Planned.
Out of scope.
16. Related Documentation
../../README.md — Product overview, goals, and repository entry point.
../srs/README.md — Software Requirements Specification entry point, if present.
../operations/ — Deployment and operational guidance, as documents are added.
../threat-model/ — Security threats and mitigations, as documents are added.
decisions/ — Architecture decision records.
17. Core Architectural Rule

One authoritative policy history, replicated through consensus, with fast local enforcement at the gateway.

Raft establishes the committed order of policy changes. The state machine defines their meaning. Persistent storage enables recovery. Gateway synchronization distributes committed state. Local enforcement applies it to requests. Telemetry and anomaly detection improve visibility without bypassing the authoritative policy workflow.