```markdown
# agent.md: Autonomous System Specification for RaftLite

This document serves as the absolute, immutable engineering specification and execution blueprint for **RaftLite**: a high-performance, fault-tolerant, polyglot distributed rate-limiting and policy engine. 

Any engineering agent or systems developer initializing this workspace must adhere strictly to the architectural constraints, language boundaries, and testing protocols defined herein.

---

## I. Architectural Core & Language Demarcation

RaftLite enforces a strict separation of concerns across its execution planes to optimize for **mechanical sympathy**, eliminating CPU/Memory starvation and minimizing tail latencies ($p99 < 1.8\text{ms}$).


```

```
   [ Live Network Traffic Streams ]
                  │

```

(HTTP Gateway)     ▼ (In-Process Memory Check)
┌─────────────────────────┐
│      Go Raft Node       │ ──► [ Allowed to Target Service ]
└────────────┬────────────┘
│
(POSIX shm)     ▼ (Asynchronous / Zero-Allocation)
┌─────────────────────────┐
│  Rust Telemetry Drain   │
└────────────┬────────────┘
│
(Memory-Mapped)    ▼ (Batch Vectorized Data)
┌─────────────────────────┐
│    Python AI Engine     │
└────────────┬────────────┘
│
(gRPC / Loopback)     ▼ (Proactive Anomaly Detected)
└───────► [ Updates Cluster State via Raft Leader ]

```

### 1. The Performance Plane (Go)
* **Role:** Active ingress gateway, reverse proxy, and consensus state holder.
* **Execution:** Processes concurrent incoming HTTP/gRPC requests via cheap goroutines. Performs state validation against an in-memory bitmap/atomic map blocklist.
* **Boundary Constraint:** **Zero disk I/O and zero complex string mutations** on the critical request path. Memory must be recycled via `sync.Pool`.

### 2. The Observability Plane (Rust)
* **Role:** High-speed, lock-free telemetry drain.
* **Execution:** Intercepts request metadata from the Go gateway asynchronously via Linux UNIX domain sockets or POSIX shared memory ring buffers.
* **Boundary Constraint:** Must run completely out-of-process relative to the Go application heap. **Strictly zero memory allocations** inside the streaming ingestion loop.

### 3. The Analytical Plane (Python)
* **Role:** Multi-dimensional pattern evaluation and anomaly detection.
* **Execution:** Consumes vectorized telemetry chunks directly via memory-mapped Apache Arrow dataframes. Runs streaming Isolation Forests or Rolling Z-Score algorithms.
* **Boundary Constraint:** Isolated to distinct CPU cores via Linux `taskset` to prevent Python runtime execution spikes from choking Go Raft heartbeats.

---

## II. Project Directory Structure

```text
.
├── .github/                     # DevSecOps & CI/CD Workflows
│   └── workflows/
│       ├── security.yml         # SAST, DAST, Dependency Scanning
│       └── test-bench.yml       # Automated Integration & Load Tests
├── cmd/
│   └── raftlite/                # Go Application Entry Point
│       └── main.go
├── internal/
│   ├── consensus/               # Core Raft Protocol Implementation
│   │   ├── fsm.go               # Finite State Machine for Blocklists
│   │   ├── joint.go             # Joint Consensus (Dynamic Membership)
│   │   └── raft.go              # Node State & Election Mechanics
│   ├── gateway/                 # Reverse Proxy & Validation Path
│   │   ├── filter.go            # Low-Latency Bitmap Checkers
│   │   └── pool.go              # sync.Pool Allocators
│   └── server/                  # Inter-node gRPC Communication
│       └── grpc.go
├── telemetry-drain/             # Rust Workspace
│   ├── Cargo.toml
│   └── src/
│       ├── main.rs              # IPC Listener & Ingestion Loop
│       ├── ring_buffer.rs       # Lock-Free Shared Memory Layout
│       └── writer.rs            # Zero-Copy Apache Arrow Batcher
├── ai-engine/                   # Python Workspace
│   ├── requirements.txt
│   ├── main.py                  # Stream Ingestion & Loopback Client
│   └── models/
│       ├── anomaly.py           # Isolation Forest / Rolling Z-Score
│       └── config.py
├── scripts/                     # Operational Tools & Benchmarks
│   ├── benchmark.sh             # ghz / wrk Load Testing Execution
│   └── simulate_chaos.sh        # Network Partition & Kill Simulations
├── tests/                       # Global Integration Suite
│   ├── deterministic_test.go    # Deterministic Simulation Harness (DST)
│   └── integration_test.go
├── docker-compose.yml           # Local Cluster Testbed Topology
└── agent.md                     # This System Specification

```

---

## III. Detailed Toolchain & Technology Purpose

| Technology / Tool | Layer | Explicit Purpose | System Rationale |
| --- | --- | --- | --- |
| **Go (1.22+)** | Core Gateway / Consensus | High-concurrency network handling and deterministic consensus state machine. | Gorilla-style concurrency via goroutines provides low-latency proxying; simple binaries fit well on edge servers. |
| **Rust (Edition 2021)** | Telemetry Drain | Low-level, zero-cost abstractions for lock-free ring-buffer ingestion. | Guarantees zero Garbage Collection (GC) pauses on telemetry capture; prevents telemetry bursts from delaying real user traffic. |
| **Python (3.11+)** | ML Analytics | Native execution of streaming data science and statistical anomaly detection libraries. | High-velocity model prototyping; libraries like `scikit-learn` and `numpy` optimize numerical calculations efficiently. |
| **Apache Arrow** | Cross-Language IPC | Zero-copy serialization across the Rust-to-Python memory boundary. | Eliminates the standard "JSON/Protobuf Serialization Tax" by aligning memory layouts identically in RAM for both processes. |
| **gRPC & Protobuf** | Inter-Node RPC | Strongly-typed, highly compressed internal cluster synchronization and loopback state updates. | Minimizes network payload sizes during cluster configuration shifts and state replication cycles. |
| **POSIX Shared Memory** | IPC Buffer | Multi-process data sharing through shared RAM segments (`/dev/shm`). | Bypasses the Linux network stack entirely for lightning-fast cross-process communication on the same hardware node. |

---

## IV. DevSecOps & Rigorous Testing Matrix

A project of this scale cannot rely on trivial unit assertions. The test suites must prove distributed correctness under high resource contention.

### 1. Deterministic Simulation Testing (DST)

The workspace must contain a specialized test harness within `tests/deterministic_test.go`.

* **Requirement:** Abstract the standard library network dialers (`net.Dial`) and timers (`time.Sleep`) into mocks driven by a single **Pseudo-Random Seed Number**.
* **Chaos Execution:** The simulation harness must inject pseudo-random clock drifts, localized disk write drops, out-of-order packet delivery, and random network splits.
* **Verification:** Passing an identical seed integer (e.g., `--seed=48291`) must reproduce the exact same interleaving of concurrent events and failures step-by-step.

### 2. Automated Integration and Load Testing

The `scripts/benchmark.sh` pipeline executes an end-to-end performance audit via `ghz` (for gRPC) or `wrk` (for HTTP):

* **Load Parameters:** Minimum 25,000 requests per second (RPS) sustained for 5 minutes with a concurrent worker count $\ge 200$.
* **Acceptance Criteria:** * $p99 \text{ latency} \le 1.8\text{ms}$
* Heap allocations on the validation path must equal exactly $0$.
* Zero consensus dropouts or split-brain states when 1 out of 3 cluster instances is killed mid-benchmark execution.



### 3. Security Architecture Rules

The `.github/workflows/security.yml` file must enforce strict gatekeeping parameters before any pull request merge:

* **Static Analysis (SAST):** `gosec` for Go syntax flaws; `cargo clippy -- -D warnings` for Rust anti-patterns; `bandit` for Python security defects.
* **Dependency Vulnerability Audits:** Mandatory execution of `govulncheck`, `cargo audit`, and `pip-audit`.
* **Runtime Containment:** Docker configurations must drop all root capabilities (`USER nonroot`). Host filesystem namespaces must remain strictly isolated, except for explicitly mapped `/dev/shm` nodes.

---

## V. Execution Checklist for Autonomous Agents

When tasked to build, update, or refactor this codebase, follow this prioritized, loop-free order of operations:

1. **Consensus Stubbing:** Build the core Go `consensus` package. Implement the Raft log replication and election state structures first. Ensure the code handles **Joint Consensus** transitions gracefully for cluster resizing.
2. **Telemetry Contract:** Establish the Apache Arrow schema layout between Rust and Python. Code the Rust shared-memory ring buffer next, ensuring no memory allocations inside the `read/write` loops.
3. **Proxy Assembly:** Tie the Go proxy path to the Rust memory drain via local UNIX domain sockets. Run `go tool pprof` immediately to verify that the validation path operates with zero heap memory allocations using `sync.Pool`.
4. **AI Modeling:** Implement the Python time-series Isolation Forest. Connect the loopback system via gRPC back to the Go Raft leader node.
5. **Chaos Validation:** Execute the `scripts/simulate_chaos.sh` suite. Intentionally kill the leader node under heavy load, ensuring the cluster achieves a stable state election in $<150\text{ms}$.

---

## VI. Production-Ready User Documentation

This section must be written out to the main project `README.md` file upon deployment.

# RaftLite: Distributed, Proactive Rate-Limiting & Policy Engine

RaftLite is an enterprise-grade, high-performance distributed rate limiter designed explicitly for resource-constrained edge systems, computing laboratories, and high-volume application networks. By combining low-level systems execution with asynchronous statistical models, RaftLite protects services from heavy botnets while maintaining predictable sub-millisecond tail latencies.

## Core Metrics Under Stress

* **Maximum Throughput:** 25,000+ Requests Per Second per cluster node.
* **Tail Latency Profile:** $p95 < 0.4\text{ms}$, $p99 < 1.8\text{ms}$ under peak concurrent load.
* **Memory Footprint:** $<45\text{MB}$ RAM base utilization per Go node instance.

## Installation & Local Cluster Deployment

### Prerequisites

* Docker and Docker Compose (V2) installed natively.
* Linux kernel environment with POSIX shared memory capability enabled (`/dev/shm`).

### Step 1: Clone and Initialize

```bash
git clone [https://github.com/yourusername/raftlite.git](https://github.com/yourusername/raftlite.git)
cd raftlite

```

### Step 2: Spin Up the 3-Node Cluster testbed

Execute the following command to initialize the Go nodes, the Rust telemetry pipelines, and the Python AI detection service:

```bash
docker-compose up --build -d

```

This initializes 3 isolated routing nodes listening on local ports `8081`, `8082`, and `8083` with CPU core pinnings applied automatically via Docker system constraints.

### Step 3: Run the Verification Benchmark

To execute an automated load test and verify cluster health under a simulated load wave:

```bash
bash ./scripts/benchmark.sh

```

### Step 4: Run a Failure Simulation (Chaos Test)

To verify the automated self-healing capabilities of the Raft cluster, execute the chaos script which kills the current leader node under load:

```bash
bash ./scripts/simulate_chaos.sh

```

Observe the docker logs to watch the remaining nodes hold a real-time election, vote a new leader, and sync the active global blocklists within milliseconds without dropping user sessions.

```

```
