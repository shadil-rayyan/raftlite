

# RaftLite

### A Highly Available Security-Policy Control Plane for Self-Hosted API Gateways

**One security policy, consistently enforced across every gateway.**

RaftLite is a self-hosted distributed control plane designed to manage and distribute security policies across multiple API gateways and reverse proxies. It uses a Raft-based replicated state machine to establish an authoritative policy state while allowing gateways to evaluate requests against locally available policies.

The project focuses on a practical distributed-systems problem: keeping security policies consistent across a gateway fleet while handling node failures, network partitions, persistent storage, and recovery.

> **Project status:** Prototype in development. Features, security guarantees, performance characteristics, and production readiness must be verified against the actual implementation and automated tests.

---

## Table of Contents

- 1\. Overview
- 2\. The Problem
- 3\. Product Vision
- 4\. Target Users
- 5\. Core Capabilities
- 6\. Architecture
- 7\. Technology Stack
- 8\. Repository Structure
- 9\. MVP Scope
- 10\. Quick Start
- 11\. Example Workflow
- 12\. Consistency and Failure Handling
- 13\. Security and Trust Boundaries
- 14\. Testing and Validation
- 15\. Product Differentiation
- 16\. Project Roadmap
- 17\. Documentation
- 18\. Project Status and Limitations
- 19\. Contributing
- 20\. License

---

## 1\. Overview

Modern API infrastructure often consists of multiple gateway instances operating behind a load balancer. These gateways must enforce compatible security policies even when individual components fail or become temporarily disconnected.

Maintaining independent policy configurations creates operational risk. A policy update applied to one gateway might not reach another, potentially allowing traffic that should have been blocked.

RaftLite proposes a centralized, highly available policy-control plane that maintains replicated policy state and distributes committed policies to gateway consumers.

The primary goal is not simply to implement the Raft consensus algorithm.

The goal is to demonstrate how distributed consensus, durable state, policy management, gateway integration, observability, and failure testing can work together to address a real infrastructure problem.

### Product summary

| Attribute | Description |
| --- | --- |
| Product | RaftLite |
| Category | Distributed security-policy control plane |
| Primary users | Platform engineers, DevOps engineers, SREs, and security engineers |
| Primary consumers | API gateways and reverse proxies |
| Core problem | Inconsistent security policies across distributed gateways |
| Core mechanism | Raft-based replicated state machine |
| Initial policies | IP blocking, IP unblocking, CIDR blocking, and temporary bans |
| Deployment target | Small self-hosted environments |
| Primary objective | Reliable policy management and distribution during infrastructure failures |

## 2\. The Problem

Consider an organization operating three API gateways.

```
                     Internet
                        |
                        v
                  Load Balancer
                        |
             +----------+----------+
             |          |          |
             v          v          v
         Gateway 1  Gateway 2  Gateway 3
             |          |          |
             +----------+----------+
                        |
                     Backend
```

Suppose an administrator blocks the IP address `203.0.113.50`.

If each gateway maintains an independent blocklist, the resulting state might be:

```
Gateway 1: BLOCKED
Gateway 2: ALLOWED
Gateway 3: ALLOWED
```

A client routed through Gateway 2 or Gateway 3 could still reach the backend.

This is a policy-coordination problem, not merely a storage problem.

A practical solution must address:

- A common authoritative policy state.
- Reliable propagation of committed policy changes.
- Consistent ordering of concurrent updates.
- Continued control-plane operation after a node failure.
- Safe behavior when a majority of nodes is unavailable.
- Recovery of nodes that restart with stale state.
- Gateway enforcement when the control plane is temporarily unreachable.

RaftLite is designed around these requirements.

## 3\. Product Vision

RaftLite aims to provide a small, self-hosted policy-management system for teams that operate their own API infrastructure and want security-policy coordination without building the entire control plane themselves.

Its core responsibility is:

**Reliable distributed management and enforcement of security policies across a fleet of API gateways.**

### Product principles

1. **Consistency first:** Only properly committed policy changes become authoritative cluster state.
2. **Local enforcement:** Ordinary HTTP requests should not require a consensus round trip.
3. **Explicit failure behavior:** Loss of control-plane connectivity must not silently disable security enforcement.
4. **Durable recovery:** Restarted nodes must recover and reconcile their state with the cluster.
5. **Auditable changes:** Operators should be able to investigate who or what changed a policy and why.
6. **Observable operation:** Cluster health, policy activity, replication, and recovery should be measurable.
7. **Narrow scope:** Build a reliable policy-control plane before expanding into advanced security analytics.

### What RaftLite is not

RaftLite is not intended to replace:

- A general-purpose relational database.
- Redis or another shared-state datastore.
- Kubernetes or a general-purpose orchestration platform.
- A complete web application firewall.
- A distributed denial-of-service protection service.
- An enterprise SIEM or security analytics platform.
- An identity-management or access-management system.

Existing infrastructure may already solve parts of the problem. RaftLite must justify its adoption through the value of its policy-specific integration and operational behavior.

## 4\. Target Users

### Primary users

**Platform and DevOps engineers**

Operate multiple self-hosted API gateways and need a common mechanism for managing dynamic security policies.

Typical responsibilities include deployment, configuration, gateway integration, and infrastructure reliability.

**Security engineers**

Create, revoke, inspect, and investigate security policies. They need auditable changes and predictable enforcement behavior.

**Site reliability engineers**

Monitor cluster health, investigate failures, validate recovery, and measure policy propagation and control-plane availability.

### Intended environments

- Small SaaS companies.
- Self-hosted API providers.
- Internal platform teams.
- Organizations operating reverse proxies.
- Teams running APIs on virtual machines, containers, or Kubernetes.
- Infrastructure teams that require local control over policy enforcement.

The initial target is a small gateway fleet and a three-node policy cluster. The exact market demand and willingness to adopt a dedicated product remain hypotheses to validate with prospective users.

## 5\. Core Capabilities

RaftLite is intended to provide the following capabilities as they are implemented and verified.

### Policy management

- Create and revoke IP-block policies.
- Block IP ranges using CIDR notation.
- Create temporary policies with expiration.
- List and inspect policies.
- Validate policy inputs.
- Record policy changes for auditing.

### Distributed control plane

- Elect a leader.
- Replicate policy commands.
- Commit commands according to Raft safety rules.
- Persist consensus state.
- Recover after process restart.
- Use snapshots to support efficient recovery.
- Reject writes that cannot be safely committed without quorum.

### Gateway integration

- Distribute committed policy state.
- Maintain a local policy representation.
- Evaluate requests without contacting the Raft leader for every request.
- Reconcile state after disconnection.
- Apply an explicit policy for control-plane outages.

### Operations

- Expose liveness and readiness endpoints.
- Report cluster status.
- Export operational metrics.
- Provide structured logs.
- Record policy-change audit information.
- Support repeatable local deployment and failure testing.

### Optional future capabilities

- Rate limiting.
- Telemetry ingestion.
- Anomaly detection.
- Human approval workflows.
- Dry-run policy evaluation.
- Policy rollback.
- Kubernetes deployment.
- Rich operational dashboards.

These extensions are not prerequisites for the first reliable release.

## 6\. Architecture

RaftLite separates policy management from request enforcement.

```
                   Administrator
                         |
                         v
                 +---------------+
                 | RaftLite API  |
                 +-------+-------+
                         |
                         v
               +-------------------+
               |  RaftLite Cluster |
               |                   |
               | Node 1: Leader    |
               | Node 2: Follower  |
               | Node 3: Follower  |
               |                   |
               | Consensus         |
               | Replicated Log    |
               | Policy State      |
               | Persistent Store  |
               +---------+---------+
                         |
                 Committed policy
                    distribution
                         |
              +----------+----------+
              |          |          |
              v          v          v
          Gateway 1  Gateway 2  Gateway 3
              |          |          |
              v          v          v
         Local policy evaluation and enforcement

```

### 6.1 Control plane

The control plane manages authoritative policy state.

Its responsibilities include:

- Accepting and validating policy commands.
- Coordinating consensus.
- Persisting state.
- Applying committed commands.
- Distributing policy updates.
- Exposing administrative and operational APIs.
- Recording policy changes.

### 6.2 Data plane

The data plane processes client requests and applies policies.

Its responsibilities include:

- Identifying the effective client IP.
- Evaluating locally available policy state.
- Applying configured allow, deny, and rate-limit rules.
- Enforcing the configured behavior during control-plane outages.
- Reporting relevant telemetry.

The data plane should not depend on a successful consensus operation for each HTTP request.

### 6.3 Consensus and authoritative state

Raft establishes an ordered history of committed commands. A deterministic state machine applies those commands to produce the authoritative policy state.

The intended authority hierarchy is:

```
Committed Raft history
          |
          v
Authoritative policy state
          |
          v
Versioned policy distribution
          |
          v
Gateway-local policy state
          |
          v
Request enforcement
```

Gateway caches are enforcement replicas, not the authoritative source of truth.

A successful Raft commit also does not, by itself, prove that every gateway has already received the update. Policy distribution and acknowledgment semantics must be defined and tested separately.

### 6.4 Optional telemetry and anomaly detection

Telemetry processing and anomaly detection are intentionally outside the core
consensus path. The Rust telemetry drain and Python anomaly detector are
temporarily removed from this branch; see `docs/rust-python-sidecars.md`. The
Go gRPC loopback service remains so a future engine can propose blocks through
the authoritative policy path.

## 7\. Technology Stack

| Component | Technology | Responsibility |
| --- | --- | --- |
| Control plane | Go | API, consensus integration, policy state machine, and cluster operations |
| HTTP contract | OpenAPI | Administrative API definition |
| RPC contract | Protocol Buffers, if gRPC is adopted | Inter-service interface definition |
| Local deployment | Docker Compose | Development and integration testing |
| Metrics | Prometheus-compatible instrumentation | Operational monitoring |
| Dashboard | Grafana, optional | Visualization of cluster and policy metrics |

Technology choices must serve explicit component requirements. A component should not exist solely to demonstrate a language or framework.

The initial priority is correctness in the Go control plane. Rust telemetry processing and Python anomaly detection can be developed independently once their interfaces are defined.

## 8\. Repository Structure

The intended repository layout is:

```
raftlite/
├── README.md
├── LICENSE
├── CONTRIBUTING.md
├── SECURITY.md
├── Makefile
├── go.mod
│
├── cmd/
│   └── raftlite/
│       └── main.go
│
├── internal/
│   ├── consensus/
│   ├── policy/
│   ├── storage/
│   ├── server/
│   ├── transport/
│   ├── gateway/
│   ├── audit/
│   └── observability/
│
├── api/
│   ├── openapi.yaml
│   └── proto/
│
│
├── configs/
├── deployments/
├── scripts/
│
├── tests/
│   ├── integration/
│   ├── e2e/
│   ├── simulation/
│   └── fixtures/
│
├── docs/
│   ├── README.md
│   ├── product/
│   ├── requirements/
│   ├── architecture/
│   ├── design/
│   ├── operations/
│   ├── testing/
│   ├── use-cases.md
│   ├── roadmap.md
│   └── interview-guide.md
│
└── .github/
    └── workflows/
```

Directory responsibilities:

| Directory | Responsibility |
| --- | --- |
| `cmd/` | Executable entry points and process initialization |
| `internal/consensus/` | Raft integration and consensus-specific behavior |
| `internal/policy/` | Policy types, validation, and deterministic state transitions |
| `internal/storage/` | Durable storage, WAL, snapshots, and recovery |
| `internal/server/` | HTTP handlers and API request processing |
| `internal/transport/` | Inter-node communication |
| `internal/gateway/` | Gateway integration and policy distribution |
| `internal/audit/` | Policy-change audit records |
| `internal/observability/` | Metrics and structured logging |
| `api/` | Versioned external API contracts |
| `configs/` | Example configurations and monitoring settings |
| `deployments/` | Deployment manifests and container definitions |
| `tests/` | Integration, end-to-end, simulation, and failure tests |
| `scripts/` | Development and test automation |
| `docs/` | Product, architecture, requirements, operations, and evaluation documentation |

This is a target structure. Add directories when their implementation is justified; do not create empty components merely to make the repository appear complete.

## 9\. MVP Scope

The first milestone should prove the core product workflow before expanding the feature set.

### Included in the MVP

**Consensus and persistence**

- Three-node Raft cluster.
- Leader election.
- Replicated policy commands.
- Durable consensus state.
- Snapshot and restart recovery.
- Safe handling of quorum loss.

**Policy management**

- IP block.
- IP unblock.
- CIDR block.
- Temporary policies.
- Policy listing and lookup.
- Input validation and audit records.

**Gateway integration**

- Local policy evaluation.
- Policy revision tracking.
- Update distribution and reconnection.
- Explicit control-plane outage behavior.

**Operations and verification**

- Health and readiness endpoints.
- Cluster status.
- Metrics and structured logs.
- Repeatable local deployment.
- Automated failure and recovery tests.

### Deferred until the core works

- Global distributed rate limiting.
- Automatic blocking based on anomaly detection.
- Advanced approval workflows.
- Policy federation across clusters.
- Kubernetes production deployment.
- Rich dashboards and external integrations.

See the Product Requirements and Roadmap.

## 10\. Quick Start

The project is under development. The commands below represent the intended workflow and are not a guarantee that every command is already implemented.

### Prerequisites

The initial development environment is expected to require:

- Go, matching the version specified in `go.mod`.
- Git.
- Docker and Docker Compose for multi-node integration testing.

### Clone the repository

```
git clone <repository-url>
cd raftlite
```

Replace `<repository-url>` with the actual repository URL.

### Inspect the available commands

```
make help
```

If a `Makefile` has not yet been added, inspect the repository's current build instructions instead.

### Run the Go tests

```
go test ./...
```

### Start the development environment

Once the relevant targets and deployment files have been implemented, the intended workflow is:

```
make dev-up
make status
make test
make dev-down
```

Use only commands supported by the current checkout.

**Important:** Successfully starting three processes is not proof of consensus safety. Validate leader election, committed-state replication, quorum behavior, and recovery using automated tests.

## 11\. Example Workflow

The following illustrates the intended administrative workflow.

### Create a temporary IP block

```
POST /v1/policies/ip-blocks
Content-Type: application/json
```

Example request:

```
{
  "ip": "203.0.113.50",
  "duration": "1h",
  "reason": "credential abuse",
  "request_id": "req-example-001"
}
```

The exact request schema and idempotency behavior must be defined in the API contract before this example is treated as an implemented interface.

### Intended processing sequence

1. The API validates the request.
2. The control plane identifies the current leader.
3. The policy command enters the Raft replication process.
4. The command is committed according to Raft's safety rules.
5. The policy state machine applies the command.
6. The committed policy becomes available for distribution.
7. Gateways update their local policy state.
8. Audit records and operational metrics reflect the change.

The API must distinguish between a request being accepted, a command being committed, and a policy being applied by a particular gateway.

These states are not interchangeable.

## 12\. Consistency and Failure Handling

RaftLite's reliability claims depend on precise behavior during failures.

### Three-node quorum

A three-voter cluster requires two votes to form a majority.

| Available voters | Quorum available? | Expected write behavior |
| --- | --- | --- |
| 3 | Yes | Writes may commit |
| 2 | Yes | Writes may commit if the remaining nodes can communicate and satisfy Raft requirements |
| 1 | No | New writes cannot commit |
| 0 | No | No progress |

A majority is necessary but not sufficient for every write. Leader status, log consistency, persistence, and successful replication must also satisfy the consensus implementation's requirements.

### Leader failure

If the leader fails, the remaining nodes can elect a new leader if a majority remains available.

Previously committed state must remain recoverable, and the system must not report an uncommitted command as committed.

### Network partition

Only a side with a majority can continue committing new entries. A minority partition must not independently establish a conflicting committed history.

### Gateway outage behavior

When the control plane becomes unavailable, gateways need an explicit enforcement policy.

Possible modes include:

- `last_known_good`: continue enforcing the last valid committed policy state.
- `fail_open`: allow traffic when the relevant policy decision cannot be obtained.
- `fail_closed`: deny traffic when the relevant policy decision cannot be obtained.

The precise behavior must be defined for startup, missing policy state, stale state, and individual lookup failures. `last_known_good` is a candidate default, not a substitute for specifying those cases.

See Consistency and Failure Handling.

## 13\. Security and Trust Boundaries

RaftLite manages security-sensitive state. Its threat model must cover both the administrative control plane and the gateway enforcement path.

Security requirements include:

- Authentication for administrative operations.
- Authorization for policy changes.
- Authenticated and encrypted inter-node communication.
- Secure configuration and secret management.
- Validation of IP addresses, CIDR ranges, durations, and policy identifiers.
- Protection against malformed requests and resource exhaustion.
- Auditable policy changes.
- Secure handling of telemetry and potentially sensitive client data.
- Explicit behavior when certificates, credentials, or dependencies fail.

Gateway client-IP extraction must also account for proxy trust boundaries. An untrusted client must not be able to bypass IP-based policies by supplying a forged forwarding header.

See Security Model and `SECURITY.md`.

## 14\. Testing and Validation

RaftLite must prove its reliability through repeatable automated tests.

### Unit tests

Test:

- Policy validation.
- IP and CIDR parsing.
- Deterministic state transitions.
- Expiration and revocation.
- Serialization and storage behavior.

### Integration tests

Test:

- Leader election.
- Log replication.
- Administrative APIs.
- Persistence and recovery.
- Snapshot installation.
- Gateway policy synchronization.

### Failure tests

Test:

- Leader crash.
- Follower crash.
- Loss of quorum.
- Network partition.
- Delayed and dropped messages.
- Restarted-node recovery.
- Disk and persistence failures.
- Gateway disconnection and reconnection.

### End-to-end tests

A core acceptance scenario should:

1. Start a three-node policy cluster.
2. Start multiple gateway instances.
3. Commit an IP-block policy.
4. Verify the policy reaches each gateway.
5. Kill the leader.
6. Verify that a new leader is elected when quorum remains.
7. Commit another policy.
8. Restart the failed node.
9. Verify that it recovers and catches up.
10. Verify that gateways enforce the intended policy state.

Additional tests should cover temporary-policy expiration, concurrent updates, and network partitions.

### Performance measurements

Measure separately:

- Policy lookup latency.
- Policy commit latency.
- Policy propagation latency.
- Gateway request latency.
- Throughput and resource consumption.
- Leader election and recovery time.

Document hardware, operating system, cluster size, workload, test duration, and latency percentiles. Do not claim a latency target has been achieved until benchmark results support it.

See Testing Strategy and Benchmarking Guide.

## 15\. Product Differentiation

RaftLite does not assume existing alternatives are inadequate.

| Alternative | Strength | RaftLite's intended distinction |
| --- | --- | --- |
| Local configuration files | Simple and easy to operate | Dynamic, coordinated policy updates |
| Redis | Fast shared-state access and a broad ecosystem | Policy-specific workflows and integrated control-plane semantics |
| Highly available databases | Mature persistence and consistency options | Focused policy distribution and gateway integration |
| etcd or Consul | Mature distributed coordination capabilities | A security-policy-oriented application layer |
| Kubernetes-native configuration | Natural integration with Kubernetes environments | Policy lifecycle and enforcement across supported gateway environments |
| Managed WAF or security services | Operational simplicity and mature security features | Self-hosted control and integration for organizations with that requirement |

The differentiation is the integrated product experience: policy semantics, replicated state, gateway distribution, auditability, and operational tooling in one focused system.

For many organizations, adopting an existing product is the better choice. RaftLite is valuable only if its specific deployment and integration model solves a real problem for its intended users.

## 16\. Project Roadmap

### Phase 1 — Reliable policy-control MVP

- Three-node Raft cluster.
- Persistent state and recovery.
- IP and CIDR policies.
- Temporary blocks.
- Administrative API.
- Gateway integration.
- Health endpoints, metrics, and audit records.
- Automated consensus and recovery tests.

### Phase 2 — Operational maturity

- Stronger authentication and authorization.
- Certificate management.
- Improved audit capabilities.
- Better policy synchronization.
- Human approval workflows.
- Policy dry-run support.
- Operational dashboards.
- Deployment hardening.

### Phase 3 — Advanced capabilities

- Rate limiting.
- Telemetry ingestion.
- Anomaly detection and policy proposals.
- Policy rollback.
- External alerting integrations.
- Kubernetes deployment.
- Multi-cluster capabilities, if justified by user requirements.

Progression between phases should depend on tested acceptance criteria, not simply the completion of a feature checklist.

## 17\. Documentation

The `docs/` directory contains the detailed product and engineering specifications.

| Document | Purpose |
| --- | --- |
| Documentation Index | Navigation across all project documentation |
| Product Requirements | Customer problem, product scope, value proposition, and roadmap |
| Software Requirements Specification | Formal functional and non-functional requirements |
| Requirements Traceability | Links requirements to code and verification tests |
| Architecture Overview | System components, responsibilities, and data flow |
| Consistency and Failure Handling | Consensus safety, quorum, partitions, and recovery |
| Security Model | Threats, trust boundaries, and security controls |
| Policy Model | Policy schema, lifecycle, precedence, and expiration |
| API Design | HTTP semantics, validation, and API behavior |
| Data Storage Design | WAL, snapshots, persistence, and recovery |
| Use Cases | Actor workflows and acceptance criteria |
| Testing Strategy | Test levels and verification approach |
| Benchmarking Guide | Performance measurement and reporting |
| Deployment Guide | Deployment and configuration |
| Monitoring Guide | Metrics, logs, and operational visibility |
| Roadmap | Delivery phases and deferred capabilities |
| Interview Guide | Technical trade-offs and evaluation questions |

Documentation links refer to the intended repository structure. Create each referenced file as the project documentation is developed.

## 18\. Project Status and Limitations

RaftLite is a distributed-systems and security-engineering project intended to demonstrate the design and validation of a reliable policy-control plane.

The architecture describes intended behavior; it does not establish that the behavior has already been implemented.

Before any release is described as production-ready, the project must demonstrate:

- Consensus safety and durable commitment.
- Correct behavior during quorum loss.
- Recovery after crashes and restarts.
- Secure administrative and inter-node communication.
- Correct gateway synchronization and enforcement.
- Explicit failure behavior.
- Repeatable reliability and performance tests.
- Operational documentation and recovery procedures.

Until those conditions are met, RaftLite should be treated as a prototype and not relied on as the sole security mechanism protecting production infrastructure.

## 19\. Contributing

Contributions should improve correctness, maintainability, security, or operational usefulness.

Before submitting a change:

1. Review the relevant architecture and requirements.
2. Keep component responsibilities and API contracts consistent.
3. Add or update automated tests.
4. Update documentation when behavior or interfaces change.
5. Run the applicable test and lint commands.
6. Explain important design trade-offs in the pull request.

See CONTRIBUTING.md for contribution guidelines.

Report security vulnerabilities according to SECURITY.md.

## 20\. License

The project's license has not been specified here.

Add a root `LICENSE` file containing the actual license selected for the project, and update this section to identify it. Do not describe the repository as open source until the licensing terms have been established.

---

## Project Philosophy

Raft is an implementation mechanism, not the product.

The product is reliable, consistent security-policy management across a distributed gateway fleet.

Persistence, snapshots, telemetry, anomaly detection, monitoring, and failure simulation should exist to support that objective.

**Build reliability first. Add intelligence second. Prove the behavior through tests.**

