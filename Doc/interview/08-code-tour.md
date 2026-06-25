# Project Code Tour

## Directory Structure

```
distributed_RaftLite/
├── api/                          # Contract definitions (done first!)
│   ├── raft.proto                # Raft gRPC service (AppendEntries, RequestVote, etc.)
│   ├── loopback.proto            # Loopback service (Python→Go anomaly injection)
│   ├── raft.pb.go                # Generated Go protobuf stubs
│   └── raft_grpc.pb.go           # Generated Go gRPC stubs
│
├── internal/
│   ├── transport/                # I/O abstractions + DST harness
│   │   ├── clock.go              # Clock interface (+ real/mock impl)
│   │   ├── network.go            # Network interface (+ real/mock impl)
│   │   ├── storage.go            # Storage interface (+ real/mock impl)
│   │   ├── wal.go                # Flat-file WAL implementation
│   │   └── dst.go                # SimulationHarness for DST
│   │
│   ├── consensus/                # Raft consensus core
│   │   ├── raft.go               # Node struct, Propose(), loop()
│   │   ├── election.go           # Leader election protocol
│   │   ├── replication.go        # Log replication + conflict resolution
│   │   ├── joint.go              # Joint consensus config transition
│   │   ├── fsm.go                # State machine (blocklist application)
│   │   ├── snapshot.go           # Size-based snapshotting
│   │   └── raft_test.go          # Consensus tests
│   │
│   ├── server/                   # gRPC server
│   │   └── grpc.go               # RaftService + LoopbackService impl
│   │
│   └── gateway/                  # HTTP gateway
│       ├── filter.go             # Blocklist middleware + Zero-alloc path
│       ├── pool.go               # sync.Pool for ReqMetadata
│       └── proxy.go              # Reverse proxy + telemetry unix socket write
│
├── cmd/
│   └── raftlite/
│       └── main.go               # Entry point: flags, HTTP routes, gRPC start
│
├── telemetry-drain/              # Rust sidecar
│   ├── Cargo.toml
│   └── src/
│       ├── main.rs               # UDS listener → ring buffer → Arrow batcher
│       ├── ring_buffer.rs        # SPSC lock-free ring buffer
│       └── writer.rs             # Arrow IPC file writer to /dev/shm
│
├── ai-engine/                    # Python sidecar
│   ├── main.py                   # SHM reader → anomaly → gRPC loopback
│   ├── models/
│   │   └── anomaly.py            # Isolation Forest detector
│   └── config.py                 # Config (thresholds, path, gRPC addr)
│
├── tests/
│   ├── e2e_test.go               # End-to-end integration test
│   └── integration_test.go       # Multi-node integration test
│
├── scripts/
│   ├── benchmark.sh              # Latency/throughput benchmarks
│   └── simulate_chaos.sh         # Chaos testing (node crash, partition, seed)
│
├── docker-compose.yml            # 3-node + Rust + Python, CPU-pinned
├── Dockerfile                    # Multi-stage Go build + deploy
├── Dockerfile.drain              # Rust deployment
├── Dockerfile.ai                 # Python deployment
└── .github/workflows/
    ├── security.yml              # SAST scans (Go, Rust, Python)
    └── test-bench.yml            # CI: build → test → DST → benchmark
```

## File Coverage by Phase

| Phase | What | Files |
|-------|------|-------|
| 0 | Contracts | `api/*.proto`, generated `.pb.go` |
| 1 | DST Interfaces | `internal/transport/{clock,network,storage,wal,dst}.go` |
| 2 | Consensus | `internal/consensus/{raft,election,replication,joint,fsm,snapshot}.go` |
| 3 | gRPC Server | `internal/server/grpc.go`, `cmd/raftlite/main.go` |
| 4 | HTTP Gateway | `internal/gateway/{filter,pool,proxy}.go` |
| 5 | Rust Drain | `telemetry-drain/src/{main,ring_buffer,writer}.rs` |
| 6 | Python AI | `ai-engine/{main,config}.py`, `ai-engine/models/anomaly.py` |
| 7 | Integration | `docker-compose.yml`, Dockerfiles, scripts |
| 8 | DevSecOps | `.github/workflows/*`, `prometheus/*` |
