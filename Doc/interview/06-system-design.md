# End-to-End System Design

## Request Flow (Normal Case)

```
1. Client sends HTTP GET /v1/ip/1.2.3.4
   → Go Gateway (port 8080)

2. Go Gateway extracts IP from URL path
   → sync.Pool.Get(ReqMetadata) for metadata
   → blocklist.Load(ip)
   → If blocklisted → 429 Too Many Requests (instantly)
   → If not blocklisted → returns 200 OK + proxy to upstream

3. After response, telemetry frame written to Rust drain:
   → TelemetryFrame{ip, timestamp_μs, status_code, latency_ns}
   → Written to Unix Domain Socket (fire-and-forget, non-blocking)

4. Rust Drain reads TelemetryFrame from UDS
   → SPSC ring_buffer.push(frame)
   → When ring buffer reaches batch_threshold (4K):
     → Serialize batch to Arrow IPC format
     → memcpy to /dev/shm (shared memory file)

5. Python AI Engine wakes on new Arrow batch (poll loop)
   → memory-map the /dev/shm file via pyarrow
   → dataframe = pa.ipc.open_file(mmap).read_all()
   → Extract features: request_rate, error_rate, latency_p50, entropy
   → model.predict(features) → anomaly_score

6. If anomaly_score > threshold:
   → Python calls gRPC loopback: stub.AddBlock(Block{Ip, Reason, TTL})
   → Go leader receives gRPC call → RaftNode.Propose(Block)
   → Propose: leader appends to WAL, replicates to majority, commits
   → Apply to FSM: blocklist.Store(blockedIP)
   → Next request from that IP → 429 (via step 2)
```

## Data Structures

### Blocklist (FSM State)
```go
// Go sync.Map — concurrent-safe, amortized O(1) read
type BlocklistFSM struct {
    entries sync.Map  // map[[16]byte]BlockEntry — IP as 16-byte key
}

type BlockEntry struct {
    IP        [16]byte  // net.IP 16 bytes
    Reason    string
    BlockedAt time.Time
    TTL       time.Duration
    ExpiresAt time.Time
}
```

### Raft Log Entry
```go
type LogEntry struct {
    Index uint64    // Monotonically increasing
    Term  uint64    // Leader's term at time of entry
    Type  uint8     // 0=Command, 1=ConfigChange, 2=NoOp
    Data  []byte    // Serialized command (e.g., AddBlock)
}
```

### Telemetry Frame
```rust
#[repr(C, packed)]
struct TelemetryFrame {
    ip: [u8; 16],        // IPv6-ready 16 bytes
    ts: u64,             // μs since epoch
    status: u16,         // HTTP status code
    latency: u64,        // ns
}
// Total: 40 bytes per frame
```

### Ring Buffer
```rust
struct RingBuffer {
    buffer: [UnsafeCell<TelemetryFrame>; 4096],
    head: AtomicU64,     // Producer index
    tail: AtomicU64,     // Consumer index
    // Lock-free SPSC: single producer (Go UDS reader),
    // single consumer (Arrow batcher goroutine)
}
```

## Configuration

RaftLite is configured via CLI flags (`cmd/raftlite/main.go`):

```
--node-id          Node identifier (e.g., "node-1")
--http-addr        HTTP gateway listen address (:8080)
--grpc-addr        gRPC listen address (:9090)  
--peers            Comma-separated peer addresses
--data-dir         WAL data directory (/tmp/raftlite)
--unix-sock        Rust telemetry drain socket path
--cpu-affinity     CPU core for taskset
--snapshot-size    WAL compaction threshold (64MB)
--election-timeout Raft election timeout (ms)
--heartbeat-interval Raft heartbeat interval (ms)
```
