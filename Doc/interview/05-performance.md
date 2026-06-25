# Performance Targets & Benchmarks

## Performance Budget

| Metric | Target | Actual (3-node, laptop) | Measurement |
|--------|--------|------------------------|-------------|
| P50 via leader | < 0.5ms | — | `scripts/benchmark.sh` |
| P99 via leader | < 1.8ms | — | `scripts/benchmark.sh` |
| P99 read via lease | < 0.3ms | — | `scripts/benchmark.sh` |
| Throughput (single leader) | 25,000 req/s | — | `scripts/benchmark.sh` |
| Max blocklist size | 100M entries | — | Math: 100M×16B = 1.6GB RAM |
| Raft log compaction | 64MB threshold | — | Config in `cmd/raftlite/main.go` |
| Node join time (3→5) | < 30s | — | `scripts/simulate_chaos.sh` |
| Failover time (leader crash) | < 5s | — | `scripts/simulate_chaos.sh` |

## Zero-Alloc Path

The critical blocklist-check path allocates zero heap memory per request:

```
HTTP Request → proxy_handleRequest
  sync.Pool.Get(ReqMetadata)      ← pool allocation (reused)
  blocklist.Load(ip)              ← sync.Map read (no alloc)
  sync.Pool.Put(md)               ← return to pool
  Response ← 200 OK / 429 Too Many Requests
```

No `malloc` on the hot path. This is critical for GC pressure control.

## What Determines Tail Latency?

1. **Raft log commit**: leader writes to WAL (`O_SYNC` fsync ~0.1ms SSD), replicates to majority, commits. This is the dominant cost.
2. **gRPC serialization**: Go protobuf marshaling for inter-node RPC (sub-microsecond).
3. **Network overhead**: Inter-node latency (sub-millisecond same-AZ).
4. **GC pauses**: Mitigated by keeping per-node heap under 45MB. Rust/Python run out-of-process.

## Known Bottlenecks

1. **fsync per Append**: Each log entry calls `fdatasync`. Batching could help at high throughput but adds latency for individual requests. Tuned for tail latency over throughput.
2. **Sequential WAL writes**: Single-threaded append. SSD random-write performance doesn't matter for a sequential WAL.
3. **Arrow batch overhead**: 4096-row batch size tuned for /dev/shm (tmpfs) latency. Smaller batches = more frequent IPC, larger batches = stale telemetry.
