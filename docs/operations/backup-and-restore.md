RaftLite Backup and Restore
1. Purpose

This document defines the requirements for protecting RaftLite's persistent state and recovering from storage loss.

A backup is useful only if its consistency, integrity, compatibility, and restoration procedure are understood and tested.

2. What Must Be Protected

Depending on the selected Raft implementation, backup requirements may include:

Persistent Raft log entries.
Current term and voting metadata.
State-machine snapshots.
Policy state and audit records.
Configuration required to identify the cluster.
Certificates and secrets, handled through an appropriate secret-management system.

The exact set of files and records must come from the implementation's storage contract.

3. Backup Principles
Do not assume copying a live database directory produces a consistent backup.
Use supported snapshots, checkpointing, or application-aware backup mechanisms.
Preserve the metadata needed to associate a snapshot with its log position.
Protect backups against unauthorized access and modification.
Encrypt backups when they contain sensitive data.
Record backup time, version, integrity information, and retention status.
Keep backup credentials separate from ordinary application credentials.
Never overwrite healthy live cluster state with an unverified backup.
4. Backup Procedure

The implementation must define a supported backup command or process before this procedure is operational.

A validated procedure should:

Identify the source node and software version.
Obtain a consistent snapshot or application-aware backup.
Include the required state and metadata.
Calculate or verify integrity information.
Store the backup in protected storage.
Record the backup's timestamp and recovery metadata.
Confirm that the backup can be read and validated.

Example inventory:

Backup ID:
Creation time:
RaftLite version:
Source node:
Snapshot index:
Included audit data:
Integrity verification:
Storage location:


Do not invent snapshot indices or claim success when a backup has not been verified.

5. Restore Procedure

Restore must follow the selected Raft implementation's documented recovery process.

Identify the failure and determine whether a healthy quorum remains.
Decide whether restoring a node or recovering the entire cluster is necessary.
Select a trusted, compatible backup.
Verify its integrity and metadata.
Isolate the target node from unsafe participation while restoration is prepared.
Restore using the supported procedure.
Verify node identity and persistent-state consistency.
Reconnect the node according to the documented recovery process.
Allow the node to reconcile with authoritative cluster state where possible.
Verify policies, audit data, and gateway synchronization.

Whole-cluster disaster recovery is different from restoring one failed follower. It requires a separately designed and tested procedure.

6. Recovery Objectives

The project must define and validate:

RPO: The maximum acceptable amount of lost data.
RTO: The maximum acceptable time to restore the required service.

These values depend on the durability model, backup frequency, storage behavior, and deployment environment. Do not promise numerical RPO or RTO values without testing.

7. Restore Testing

At least one controlled restoration test should verify:

The backup is readable.
Snapshot and log metadata are valid.
The restored node starts safely.
The node does not create a conflicting authoritative history.
Policy state matches the expected recovered state.
Gateway synchronization resumes.
Audit history is available according to the backup scope.

Repeat restore tests when the storage format or Raft implementation changes.

8. Security

Backups can contain sensitive policy metadata, internal addresses, or audit information.

Restrict access, protect integrity, document retention, and securely dispose of expired backups according to the deployment's requirements.

9. Related Documents
Deployment guide
Failure recovery
Threat model