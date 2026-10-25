RaftLite Gateway Integration Guide
1. Purpose

This document describes how an API gateway consumes RaftLite policy state and enforces it locally.

It defines the intended integration contract, not a guarantee that a particular gateway plugin or adapter already exists.

2. Responsibilities

RaftLite control plane:

Maintains authoritative committed policy state.
Distributes committed updates.
Exposes synchronization and operational status.

Gateway adapter:

Authenticates the synchronization source.
Installs policy updates.
Tracks the installed revision.
Evaluates supported policies locally.
Reports synchronization health.

Gateway request handler:

Determines the effective client IP.
Evaluates the installed policy projection.
Applies the documented enforcement result.
Emits suitable telemetry.
3. Request Path
Incoming HTTP request
         |
         v
Determine client IP
         |
         v
Evaluate local policy projection
         |
         +---- Matching deny policy ---> Reject
         |
         +---- No applicable deny -----> Continue


The initial implementation should define exact response behavior for blocked requests and should not imply that every policy type is supported.

4. Policy Distribution

The adapter must support an initial consistent policy snapshot and subsequent incremental updates.

Each installed projection must be associated with an identifiable revision or synchronization position.

The gateway must not report full synchronization if it has missed required updates.

When an update gap is detected, the adapter must follow the documented recovery protocol rather than silently skipping the missing update.

5. Atomic Installation

The active policy projection must not expose partially installed updates.

For an update that affects multiple policies, the implementation must define whether the change is installed atomically or whether intermediate states are allowed.

If atomic installation is required, build a replacement projection and switch to it only after validation succeeds.

6. Client IP Handling

The gateway must define how it obtains the client IP.

Direct connection addresses and forwarded headers are not interchangeable.

Forwarded headers may be trusted only when received through configured trusted proxies. Otherwise, a client may spoof its identity to bypass blocks or cause another client to be blocked.

The implementation must define IPv4 and IPv6 normalization behavior.

7. Failure Behavior

The initial intended mode is last-known-good.

When synchronization fails:

Continue using the last valid installed projection.
Do not clear the projection automatically.
Report degraded synchronization health.
Track the last successfully installed revision.
Attempt recovery through the supported synchronization mechanism.

This behavior does not guarantee that the projection is current.

A disconnected gateway may miss a new block or revocation. Deployments must define acceptable synchronization delays and how to respond when a gateway remains stale.

8. Policy Expiration

Temporary policies contain explicit expiration metadata.

The gateway evaluates expiration according to the documented clock assumptions and expiration boundary.

The gateway must not extend a policy's duration simply because it loses connectivity.

Clock-health failures must be handled according to a documented enforcement policy.

9. Performance

Normal request evaluation must not require a consensus operation.

The implementation should measure:

Local lookup latency.
Throughput.
Memory consumption.
Synchronization propagation delay.
Full-state installation time.
Catch-up time after disconnection.

The data structure must suit the supported policy types. A hash-based lookup may be appropriate for individual IP addresses; CIDR matching requires prefix-aware lookup.

10. Integration Testing

The integration test suite should verify:

Initial synchronization.
IP-block enforcement.
Policy revocation.
Temporary-policy expiration.
Duplicate update handling.
Interrupted synchronization.
Full-state recovery.
Control-plane outage behavior.
Invalid update rejection.
Trusted-proxy behavior.
11. Security

The gateway must authenticate the synchronization source and reject invalid policy data.

Administrative or consensus credentials must not be exposed to ordinary gateway clients.

Logs should include enough information to diagnose policy decisions without unnecessarily disclosing sensitive information.

12. Related Documents
Architecture overview
Local policy projection ADR
Expiration ADR
API contract