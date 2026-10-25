RaftLite Monitoring and Observability
1. Purpose

RaftLite must help operators determine whether the control plane is healthy, whether writes can be committed, whether gateways have current policies, and whether the system is recovering from a failure.

Metrics listed in this document are proposed instrumentation requirements. They must not be assumed to exist until implemented.

2. Health Signals
Liveness

GET /healthz

Indicates whether the process is alive according to the implementation's liveness contract.

Readiness

GET /readyz

Indicates whether the node is ready for the operation being evaluated.

Readiness should distinguish conditions such as:

Storage unavailable.
Node initializing.
Required configuration invalid.
Consensus transport unavailable.
No current leader or quorum.
Gateway synchronization not complete, where relevant to the component.

Liveness and readiness must not be treated as interchangeable.

3. Recommended Metrics
Consensus
raftlite_leader_changes_total
raftlite_elections_total
raftlite_commit_index
raftlite_snapshot_index
raftlite_raft_log_entries
Policy API
raftlite_requests_total
raftlite_policy_commits_total
raftlite_policy_rejections_total
raftlite_policy_commit_duration_seconds
Gateway
raftlite_policy_lookup_duration_seconds
raftlite_blocklist_size
raftlite_gateway_sync_revision
raftlite_gateway_sync_failures_total

These names are recommendations. The implementation must define metric types, labels, units, and semantics. Export replication lag only if its calculation is well defined.

Avoid unbounded labels such as raw IP addresses, policy IDs, or arbitrary error strings.

4. Operational Questions

An operator should be able to answer:

Which node is the leader?
Is the cluster able to commit writes?
Are followers making progress?
Is a node persistently behind?
Are policy writes being rejected?
How long do policy commits take?
Which gateways are disconnected?
Which gateways have stale policy revisions?
Is the system repeatedly electing leaders?
Is storage approaching capacity?
5. Alerting Recommendations

Create alerts for:

Loss of quorum.
Sustained failure to commit writes.
Repeated leader elections.
Persistent storage errors.
Increasing disk usage.
Repeated gateway synchronization failures.
Excessive policy rejection rates.
Abnormally high policy commit latency.
A gateway remaining stale beyond the configured operational threshold.

Alert thresholds must be chosen from actual deployment behavior and service objectives.

6. Logging

Use structured logs for important events, including:

Node startup and shutdown.
Leader transitions.
Policy creation and revocation.
Failed policy validation.
Persistence failures.
Snapshot installation.
Gateway synchronization errors.
Authentication and authorization failures.

Logs should contain correlation identifiers where useful and must not contain credentials or unnecessary sensitive request data.

7. Audit Versus Metrics

Metrics describe aggregate behavior. Logs help investigate individual events. Audit records provide an attributable history of security-sensitive operations.

These functions are related but not interchangeable.

A counter showing that a policy was committed is not a substitute for an audit record identifying the actor and operation.

8. Dashboard Layout

A useful initial dashboard has four sections:

Cluster: Node status, leader, quorum, term.
Replication: Commit position, snapshot position, follower progress.
Policies: Commits, rejections, active-policy count.
Gateways: Installed revisions, synchronization failures, policy lookup latency.

Only display fields that are actually available and correctly defined.

9. Validation

Monitoring acceptance tests should verify that:

Health endpoints respond with the documented semantics.
Metrics are exported in the configured format.
Policy operations update their associated counters.
Simulated failures produce observable signals.
Sensitive data is not exposed through metrics or logs.
10. Related Documents
Deployment guide
Failure recovery
Troubleshooting