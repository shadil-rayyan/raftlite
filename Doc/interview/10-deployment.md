# Deployment & Operations

## Local Development

```bash
# Single-node (development only)
go run ./cmd/raftlite/ --node-id=dev-1 --data-dir=/tmp/raftlite

# Or via docker-compose (3 nodes, Rust drain, Python AI)
docker-compose up
```

## Docker Compose Topology

```
┌────────────────────────────────────────┐
│             docker-compose              │
├────────────────────────────────────────┤
│ node-1 (Go)   ← CPU 0                  │
│   ├── HTTP :8080                        │
│   ├── gRPC :9090                        │
│   └── WAL  /data/raftlite              │
│        ↓ UDS                           │
│ drain-1 (Rust) ← CPU 1                  │
│        ↓ SHM /dev/shm                  │
│ ai-engine (Python) ← CPU 2              │
├────────────────────────────────────────┤
│ node-2 (Go)   ← CPU 3                  │
│ drain-2 (Rust) ← CPU 4                  │
├────────────────────────────────────────┤
│ node-3 (Go)   ← CPU 5                  │
│ drain-3 (Rust) ← CPU 6                  │
└────────────────────────────────────────┘
```

## Key Operations

### Cluster Bootstrap (3 nodes)
1. `docker-compose up` starts all 3 Go nodes
2. Nodes connect via gRPC peers
3. Leader election completes in < 5s (150-300ms per election)
4. All nodes discover each other, cluster is healthy

### Node Failure Detection
1. Leader's AppendEntries to a follower times out (default 150ms heartbeat)
2. After `election timeout` (150-300ms), followers start election
3. New leader elected, log replicated
4. If failed node returns, its WAL replays missing entries

### Scaling Out (3→5 nodes)
1. Admin proposes config change via gRPC
2. Joint consensus: C_OLD,NEW → majority of both required
3. Once committed, C_NEW entry is proposed
4. All 5 nodes operating under new config

### AI-Initiated Block
1. Python AI detects anomaly → gRPC loopback to Go leader
2. Leader proposes `AddBlock` to Raft log
3. Majority commits → applied to FSM (blocklist)
4. Leader response: future requests from IP get 429

## Production Considerations

- **WAL directory**: Use persistent SSD-backed volume (not tmpfs)
- **CPU pinning**: Required for latency guarantees. Without it, Python can starve Raft.
- **Network**: All gRPC between nodes. Keep latency < 5ms between nodes.
- **Monitoring**: Prometheus `/metrics` on each node. Key metrics:
  - `raft_leader`: 1 if this node is leader
  - `raft_log_entries`: total log entries
  - `raft_applied`: committed entries applied to FSM
  - `blocklist_size`: current blocklist entries
  - `request_count`: total requests handled
- **Logging**: Structured JSON logs, collected via container stdout
- **Backup**: WAL files + periodic snapshot exports

## Chaos Engineering

Run `scripts/simulate_chaos.sh` to test failure modes:

```
Simulation Config:
  - Seed: 42 (deterministic replay)
  - Nodes: 3 (or more)
  - Max steps: 10,000
  - Node crash probability: 0.1 per step
  - Network partition probability: 0.05 per step
  - Message drop rate: 0.01
  - Clock drift: ±10ms

Guarantees verified:
  ✓ Safety: no two leaders in same term
  ✓ Liveness: cluster elects a leader eventually
  ✓ No split-brain under partitions
  ✓ Log consistency across all nodes after recovery
```

## Resource Requirements (per node)

| Component | CPU | RAM | Disk |
|-----------|-----|-----|------|
| Go Raft Node | 1 core | 64MB | 200MB+ for WAL |
| Rust Drain | 0.5 core | 16MB | None (SHM) |
| Python AI | 0.5 core | 128MB | None |
| **Total** | **2 cores** | **208MB** | **200MB+** |
