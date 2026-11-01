For RaftLite, I would design the overall components around one central idea:

**An operator creates a policy once, RaftLite distributes it to multiple Envoy gateways, and RaftLite shows which gateways have successfully applied it.**

Since we're leaning toward standalone Envoy on VPSs or VMs, let's design the architecture around **RaftLite + an xDS control plane + multiple Envoy gateways**.

# 1. Overall RaftLite architecture



Here is what the system looks like conceptually:

```text
                    OPERATOR
                       |
                       v
             +--------------------+
             |  RaftLite UI / API |
             +--------------------+
                       |
                       v
             +--------------------+
             |  Policy Manager    |
             |                    |
             | Create / Update    |
             | Validate policies  |
             | Revision tracking  |
             +--------------------+
                       |
                       v
             +--------------------+
             |   Policy Storage   |
             |                    |
             | Policies           |
             | Revisions          |
             | Audit history      |
             +--------------------+
                       |
                       v
             +--------------------+
             |  xDS Control Plane |
             |                    |
             | Translate policies |
             | Publish resources  |
             | Process ACK / NACK |
             +--------------------+
                       |
                gRPC / xDS
             ________|________
            |        |        |
            v        v        v
       +---------+ +---------+ +---------+
       | Envoy 1 | | Envoy 2 | | Envoy 3 |
       | Gateway | | Gateway | | Gateway |
       +---------+ +---------+ +---------+
            |        |        |
            v        v        v
       Backend 1 Backend 2 Backend 3

             +--------------------+
             | Rollout Tracker    |
             |                    |
             | Applied / Stale    |
             | Rejected / Offline |
             +--------------------+
```

This is a logical architecture. Some components can live inside the same application initially; they don't all need separate processes or containers.

---

# 2. The main components

I'd break the system into eight logical components.

## Component 1: RaftLite API

**Purpose:** The entry point for operators and administrative tools.

Example operations:

```http
POST   /api/policies
GET    /api/policies
GET    /api/policies/{id}
PUT    /api/policies/{id}
DELETE /api/policies/{id}

GET    /api/gateways
GET    /api/gateways/{id}/status
GET    /api/rollouts/{id}
```

An operator might create a policy like this:

```json
{
  "name": "block-unwanted-ip",
  "type": "ip_deny",
  "targets": ["production"],
  "rules": [
    {
      "cidr": "203.0.113.10/32",
      "action": "deny"
    }
  ]
}
```

This is an illustrative API design, not an existing RaftLite API specification.

The API should authenticate the operator, validate the request, and pass it to the policy manager.

## Component 2: Policy Manager

**Purpose:** Understand what the operator wants the gateways to enforce.

This component handles:
- Policy creation and modification.
- Input validation.
- Policy revision numbers.
- Target gateway selection.
- Policy expiry.
- Rollback requests.

For example:

```text
Policy: block-unwanted-ip

Revision 1:
  Deny 203.0.113.10/32

Revision 2:
  Deny 203.0.113.10/32
  Deny 203.0.113.20/32
```

When the operator changes the policy, RaftLite creates a new revision rather than silently overwriting the previous one.

That lets RaftLite compare the desired revision with each gateway's last acknowledged revision.

**Important:** A policy being saved successfully does not mean any gateway has applied it.

## Component 3: Policy Storage

**Purpose:** Persist the policy and its history.

For the first release, I would use SQLite if you are building a single-instance application. PostgreSQL is another reasonable choice, particularly if you anticipate concurrent access or a larger deployment.

Store information such as:

| Record | Example information |
|---|---|
| Policies | ID, name, type, current revision |
| Policy revisions | Revision number, contents, creation time |
| Gateway registry | Gateway ID, environment, last-seen time |
| Rollouts | Target revision, rollout state |
| Gateway acknowledgements | Resource version, response, timestamp |
| Audit history | Actor, action, affected policy |

You don't need a distributed database for your first prototype.

Also, distinguish durable policy state from transient connection information. A gateway reconnecting should not erase the policy history.

## Component 4: Gateway Registry

**Purpose:** Know which Envoy instances RaftLite manages.

For example:

```text
Gateway 1
  ID: gateway-01
  Environment: production
  Address: 10.0.1.10
  Last seen: recently

Gateway 2
  ID: gateway-02
  Environment: production
  Address: 10.0.1.11
  Last seen: recently

Gateway 3
  ID: gateway-03
  Environment: production
  Address: 10.0.1.12
  Last seen: 5 minutes ago
```

These are example records.

In a real deployment, the control plane should identify gateways using authenticated connections and stable identities. Don't rely on an IP address alone as a gateway's identity.

This component helps answer questions such as:
- Which gateways belong to production?
- Which gateways should receive a policy?
- Which gateways are connected?
- Which gateways have stopped reporting?

## Component 5: xDS Control Plane

**This is the bridge between RaftLite and Envoy.**

Envoy connects to the control plane using gRPC and the relevant xDS APIs.

The control plane:
1. Receives or reads desired configuration from RaftLite.
2. Translates supported policies into Envoy resources.
3. Publishes updates to connected Envoy instances.
4. Processes their ACK or NACK responses.
5. Exposes the information needed for rollout tracking.

For your first implementation, I would use Go and the existing Envoy go-control-plane project.

Official repository: [Envoy go-control-plane on GitHub](https://github.com/envoyproxy/go-control-plane?utm_source=chatgpt.com)

### What configuration does it distribute?

For the initial IP denylist feature, the control plane might use Envoy's HTTP RBAC filter with an appropriate deny policy.

For example, conceptually:

```text
If request source IP is 203.0.113.10
    deny request
Otherwise
    continue normal request processing
```

The exact implementation depends on the chosen Envoy filter and resource types.

A critical design detail: source-IP policies must account for whether Envoy sees the real client IP or a trusted proxy's IP. Incorrectly trusting forwarded headers can create a security vulnerability.

## Component 6: Envoy Gateways

These are the data-plane instances that receive real traffic and enforce the policies.

Initially, you might run two Envoy instances:

```text
VPS 1
  └── Envoy gateway-01

VPS 2
  └── Envoy gateway-02
```

Both connect to the RaftLite control plane.

When an operator updates a policy, the control plane publishes the corresponding configuration to both gateways.

You can then test situations where:
- Both gateways accept the update.
- One gateway rejects it.
- One gateway disconnects.
- A gateway reconnects after missing an update.

That's where RaftLite starts to provide value beyond a basic configuration API.

## Component 7: Rollout Tracker

**This is one of the most important parts of your product.**

The rollout tracker compares the desired policy state with the state reported by each gateway.

For example:

| Gateway | Desired revision | Acknowledged revision | Status |
|---|---:|---:|---|
| gateway-01 | 5 | 5 | Applied |
| gateway-02 | 5 | 4 | Stale |
| gateway-03 | 5 | — | Offline |

These are illustrative states.

The status definitions need to be precise:

- **Applied:** The relevant configuration was accepted, and the system's defined verification criteria are satisfied.
- **Stale:** The gateway is known to be behind the desired revision.
- **Rejected:** The gateway rejected the relevant configuration update.
- **Offline:** The gateway is not currently reachable or connected, according to your health-detection rules.
- **Unknown:** There is insufficient evidence to determine the current state.

Do not treat an ACK as proof that every request is being enforced correctly. If you want to report verified enforcement, you'll need additional evidence, such as targeted tests or suitable runtime checks.

You should also track the status of the relevant Envoy resources, not merely assume that a single response covers every configuration resource involved in a policy.

## Component 8: Audit, Health, and Recovery

This component covers the operational details needed to make RaftLite dependable.

It should record:
- Who changed a policy.
- Which revision was created.
- Which gateways were targeted.
- Which configuration updates were acknowledged or rejected.
- When gateways disconnected and reconnected.
- Whether a rollback was requested and what happened.

It should also support:
- Persistent storage and backups.
- Health endpoints.
- Secure control-plane connections.
- Authentication and authorization.
- Safe handling of invalid policies.
- Recovery after a RaftLite restart.

These are not optional details if a customer will rely on RaftLite to manage security policies.

---

# 3. How the components work together

Let's follow a single policy update.

Suppose an operator wants to block an IP address across three gateways.

### Step 1: The operator creates the policy

The operator sends a request to the RaftLite API.

### Step 2: The policy manager validates it

RaftLite validates the CIDR, action, target gateways, and any applicable expiry settings.

### Step 3: RaftLite creates a revision

The policy is persisted as revision 6.

The desired state is now revision 6, but the gateways have not necessarily applied it.

### Step 4: The control plane publishes configuration

The xDS control plane translates the policy into the relevant Envoy resources and publishes the update.

### Step 5: Envoy processes the configuration

Each gateway either accepts or rejects the relevant update.

### Step 6: RaftLite tracks the result

The rollout tracker records the available response and connection information for each gateway.

### Step 7: The operator sees the outcome

For example:

```text
Policy: block-unwanted-ip
Desired revision: 6

gateway-01: Applied
gateway-02: Applied
gateway-03: Rejected
```

The operator can then inspect the failure and decide whether to fix the policy, retry the rollout, or roll back.

**The key product promise is not simply that RaftLite distributes policies. It's that RaftLite gives the operator an accurate, trustworthy picture of rollout progress.**

---

# 4. How should you deploy these components initially?

Don't turn every logical component into a separate microservice. That adds unnecessary complexity while you're still proving the product.

I'd start with this:

```text
Ubuntu laptop
│
└── Docker Compose
    │
    ├── raftlite
    │   ├── HTTP API
    │   ├── Policy manager
    │   ├── Gateway registry
    │   ├── Rollout tracker
    │   └── xDS server
    │
    ├── envoy-01
    │
    ├── envoy-02
    │
    └── test-backend
```

Use a persistent volume for RaftLite's database.

For development, the API, policy manager, rollout tracker, and xDS server can all live in one Go application. You can separate them later if you discover a real operational reason to do so.

One nuance: running the xDS server in the same application is a good starting point, but you should keep its interfaces and internal modules separate enough that you can scale or isolate it later.

---

# 5. What should you actually implement first?

I would build the first version in this order:

| Order | Component | Initial goal |
|---:|---|---|
| 1 | Docker Compose environment | Start RaftLite and Envoy locally |
| 2 | Minimal xDS server | Make one Envoy connect and receive dynamic configuration |
| 3 | Policy manager | Create and persist one IP-blocking policy |
| 4 | Policy translation | Convert the policy into the required Envoy resources |
| 5 | Rollout tracker | Record responses and compare desired and acknowledged revisions |
| 6 | Gateway registry | Track two Envoy instances independently |
| 7 | Failure handling | Test rejection, disconnection, reconnection, and recovery |
| 8 | API and simple UI | Allow an operator to manage policies and inspect status |
| 9 | Security and deployment hardening | Prepare for a controlled customer pilot |

You can build the API and basic UI earlier if that helps your development process, but I would not postpone proving that dynamic configuration works.

## My final recommendation

For your current Ubuntu laptop, start with **one RaftLite application, one xDS control plane, and two Envoy gateways**.

Your first technical milestone should be:

> Create a policy in RaftLite, distribute it to Envoy through xDS, observe the configuration response, and verify that the policy actually changes request behaviour.

Once that works reliably, you can build the remaining management and rollout features around it.