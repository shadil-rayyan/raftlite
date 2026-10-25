RaftLite Deployment Guide
1. Purpose

This guide defines the intended deployment procedure for a three-node RaftLite development cluster.

Commands and service names must be adapted to the actual repository implementation. Do not treat the examples as executable until the corresponding Docker Compose services and flags exist.

2. Prerequisites
Docker and Docker Compose.
Git.
Access to the RaftLite source repository.
Sufficient disk space for persistent node data.
Available ports for the API, consensus transport, metrics, and gateway.
A development configuration for each node.

Production deployments additionally require secured credentials, authenticated inter-node communication, persistent storage, monitoring, and a documented recovery procedure.

3. Intended Topology
                 Administrator
                      |
                      v
                RaftLite API
                      |
          +-----------+-----------+
          |           |           |
          v           v           v
       Node 1       Node 2       Node 3
       Voter        Voter        Voter
          \           |           /
           +------ Raft ----------+

                 Policy Sync
                      |
              +-------+-------+
              |               |
              v               v
           Gateway A       Gateway B


The cluster has three voting members. A majority of two is required for quorum.

4. Build the Project

From the repository root:

git status
go test ./...
go build ./...


Run these commands only after the Go module and packages have been created. If the repository contains multiple Go modules, run the commands from the appropriate module directories.

Build and test the Rust and Python components separately if they are present.

5. Configure the Nodes

Each node must have a unique identity and persistent storage.

Example conceptual configuration:

node:
  id: node-1
  data_dir: /var/lib/raftlite

cluster:
  members:
    - node-1
    - node-2
    - node-3

security:
  require_authenticated_peers: true

observability:
  metrics_enabled: true


The actual configuration keys must match the implementation. Do not copy this example into production without checking the supported schema.

6. Start the Development Cluster

If the repository provides a Compose file:

docker compose config
docker compose up --build -d
docker compose ps


Inspect the logs:

docker compose logs --tail=100


If the Compose file or required configuration is not yet implemented, complete those files before attempting to start the cluster.

7. Verify Cluster Health

Use the configured health endpoints or CLI commands to verify:

All three nodes have started.
Each node has a unique identity.
The cluster has elected a leader.
Followers can communicate with the leader.
Persistent data directories are writable.
The cluster can commit a test policy.

A process being alive does not prove that the cluster has quorum or is ready to commit writes.

8. Verify Policy Enforcement

After the policy API and gateway synchronization are implemented:

Create a test IP-block policy.
Verify the API reports the correct commitment status.
Verify the gateway installs the policy.
Send a request that matches the policy.
Verify the expected enforcement response.
Inspect the gateway's installed policy revision.

Use reserved documentation IP ranges and isolated test traffic. Do not block real users during deployment testing.

9. Development Versus Production

Docker Compose is suitable for local development and controlled failure tests.

A production deployment requires additional engineering, including:

Secure transport and identity management.
Persistent volumes and storage monitoring.
Resource limits and capacity planning.
Network isolation.
Backup and restore procedures.
Monitoring and alerting.
Credential rotation.
Upgrade and rollback procedures.
Tested disaster recovery.

A successful local startup does not establish production readiness.

10. Shutdown

For a development environment:

docker compose down


Use docker compose down -v only when intentionally deleting the associated volumes and their data.

Do not remove persistent volumes as a routine recovery action.

11. Deployment Acceptance Checklist
All intended services start.
Node identities are unique.
The leader is elected.
Quorum-dependent operations behave correctly.
A policy can be committed.
A gateway can install and enforce the policy.
Restart recovery has been tested.
Logs and metrics are accessible.
Secrets are not stored in source control.
The documented shutdown procedure preserves persistent state.
12. Related Documents
Configuration reference
Monitoring guide
Failure recovery
Backup and restore
Troubleshooting