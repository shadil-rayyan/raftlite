

# RaftLite — Product Requirements

**Document ID:** PRD-001\
**Version:** 1.0\
**Status:** Proposed\
**Product:** RaftLite\
**Category:** Distributed security-policy control plane\
**Primary deployment:** Self-hosted infrastructure\
**Primary interface:** Versioned administrative API\
**Primary enforcement target:** API gateways and reverse proxies

---

## 1\. Executive Summary

RaftLite is a proposed self-hosted security-policy control plane for teams operating multiple API gateways or reverse proxies.

It provides a centralized way to manage security policies while maintaining a replicated, strongly consistent authoritative policy state across a small cluster of control-plane nodes.

The initial product focuses on IP blocking, CIDR blocking, temporary bans, policy removal, and reliable propagation of committed policy changes to connected gateways.

RaftLite addresses a specific operational problem: when several gateways protect the same application, independently maintained security rules can produce inconsistent enforcement. A malicious client may be blocked by one gateway but allowed through another.

RaftLite is designed to give these gateways a common source of truth for policy management while allowing the request path to continue using locally available policy state.

The initial implementation is intended to demonstrate correct distributed-system behavior, including leader election, quorum-based writes, persistence, recovery, and gateway policy synchronization.

**Product promise:** One authoritative security policy, consistently distributed across a fleet of gateways, with explicit behavior during infrastructure failures.

RaftLite is a proposed product. Its reliability, performance, security, and usability claims must be established through implementation and testing rather than inferred from the architecture alone.

---

## 2\. Problem Statement

Organizations that operate several API gateways often need to enforce common security policies across every gateway instance.

Examples include:

- Blocking a malicious IP address.
- Temporarily banning a client involved in credential abuse.
- Denying traffic from a suspicious CIDR range.
- Allowing a previously blocked client.
- Applying consistent rules during an incident.
- Tracking who changed a policy and why.

Without a reliable policy-management mechanism, individual gateways may maintain different local configurations or receive updates at different times.

This creates operational risks:

1. A blocked client may reach a gateway that has not received the update.
2. Administrators may have to update multiple systems manually.
3. A failed central service may prevent new policies from being committed.
4. Retried requests may create duplicate operations or ambiguous results.
5. Operators may not know which policy version a gateway is enforcing.
6. Temporary policies may expire inconsistently.
7. A recovery event may leave a gateway or control-plane node with stale state.

The problem becomes more significant when security-policy changes must be made quickly and must remain auditable.

### 2.1 Illustrative scenario

A company operates three API gateways behind a load balancer.

An administrator blocks `203.0.113.50` after detecting credential abuse.

Without reliable distribution:

- Gateway A rejects the IP.
- Gateway B still allows the IP.
- Gateway C may receive the update several seconds later.

The load balancer can direct subsequent requests to any gateway.

RaftLite aims to make policy management authoritative and replicated, while explicitly defining how and when each gateway receives and enforces the resulting policy.

### 2.2 Problem boundary

RaftLite is intended to solve the consistency and operational coordination of security-policy management.

It does not, by itself, detect every attack, guarantee that a client is malicious, protect against volumetric DDoS attacks, or eliminate the need for gateway security controls.

The correctness of policy distribution and the quality of a security decision are separate concerns.

---

## 3\. Product Vision

Provide a lightweight, understandable, self-hosted control plane that allows small infrastructure teams to manage security policies across multiple gateways without requiring each gateway to maintain an independent authoritative policy configuration.

The product should make three things explicit:

1. **Authority:** Which committed policy state is authoritative?
2. **Enforcement:** Which policy revision is each gateway currently enforcing?
3. **Failure behavior:** What happens when a node, network connection, or policy service becomes unavailable?

The long-term vision includes policy auditing, operational visibility, optional anomaly-driven proposals, and integrations with existing self-hosted gateway infrastructure.

The initial product must establish reliable policy management before adding automated security decisions.

---

## 4\. Target Customer

### 4.1 Primary customer profile

The initial target customer is a small or medium-sized engineering organization that operates its own API gateway or reverse-proxy infrastructure.

Typical characteristics include:

- Multiple gateway instances serving the same APIs.
- Self-managed deployment using containers, virtual machines, Kubernetes, or bare metal.
- A platform, DevOps, or SRE team responsible for the infrastructure.
- A need to change security policies without manually modifying every gateway.
- A preference for self-hosted control and operational visibility.
- Limited appetite for deploying a large new infrastructure platform solely for policy coordination.

The proposed deployment is a small RaftLite cluster, normally three voting nodes, connected to several policy-consuming gateways.

The number of gateways and nodes above describes the intended deployment profile, not a validated market-size claim.

### 4.2 Potential customer segments

- Small SaaS companies operating their own public APIs.
- API providers with self-hosted gateway fleets.
- Platform engineering teams managing internal edge infrastructure.
- Organizations that require local control over security-policy data.
- Development and research teams investigating distributed security-policy management.

### 4.3 Explicit exclusions

RaftLite is not initially targeted at:

- Large enterprises requiring a comprehensive commercial WAF.
- Organizations needing a global edge network or managed DDoS mitigation.
- Teams requiring a complete SIEM or security analytics platform.
- Environments requiring mature multi-region policy federation from the first release.
- Organizations that already have an adequate, well-operated policy-management solution and no reason to replace it.

These exclusions keep the initial product focused.

---

## 5\. Buyer, User, and System Actors

The person who pays for a product is not necessarily the person who operates it.

| Role | Relationship to RaftLite | Primary need |
| --- | --- | --- |
| Engineering manager or infrastructure owner | Potential buyer or sponsor | Reduce operational complexity and security-policy inconsistency |
| Platform engineer | Primary operator | Deploy, configure, upgrade, and maintain the cluster |
| Security engineer | Primary policy user | Create, inspect, revoke, and audit policies |
| SRE or operations engineer | Operational user | Diagnose failures, monitor quorum, and recover nodes |
| API gateway | Automated consumer | Obtain and enforce current policy state |
| Anomaly detector | Optional automated producer | Propose policy changes based on traffic analysis |
| Auditor or evaluator | Reviewer | Understand policy history, controls, and verification evidence |

The initial product assumes that an organization has people responsible for operating its infrastructure.

A self-service commercial onboarding experience is outside the MVP.

---

## 6\. Jobs to Be Done

RaftLite should help its users complete the following tasks.

### Security engineer

"When I identify a suspicious IP address, I want to create one policy and know whether it was committed and propagated, rather than manually updating several gateways."

### Platform engineer

"When one policy node fails, I want the remaining cluster to continue accepting safe policy updates if quorum exists."

### SRE

"When a node restarts or becomes disconnected, I want to determine its current state and verify that it has recovered correctly."

### Infrastructure owner

"When I evaluate this system, I want evidence that policy changes survive failures and that gateway request processing does not depend on a consensus round trip for every request."

### Auditor

"When a security policy changes, I want to know who initiated it, why it was created, what was committed, and which result the system reported."

These jobs define the practical outcomes the implementation should support.

---

## 7\. Existing Alternatives

RaftLite competes with existing ways of solving the problem, not only with other consensus implementations.

### 7.1 Local configuration files

**Advantages:**

- Simple.
- Easy to understand.
- No additional distributed service required.

**Limitations:**

- Updates may require deployment or reload operations.
- Configuration can drift between gateway instances.
- Auditability and propagation status may require additional tooling.

**When local files are sufficient:** Small installations with infrequent policy changes and reliable configuration management.

### 7.2 Shared databases

A database can provide shared policy storage and transactional updates.

**Advantages:**

- Mature data management.
- Rich querying and transaction support.
- Existing operational knowledge and tooling.

**Limitations depend on deployment:** A single database may become a dependency for policy-management operations. Highly available database deployments can address many availability concerns.

**When a database is preferable:** The organization already operates a reliable database and can implement the required policy semantics without unnecessary additional infrastructure.

### 7.3 Redis

Redis can support shared state, caching, and rate-limiting use cases.

**Advantages:**

- Low-latency operations.
- Mature ecosystem.
- Familiar operational patterns.

**Limitations depend on the chosen topology and application design:** Replication, persistence, failover, conflict handling, and application-level consistency guarantees must be evaluated for the particular deployment.

**When Redis is preferable:** The team already has suitable Redis infrastructure and the required semantics can be achieved with it.

### 7.4 etcd or Consul

These platforms provide mature distributed coordination or configuration capabilities.

**Advantages:**

- Established distributed-system implementations.
- Operational tooling and ecosystem integrations.
- Existing mechanisms for replicated state or configuration distribution.

**Trade-off:** A team must integrate its specific policy model, gateway enforcement, audit semantics, and user-facing workflows.

**When an existing coordination platform is preferable:** The organization already operates one and can satisfy its requirements with a small amount of additional application code.

### 7.5 Managed WAF and security platforms

Managed services can provide policy distribution, managed rules, traffic filtering, analytics, and other security features.

**Advantages:**

- Less infrastructure to operate.
- Potentially broader security capabilities.
- Established managed-service workflows.

**Limitations depend on the provider and environment:** A service may not fit every self-hosting, integration, data-control, or deployment requirement.

**When a managed platform is preferable:** It meets the organization's requirements at an acceptable cost and operational complexity.

### 7.6 RaftLite's intended differentiation

RaftLite proposes a focused combination of:

- A policy-specific management API.
- A replicated authoritative policy state.
- Gateway-oriented policy distribution.
- Local policy evaluation.
- Explicit failure semantics.
- Policy auditability.
- Operational visibility.
- Optional telemetry-driven policy proposals.

The differentiator is the integration and focus of these capabilities, not the invention of a new consensus protocol.

RaftLite should not be selected solely because it uses Raft. A customer should choose it only if its specialized workflow is more suitable than adopting or extending an existing solution.

---

## 8\. Product Goals

The MVP must establish the following capabilities.

### G-01: Authoritative policy management

Provide a single logical authority for committed security-policy changes.

### G-02: Consistent committed state

Use replicated state-machine semantics so that committed policy commands are applied in a consistent order.

### G-03: Fault-tolerant control plane

Continue safe write operations when the cluster retains a functioning majority and the remaining nodes can make progress.

### G-04: Gateway policy enforcement

Allow gateways to evaluate locally available policy state without contacting the Raft leader for every request.

### G-05: Durable recovery

Recover committed state after process restarts and bring lagging nodes up to date.

### G-06: Operational transparency

Expose cluster health, leadership, policy revisions, errors, and audit information.

### G-07: Verifiable behavior

Provide automated tests that exercise normal operation, leader failure, network partitions, and node recovery.

### G-08: Deployability

Make the initial system reproducible in a local multi-node development environment.

---

## 9\. Non-Goals

The MVP will not attempt to deliver:

- A complete web application firewall.
- A replacement for a general-purpose database.
- A new general-purpose consensus platform.
- A complete distributed rate-limiting system.
- Global multi-region consensus.
- DDoS mitigation at internet scale.
- An identity provider.
- A complete SIEM.
- Autonomous security remediation with no approval controls.
- A sophisticated commercial dashboard.
- An extensive policy expression language.
- Production certification or compliance claims without appropriate evidence.

These capabilities may be revisited after the core policy-management workflow is reliable.

---

## 10\. MVP Scope

### 10.1 Included capabilities

**Control plane**

- Multi-node cluster.
- Leader election and log replication using a selected Raft implementation.
- Quorum-aware writes.
- Durable state and restart recovery.
- Snapshot and log-compaction support where provided or implemented.
- Cluster status and health endpoints.

**Policy management**

- Create an IP block.
- Remove or revoke a block.
- Create a CIDR block.
- Create a temporary block.
- List policies.
- Retrieve a policy by identifier.
- Query whether an address is blocked.
- Validate requests and support safe retries.

**Gateway integration**

- Receive committed policy updates.
- Maintain local policy state.
- Enforce applicable committed policies.
- Report its applied policy revision.
- Continue operating according to a documented policy during control-plane disconnection.

**Operations**

- Structured logs.
- Essential metrics.
- Audit records for policy changes.
- Repeatable deployment.
- Automated integration and failure tests.

### 10.2 Deferred capabilities

The following should not block delivery of the initial policy-management product:

- Advanced rate limiting.
- Automated anomaly-driven enforcement.
- Human approval workflows.
- Dynamic cluster membership changes beyond what the chosen implementation safely supports.
- Full RBAC administration.
- Kubernetes operator or Helm-based deployment.
- Policy dry-run and rollback interfaces.
- Multi-cluster federation.
- SIEM and webhook integrations.
- Advanced dashboards.

Security controls required to protect the MVP itself are not deferred merely because advanced security features appear on the roadmap.

---

## 11\. Product Architecture at a Glance

RaftLite separates policy management from request enforcement.

```
                  Security Engineer
                         |
                         v
                   Admin API
                         |
                         v
                +------------------+
                | RaftLite Cluster |
                |                  |
                | Leader + Raft    |
                | Replicated State |
                | Durable Storage  |
                +--------+---------+
                         |
                  Committed policies
                         |
             +-----------+-----------+
             |           |           |
             v           v           v
          Gateway A   Gateway B   Gateway C
             |           |           |
             v           v           v
          Local policy evaluation on request path
```

The authoritative policy state is the committed state maintained by the replicated state machine.

Gateway-local policy data is a replica used for fast enforcement. It is not an independent authority for administrative policy changes.

The gateway integration protocol must define how updates are delivered, how applied revisions are reported, and how reconnection and recovery work.

Optional telemetry and anomaly detection components are outside the critical consensus path.

---

## 12\. Core Product Workflows

### WF-01: Create a block policy

1. An administrator submits a validated policy request.
2. The control plane authenticates and authorizes the caller.
3. The request is submitted to the Raft leader.
4. The command is replicated and committed according to Raft's safety rules.
5. The state machine applies the committed command.
6. The API reports the operation's actual result.
7. Gateways receive the resulting policy revision.
8. The system exposes propagation status where supported.

An API timeout must not automatically be interpreted as a failed commit. The client may need to query the operation or retry using the same idempotency key.

### WF-02: Handle leader failure

1. The current leader becomes unavailable.
2. Remaining nodes detect the failure according to their election timeouts.
3. A new leader is elected if the remaining nodes can form a majority.
4. Policy writes resume after the new leader is able to safely serve them.
5. Previously committed state remains authoritative.
6. Recovered nodes reconcile with the cluster.

### WF-03: Handle loss of quorum

1. The cluster loses the ability to form a majority.
2. New writes cannot be reported as successfully committed without the required quorum.
3. Gateways continue using their last accepted committed policy according to the configured enforcement mode.
4. Operators can inspect the health and recovery status of the cluster.

### WF-04: Create a temporary block

1. An authorized caller requests a temporary block.
2. The control plane validates the target and expiration semantics.
3. The policy change is committed.
4. Gateways receive the policy and its revision.
5. Expiration is processed according to a deterministic, documented design.
6. The resulting state is propagated to policy consumers.

### WF-05: Generate a policy proposal

1. Telemetry is collected from a gateway.
2. An optional analytical component identifies suspicious activity.
3. The component generates a policy proposal with a reason and supporting metadata.
4. The proposal is submitted through the same authorized policy-management path.
5. Depending on configuration, an operator approves it or an authorized automation policy permits it to proceed.
6. The resulting policy becomes authoritative only after successful commitment.

The anomaly detector must not independently modify authoritative policy state or bypass the control plane.

---

## 13\. Critical Product Semantics

### 13.1 Committed versus propagated

A policy being committed by Raft does not automatically mean every gateway has already received or applied it.

The product must distinguish at least:

- **Accepted:** The request passed initial API validation.
- **Committed:** The policy command became part of the authoritative committed state.
- **Applied:** The state machine applied the committed command.
- **Propagated:** The update was delivered to a consumer.
- **Enforced:** The gateway applied the relevant policy revision to request processing.

The exact status model may differ by API operation, but these concepts must not be conflated.

### 13.2 Control-plane availability versus data-plane availability

Loss of control-plane connectivity should not automatically disable gateway request processing.

The gateway's behavior must be explicitly defined for the period during which it cannot receive policy updates.

The initial design should support a last-known-good policy mode, while ensuring that operators understand the risk of stale policies.

### 13.3 Consistency versus availability

When quorum is unavailable, RaftLite must not accept unsafe policy writes and represent them as committed.

This does not mean every subsystem must become unavailable. Gateways may continue enforcing their last applied policy while policy management is degraded.

### 13.4 Expiration semantics

Temporary policies require particular care.

Expiration must not depend on unrelated local timers independently mutating the authoritative state on each node.

The implementation must define how expiration is represented, how clock differences are handled, and when gateways stop enforcing an expired policy.

### 13.5 Client IP identification

IP-based security policies are only as reliable as the gateway's identification of the actual client address.

The product must define how forwarded client-IP headers are trusted, including the trusted-proxy configuration. An untrusted client must not be able to bypass an IP block by supplying a forged forwarding header.

---

## 14\. Security and Trust Requirements

RaftLite manages security-sensitive state and must be designed accordingly.

The product requirements include:

- Authentication for administrative requests.
- Authorization for policy modifications.
- Authentication of cluster members.
- Protected node-to-node communication.
- TLS support for relevant interfaces.
- Validation of IP addresses, CIDRs, durations, and policy fields.
- Safe handling of secrets.
- Auditable policy changes.
- Protection against duplicate or ambiguous operations.
- Explicit failure behavior during dependency outages.
- Secure gateway identity and synchronization.
- Restricted access to sensitive operational endpoints.

The precise security mechanisms belong in the security model and SRS.

No deployment should be described as production-ready solely because TLS or authentication has been added. Security testing, threat analysis, operational hardening, and review remain necessary.

---

## 15\. Operational Requirements

An operator should be able to determine:

- Which node is currently leader.
- Whether the cluster has quorum.
- Which nodes are healthy.
- Whether a policy write committed.
- Whether a gateway has applied a particular revision.
- Whether a follower is behind.
- Whether a restarted node has recovered.
- Whether policy expiration or enforcement has failed.
- Which actor initiated a policy change.
- What errors occurred during an operation.

The initial implementation should provide sufficient logs, metrics, health endpoints, and documented diagnostic procedures to answer these questions without inspecting internal memory or modifying data files manually.

---

## 16\. Performance Requirements

The primary performance principle is that ordinary gateway policy checks should use local policy state.

A gateway request should not require a new Raft consensus round trip merely to determine whether an IP address is blocked.

Performance must be evaluated separately for:

- Gateway policy lookup latency.
- Gateway request overhead.
- Policy commit latency.
- Policy propagation delay.
- Telemetry ingestion throughput.
- Leader election duration.
- Recovery duration.
- Memory and CPU consumption.

Numerical service-level objectives should be selected after defining the deployment environment and workload.

No latency or throughput target should be advertised as achieved until a reproducible benchmark provides supporting evidence.

---

## 17\. Product Success Criteria

The MVP is considered functionally successful when the following conditions are demonstrated in automated tests or a reproducible acceptance environment.

### SC-01: Policy creation

An authorized administrator can create a valid IP-block policy, and the API reports the actual operation outcome.

### SC-02: Consistent authoritative state

The committed policy is applied consistently by healthy Raft nodes.

### SC-03: Gateway enforcement

A connected gateway receives and enforces the committed policy.

### SC-04: Leader failure

The cluster elects a replacement leader after a leader failure when quorum remains available.

### SC-05: Quorum safety

The system does not report a new write as committed when it lacks the required quorum.

### SC-06: Node recovery

A restarted node can recover its durable state and synchronize with the cluster.

### SC-07: Gateway continuity

A gateway can continue processing requests using its documented last-known policy behavior while the control plane is temporarily unavailable.

### SC-08: Temporary policy

A temporary policy follows the specified expiration semantics without inconsistent local mutation of authoritative state.

### SC-09: Auditability

Policy operations produce records sufficient to identify the actor, target, operation, reason where supplied, and outcome.

### SC-10: Reproducibility

A developer can start the documented development environment and run the core acceptance tests using the repository instructions.

### SC-11: Observability

An operator can identify leader status, quorum problems, policy commits, and relevant errors using the available operational interfaces.

### SC-12: Measured performance

The repository provides repeatable benchmarks and reports measured results with sufficient environment and workload information to interpret them.

Passing these criteria does not automatically establish production readiness, regulatory compliance, or suitability for every deployment.

---

## 18\. Assumptions and Open Questions

The following assumptions require validation.

### Customer assumptions

- Target teams experience enough policy-management friction to justify another service.
- Self-hosting and policy control are meaningful advantages for the target customer.
- The team can operate a small distributed control-plane cluster.
- Existing gateway integrations can be implemented without extensive changes to the request path.

### Technical assumptions

- A suitable Raft implementation can satisfy the persistence and recovery requirements.
- The target gateways can receive updates and maintain local policy state.
- Policy propagation delay can be measured and exposed to operators.
- The expected policy volume is manageable without a more complex storage architecture.

### Product questions

1. Which gateway or reverse-proxy integration should be supported first?
2. Should the MVP provide a native gateway plugin, a sidecar, or a separate policy agent?
3. What policy propagation delay is acceptable for the target users?
4. How should gateways behave when a policy update cannot be received?
5. Which administrative authentication mechanism is appropriate for the first release?
6. What is the minimum useful audit history?
7. Should temporary-policy expiration be represented by replicated expiration commands, deterministic evaluation of committed timestamps, or another rigorously defined mechanism?
8. Which existing solution would a target customer otherwise deploy?
9. What measurable reduction in operational effort would justify adoption?
10. Which features are necessary for the first real deployment rather than only for a demonstration?

These questions should be resolved through architecture decisions, implementation evidence, and, where possible, conversations with intended users.

---

## 19\. Roadmap

### Phase 1 — Reliable policy-control MVP

- Three-node Raft cluster.
- Core policy commands.
- Durable state and recovery.
- Administrative API.
- Gateway-local policy evaluation.
- Policy synchronization.
- IP and CIDR blocking.
- Temporary blocks.
- Audit records.
- Metrics and health endpoints.
- Automated failure tests.

**Exit criterion:** The end-to-end policy workflow and defined failure scenarios pass reproducible tests.

### Phase 2 — Operational hardening

- Stronger authentication and authorization.
- Improved audit capabilities.
- Policy propagation visibility.
- Better recovery tooling.
- Deployment improvements.
- Rate-limit policy support.
- More comprehensive security testing.
- Documented operational procedures.

**Exit criterion:** The system's operational and security assumptions are documented and tested against the target deployment.

### Phase 3 — Assisted security operations

- Telemetry ingestion improvements.
- Anomaly-driven policy proposals.
- Approval workflows.
- Dry-run mode.
- Policy rollback.
- Additional gateway integrations.
- Advanced monitoring and alerting.

**Exit criterion:** Automated proposals remain auditable and use the same safe policy-commit path as other changes.

### Phase 4 — Broader deployment capabilities

Potential future work includes Kubernetes packaging, multi-cluster operation, external security integrations, and more advanced policy management.

These features require separate scope and architecture decisions.

---

## 20\. Relationship to the SRS

This Product Requirements document describes the problem, intended users, product goals, boundaries, and success criteria.

The Software Requirements Specification at `../requirements/SRS.md` must translate these into precise system requirements.

The SRS should define:

- Functional requirements and their identifiers.
- Non-functional requirements and measurable acceptance criteria.
- External interfaces and API behavior.
- Policy lifecycle and precedence.
- Consensus and durability guarantees.
- Gateway synchronization semantics.
- Security and audit requirements.
- Deployment and recovery requirements.
- Verification methods.

The SRS must not silently introduce product behavior that conflicts with this document. If the product scope changes, update the relevant requirements and record consequential architecture decisions.

---

## 21\. Guiding Product Principle

RaftLite exists to make distributed security-policy management more reliable and understandable for teams operating their own gateway infrastructure.

Raft is an implementation mechanism, not the customer outcome.

The customer outcome is consistent, auditable policy management with explicit behavior during failures.

The first release should prove that outcome with a small set of policies, a clear gateway integration, durable state, recovery, and automated failure testing.

**Build reliable policy management first. Add automated intelligence only after the core behavior is trustworthy.**
