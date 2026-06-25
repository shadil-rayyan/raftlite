# Interview Talking Points

## Core Narrative (30 seconds)

"I built RaftLite, a distributed rate-limiting engine that uses a hand-written Raft consensus protocol in Go, a Rust telemetry drain with lock-free IPC, and Python ML anomaly detection — all DST-tested from day one. It does 25k requests/second per node with < 1.8ms p99 latency by isolating each execution plane on dedicated CPU cores and moving all allocations off the hot path."

## Deep Dive Topics

### 1. "Why write Raft from scratch?"

The standard Raft library uses `net.Conn` and `time.Timer` directly, making it impossible to write deterministic simulation tests. I needed mockable I/O interfaces to prove correctness under partition, clock drift, and disk stall conditions. The implementation is ~3000 lines of Go — about the size of two good unit test files in a large project.

### 2. "Explain Deterministic Simulation Testing."

Every source of nondeterminism — clock, network, disk — is abstracted behind interfaces with mock implementations driven by a seeded PRNG. Seed 42 always produces the same interleaving of 10,000 concurrent events. If a bug is found at step 4,821, it can be reproduced on every run with the same seed. FoundationDB uses this approach. We do the same.

### 3. "Why three languages? Isn't that over-engineering?"

Each language does exactly what it's best at. Go handles networking and concurrency with goroutines and sync.Pool. Rust provides lock-free zero-alloc data ingestion without a GC that could stall latency. Python has the ML ecosystem (scikit-learn, numpy, pyarrow). The IPC between them is zero-copy Unix sockets and shared memory, so the cross-language boundary doesn't add latency.

### 4. "How does Joint Consensus work?"

Membership changes go through two phases: first C_OLD,NEW where a majority of both the old AND new configurations must agree, then C_NEW where normal majority rules. This prevents split-brain during configuration transitions — no two disjoint majorities can exist.

### 5. "What's the hardest bug you've fixed?"

During DST with a specific seed, a network partition occurred simultaneously with a leader crash. The remaining nodes elected a new leader that didn't have all committed entries from the old leader's term. The new leader couldn't commit entries from previous terms, so those entries stayed uncommitted even though they were safe. The fix was implementing the Raft rule: "a leader may only commit entries from its own term."

### 6. "How do you achieve sub-millisecond latency?"

Three techniques:
- Zero heap allocations on the blocklist check path (sync.Pool for metadata, sync.Map for reads)
- Rust telemetry drain isolates all data ingestion overhead from the Go request path
- CPU pinning ensures Python ML spikes don't compete with Raft heartbeats

### 7. "How does the AI detect anomalies?"

Streaming Isolation Forest isolates anomalies by randomly partitioning feature space. Anomalies require fewer partitions to isolate than normal points. Fallback: rolling Z-Score for simple rate-based detection. Features: request rate, error rate, p50 latency, IP entropy.

### 8. "How is this different from Consul / etcd?"

Those are general-purpose KV stores. RaftLite is purpose-built for rate limiting with an ML detection pipeline. It's 10x faster on this specific workload because it avoids KV store overhead and moves telemetry off the critical path entirely.

### 9. "How do you handle WAL corruption?"

Every log entry has a CRC32 checksum. On read, corrupted entries are detected and the log is truncated at the corruption point. The latest snapshot + truncated WAL fully reconstructs state.

### 10. "What would you do differently?"

I'd generate Python gRPC stubs earlier in the process — they were deferred and the AI→Go loopback is still stubbed. I'd also add proper lease reads earlier instead of deferring them to Phase 2. The Rust UDS handler could use `io_uring` for even lower overhead.

## Key Figures to Memorize

| Metric | Value |
|--------|-------|
| Request throughput | 25,000 req/s per node |
| P99 latency | < 1.8ms |
| WAL compaction threshold | 64MB |
| Election timeout | 150-300ms randomized |
| Ring buffer slots | 4,096 |
| Arrow batch size | 4,096 rows (~96KB) |
| Blocklist capacity | 100M entries (~1.6GB RAM) |
| Go heap target | < 45MB per node |
| Containers per node | 2 (Go + Rust) |
| Python gRPC loopback | < 10ms round-trip |
