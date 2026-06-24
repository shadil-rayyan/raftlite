Here is the breakdown of the **RaftLite** project, written in everyday engineering language. It outlines what we are building, who it's for, and why it is a unique solution.

---

## 1. The Core Problem

Imagine you run a hot online store selling limited-edition sneakers. When a new sneaker drops, millions of people hit your website at the exact same millisecond.

To prevent your servers from crashing, you need a **Security Guard** (a Rate Limiter) to block bots or users refreshing the page 500 times a second.

But because your website is huge, you have **three front doors** (three servers).

* **The Coordination Issue:** If a clever bot hits Door 1 twice, Door 2 twice, and Door 3 twice, no individual guard thinks anything is wrong. They need to sync up instantly.
* **The Failover Issue:** If the guard at Door 1 suddenly crashes, the other guards need to immediately elect a new leader and ensure no security data is lost.

---

## 2. The Competitors & How They Work

The big names in this space are **Kong Gateway**, **Tyk**, and **HashiCorp Consul**.

They usually build their systems in one of two ways:

1. **The Centralized Brain Approach:** All doors talk to a single, central database (like Redis) to ask, *"Is this user allowed in?"*
2. **The Heavy Sidecar Approach:** They run a heavy secondary program right next to every single application to manage rules and sync up.

---

## 3. Their Limitations

* **They are Dumb & Reactive:** Competitors rely on hard-coded rules, like: *"Block if requests > 100 per minute."* Sophisticated bots bypass this by making exactly 99 requests per minute across multiple doors.
* **They Introduce Latency:** Checking a central database for *every single click* slows down your website for real human users.
* **They Eat Up Memory (Resource Bloat):** These systems are massive. If you try to run them in a local computer lab, an edge environment, or on cheap cloud servers, they eat up all your RAM just keeping themselves alive.

---

## 4. Who Are Your Users?

1. **Platform & DevOps Engineers:** Teams managing high-traffic applications who need to protect their infrastructure without paying a fortune to giant cloud companies like Cloudflare or AWS.
2. **Edge Computing Developers:** Engineers running code on small, local servers (like smart factory floors, IoT setups, or small computing labs) where memory and processing power are highly limited.

---

## 5. Why Your Solution Is Unique

**RaftLite** replaces clumsy, static rules with a tight, automated self-healing loop. It keeps the data safe locally, watches traffic patterns invisibly, and updates itself *before* a failure occurs.

| Feature | Existing Tools (Kong/Consul) | Your **RaftLite** System |
| --- | --- | --- |
| **Intelligence** | **Reactive:** Only blocks if an attacker crosses a hard-coded limit. | **Proactive:** AI predicts and blocks a bot *before* it breaks your site based on pattern deviations. |
| **Speed** | **Slow:** Pauses the user's request to check an external database. | **Blazing Fast:** The main guard approves requests instantly; metrics are collected in the background. |
| **Footprint** | **Heavy:** Requires large database clusters and heavy frameworks. | **Lightweight:** A single, clean system built explicitly for low-resource environments. |

---

## 6. The Technology Package & Why We Use It

We are using three specific languages because each does one job perfectly:

* **Go (The Muscle):** We use Go to build the Core Guards. Go is excellent for networking and concurrency. It uses the **Raft Consensus Algorithm** to ensure that if Guard 1 blocks a bot, Guards 2 and 3 instantly agree—even if one of the servers physically loses power.
* **Rust (The Camera):** We use Rust to build the logging pipeline. Rust is incredibly fast and memory-safe. It acts as an invisible security camera, scooping up millions of data points about traffic speeds without slowing down the Go guards.
* **Python (The Detective):** We use Python to run the AI. Python has the best data science libraries. It acts as the detective in the back room, scanning the streaming data from the Rust camera to flag weird rhythmic patterns and automatically telling the Go guards who to block.

---

## 7. How Users Use Your Solution & How You Distribute It

### How They Use It

A DevOps engineer will add **RaftLite** as a lightweight piece of middleware directly in front of their application code.

1. When a web request comes in, RaftLite instantly checks its internal memory blocklist.
2. If the user is clean, they go through immediately.
3. Behind the scenes, the system handles the logging, AI analysis, and blocklist updates automatically.

### How You Distribute It

To make it completely painless for a developer to try, you will distribute it via **Docker Compose** and **GitHub**:

* You will provide a single configuration file (`docker-compose.yml`).
* A user can type a single command: `docker-compose up`.
* This instantly spins up a local 3-node cluster of your Go guards, links them to the Rust pipeline, attaches the Python detective, and lets the developer test failure simulations right on their laptop.
