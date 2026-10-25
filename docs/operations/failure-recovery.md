RaftLite Failure Recovery Runbook
1. Purpose

This runbook describes safe responses to common RaftLite failures.

The instructions are operational guidance. Use only commands supported by the deployed release and the selected Raft implementation.

2. General Safety Rules
Determine whether quorum exists before troubleshooting writes.
Do not manually edit the live Raft log.
Do not delete persistent state to force an election.
Do not start two processes with the same node identity and shared storage.
Preserve logs and persistent data during incident investigation.
Do not claim a policy is committed merely because an API request was received.
Verify gateway policy revisions independently from cluster health.
3. Leader Failure
Symptoms
The former leader becomes unreachable.
Policy writes temporarily fail or remain pending.
Followers report an election or leadership transition.
Procedure
Check the status of all voting members.
Confirm whether a communicating majority remains.
Inspect the current leader and election logs.
Allow the configured Raft election mechanism to proceed.
Verify that a new leader is elected.
Submit a test policy through the supported API.
Verify commitment and gateway propagation.

Expected outcome: The cluster resumes writes if a majority remains healthy and the protocol can make progress.

Do not manually designate a leader outside the supported Raft procedure.

4. Follower Failure
Symptoms
One node is unavailable.
Replication progress stops for that node.
The remaining nodes may still form a majority.
Procedure
Confirm that the remaining members can form quorum.
Inspect the failed node's process logs.
Check disk capacity, permissions, and network connectivity.
Restart the node using its supported deployment procedure.
Verify persistent-state recovery.
Confirm that the node catches up with authoritative state.

Do not remove and recreate the node's data directory as a first response.

5. Loss of Quorum
Symptoms
No majority of voters can communicate.
Writes cannot be committed.
Nodes may report no usable leader.
Procedure
Identify all voting members and their last observed status.
Check network connectivity and host health.
Restore communication between a majority of the existing members.
Inspect storage errors before restarting affected nodes.
Verify election and commitment progress after connectivity returns.
Confirm policy consistency and gateway synchronization.

Expected outcome: Writes resume after the cluster regains quorum and the protocol can make progress.

If quorum cannot be restored, follow a separately reviewed disaster-recovery procedure. Never invent a new authoritative history by independently restarting isolated nodes with modified state.

6. Gateway Is Stale
Symptoms
A gateway's installed policy revision is behind the control plane.
Synchronization errors appear in logs or metrics.
A recently committed policy is not yet enforced.
Procedure
Check control-plane availability.
Inspect the gateway's last successfully installed revision.
Check authentication and connectivity for the synchronization transport.
Determine whether the gateway can resume incremental synchronization.
If required history is unavailable, initiate the supported full-state synchronization.
Verify the installed revision and test enforcement.

Do not assume that a healthy control-plane cluster means every gateway has received the latest policy.

7. Storage Failure
Symptoms
Persistence operations fail.
A node cannot recover its log or snapshot.
Disk capacity or filesystem errors appear.
Procedure
Preserve relevant logs and error information.
Determine whether the failure is transient or indicates data corruption.
Protect the remaining healthy quorum.
Follow the storage and Raft implementation's recovery guidance.
Restore from trusted persistent data or a supported recovery mechanism.
Verify node identity, log consistency, and catch-up.

Do not bypass storage errors or manually edit protocol metadata.

8. Unexpected Policy Behavior
Identify the policy ID and request correlation ID.
Verify whether the policy was committed.
Inspect the authoritative policy record.
Inspect the gateway's installed revision.
Check expiration, revocation, precedence, and client-IP extraction.
Check whether the gateway uses the expected configuration.
Preserve relevant audit records and logs.

Distinguish a control-plane inconsistency from a delayed update, expired policy, incorrect client identity, or gateway configuration problem.

9. Post-Incident Validation

After recovery:

Quorum and leader status are healthy.
No unexplained persistence errors remain.
Failed nodes have caught up.
Gateway revisions are current or their lag is understood.
Policy enforcement has been verified.
Audit history has been preserved.
Recovery actions are documented.
Follow-up tests or corrective work are recorded.
10. Related Documents
Deployment guide
Monitoring guide
Backup and restore
Troubleshooting