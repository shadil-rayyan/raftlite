RaftLite — Security Threat Model
Document ID: RL-THREAT-001
Version: 1.0
Status: Initial assessment
Last updated: 2026-10-11
Review owner: RaftLite maintainers
1. Purpose

This document identifies security threats affecting RaftLite's control plane, consensus cluster, policy synchronization, gateway enforcement, and optional telemetry and anomaly-detection components.

It defines the assets to protect, trust boundaries, attacker capabilities, mitigations, and security tests.

This is an initial threat assessment, not a completed penetration test, security audit, or certification.

2. Security Objectives

RaftLite must protect:

Policy integrity: Unauthorized actors must not create, modify, or revoke security policies.
Consensus safety: Network or application failures must not cause conflicting histories to be reported as committed.
Policy authenticity: Gateways must accept policy updates only from authorized sources.
Confidentiality: Credentials, secrets, and sensitive operational data must not be unnecessarily exposed.
Availability: Attackers should not easily exhaust resources or disable policy management.
Auditability: Security-sensitive actions should be attributable to an actor or authorized automation.
Recovery integrity: Restart and recovery procedures must not silently replace authoritative state with untrusted data.
3. System Assets
Asset	Security concern
Committed policy state	Unauthorized or incorrect security decisions
Raft log and persistent metadata	Corruption, tampering, or unsafe recovery
Administrative credentials	Unauthorized policy management
Inter-node credentials and certificates	Node impersonation
Gateway policy projection	Forged, stale, or incomplete policy updates
Audit records	Tampering or loss of evidence
Telemetry	Sensitive request metadata and poisoning
Anomaly-detection output	False policy proposals
Cluster membership configuration	Unauthorized node admission
Secrets and private keys	Credential theft and impersonation
4. Trust Boundaries
4.1 Administrative boundary

Administrative clients and automation communicate with the management API.

Requests must be authenticated, authorized, validated, and audited.

4.2 Consensus boundary

Raft nodes communicate over the consensus transport.

Network reachability alone must not establish node identity or authorization.

4.3 Gateway synchronization boundary

Gateways receive policy data from the control plane.

The synchronization mechanism must authenticate its source and validate updates before installation.

4.4 Telemetry boundary

Gateway events enter the telemetry pipeline and may influence anomaly-detection proposals.

Telemetry must be treated as untrusted input. Detection results must not bypass policy authorization or consensus.

4.5 Deployment boundary

RaftLite operates within an environment containing networks, hosts, persistent volumes, certificates, and deployment configuration.

Compromise of the host or storage administrator may exceed the protection provided by application-level consensus.

5. Threat Actors and Assumptions

Potential threat actors include:

An unauthenticated internet attacker.
A malicious or compromised API client.
A compromised gateway.
A compromised control-plane node.
A malicious insider with excessive privileges.
An attacker able to observe, delay, replay, or drop network traffic.
An attacker able to submit misleading telemetry.

The initial threat model assumes that an attacker may control a client or compromise a single component.

It does not assume that Raft alone protects the system from arbitrary Byzantine behavior, malicious cluster administrators, or complete compromise of a quorum.

Raft is designed primarily for crash and omission failures, not arbitrary malicious behavior by participating nodes.

6. Threat Register
T-001 — Unauthorized policy creation

Scenario: An attacker submits a policy-management request without permission.

Impact: Legitimate clients may be blocked, or malicious traffic may be allowed.

Mitigations:

Authenticate administrative requests.
Enforce role-based authorization.
Validate every policy command.
Audit successful and rejected requests.
Apply rate limits to administrative endpoints.

Verification: Attempt policy creation without credentials and with insufficient privileges. Both requests must be rejected without changing committed state.

Priority: Critical.

T-002 — Administrative credential theft

Scenario: An attacker obtains an administrator's API token or client certificate.

Impact: The attacker may make authorized-looking policy changes.

Mitigations:

Use short-lived credentials where practical.
Store secrets outside source control.
Support credential rotation and revocation.
Restrict administrative network access.
Audit sensitive operations.
Apply least-privilege authorization.

Residual risk: Valid compromised credentials may still be used until revoked or expired.

Priority: Critical.

T-003 — Unauthorized Raft node impersonation

Scenario: An attacker impersonates a cluster member or sends forged consensus traffic.

Impact: Cluster disruption, resource exhaustion, or unsafe behavior if transport and membership checks are inadequate.

Mitigations:

Authenticate inter-node connections.
Use TLS with appropriate peer identity validation.
Restrict consensus ports to trusted networks.
Validate node identity against cluster configuration.
Protect membership-change operations.

Verification: A client without valid node credentials must not participate in the cluster.

Priority: Critical.

T-004 — Network partition and denial of service

Scenario: An attacker or network failure prevents nodes from communicating.

Impact: The cluster may lose quorum and become unable to commit writes.

Mitigations:

Use redundant network paths where appropriate.
Monitor quorum and connectivity.
Configure operational alerts.
Preserve last-known-good gateway policy state.
Test partition behavior and recovery.

Residual risk: A sufficiently severe network outage can legitimately prevent consensus progress.

Priority: High.

T-005 — Split-brain policy updates

Scenario: Two parts of the system independently accept conflicting policy histories.

Impact: Different nodes or gateways may receive conflicting security decisions.

Mitigations:

Enforce Raft protocol rules.
Reject unsafe writes without quorum.
Apply only committed commands to authoritative state.
Prevent administrative APIs from bypassing consensus.
Validate behavior under network partitions.

Verification: Isolate a minority partition and verify that it cannot commit an independent conflicting policy update.

Priority: Critical.

T-006 — Gateway policy forgery

Scenario: An attacker sends forged policy updates directly to a gateway.

Impact: Unauthorized blocking or unauthorized access.

Mitigations:

Authenticate synchronization sources.
Protect update transport with TLS.
Validate message schemas and policy targets.
Reject invalid revisions and malformed snapshots.
Restrict gateway synchronization endpoints.

Verification: Forged or unauthenticated updates must not modify the active local policy projection.

Priority: Critical.

T-007 — Stale gateway policy

Scenario: A gateway misses a policy revocation or new block because it is disconnected.

Impact: The gateway may continue enforcing an outdated security decision.

Mitigations:

Track the installed policy revision.
Expose synchronization health.
Detect revision gaps.
Support catch-up and full-state synchronization.
Define operational responses to prolonged disconnection.
Monitor the age of the last successful synchronization.

Residual risk: A disconnected gateway cannot learn about changes it has not received.

Priority: High.

T-008 — Malicious or spoofed client IP

Scenario: An attacker manipulates forwarded headers to evade an IP block or cause another client to be blocked.

Impact: Security-policy bypass or false-positive enforcement.

Mitigations:

Define trusted proxy boundaries.
Ignore untrusted forwarded headers.
Normalize IPv4 and IPv6 addresses.
Test multiple proxy-hop configurations.
Document the authoritative client-IP extraction method.

Verification: An untrusted client must not be able to override its effective identity using arbitrary forwarding headers.

Priority: Critical.

T-009 — Policy input abuse

Scenario: An attacker submits malformed IP addresses, oversized payloads, extreme durations, or excessive policy counts.

Impact: Incorrect policy evaluation, crashes, memory exhaustion, or degraded performance.

Mitigations:

Validate IP and CIDR syntax.
Enforce request-body limits.
Bound policy duration and size where appropriate.
Limit request rates and concurrent operations.
Reject invalid data before submitting commands to Raft.
Test malformed and adversarial inputs.

Priority: High.

T-010 — Replay and duplicate operations

Scenario: An attacker or retrying client resubmits a previously valid policy operation.

Impact: Duplicate audit entries, repeated side effects, or unintended state changes.

Mitigations:

Support idempotency keys for applicable operations.
Associate operations with stable request identifiers.
Define duplicate-request behavior.
Make state-machine transitions safe under the documented retry model.

Verification: Replaying the same idempotent request must not create an unintended second logical operation.

Priority: High.

T-011 — Persistent-state corruption

Scenario: A disk failure, corrupted log, or unauthorized storage modification affects a Raft node.

Impact: Node failure, recovery failure, or loss of expected durability.

Mitigations:

Use the selected Raft library's supported persistence mechanisms.
Check and propagate storage errors.
Protect storage permissions.
Test restart recovery and corrupted-storage behavior.
Maintain backups where required.
Never silently treat invalid persistent state as valid committed state.

Priority: Critical.

T-012 — Snapshot tampering

Scenario: A snapshot is modified or replaced before installation.

Impact: A recovering node may load incorrect policy state.

Mitigations:

Use integrity verification provided by the storage and snapshot design.
Validate snapshot metadata and indices.
Authenticate snapshot transfer.
Reject malformed snapshots.
Test recovery from interrupted or invalid snapshot transfers.

Priority: High.

T-013 — Telemetry poisoning

Scenario: An attacker generates misleading traffic or manipulates telemetry to trigger false anomaly detections.

Impact: Legitimate IPs may be proposed for blocking, or attacks may evade detection.

Mitigations:

Validate event schemas.
Authenticate telemetry producers where appropriate.
Apply rate limits and input-size limits.
Record evidence supporting proposals.
Support human approval for sensitive policy changes.
Prevent the anomaly detector from directly modifying authoritative policy state.

Verification: Malformed or suspicious telemetry must not bypass policy validation and authorization.

Priority: High.

T-014 — False-positive automated blocking

Scenario: An anomaly-detection model incorrectly identifies legitimate traffic as malicious.

Impact: Legitimate clients lose access to the API.

Mitigations:

Begin with proposal-only or human-approval mode.
Apply bounded temporary policies.
Provide policy revocation.
Record proposal evidence and outcomes.
Monitor false-positive rates.
Support dry-run evaluation before automatic enforcement.

Priority: High.

T-015 — Denial of service through administrative APIs

Scenario: An attacker floods policy creation, listing, or status endpoints.

Impact: Increased CPU, memory, disk, or replication load; degraded administrative availability.

Mitigations:

Apply authentication and rate limiting.
Bound request sizes and concurrent operations.
Paginate large policy listings.
Monitor rejected requests and resource consumption.
Apply backpressure to expensive operations.

Priority: High.

T-016 — Secret exposure

Scenario: Credentials or private keys are committed to source control, logged, or exposed through configuration endpoints.

Impact: Unauthorized access to nodes or administrative functions.

Mitigations:

Keep real secrets out of version control.
Provide safe example configuration.
Restrict secret-file permissions.
Redact sensitive values from logs.
Rotate exposed credentials.
Use secret scanning in development and CI.

Priority: Critical.

T-017 — Audit-log tampering

Scenario: An attacker modifies or deletes audit records after making a policy change.

Impact: Loss of accountability and incident-response evidence.

Mitigations:

Restrict write access to audit storage.
Record actor, operation, outcome, and request identifier.
Consider forwarding security events to an independently managed log system.
Document retention and backup policies.
Do not claim tamper-proof auditing without a mechanism that supports that guarantee.

Priority: High.

T-018 — Malicious cluster membership changes

Scenario: An unauthorized actor attempts to add or remove a voter.

Impact: Loss of quorum, unauthorized participation, or weakened cluster safety.

Mitigations:

Restrict membership changes to authorized operators.
Use the selected Raft implementation's supported membership-change procedure.
Audit membership operations.
Test membership transitions and failure scenarios.
Avoid direct edits to membership state.

Priority: Critical if dynamic membership is implemented.

7. Security Controls by Release Phase
MVP — Required baseline
Authenticated administrative access.
Input validation.
Protected inter-node transport.
Safe handling of quorum loss.
Trusted client-IP extraction.
Policy update authentication.
Persistent-state error handling.
Audit events for policy changes.
Resource limits on exposed endpoints.
Tests for authorization failures and malformed requests.
Subsequent releases
Fine-grained role-based access control.
Advanced key rotation.
Approval workflows.
Independent audit-log retention.
Additional gateway integrations.
Advanced anomaly-detection protections.
More comprehensive security testing and deployment hardening.

A release must not advertise a control as implemented until its code and tests demonstrate that it exists.

8. Security Testing Plan

The security test suite should include:

Unauthenticated administrative requests.
Authenticated requests with insufficient permissions.
Invalid IP and CIDR inputs.
Oversized request bodies.
Forged gateway policy updates.
Invalid or expired credentials.
Network partitions and minority writes.
Restart after persistent-state errors.
Malformed snapshot installation.
Spoofed forwarded headers.
Duplicate administrative operations.
Telemetry payload abuse.
Unauthorized membership changes, if supported.
Secret-leak checks in logs and repository artifacts.

Security tests should run in CI where practical. Tests requiring privileged network manipulation or destructive storage scenarios may run in a separate controlled test environment.

9. Incident Response Considerations

Operators should have documented procedures for:

Revoking compromised administrative credentials.
Rotating node certificates.
Isolating a compromised node.
Restoring a failed node from trusted persistent state or snapshots.
Investigating unauthorized policy changes.
Recovering from lost quorum.
Identifying gateways with stale policy projections.
Disabling automatic anomaly-driven proposals.
Preserving relevant logs during an incident.

Recovery must follow the selected Raft implementation's supported procedures. Operators must not manually invent a new authoritative history by editing live Raft state.

10. Residual Risks

The following risks remain even with the proposed mitigations:

A compromised authorized administrator may issue harmful policies.
A compromised quorum may violate assumptions that depend on trusted Raft participants.
A network partition can prevent safe progress.
A disconnected gateway may enforce stale policy state.
Clock differences can affect the exact timing of temporary-policy expiration.
A compromised host may expose secrets or modify local files.
An anomaly detector may produce incorrect proposals.
A software defect may violate intended safety or security guarantees.

These risks require operational controls, implementation testing, and explicit documentation rather than unsupported assurances.

11. Review and Maintenance

This threat model must be reviewed when:

New administrative endpoints are added.
Cluster membership behavior changes.
A new gateway integration is introduced.
Automatic policy enforcement is enabled.
Authentication or authorization changes.
Snapshot and recovery behavior changes.
New externally reachable components are deployed.

Security-sensitive architecture decisions should be documented in architecture decision records.

12. Related Documents
../../README.md — Product scope and project overview.
../architecture/overview.md — System architecture.
../architecture/decisions/0001-use-raft-for-policy-consistency.md — Consensus decision.
../architecture/decisions/0002-local-gateway-policy-cache.md — Gateway synchronization.
../architecture/decisions/0003-deterministic-policy-expiration.md — Temporary policy expiration.
../srs/software-requirements-specification.md — Functional and non-functional requirements.
13. Security Principle

Consensus does not replace authentication, authorization, secure transport, input validation, or operational security.

RaftLite must protect both the correctness of distributed policy state and the trustworthiness of the systems that create, distribute, and enforce that state.