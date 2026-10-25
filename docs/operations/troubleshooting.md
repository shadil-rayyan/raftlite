RaftLite Troubleshooting Guide
1. Purpose

Use this guide to investigate common cluster, API, storage, and gateway synchronization problems.

Collect diagnostic information before restarting services or changing persistent state.

2. No Leader Is Reported

Possible causes:

Initial election is still in progress.
Nodes cannot communicate.
Fewer than a majority of voters are available.
Storage or configuration errors prevent participation.
Election settings are unsuitable for the environment.

Checks:

Inspect the status and logs of every voter.
Verify unique node identities.
Check consensus connectivity.
Check persistent storage health.
Review recent leader-election events.

Do not manually assign a leader.

3. Policy Writes Fail

Possible causes:

No quorum.
Administrative authorization failure.
Invalid policy input.
Storage failure.
Request timeout.
Leader transition.

Checks:

Inspect the API error and request ID.
Check the current leader and quorum state.
Validate the request payload.
Inspect persistence errors.
Determine whether the operation was committed before retrying.

Use idempotency keys where supported to avoid unintended duplicate operations.

4. A Policy Is Missing From a Gateway

Possible causes:

The policy was not committed.
Synchronization is delayed.
The gateway is disconnected.
The gateway rejected an update.
The gateway has not completed initial synchronization.

Checks:

Confirm the policy's authoritative status.
Inspect the gateway's installed revision.
Review synchronization logs.
Check transport authentication and connectivity.
Verify whether a full-state synchronization is needed.
5. A Blocked IP Is Still Allowed

Investigate:

Whether the policy is committed and active.
Whether it has expired or been revoked.
Whether the request reaches the expected gateway.
Whether the gateway has installed the policy.
Whether the actual client IP matches the policy target.
Whether a trusted proxy configuration is correct.
Whether policy precedence affects the decision.

Do not assume the consensus algorithm is at fault until these conditions have been checked.

6. A Legitimate Client Is Blocked

Investigate:

The policy ID and reason.
The source and creator of the policy.
The target IP or CIDR.
Client-IP extraction and proxy configuration.
Expiration and revocation status.
Automated detection evidence, if applicable.

Revoke or modify a policy through the supported administrative API. Preserve the audit record.

7. A Node Will Not Restart

Check:

Configuration validity.
Node identity.
File ownership and permissions.
Disk capacity.
Persistent-state integrity.
Port conflicts.
Certificate validity.
Compatibility with the deployed software version.

Do not delete the data directory simply to make startup succeed.

8. Disk Usage Is Growing

Inspect:

Raft log growth.
Snapshot creation and retention.
Audit retention.
Application logs.
Temporary files.
Backup artifacts.

Only compact or remove data through supported mechanisms. Do not manually delete live consensus-log files.

9. Gateway Synchronization Repeatedly Fails

Check:

Network connectivity.
TLS and peer authentication.
Update schema compatibility.
Revision gaps.
Snapshot-transfer errors.
Resource limits.
Gateway logs.

Record the last successfully installed revision before attempting recovery.

10. Performance Is Poor

Measure separately:

Local gateway policy lookup latency.
Policy commit latency.
Replication progress.
API request latency.
CPU and memory usage.
Disk latency.
Gateway synchronization delay.

Do not infer local lookup performance from policy commit latency. They are different operations.

11. Diagnostic Information to Collect

For an incident, record:

RaftLite version.
Deployment topology.
Node roles and availability.
Relevant configuration, with secrets removed.
Error messages and timestamps.
Policy request IDs.
Commit and snapshot positions, if available.
Gateway synchronization revision.
Recent deployment or configuration changes.
12. Escalation

If the cause remains unclear:

Preserve logs and persistent data.
Avoid destructive recovery actions.
Reproduce the issue in an isolated test environment where possible.
Create a minimal failure scenario.
Add a regression test before considering the issue resolved.
13. Related Documents
Monitoring guide
Failure recovery
Backup and restore