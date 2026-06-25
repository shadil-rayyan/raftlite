# Polyglot Architecture Rationale

## Why Three Languages?

Each language is chosen for a specific, non-overlapping purpose — not for novelty. The goal is "mechanical sympathy": matching the tool to the hardware and workload.

## Go (Performance Plane)

**Role:** Ingress gateway, Raft consensus, reverse proxy.

**Why Go:**
- Goroutines provide cheap concurrency for thousands of simultaneous connections
- `sync.Pool` enables zero-alloc request paths
- Fast compilation, small static binaries
- Good standard library for HTTP/gRPC
- Raft consensus is CPU-bound on networking, not computation — Go's goroutine scheduler excels here

**Constraint:** Zero disk I/O on the critical path. Zero heap allocations per request on the validation path. Memory recycled via `sync.Pool`.

**Trade-off:** Go's garbage collector can cause latency spikes under heap pressure. Solved by keeping the Go heap small (< 45MB) and moving all telemetry/allocation-heavy work to Rust.

## Rust (Observability Plane)

**Role:** High-speed lock-free telemetry ingestion and zero-copy data batching.

**Why Rust:**
- No garbage collector — deterministic sub-millisecond execution
- Zero-cost abstractions for lock-free ring buffer design
- Memory safety without GC (no dangling pointers, no data races)
- `#[repr(C, packed)]` structs map directly to shared memory layout
- Perfect for the "ingest millions of small frames without allocation" use case

**Constraint:** Zero memory allocations in the streaming ingestion loop. Must run out-of-process from Go to prevent GC pressure.

**Trade-off:** Higher development complexity. Ownership model requires careful design for shared-state structures. Mitigated by the simple SPSC ring buffer pattern.

## Python (Analytical Plane)

**Role:** Streaming anomaly detection, pattern analysis, gRPC loopback.

**Why Python:**
- Dominant ecosystem for data science and ML (scikit-learn, numpy, pyarrow)
- Rapid prototyping of detection algorithms
- Isolation Forest, Rolling Z-Score, and statistical methods are Python-first libraries
- Rich ecosystem for gRPC clients

**Constraint:** Must NOT run on the same CPU cores as Go Raft heartbeats. CPU-pinned via `taskset` to dedicated cores.

**Trade-off:** Slowest of the three languages. Solved by: (1) running asynchronously — never on the request path, (2) CPU isolation prevents it from starving Go/Rust, (3) data arrives pre-vectorized via Arrow.

## Cross-Language IPC Strategy

```
Go → Rust: Unix Domain Sockets (datagram mode)
  - 32-byte TelemetryFrame per request
  - Asynchronous fire-and-forget (Go never waits for Rust)
  - Zero-copy on Rust side (reads directly into ring buffer)

Rust → Python: POSIX Shared Memory + Apache Arrow
  - Fixed-size IPC record batches (4096 rows, 96KB each)
  - Rust writes to /dev/shm/raftlite_telemetry.arrow
  - Python memory-maps the same file via pyarrow
  - Zero serialization: both processes share the same physical RAM

Python → Go: gRPC (loopback)
  - Single RPC: AddBlock(ip, reason, ttl)
  - Go Raft leader receives and proposes the block
  - Replicates to followers via Raft log
  - Latency: < 10ms for full loop
```

## Why Not Single-Language?

| Language | If used alone... |
|----------|-----------------|
| Go | GC pauses would affect telemetry ingestion; heap would grow with analytics |
| Rust | Excellent performance but slow for ML prototyping and HTTP proxy development |
| Python | Impossible to reach 25k RPS with sub-millisecond latency |
| C++ | Development velocity would be too slow; ML ecosystem is Python |
