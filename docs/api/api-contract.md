RaftLite API Contract
1. Purpose

This document defines the proposed HTTP API for managing security policies and inspecting cluster status.

The API is a contract for implementation. Endpoints must not be advertised as supported until they are implemented and tested.

2. General Conventions
Base path: /v1.
Content type: application/json for JSON request and response bodies.
Policy IDs are stable identifiers.
Administrative endpoints require authentication and authorization.
Errors use a consistent JSON structure.
Write responses distinguish acceptance from commitment.
Retryable operations support idempotency where appropriate.
3. Error Format

Proposed error structure:

{
  "error": {
    "code": "QUORUM_UNAVAILABLE",
    "message": "The operation could not be committed.",
    "request_id": "req_example"
  }
}


The actual error codes and HTTP status mappings must be standardized by the implementation.

Suggested categories include:

INVALID_ARGUMENT
UNAUTHENTICATED
PERMISSION_DENIED
NOT_FOUND
CONFLICT
QUORUM_UNAVAILABLE
UNAVAILABLE
INTERNAL_ERROR

Do not return internal credentials, filesystem details, or sensitive stack traces in public error responses.

4. Health Endpoints
GET /healthz

Purpose: process liveness.

The response must follow the documented liveness contract and must not imply that the cluster has quorum merely because the process is running.

GET /readyz

Purpose: readiness for the relevant operation.

The response should identify readiness status without exposing sensitive configuration.

5. Cluster Status
GET /v1/leader

Returns information about the currently known leader.

The response must distinguish a known leader from a state in which no leader is currently known.

GET /v1/cluster

Returns cluster status, including configured member identities and available role or health information.

Only expose the information appropriate for the caller's authorization level.

6. Create a Policy
POST /v1/policies

Proposed request:

{
  "type": "ip_block",
  "target": "203.0.113.50",
  "reason": "credential abuse",
  "duration": "30m"
}


The server validates the request and submits a policy command through the supported consensus path.

The response must distinguish a committed operation from a rejected or unresolved request.

Proposed committed response:

{
  "policy_id": "pol_example",
  "status": "committed",
  "request_id": "req_example"
}


The example ID is illustrative. The implementation must define ID generation and status semantics.

A successful HTTP response must never claim commitment merely because the request was accepted by an API handler.

7. Retrieve and List Policies
GET /v1/policies

Returns authorized policy records.

The final contract must define pagination, ordering, filtering, and consistency semantics.

GET /v1/policies/{id}

Returns the specified policy or a documented not-found response.

The response should include its target, type, lifecycle metadata, and authoritative revision where available.

8. Revoke a Policy
DELETE /v1/policies/{id}

Requests policy revocation.

The operation must be represented through the authoritative policy-control path rather than by directly mutating local gateway state.

The contract must define whether the response means the revocation was accepted, committed, or installed on gateways.

9. Policy Decision Lookup
GET /v1/decisions/{ip}

Returns a policy decision for a validated IP address.

The contract must specify whether the response reflects authoritative control-plane state or a particular local projection. It must not imply that all gateways share the same installed revision.

10. Idempotency

Where supported, policy-creation requests should accept an idempotency key.

The server must document:

Key scope.
Key retention.
Behavior when the same key is reused with the same payload.
Behavior when the same key is reused with a different payload.
How clients determine the result of an operation whose original response was lost.
11. Authentication and Authorization

The API must define authentication requirements and permissions for each endpoint.

At minimum, distinguish read-only status access from policy mutation.

The final contract must specify how credentials are supplied, validated, rotated, and revoked.

12. Compatibility

Changes to request fields, response fields, error semantics, or commitment guarantees must be reviewed for compatibility.

Breaking changes should use an explicitly versioned API path or another documented versioning mechanism.

13. Required API Tests
Valid policy creation.
Invalid IP and duration.
Unauthenticated request.
Unauthorized policy mutation.
Missing policy retrieval.
Policy revocation.
No-quorum write.
Duplicate idempotency key.
Lost-response retry.
Correct distinction between committed and merely accepted operations.
14. Related Documents
Software Requirements Specification
Architecture overview
Gateway integration