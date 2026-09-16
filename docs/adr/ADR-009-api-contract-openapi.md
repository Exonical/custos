# ADR-009: API contract — hand-maintained OpenAPI 3.1, oapi-codegen, stdlib ServeMux

Status: Accepted
Date: 2026-09-14

## Context

The REST API is Custos's public contract, consumed by the Next.js UI, the
CLI, and automation. The contract must be reviewable as a document, must
not leak internal types, and must keep Go and TypeScript clients in lock
step (docs/api.md). Tenancy must be structural in URLs, and error
semantics (existence-hiding 404s, path-addressed 422 details, idempotency
409s) are part of the security model.

## Decision

- `internal/api/openapi/openapi.yaml` (OpenAPI 3.1) is the **authoritative
  contract**, hand-maintained, served at `GET /api/v1/openapi.json`.
- `oapi-codegen` generates `pkg/api/v1` types + a **strict server
  interface** only (no runtime framework); TypeScript types for UI/CLI are
  generated from the same document; a CI `openapi-check` fails if
  generated code is stale.
- Routing uses stdlib `net/http` `ServeMux` with Go 1.22+ pattern routing;
  no third-party router. Middleware chain is fixed: recover → request-id →
  real-ip → otel → access-log → security-headers → CORS → body-limit →
  timeout → authn → tenant-context → handler.
- **Tenant-in-path URL design**: `/api/v1/tenants/{tenant}/...`; scope is
  structural, never a query parameter or request-body field. Slugs or
  UUIDs accepted; Slurm job IDs are never Custos identifiers.
- Error envelope `{error:{code,message,request_id,details[]}}` with a
  defined status mapping and an error-code enum in the OpenAPI document;
  upstream error bodies never echoed. List envelope with opaque
  HMAC-signed keyset cursor; `total` only when cheap.
- Conventions: `snake_case` JSON, RFC 3339 UTC, JSON Merge Patch +
  `If-Match`/`etag` optimistic concurrency,
  (`/cancel`, `/publish`, `/validate` verb sub-paths), `Idempotency-Key` on creation
  endpoints, explicit filter params only, per-principal rate limits,
  Deprecation/Sunset headers, `/api/v1` stable after Milestone 8.

## Alternatives considered

- **Code-first generation (swag)** — rejected: the document, not the
  code, should be the reviewable contract; code-first leaks internal
  types.
- **chi / gin / echo routers** — rejected: Go 1.22+ `ServeMux` covers the
  pattern routing needed; fewer dependencies, no framework lock-in.
- **Tenant as header or query param** — rejected: structural scope in the
  path makes cross-tenant access unrepresentable and enumeration
  resistant (foreign tenant ⇒ identical 404).

## Consequences

- Hand-maintaining the YAML is ongoing work, but drift is caught by CI.
- Strict-server codegen constrains handler signatures to the contract —
  good, but middleware must do anything the codegen can't express.
- Every new endpoint is a contract change requiring doc review.

Source: docs/api.md (all sections); docs/architecture.md §8–9.
