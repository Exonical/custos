# Tenancy

## Model

```text
Platform
  └── Tenant                     (isolation + billing + policy unit)
        ├── TenantMembership     (User × roles; the only entry point)
        ├── Group                (bulk role/membership management)
        ├── Project              (work + accounting unit)
        │     ├── ProjectMembership
        │     ├── ProjectClusterBinding → Slurm account/QoS/partitions
        │     └── Allocation
        ├── Workflow / WorkflowVersion / WorkflowExecution / Job
        ├── SecretReference
        ├── ResourcePolicy
        └── ClusterTenantAssignment (visibility of platform clusters)
```

`User` and `Cluster` are platform-level; everything else is tenant-owned.

## Tenant context establishment

Tenant-scoped routes carry the tenant in the path
(`/api/v1/tenants/{tenant}/...`). Middleware:

1. Resolves `{tenant}` (slug or UUID) to a `Tenant` row.
2. Loads the principal's `TenantMembership` for that tenant.
3. If none and principal has no platform role: respond `404`
   (`TENANT_NOT_FOUND`) — identical to a non-existent tenant, preventing
   enumeration.
4. Stores `TenantContext{TenantID, Membership, EffectiveRoles}` in `ctx`.

Repositories accept a `tenants.Scope` value (not a string) that can only be
constructed from a verified `TenantContext` or by platform services via an
explicitly named constructor (`tenants.PlatformScope()`), making cross-tenant
access grep-able.

Request bodies are **never** allowed to carry `tenant_id`. DTO decoding uses
`json.Decoder.DisallowUnknownFields` and DTOs contain only writable fields
(prevents mass assignment).

## Isolation layers (defense in depth)

| Layer | Mechanism |
| --- | --- |
| Routing | tenant in path; tenant context middleware |
| Authorization | membership required; `Authorizer.Check` per operation |
| Repository | `Scope` parameter on every method; `WHERE tenant_id = $1` |
| Database | RLS policies keyed on `current_setting('app.tenant_id')` |
| Secrets | OpenBao namespace per tenant; Custos token for a request is scoped to that namespace |
| Slurm | job's Slurm account comes from `ProjectClusterBinding`, never from the client |
| Observability | tenant appears as a bounded label only for platform-level counters; never user/job ids |

Object identifiers are UUIDs; ownership is still always checked (unguessable
IDs are not an authorization mechanism).

## Cross-tenant users

A user may hold memberships in many tenants. The UI has a tenant switcher;
the CLI has `--tenant` / a default in config. There is no "current tenant"
stored server-side — it's always in the request path.

## Global vs assigned clusters

Rather than a nullable tenant on clusters, a cluster has `visibility` in
`{assigned, all_tenants}`. For `all_tenants`, the cluster-sync worker keeps a
`cluster_tenant_assignments` row per tenant (source = `auto`). Queries never
special-case NULL; access remains a join on the assignment table. Projects
still need an explicit `ProjectClusterBinding` to *submit*.

## Tenant lifecycle

`provisioning → active → suspended → deleting → deleted`. Suspension blocks
new submissions and executions (existing jobs are left to Slurm; a
tenant-admin may still cancel). Deletion is asynchronous (work item): cancel
executions, revoke OpenBao namespace, retain audit and usage records under a
tombstoned tenant for the retention period.

## Isolation test matrix (mandatory from Milestone 2 on)

For tenants A and B with user `a` in A and `b` in B, for each resource kind
(project, workflow, workflow version, execution, job, cluster assignment,
secret reference, policy, usage, audit event):

| Attempt | Expected |
| --- | --- |
| `a` lists B's resources | 404 tenant |
| `a` GETs B resource by ID under `/tenants/A/...` | 404 resource |
| `a` GETs B resource by ID under `/tenants/B/...` | 404 tenant |
| `a` creates in A referencing B's cluster binding / secret reference / workflow version | 422 validation, no side effects |
| platform-auditor reads B | 200 |
| platform-auditor mutates B | 403 |
| RLS test: repository called with Scope(A) on a row of B via raw SQL bypass attempt | 0 rows |

Tests run against a real PostgreSQL (testcontainers) with fake Slurm/OpenBao.
