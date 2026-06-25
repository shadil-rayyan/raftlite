# Architecture Overview

## The Three Execution Planes

RaftLite enforces a strict separation of concerns across three isolated execution planes to eliminate CPU/memory starvation and minimize tail latency.

```
[ HTTP/gRPC Traffic ]
         │
         ▼
┌─────────────────────┐   Performance Plane (Go)
│   Go Raft Node      │   In-memory blocklist check (< 0.1ms)
│   (Consensus+Proxy) │   Zero heap alloc on critical path
└────────┬────────────┘
         │ (Unix Socket IPC)
         ▼
┌─────────────────────┐   Observability Plane (Rust)
│  Rust Telemetry     │   Lock-free ring buffer
│  Drain              │   Zero allocations in ingestion loop
└────────┬────────────┘
         │ (Shared Memory / Apache Arrow)
         ▼
┌─────────────────────┐   Analytical Plane (Python)
│  Python AI Engine   │   Streaming Isolation Forest
│  (Anomaly Detection)│   CPU-pinned to separate core
└────────┬────────────┘
         │ (gRPC Loopback)
         ▼
   Go Raft Leader updates blocklist
```

### Performance Plane (Go)
- HTTP/gRPC reverse proxy gateway
- Raft consensus algorithm (leader election, log replication, joint config)
- In-memory blocklist check via `sync.Map`
- `sync.Pool` for zero-alloc request metadata
- Exposes `/metrics` for Prometheus scraping

### Observability Plane (Rust)
- Unix socket listener receives telemetry from Go
- Lock-free SPSC ring buffer in shared memory
- Zero-copy Apache Arrow IPC batches to `/dev/shm`
- No GC pauses — deterministic sub-millisecond execution

### Analytical Plane (Python)
- Reads memory-mapped Arrow dataframes from shared memory
- Streaming Isolation Forest anomaly detection
- Rolling Z-Score fallback for baseline drift detection
- gRPC loopback: sends `AddBlock` to Go Raft leader on anomaly detection

## Key Design Principles

1. **Mechanical Sympathy**: Each language does what it does best. Go for networking, Rust for zero-copy data, Python for ML.
2. **CPU Isolation**: Linux `taskset` pins each plane to dedicated cores. A Python CPU spike cannot choke Go Raft heartbeats.
3. **No Serialization Tax**: Rust→Python IPC uses POSIX shared memory + Apache Arrow. Zero serialization overhead between planes.
4. **DST from Day 1**: All I/O goes through mockable interfaces (`transport.Clock`, `transport.Network`, `transport.Storage`).
