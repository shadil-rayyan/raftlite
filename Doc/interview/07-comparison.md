# Comparison With Alternatives

## RaftLite vs HashiCorp Consul

| Aspect | Consul | RaftLite |
|--------|--------|----------|
| **Purpose** | Service discovery, KV store, segmentation | Distributed rate-limiting + blocklist engine |
| **Performance** | ~5-10k ops/s per server | Target: 25k req/s (p99 < 1.8ms) |
| **Consensus** | hashicorp/raft (wrapped) | Hand-written Raft with DST |
| **DST** | None | Built into every interface from day 1 |
| **Joint Consensus** | Single-server changes only | Full C_OLD,NEW → C_NEW transition |
| **Anomaly Detection** | None | Isolation Forest + streaming ML |
| **Telemetry** | Agent with retry + queue | Rust lock-free ring buffer + Arrow IPC |
| **Deployment** | Agent per node, separate servers | 2 containers per node (Go + Rust) |
| **Key Difference** | General-purpose | Specialized, 10x faster on specific workload |

## Why Not Use HashiCorp/Raft Library?

The standard Raft library was explicitly rejected because:

1. **No DST mocks**: `hashicorp/raft` uses `net.Conn` and `time.Timer` directly. These cannot be mocked for deterministic replay.
2. **No full Joint Consensus**: The library supports adding/removing one server at a time. The Raft dissertation's full Joint Consensus protocol provides stronger safety guarantees during membership changes.
3. **No zero-alloc IPC**: The library's internal architecture doesn't support the telemetry pipeline integration.

Writing Raft from scratch was ~3000 lines of Go over 3 phases. The DST interfaces were worth the effort.

## RaftLite vs Envoy Rate Limiting

| Aspect | Envoy | RaftLite |
|--------|-------|----------|
| **Scope** | Service mesh sidecar | Standalone rate limiter |
| **Consensus** | Global rate limit service (separate deploy) | Raft-integrated |
| **Policy** | Fixed rate limits | ML-driven adaptive |
| **Anomaly Detection** | No | Yes (Isolation Forest) |
| **Integration** | Requires Envoy mesh | Plain HTTP reverse proxy |

## RaftLite vs Fail2Ban / CrowdSec

| Aspect | Fail2Ban | CrowdSec | RaftLite |
|--------|----------|----------|----------|
| **Architecture** | Single-node, log file | Agent + central API | Distributed consensus |
| **Detection** | Regex pattern matching | YARA + community blocks | ML anomaly detection |
| **Consistency** | None (eventual via API) | Eventual via central API | Strong (Raft majority commit) |
| **Latency** | Seconds to minutes | Seconds | Milliseconds (p99 < 1.8ms) |
| **Scalability** | Per-server | Central API bottleneck | Raft log replication |
| **Use Case** | SSH brute force | Web attacks | Sub-ms distributed rate limiting |

## When NOT to Use RaftLite

- You need a general-purpose KV store → use Consul/etcd
- You don't need strong consistency → use Redis or a CDN edge rate limiter
- You have < 100 req/s → use a single-node solution (simpler, zero networking overhead)
- Your budget is zero → use iptables rate limiting + Fail2Ban (free)
