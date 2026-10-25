RaftLite Configuration Reference
1. Purpose

This document defines the intended configuration categories for RaftLite.

The actual configuration schema must be derived from the application's parser and validated against its implementation. The examples below are illustrative until implemented.

2. Configuration Principles
Give every node a stable, unique identity.
Keep persistent state in a dedicated data directory.
Separate node-specific settings from cluster-wide settings.
Reject invalid or contradictory configuration at startup.
Never silently generate production credentials.
Keep secrets out of version control.
Document defaults and configuration changes between releases.
3. Example Configuration
node:
  id: node-1
  data_dir: /var/lib/raftlite

cluster:
  members:
    - node-1
    - node-2
    - node-3

server:
  listen_address: "0.0.0.0:8080"

consensus:
  election_timeout: "1s"
  heartbeat_interval: "200ms"

policy:
  maximum_ttl: "24h"

gateway:
  enforcement_mode: "last_known_good"

security:
  tls_enabled: true

audit:
  enabled: true

telemetry:
  enabled: false


These values are examples, not validated production defaults. The implementation must define supported fields, legal ranges, and whether settings may be changed at runtime.

4. Node Identity

Each Raft node must have a stable identity.

The node identity must not change merely because the process restarts. Reusing an identity with incompatible persistent state must be handled according to the selected Raft implementation's recovery rules.

5. Persistent Storage

The data directory must:

Be writable by the service account.
Be protected against unauthorized access.
Have sufficient disk capacity.
Survive container replacement when durability is required.
Be included in the documented backup procedure.

Storage errors must be reported explicitly. A node must not claim durable success after a required persistence operation fails.

6. Network Configuration

Document the address and purpose of every listener:

Administrative API.
Raft transport.
Gateway synchronization.
Metrics endpoint.

Expose only the listeners required by the deployment. Administrative and consensus interfaces should not be exposed directly to untrusted networks.

7. Timeouts

Consensus timing values must be compatible with the selected Raft library and expected network latency.

Do not tune election and heartbeat settings independently without understanding the implementation's timing assumptions.

Policy expiration uses UTC timestamps and documented clock assumptions. Raft timeouts do not synchronize physical clocks.

8. Enforcement Mode

The initial intended mode is last_known_good.

When control-plane connectivity is lost, the gateway continues using its last valid installed policy projection and reports degraded synchronization health.

Fail-open and fail-closed modes require explicit semantics and security review before being advertised as supported.

9. Security Configuration

Production configuration must define:

Peer authentication.
TLS certificate and key locations.
Administrative authentication.
Authorization policy.
Secret rotation procedures.
Trusted proxy configuration.
Audit settings.

Private keys and tokens must not be embedded in committed example configuration.

10. Configuration Validation

At startup, the application should reject:

Missing node identity.
Duplicate or invalid member identities.
Invalid addresses.
Unsupported enforcement modes.
Invalid durations.
Unreadable required storage paths.
Invalid certificate configuration.
Incompatible consensus settings.

Validation errors should identify the invalid setting without exposing secrets.

11. Environment Overrides

If environment-variable overrides are supported, document their exact names and precedence.

Recommended precedence:

Explicit command-line override, if supported.
Environment overrides.
Configuration file.
Documented safe default.

The implementation must choose one precedence model and test it.

12. Related Documents
Deployment guide
Monitoring guide
Failure recovery