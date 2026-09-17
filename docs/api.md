# REST API Conventions

## Contract

The OpenAPI 3.1 document `internal/api/openapi/openapi.yaml` is the
authoritative contract, hand-maintained, served at
`GET /api/v1/openapi.json`. Go request/response types in `pkg/api/v1` are
generated from it with `oapi-codegen` (types + strict server interface
only, chi/stdlib router; no runtime framework). TypeScript types for the UI
and CLI client are generated from the same document. A CI check fails if
the generated code is stale. ADR-009.

Alternative considered: generate OpenAPI from Go code comments (swag) —
rejected because the document, not the code, should be reviewable as the
contract; and code-first tends to leak internal types.

## Routing

`net/http` `ServeMux` (Go 1.22+ pattern routing: `GET /api/v1/tenants/{tenant}/jobs/{job}`)
is sufficient; no third-party router. Middleware chain (outermost first):

```text
recover → request-id → real-ip (trusted proxies only) → otel → access-log →
security-headers → CORS → body-limit (default 1 MiB; 4 MiB for workflow specs) →
timeout → authn → tenant-context (tenant routes) → handler
```

## Resource identifiers

UUIDv7 in URLs and bodies. Tenants and clusters also accept `slug`. Slurm
job IDs are never used as Custos identifiers; they appear as
`slurm.job_id` inside the job representation.

## Envelope and errors

Success responses return the resource directly (no `data` wrapper). List
responses:

```json
{ "items": [...], "next_cursor": "opaque", "total": null }
```

`total` is provided only when cheap (never for jobs/usage). Cursor is an
opaque base64url of the keyset (`created_at`, `id`). Cursors are strictly
validated but not signed — they carry no authority; every page is
re-authorized and re-scoped.

Errors:

```json
{
  "error": {
    "code": "WORKFLOW_INVALID",
    "message": "workflow failed validation",
    "request_id": "01J...",
    "details": [
      { "path": "spec.tasks[2].resources.walltime", "code": "EXCEEDS_POLICY", "message": "max walltime is 24h" }
    ]
  }
}
```

Status mapping: 400 malformed request, 401 unauthenticated, 403 forbidden,
404 not found (also used for tenant/resource existence hiding),
409 conflict (idempotency reuse, version conflict, state transition),
413 body too large, 422 validation failed, 429 rate limited, 500 internal
(`INTERNAL`, message fixed, details omitted), 502/503 upstream Slurm /
OpenBao / DB unavailable (`UPSTREAM_UNAVAILABLE`, `SECRETS_UNAVAILABLE`).
No stack traces, no upstream error bodies verbatim (they may contain paths
or hostnames; they are logged server-side with the request id).

Error codes are an enum in the OpenAPI document.

## Conventions

- JSON, `snake_case` fields, RFC 3339 UTC timestamps, durations as
  strings (`"4h"`, `"1-12:00:00"` accepted on input, normalized to seconds
  as `*_seconds` integers on output alongside the human form).
- `PATCH` with JSON Merge Patch for mutable resources; `If-Match` with
  resource `etag` (the `version` column) for optimistic concurrency on
  state-bearing resources.
- Actions that aren't CRUD are POST sub-paths named with a verb:
  `/{resource}/{id}/{verb}` (e.g. `POST .../jobs/{job}/cancel`,
  `POST .../workflows/{w}/versions/{v}/publish`). No `:verb` suffixes —
  ServeMux patterns and OpenAPI path templating both prefer plain
  sub-paths.
- `Idempotency-Key` header (1–128 chars) required on `POST .../jobs` and
  `POST .../workflow-executions`; optional elsewhere. Missing →
  `400 IDEMPOTENCY_KEY_REQUIRED`. The server hashes the raw request body;
  same key + same body replays the stored response (same job, `200`),
  same key + different body → `409 IDEMPOTENCY_MISMATCH`. Keys expire
  after 24h (`idempotency.expire` worker).
- `422` responses on job submission carry the error envelope plus a
  `diagnostics` array (`{error:{code,message,request_id}, diagnostics:
  [{source,code,severity,line,column,message,field,fix}...]}`) — codes
  are the `CUSTOS*` diagnostics of `docs/script-validation.md`;
  admission failures use `POLICY_VIOLATION` with the denied field in the
  message.
- Filtering via explicit query parameters (`state=`, `cluster=`,
  `since=`), never a free-form query language.
- Rate limiting: per-principal token bucket (in-process, per instance)
  with `Retry-After`; a stricter bucket on submission endpoints.
- Deprecation: `Deprecation` and `Sunset` headers; `/api/v1` is stable
  after Milestone 8; breaking changes → `/api/v2`.

## Endpoint families (v1 target)

```text
GET    /api/v1/me                                   principal, memberships
GET    /api/v1/tenants                              platform
POST   /api/v1/tenants                              platform
GET    /api/v1/tenants/{tenant}
PATCH  /api/v1/tenants/{tenant}
DELETE /api/v1/tenants/{tenant}                     platform; async purge, 202
GET    /api/v1/tenants/{tenant}/members
POST   /api/v1/tenants/{tenant}/members
PATCH  /api/v1/tenants/{tenant}/members/{user}
DELETE /api/v1/tenants/{tenant}/members/{user}
GET    /api/v1/tenants/{tenant}/users/lookup        exact email match
GET    /api/v1/tenants/{tenant}/groups
POST   /api/v1/tenants/{tenant}/groups
GET    /api/v1/tenants/{tenant}/groups/{group}
PATCH  /api/v1/tenants/{tenant}/groups/{group}
DELETE /api/v1/tenants/{tenant}/groups/{group}
GET    /api/v1/tenants/{tenant}/groups/{group}/members
POST   /api/v1/tenants/{tenant}/groups/{group}/members
DELETE /api/v1/tenants/{tenant}/groups/{group}/members/{user}
GET    /api/v1/tenants/{tenant}/claim-rules
POST   /api/v1/tenants/{tenant}/claim-rules
GET    /api/v1/tenants/{tenant}/claim-rules/{rule}
PATCH  /api/v1/tenants/{tenant}/claim-rules/{rule}
DELETE /api/v1/tenants/{tenant}/claim-rules/{rule}
GET    /api/v1/platform/role-bindings               platform
PUT    /api/v1/platform/role-bindings/{user}/{role} platform
DELETE /api/v1/platform/role-bindings/{user}/{role} platform
GET    /api/v1/tenants/{tenant}/projects                       all for project.read; member projects otherwise
POST   /api/v1/tenants/{tenant}/projects                       project.create; creator becomes project-admin
GET    /api/v1/tenants/{tenant}/projects/{project}
PATCH  /api/v1/tenants/{tenant}/projects/{project}             project.manage (optimistic version)
POST   /api/v1/tenants/{tenant}/projects/{project}/archive     project.manage
POST   /api/v1/tenants/{tenant}/projects/{project}/unarchive   project.manage
GET    /api/v1/tenants/{tenant}/projects/{project}/members
POST   /api/v1/tenants/{tenant}/projects/{project}/members     project.members.manage
PATCH  /api/v1/tenants/{tenant}/projects/{project}/members/{user}
DELETE /api/v1/tenants/{tenant}/projects/{project}/members/{user}
GET    /api/v1/tenants/{tenant}/projects/{project}/cluster-bindings
POST   /api/v1/tenants/{tenant}/projects/{project}/cluster-bindings  project.manage
GET    /api/v1/tenants/{tenant}/projects/{project}/cluster-bindings/{binding}
PATCH  /api/v1/tenants/{tenant}/projects/{project}/cluster-bindings/{binding}
DELETE /api/v1/tenants/{tenant}/projects/{project}/cluster-bindings/{binding}
GET    /api/v1/tenants/{tenant}/clusters            clusters visible to tenant (summaries; no base_url/credentials)
GET    /api/v1/tenants/{tenant}/clusters/{cluster}  visible cluster summary
GET    /api/v1/tenants/{tenant}/clusters/{cluster}/partitions
GET    /api/v1/tenants/{tenant}/policies/resource              policy.read
PUT    /api/v1/tenants/{tenant}/policies/resource              policy.manage
GET    /api/v1/tenants/{tenant}/projects/{project}/policies/resource   policy.read; includes `effective` (tenant ∩ project)
PUT    /api/v1/tenants/{tenant}/projects/{project}/policies/resource   policy.manage at tenant level
...    /api/v1/tenants/{tenant}/secret-references
...    /api/v1/tenants/{tenant}/workflows
...    /api/v1/tenants/{tenant}/workflows/{workflow}/versions
POST   /api/v1/tenants/{tenant}/workflows/{workflow}/versions/validate
POST   /api/v1/tenants/{tenant}/workflows/{workflow}/versions/{version}/publish
POST   /api/v1/tenants/{tenant}/workflows/{workflow}/versions/{version}/tasks/{task}/validate
POST   /api/v1/tenants/{tenant}/workflows/{workflow}/versions/{version}/tasks/{task}/import-sbatch
POST   /api/v1/tenants/{tenant}/workflows/{workflow}/versions/{version}/tasks/{task}/preview-submission
GET    /api/v1/tenants/{tenant}/workflows/{workflow}/versions/{version}/validations
POST   /api/v1/tenants/{tenant}/scripts/validate                (ad-hoc editor validation; see script-validation.md)
GET    /api/v1/tenants/{tenant}/workflow-executions/{execution}/tasks/{task}/execution-spec
GET    /api/v1/tenants/{tenant}/workflow-executions/{execution}/tasks/{task}/validation
...    /api/v1/tenants/{tenant}/workflow-executions
POST   /api/v1/tenants/{tenant}/workflow-executions/{execution}/cancel
GET    /api/v1/tenants/{tenant}/workflow-executions/{execution}/tasks
POST   /api/v1/tenants/{tenant}/projects/{project}/jobs                    job.submit; 202; Idempotency-Key required
GET    /api/v1/tenants/{tenant}/projects/{project}/jobs                    job.read.project, or own jobs
GET    /api/v1/tenants/{tenant}/projects/{project}/jobs/{job}
POST   /api/v1/tenants/{tenant}/projects/{project}/jobs/{job}/cancel       202; job.cancel.self/any
GET    /api/v1/tenants/{tenant}/projects/{project}/jobs/{job}/execution-spec
GET    /api/v1/tenants/{tenant}/jobs                     tenant-wide; job.read.tenant
GET    /api/v1/tenants/{tenant}/projects/{project}/jobs/{job}/output?stream=stdout   (Milestone 7+, via cluster file access policy)
GET    /api/v1/tenants/{tenant}/accounting/usage?group_by=...&from=&to=
GET    /api/v1/tenants/{tenant}/accounting/allocations
GET    /api/v1/tenants/{tenant}/audit-events
GET    /api/v1/clusters                             platform registry list
POST   /api/v1/clusters                             register cluster (SSRF-vetted base_url, token_ref)
GET    /api/v1/clusters/{cluster}                   platform detail
PATCH  /api/v1/clusters/{cluster}                   update (optimistic version)
POST   /api/v1/clusters/{cluster}/disable           disable (stops the sync chain)
POST   /api/v1/clusters/{cluster}/test-connection   open + ping + capabilities (no state change)
GET    /api/v1/clusters/{cluster}/tenants           list assignments
PUT    /api/v1/clusters/{cluster}/tenants/{tenant}  assign (defaults)
DELETE /api/v1/clusters/{cluster}/tenants/{tenant}  unassign
...    /api/v1/clusters/{cluster}/nodes | partitions | health   (M4+)
...    /api/v1/admin/work-items                     platform
GET    /api/v1/audit-events                         platform auditor
GET    /api/v1/openapi.json
GET    /health/live  /health/ready  /metrics       (metrics on a separate listener/port by default)
```
