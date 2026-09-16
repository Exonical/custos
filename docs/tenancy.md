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
`cluster_tenant_assignments` row per qualifying tenant (`source = auto`):
on each successful sync it upserts rows for tenants in `active`/`suspended`
state and drops auto rows for tenants that leave those states
(`deleting`/`deleted`). `manual` assignment rows are never touched by the
reconciler, and manual assign/unassign is refused (409 `CLUSTER_VISIBILITY`)
on `all_tenants` clusters. Queries never special-case NULL; access remains
a join on the assignment table. Projects still need an explicit
`ProjectClusterBinding` to *submit*.

## Groups

A `Group` is a named set of tenant members (`tenant_id + name` unique, name
`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,62}$`). Group memberships carry a `source`
(`manual` | `idp`) like tenant memberships; a database trigger enforces that
every group member is a tenant member. Groups give bulk membership
management and are the target of claim mapping rules: a rule may grant
roles *and* add the user to a group in the same reconcile.

## Claim mapping rules

`claim_mapping_rules` map an IdP claim value to tenant roles and an optional
group: `(tenant_id, claim, match_value)` unique, `roles text[]` non-empty,
`group_id` optional (`ON DELETE SET NULL`), `enabled` flag. Match is an
exact string compare — no wildcards. Rules are evaluated during JIT claim
reconciliation (see `docs/authentication.md`).

## Tenant lifecycle

`provisioning → active → suspended → deleting → deleted`. Suspension blocks
new submissions and executions (existing jobs are left to Slurm; a
tenant-admin may still cancel). Deletion is asynchronous: `DELETE
/api/v1/tenants/{tenant}` (`platform.manage`) sets state `deleting` and
enqueues a `tenant.delete` work item in the same transaction, then a worker
purges memberships, groups, and claim rules and sets state `deleted`. The
`tenants` row is retained as a tombstone — slugs are never reused. Tenants
in `deleting`/`deleted` are invisible to non-platform principals (same 404
as a nonexistent tenant) but remain readable by platform roles for audit
review.

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
