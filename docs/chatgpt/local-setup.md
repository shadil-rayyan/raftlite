for ai reading this dont use this doucment 

# Complete setup: Ubuntu Linux + Docker + Envoy + RaftLite

We'll set this up step by step on your Ubuntu laptop. I'll assume you're starting from scratch.

Our goal is to get a working development environment where you can:
1. Run Envoy as a gateway.
2. Run a test web application behind Envoy.
3. Send HTTP requests through Envoy.
4. Start building RaftLite to manage policies.
5. Eventually test whether RaftLite can push a policy to Envoy and verify its status.

**Important:** We'll get Envoy working first, then connect RaftLite to it. Running both programs is not the same as integrating them.

## What you'll build



Your laptop will run these components:

```text
YOUR UBUNTU LAPTOP
│
├── Docker
│   │
│   ├── Envoy gateway
│   │     └── Accepts HTTP requests on port 10000
│   │
│   └── Test web application
│         └── Receives requests from Envoy
│
└── RaftLite
      └── We'll connect this to Envoy in a later step
```

You don't need a VPS, Kubernetes, or a second computer yet.

# Step 1 — Check your Ubuntu version

Open the Terminal application. You can usually press **Ctrl + Alt + T**.

Run:

```bash
lsb_release -a
```

Then check your computer's architecture and available memory:

```bash
uname -m
free -h
```

If `uname -m` returns `x86_64`, you're using a typical 64-bit Intel or AMD laptop.

# Step 2 — Install Docker Engine and Docker Compose

Docker will run Envoy and the test application in separate containers.

We'll use Docker's official Ubuntu package repository. These are the installation steps from the official [Docker Engine for Ubuntu guide](https://docs.docker.com/engine/install/ubuntu/). [Docker Documentation](https://docs.docker.com/engine/install/ubuntu/?utm_source=chatgpt.com)

### 2.1 Install the prerequisites

Copy and run:

```bash
sudo apt update
sudo apt install -y ca-certificates curl
```

Enter your Ubuntu password if prompted. The password won't necessarily appear as you type.

### 2.2 Add Docker's official signing key

Run:

```bash
sudo install -m 0755 -d /etc/apt/keyrings
```

Then:

```bash
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
  -o /etc/apt/keyrings/docker.asc
```

Set its permissions:

```bash
sudo chmod a+r /etc/apt/keyrings/docker.asc
```

### 2.3 Add Docker's package repository

Copy the entire command below, including the `EOF` line:

```bash
sudo tee /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
```

Update the package list:

```bash
sudo apt update
```

### 2.4 Install Docker and Docker Compose

Run:

```bash
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Wait for installation to finish.

Start Docker and configure it to start automatically:

```bash
sudo systemctl enable --now docker
```

### 2.5 Verify installation

Run these commands individually:

```bash
sudo docker --version
```

```bash
sudo docker compose version
```

```bash
sudo docker run hello-world
```

**Success condition:** The last command prints a message containing `Hello from Docker!`.

If it works, your Docker environment is ready.

For now, use `sudo docker` and `sudo docker compose` for Docker commands. Docker access can grant root-equivalent privileges, so don't add your user to the Docker group casually.

---

# Step 3 — Create your RaftLite lab directory

Now we'll create a clean workspace.

Run:

```bash
mkdir -p ~/raftlite-lab
cd ~/raftlite-lab
```

Check your current directory:

```bash
pwd
```

It should display something similar to:

```text
/home/your-username/raftlite-lab
```

Everything for this first lab will live inside this directory.

# Step 4 — Create the Envoy configuration

We'll start with a static configuration. That means Envoy reads its configuration from a file when it starts.

Later, we'll connect RaftLite to a dynamic control plane so it can update policies without manually editing this file.

Create `envoy.yaml`:

```bash
nano envoy.yaml
```

Paste the following configuration into the editor:

```yaml
admin:
  address:
    socket_address:
      address: 0.0.0.0
      port_value: 9901

static_resources:
  listeners:
    - name: main_listener
      address:
        socket_address:
          address: 0.0.0.0
          port_value: 10000

      filter_chains:
        - filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                stat_prefix: ingress_http

                route_config:
                  name: local_route
                  virtual_hosts:
                    - name: backend_service
                      domains:
                        - "*"
                      routes:
                        - match:
                            prefix: "/"
                          route:
                            cluster: backend_cluster

                http_filters:
                  - name: envoy.filters.http.router
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router

  clusters:
    - name: backend_cluster
      type: STRICT_DNS
      connect_timeout: 5s
      lb_policy: ROUND_ROBIN

      load_assignment:
        cluster_name: backend_cluster
        endpoints:
          - lb_endpoints:
              - endpoint:
                  address:
                    socket_address:
                      address: backend
                      port_value: 80
```

Save the file:

1. Press **Ctrl + O**.
2. Press **Enter**.
3. Press **Ctrl + X**.

This configuration tells Envoy to:
- Listen for HTTP traffic on port `10000`.
- Forward requests to a backend application named `backend`.
- Expose its administrative interface on port `9901` inside its container.

We'll bind the admin interface to your laptop's localhost only in the next step.

# Step 5 — Create the Docker Compose file

Create the file:

```bash
nano compose.yaml
```

Paste:

```yaml
services:
  envoy:
    image: envoyproxy/envoy:v1.37.7
    command:
      - -c
      - /etc/envoy/envoy.yaml
      - --log-level
      - info
    ports:
      - "127.0.0.1:10000:10000"
      - "127.0.0.1:9901:9901"
    volumes:
      - ./envoy.yaml:/etc/envoy/envoy.yaml:ro
    depends_on:
      - backend

  backend:
    image: traefik/whoami:v1.11.0
    expose:
      - "80"
```

This uses a versioned Envoy image and the Envoy v3 configuration API. The official [Envoy getting-started guide](https://www.envoyproxy.io/docs/envoy/latest/start/start.html) describes static and dynamic configuration options. [envoyproxy.io](https://www.envoyproxy.io/docs/envoy/latest/start/start.html?highlight=trace\&utm_source=chatgpt.com)

Save the file using **Ctrl + O**, Enter, and **Ctrl + X**.

### What have you configured?

| Component | Purpose |
|---|---|
| Envoy | Receives and forwards HTTP requests |
| Backend | A small test application |
| Port 10000 | The HTTP entry point for testing |
| Port 9901 | Envoy's local administrative interface |
| Docker Compose | Starts both containers together |

Notice that both host ports are bound to `127.0.0.1`. This keeps this lab's published ports accessible only through your own laptop's loopback interface.

# Step 6 — Validate and start Envoy

Make sure you're still inside the project directory:

```bash
cd ~/raftlite-lab
```

Check that both files exist:

```bash
ls -l
```

You should see:

```text
compose.yaml
envoy.yaml
```

### 6.1 Validate the Compose file

Run:

```bash
sudo docker compose config
```

This checks the Compose configuration and prints the normalized result.

If it reports a YAML error, fix that before proceeding.

### 6.2 Download the images and start the containers

Run:

```bash
sudo docker compose up -d
```

The first run downloads the images, so it may take a little while.

### 6.3 Check the containers

Run:

```bash
sudo docker compose ps
```

You should see the `envoy` and `backend` services running.

If Envoy exits or restarts repeatedly, check the logs:

```bash
sudo docker compose logs --tail=100 envoy
```

If necessary, check the backend too:

```bash
sudo docker compose logs --tail=100 backend
```

# Step 7 — Test whether Envoy works

This is the first important milestone.

### 7.1 Send a request through Envoy

Run:

```bash
curl -i http://127.0.0.1:10000/
```

You should receive an HTTP response from the backend, usually with details such as its hostname and the request information.

That means your request travelled through Envoy to the backend.

### 7.2 Check Envoy's readiness

Run:

```bash
curl -i http://127.0.0.1:9901/ready
```

A ready Envoy instance should return a successful response.

### 7.3 Inspect Envoy's active configuration

Run:

```bash
curl -s http://127.0.0.1:9901/config_dump
```

You should see a JSON document containing configuration information.

If all three tests work, **you have successfully set up Envoy on your Ubuntu laptop using Docker Compose**.

---

# Step 8 — Make your first policy test

Before connecting RaftLite, let's understand what policy enforcement looks like.

At the moment, Envoy forwards all HTTP paths to the backend. We can change the configuration to reject a particular path as a simple test of Envoy's local configuration.

For example, let's make `/blocked` return HTTP 403.

Open the configuration:

```bash
nano ~/raftlite-lab/envoy.yaml
```

Find this section:

```yaml
routes:
  - match:
      prefix: "/"
    route:
      cluster: backend_cluster
```

Replace it with:

```yaml
routes:
  - match:
      path: "/blocked"
    direct_response:
      status: 403

  - match:
      prefix: "/"
    route:
      cluster: backend_cluster
```

Save the file.

Restart Envoy so it loads the changed static configuration:

```bash
cd ~/raftlite-lab
sudo docker compose restart envoy
```

Check the logs:

```bash
sudo docker compose logs --tail=50 envoy
```

Test the blocked path:

```bash
curl -i http://127.0.0.1:10000/blocked
```

You should receive:

```text
HTTP/1.1 403 Forbidden
```

Now test another path:

```bash
curl -i http://127.0.0.1:10000/hello
```

That request should reach the backend.

**What this proves:** Envoy can enforce a rule that denies a request.

**What it does not prove:** RaftLite can create, deliver, track, or roll back a policy. We changed the Envoy file manually. The next stage is to make RaftLite manage that process.

---

# Step 9 — Connect RaftLite to Envoy

This is where your actual product begins.

For standalone Envoy, I recommend investigating the **xDS control-plane integration**.

Your target architecture is:

```text
                 You
                  |
                  v
         +-----------------+
         |    RaftLite     |
         |                 |
         | Policy API      |
         | Policy storage  |
         | Revision state  |
         | Rollout tracking|
         +-----------------+
                  |
                  | xDS control plane
                  |
                  v
         +-----------------+
         |      Envoy      |
         |                 |
         | Receives updates|
         | ACKs or NACKs   |
         +-----------------+
                  |
                  v
             Test backend
```

For this to work, you will need to implement or integrate an xDS server, configure Envoy to connect to it, and establish how RaftLite's policy model translates into Envoy configuration.

A good first workflow is:

1. Create a policy in RaftLite.
2. Assign it a revision number.
3. Translate the policy into the appropriate Envoy configuration.
4. Deliver the update through xDS.
5. Record the configuration response.
6. Verify the resulting enforcement.
7. Show the gateway's state in RaftLite.

Don't mark a gateway as applied merely because RaftLite saved the policy. Also distinguish configuration ACKs from actual enforcement verification.

## Step 10 — Checkpoint before we write RaftLite code

First, get the following commands working on your laptop:

```bash
cd ~/raftlite-lab

sudo docker compose ps

curl -i http://127.0.0.1:10000/

curl -i http://127.0.0.1:10000/blocked

curl -i http://127.0.0.1:9901/ready
```

You should see:
- Both containers running.
- A successful response from `/`.
- HTTP 403 for `/blocked`.
- A successful readiness response from Envoy.

Once these tests pass, you're ready to begin the RaftLite integration.

### If something fails

Run:

```bash
sudo docker compose ps
sudo docker compose logs --tail=100 envoy
sudo docker compose logs --tail=100 backend
```

Keep the error output. It will help identify whether the problem is with Docker, YAML syntax, Envoy startup, or backend connectivity.

**For now, focus on getting this exact lab running.** Don't install Kubernetes, deploy a VPS, or write the xDS control plane until the local Envoy test works.