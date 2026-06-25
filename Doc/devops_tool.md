
# RaftLite: Production DevOps & Infrastructure Specification

This document outlines the strictly necessary infrastructure tooling required to deploy, validate, and observe the RaftLite cluster. Every tool here serves a distinct, non-overlapping purpose to maintain tight control over the Go/Rust/Python execution planes.

## 1. Infrastructure as Code (IaC): The Explicit Foundation

You must not provision servers by clicking through a cloud console. Every network route, CPU allocation, and memory limit must be hardcoded.

### Tool: Terraform (or OpenTofu)

* **Why it helps us:** It forces you to treat your infrastructure as immutable, directory-based code. If a cloud server dies, you do not manually SSH in to fix it; you destroy it and let Terraform rebuild it from the exact blueprint.
* **How to use it here:** Write explicit `.tf` files to provision three separate cloud instances (e.g., AWS EC2 or DigitalOcean Droplets). Hardcode the firewall rules (Security Groups) to *only* allow internal cluster communication on your Raft gRPC ports and expose only the Go gateway port to the public internet.

## 2. Containerization & Orchestration: The Execution Plane

You already have Docker, but running raw `docker-compose` on a single machine is not a distributed system. You need to orchestrate across physical network boundaries while maintaining deep control over the system architecture.

### Tool: Kubernetes (K3s for Edge / Bare Metal)

* **Why it helps us:** Kubernetes handles the brutal reality of distributed systems: nodes die. If the hardware running Go Node 2 catches fire, Kubernetes instantly detects the failure and reschedules that container onto healthy hardware, allowing your Raft cluster to trigger its leader election seamlessly.
* **How to use it here:** Do not use heavy abstractions. Write raw, manual YAML manifests. Use Kubernetes `DaemonSets` to ensure your Rust telemetry pipeline runs exactly once per physical node. Use `StatefulSets` for your Go Raft nodes to guarantee they maintain their persistent write-ahead logs (WAL) across restarts.

## 3. The CI/CD Pipeline: The Security Gate

You must completely automate the testing of your Deterministic Simulation Testing (DST) harness. If code is merged without passing the chaos tests, the system is compromised.

### Tool: GitHub Actions

* **Why it helps us:** It provides a transparent, version-controlled execution environment that lives right next to your code. No external servers to maintain.
* **How to use it here:** Build a multi-stage YAML pipeline.
1. **Stage 1 (Lint/Build):** Run `go vet`, `cargo clippy`, and Python `flake8`. Compile the binaries.
2. **Stage 2 (Test):** Execute your Go DST harness.
3. **Stage 3 (Containerize):** Build lightweight, multi-stage Docker images (distroless) and push them to a container registry.



## 4. Observability & Telemetry: The Proof

You claim your Go gateway achieves $<1.8\text{ms}$ latency and your Python loopback takes $<10\text{ms}$. A Staff Engineer will not believe you unless you have the dashboard to prove it.

### Tool 1: Prometheus (Metrics Scraping)

* **Why it helps us:** Prometheus is a time-series database designed to pull metrics from highly concurrent systems without slowing them down.
* **How to use it here:** Expose a `/metrics` endpoint in your Go gateway. Have Prometheus scrape the exact memory heap size, garbage collection pauses, and request throughput every second.

### Tool 2: Grafana (Visualization)

* **Why it helps us:** It translates raw Prometheus data into the exact visual proof you need for your portfolio.
* **How to use it here:** Build a strict, manual dashboard. Panel 1: Go Gateway RPS. Panel 2: Rust Shared Memory Buffer Depth. Panel 3: Raft Leader Election frequency.

## 5. Continuous Profiling & Chaos: The Stress Test

You must prove the system survives what you designed it to survive.

### Tool: Chaos Mesh

* **Why it helps us:** It systematically destroys your infrastructure to validate your Raft consensus architecture.
* **How to use it here:** Deploy Chaos Mesh into your Kubernetes cluster. Configure it to randomly sever the network connection between Go Node 1 and Go Node 3 for 500 milliseconds every hour. Monitor Grafana to physically watch the Raft heartbeat fail, the election trigger, and the system self-heal without dropping external user traffic.

---

