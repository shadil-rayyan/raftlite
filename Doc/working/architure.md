This is how the entire system design fits together.

Imagine a user trying to access a **Web Application** (like a frontend website backed by an API server). Instead of hitting the application directly, their traffic passes through our **Go-based RaftLite Guard**.

Here is the complete, production-ready architectural blueprint showing how the data flows from a user's click all the way through our multi-language self-healing loop:

---

## 1. Overall System Architecture Diagram

```text
                                  +------------------------------------+
                                  |        MALICIOUS BOT / USER        |
                                  +------------------------------------+
                                                     │
                                                     ▼ (HTTP Request)
==================================================================================================
THE PERFORMANCE PLANE (Low Latency Gateway)
==================================================================================================
                     ┌───────────────────────────────┼───────────────────────────────┐
                     │                               │                               │
                     ▼                               ▼                               ▼
         ┌───────────────────────┐       ┌───────────────────────┐       ┌───────────────────────┐
         │     GO RAFT NODE 1    │       │     GO RAFT NODE 2    │       │     GO RAFT NODE 3    │
         │     (Active Leader)   │◄====─►│      (Follower)       │◄====─►│      (Follower)       │
         └───────────┬───────────┘  RPC  └───────────────────────┘  RPC  └───────────────────────┘
                     │
                     ├─► [Memory Blocklist Check] ──► (If Banned: Return HTTP 429 Too Many Requests)
                     │
                     └─► [Clean Request Allowed] ───► +------------------------------------------+
                                                     |    YOUR ACTUAL BACKEND WEB APPLICATION   |
                                                     +------------------------------------------+
==================================================================================================
THE OBSERVABILITY & TELEMETRY PLANE (Background Processing)
==================================================================================================
                     │
                     │ (Asynchronous Data Stream via Linux Unix Sockets / IPC)
                     ▼
         ┌───────────────────────┐
         │   RUST TELEMETRY      │
         │   DRAIN PIPELINE      │  <-- Acts as an ultra-fast background camera
         └───────────┬───────────┘
                     │
                     │ (Vectorized Data Batches via Shared Memory / Apache Arrow Dataframe)
                     ▼
         ┌───────────────────────┐
         │  PYTHON AI ENGINE     │  <-- Evaluates multi-node traffic signatures via ML
         │  (Isolation Forest)   │
         └───────────┬───────────┘
                     │
                     │ (Proactive Bot Pattern Detected!)
                     ▼
                     └──────────────────[ AUTOMATED LOOPBACK RULE ]──────────────────┐
                                                                                     │
                                                                                     ▼
                                                                     +-------------------------------+
                                                                     |  Injects: PUT block_ip: true  |
                                                                     +-------------------------------+

```

---

## 2. Step-by-Step Data Flow (Walking Through the Blueprint)

To understand exactly how this works in real life, follow the life cycle of a request through the system design:

### Step 1: The Request Hits the Guard

A user (or a malicious bot) sends an HTTP request to your web application. Before it ever touches your backend code or database, it is intercepted by one of the **Go Raft Nodes** acting as a high-performance proxy gateway.

### Step 2: The Fractional-Millisecond Memory Check

The Go node instantly checks its internal, in-memory blocklist.

* **If the IP is already blocked:** The Go node instantly cuts the connection and returns an `HTTP 429 Too Many Requests` error. The backend app stays completely untouched and safe.
* **If the IP is clean:** The Go node immediately forwards the request straight to your **Backend Web Application**, keeping website loading speeds fast.

### Step 3: The Rust Camera Snaps the Logs (Asynchronous)

While the Go node is busy serving the web visitor, it simultaneously drops a tiny snippet of metadata (the user's IP, exact timestamp, and latency profile) into a lock-free, local background ring buffer.
The **Rust Pipeline Daemon** instantly scoops up these logs via a fast Unix Socket. Because this is asynchronous, the web visitor experiences **zero lag**.

### Step 4: Python Solves the Pattern

The Rust daemon batches thousands of these data metrics together using pre-allocated memory blocks and flashes them over to the **Python AI Engine**.
The Python detective looks at the behavioral trends across *all* the nodes at once. It notices if a bot is trying to play hide-and-seek by spreading its requests evenly across Node 1, Node 2, and Node 3 to bypass traditional limits.

### Step 5: The Automated Self-Healing Feedback Loop

The moment the Python engine flags a machine-like rhythmic pattern, it triggers the loopback. It sends an automatic command to the **Go Raft Leader**: *"I've detected a distributed bot footprint. Add `block_list:192.168.1.50` to the ledger."* Because it's a **Raft Cluster**, the leader safely replicates this new security rule to all other follower nodes across the cluster. Within milliseconds, the entire multi-node guard wall is updated, and the bot is blocked globally from entering any door.
