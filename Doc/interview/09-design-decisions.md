# Key Design Decisions

## 1. From-Scratch Raft over HashiCorp/Raft

**Decision:** Implement Raft consensus from scratch instead of wrapping `hashicorp/raft`.

**Rationale:**
- `hashicorp/raft` uses `net.Conn` and `time.Timer` directly — cannot mock for DST
- No full Joint Consensus support (only single-server membership changes)
- Internal architecture doesn't expose hooks for zero-alloc IPC integration

**Cost:** ~3000 lines of Go over 3 phases instead of importing a library.

**When to reconsider:** If DST capability or Joint Consensus isn't needed. For most applications, `hashicorp/raft` is fine.

## 2. Two Containers per Node

**Decision:** Run Go (Raft) and Rust (telemetry) in separate containers on each node, rather than a single binary with FFI.

**Rationale:**
- Go crash doesn't kill Rust drain — Rust telemetry survives Go process death
- No cross-language GC interaction (Go's GC doesn't pause Rust)
- Independent CPU pinning via `taskset`
- Independent scaling (could run multiple Rust drains per Go node in production)
- Simpler build and deployment (no cross-compilation for FFI)

**Cost:** Double the container count. Two Dockerfiles instead of one.

## 3. Flat-File WAL over BoltDB

**Decision:** Implement a flat binary WAL file instead of using bbolt/bolt for log storage.

**Rationale:**
- Full control over fsync timing — critical for DST disk stall injection
- Flat format enables direct CRC checksum on every entry (bolt doesn't give you this at the entry level)
- Size-based compaction is trivial with a flat file (check file size, truncate + snapshot)
- No C dependency (bolt uses lmdb's C implementation)

**Cost:** ~200 lines for WAL implementation. No transactions or B-tree indexing (not needed — Raft log is append-only with occasional truncation).

## 4. Arrow IPC over Protobuf for Rust→Python

**Decision:** Apache Arrow IPC via shared memory instead of serializing protobuf or JSON.

**Rationale:**
- Zero serialization: both processes access the same memory layout
- Python pyarrow reads Arrow data natively into numpy-compatible arrays
- Schema enforcement catches version mismatches at read time
- /dev/shm (tmpfs) is in RAM — no disk I/O

**Cost:** Requires both processes to agree on a schema and coordinate synchronization.

## 5. Unix Socket over TCP for Go→Rust IPC

**Decision:** Unix Domain Sockets (datagram) instead of TCP for Go→Rust communication.

**Rationale:**
- Lower latency (no TCP handshake, no checksum offload)
- Same-host communication doesn't need TCP reliability
- Docker compose supports UDS between containers with volumes
- Fire-and-forget semantics: Go never blocks on telemetry write

**Cost:** Not suitable for cross-host communication (UDS is same-host only).

## 6. CPU Isolation via taskset

**Decision:** Pin each execution plane to dedicated CPU cores via Linux `taskset`.

**Rationale:**
- Python ML inference spike cannot starve Go Raft heartbeats
- Prevent CPU cache thrashing between planes
- Deterministic latency for Raft heartbeats (interrupts on pinned cores)

**Cost:** Requires 6+ CPU cores for a 3-node cluster (2 per node). Tighter scheduling constraints.

## 7. sync.Map for Blocklist over RWMutex

**Decision:** `sync.Map` for blocklist storage instead of `map[string]Block` + `sync.RWMutex`.

**Rationale:**
- Reads >> writes (blocklist is read-heavy, writes only on anomaly detection)
- `sync.Map` amortized O(1) read with no lock contention under high concurrency
- Simpler code (no explicit locking on read path)

**When to reconsider:** For < 10k concurrent readers, a `RWMutex`-protected map would be faster.

## 8. Size-Based over Interval-Based Snapshots

**Decision:** Compact WAL at 64MB size threshold rather than every N minutes.

**Rationale:**
- Predictable disk usage regardless of write rate
- No compaction during idle periods
- Simpler implementation (check WAL size on every append)

**Cost:** Under high write rates, snapshots could occur rapidly. Mitigated by snapshot rate-limiting.
