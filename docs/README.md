RaftLite Documentation

This directory contains the product specification, software requirements, architecture, technical design, operational procedures, and verification documentation for RaftLite.

The documentation describes the intended system behavior and the engineering decisions required to build and validate a distributed security-policy control plane for self-hosted API gateways.

The root README.md is the entry point for the project. This document is the entry point for its detailed engineering documentation.

Documentation principle: Requirements define what the system must do. Architecture defines how the system is organized. Design documents define component behavior and interfaces. Tests provide evidence of what the implementation actually satisfies.
1. Documentation Structure
docs/
├── README.md
│
├── product/
│   └── product-requirements.md
│
├── requirements/
│   ├── SRS.md
│   └── traceability.md
│
├── architecture/
│   ├── overview.md
│   ├── consistency-and-failures.md
│   ├── security-model.md
│   └── decisions/
│       ├── ADR-001-consensus-library.md
│       ├── ADR-002-gateway-policy-cache.md
│       └── ADR-003-policy-expiration.md
│
├── design/
│   ├── policy-model.md
│   ├── api-design.md
│   └── data-storage.md
│
├── operations/
│   ├── deployment.md
│   ├── configuration.md
│   ├── monitoring.md
│   ├── backup-and-recovery.md
│   └── troubleshooting.md
│
├── testing/
│   ├── strategy.md
│   ├── failure-injection.md
│   └── benchmarks.md
│
├── use-cases.md
├── roadmap.md
└── interview-guide.md


This is the intended documentation structure. Files should be added as their corresponding decisions, requirements, or implementations become sufficiently defined.

Do not create empty documents simply to match the tree.

2. Start Here

Choose the document that matches your task.

If you want to understand...	Read
What RaftLite is and how to run the repository	Root README
Why the product exists and who it serves	Product Requirements
What the software must do	Software Requirements Specification
How requirements map to implementation and tests	Requirements Traceability
How the complete system fits together	Architecture Overview
What happens during leader failure or quorum loss	Consistency and Failure Handling
Which security threats must be addressed	Security Model
How policies are represented and evaluated	Policy Model
How the administrative API behaves	API Design
How persistence and recovery work	Data Storage Design
How the product is deployed	Deployment Guide
How to operate and monitor the system	Monitoring Guide
How correctness and reliability are verified	Testing Strategy
How performance is measured	Benchmarking Guide
What features are in scope and what comes later	Roadmap
How to explain technical decisions to an evaluator	Interview Guide
3. Product Documentation
product/product-requirements.md

Defines the product problem, target users, intended deployment environment, value proposition, competitive alternatives, scope, non-goals, and roadmap priorities.

This document answers:

Who is the intended customer?
Who uses the system day to day?
What operational problem does RaftLite solve?
Why might existing solutions be insufficient for the target deployment?
What belongs in the MVP?
Which assumptions require customer validation?

The product requirements guide the SRS and help prevent implementation work from drifting away from the intended use case.

4. Requirements Documentation
requirements/SRS.md

The Software Requirements Specification defines externally observable system behavior and the constraints under which the system must operate.

It covers:

System scope and stakeholders.
Functional requirements.
Non-functional requirements.
External interfaces.
Policy management.
Consensus and persistence.
Gateway integration.
Security and auditing.
Monitoring and deployment.
Verification and acceptance criteria.

Each requirement should have a stable identifier, a clear statement, and a testable acceptance criterion whenever practical.

requirements/traceability.md

Maps requirements to the implementation and verification evidence.

A traceability entry should identify:

Requirement ID.
Requirement summary.
Relevant design document.
Implementation component.
Verification test.
Current status.
Evidence or outstanding work.

The document must distinguish implemented functionality from planned work.

5. Architecture Documentation
architecture/overview.md

Describes the system's major components, responsibilities, interfaces, deployment topology, and data flows.

It defines the relationship between:

Go control plane.
Raft consensus.
Policy state machine.
Durable storage.
Gateway integration.
Rust telemetry drain.
Python anomaly-detection engine.
Monitoring and administration.

It also establishes the distinction between the authoritative policy state and gateway-local policy replicas.

architecture/consistency-and-failures.md

Defines the correctness and failure semantics of the distributed system.

Topics include:

Raft leadership and terms.
Quorum requirements.
Commit and apply semantics.
Concurrent policy changes.
Network partitions.
Leader and follower failures.
Persistent-state recovery.
Snapshot installation.
Gateway synchronization.
Behavior during loss of control-plane connectivity.
architecture/security-model.md

Defines assets, trust boundaries, threats, security assumptions, and required mitigations.

Topics include administrative authentication, authorization, node identity, transport encryption, input validation, secret management, auditability, gateway client-IP identification, and failure behavior.

architecture/decisions/

Contains Architecture Decision Records (ADRs).

Each ADR records a significant technical decision, the context that motivated it, the alternatives considered, the decision made, and its consequences.

ADRs should be written when a decision has meaningful long-term implications, rather than for every minor implementation choice.

6. Technical Design Documentation
design/policy-model.md

Defines the policy data model and its semantics.

Topics include:

Policy identifiers and versions.
Supported policy types.
IP and CIDR validation.
Allow/deny precedence.
Policy lifecycle.
Temporary policies and expiration.
Revocation and idempotency.
Policy distribution revisions.
Dry-run and rollback semantics, when implemented.
design/api-design.md

Defines administrative API behavior and external contracts.

Topics include:

API versioning.
Request and response formats.
Validation and error handling.
Authentication and authorization.
Idempotency.
Policy creation, deletion, and lookup.
Cluster status.
Health and readiness.
Retry and timeout semantics.

The machine-readable API definition belongs in api/openapi.yaml, while this document explains design intent and behavior.

design/data-storage.md

Defines the persistence model and crash-recovery requirements.

Topics include:

Raft metadata.
Write-ahead logging.
Log durability.
Snapshot creation and installation.
State reconstruction.
Crash consistency.
Disk errors.
Recovery of restarted nodes.
Persistent policy state.

The document must distinguish the guarantees of the selected Raft implementation from the guarantees provided by RaftLite's own storage and state-machine code.

7. Operations Documentation
operations/deployment.md

Describes supported deployment environments and deployment procedures.

Topics include local development, multi-node configuration, container deployment, persistent storage, startup and shutdown, upgrades, and deployment verification.

operations/configuration.md

Documents configuration fields, defaults, validation rules, security-sensitive settings, and environment-specific overrides.

Example configuration files belong in configs/.

operations/monitoring.md

Defines the metrics, logs, health checks, alerts, and dashboards required to understand the system's operational state.

It should cover cluster health, leader changes, policy commits, replication progress, policy propagation, request latency, and error rates.

operations/backup-and-recovery.md

Defines backup scope, recovery prerequisites, supported recovery procedures, and verification of restored state.

A backup procedure must preserve the consistency requirements of the selected storage and consensus implementation.

operations/troubleshooting.md

Provides practical diagnostic procedures for common problems, such as:

No elected leader.
Loss of quorum.
Failed policy writes.
Follower replication lag.
Storage failures.
Snapshot installation errors.
Gateway synchronization failures.
Expired or unexpectedly enforced policies.

Each procedure should describe observable symptoms, diagnostic steps, safe remediation, and verification.

8. Testing Documentation
testing/strategy.md

Defines the overall verification strategy and how tests establish that requirements are satisfied.

It covers unit, integration, end-to-end, security, recovery, simulation, failure-injection, and performance tests.

testing/failure-injection.md

Documents reproducible failure scenarios, including node crashes, network partitions, dropped or delayed messages, storage errors, and recovery.

Where supported, simulations should use reproducible seeds and record the scenario configuration and observed result.

testing/benchmarks.md

Defines the benchmark methodology for policy lookups, policy commits, policy propagation, gateway request latency, throughput, resource usage, and recovery time.

Every reported performance result should include its workload, environment, configuration, sample count, and measurement methodology.

9. Use Cases and Roadmap
use-cases.md

Describes system interactions from the perspective of administrators, gateways, operators, and optional analytical components.

Each use case should include an actor, preconditions, main flow, alternative flows, failure conditions, and acceptance criteria where applicable.

roadmap.md

Describes the MVP, subsequent development phases, deferred capabilities, and the acceptance gates that determine when work can progress.

Roadmap entries represent planned work, not claims of existing functionality.

10. Interview and Evaluation Guide
interview-guide.md

Helps explain the engineering decisions and evidence behind RaftLite.

It should cover:

The original product problem.
Why Raft is used.
Quorum and leader failure.
Consistency and availability trade-offs.
WAL and snapshots.
Gateway-local policy evaluation.
Failure handling and recovery.
Security boundaries.
Alternatives such as Redis and etcd.
Benchmark methodology.
Known limitations and future work.

Answers should reflect the actual implementation and measured test results. They must not claim guarantees that the code has not demonstrated.

11. Documentation Conventions
Requirement identifiers

Use stable identifiers for requirements:

FR-001, FR-002, and so on for functional requirements.
NFR-001, NFR-002, and so on for non-functional requirements.
UC-001, UC-002, and so on for use cases.
ADR-001, ADR-002, and so on for architecture decisions.

Do not reuse an identifier for a different requirement after the original requirement has been removed. Mark it as retired and create a new identifier when necessary.

Requirement quality

Requirements should describe observable behavior rather than dictate implementation details unnecessarily.

For example:

Weak: The system should be fast.

Better: The system shall evaluate a policy against gateway-local state without requiring a Raft consensus round trip for each HTTP request.

Measurable performance requirement: Specify a target latency percentile, workload, hardware, and measurement method in the SRS after establishing a baseline.

Implementation status

Use explicit status labels where useful:

Proposed: Under consideration.
Planned: Approved for future implementation.
In progress: Being implemented.
Implemented: Present in the code.
Verified: Meets its defined acceptance criteria with recorded evidence.
Deferred: Intentionally postponed.
Retired: No longer supported or required.

Implemented does not automatically mean verified.

Architecture diagrams

Diagrams should show meaningful system boundaries, responsibilities, and data flows. Keep detailed protocol and recovery sequences in their relevant architecture documents instead of duplicating them throughout the repository.

Keeping documentation current

A change to behavior or an interface should update the relevant specification and tests in the same change whenever practical.

Pull requests should identify affected requirements, documentation, and verification evidence.

12. Recommended Reading Order

For someone new to RaftLite:

Start with the root README.md.
Read the Product Requirements.
Read the Software Requirements Specification.
Read the Architecture Overview.
Study Consistency and Failure Handling.
Review the Policy Model and Data Storage Design.
Follow the Testing Strategy.
Consult the operations documentation before deploying or diagnosing the system.

Developers should use the design documents alongside the code. Evaluators should use the requirements and testing evidence to distinguish intended behavior from demonstrated behavior.

Guiding Principle

RaftLite's documentation exists to support engineering decisions, implementation, verification, and operation.

The goal is not to maximize the number of documents. It is to ensure that the product's requirements are explicit, its architecture is understandable, its interfaces are defined, and its reliability claims can be verified.

Document the contract. Implement the contract. Test the contract. Record the evidence.