# Authorization

## Model

Authorization is a pure function over `(principal, action, resource)` behind a
single port:

```go
package authz

type Action string          // e.g. "job.submit", "cluster.manage"

type Resource struct {
    Kind      string        // "tenant", "project", "cluster", "workflow", "job", "secret_reference", ...
    ID        string        // resource id ("" for collection-level actions)
    TenantID  string        // "" for platform resources
    ProjectID string        // optional
    OwnerID   string        // optional; enables *.self permissions
    ClusterID string        // optional; enables cluster-scoped permissions
    Attrs     map[string]string // extension point for ABAC (labels, sensitivity)
}

type Decision struct {
    Allow  bool
    Reason string           // stable, non-sensitive; logged and audited on deny
}

type Authorizer interface {
    Check(ctx context.Context, p authn.Principal, a Action, r Resource) (Decision, error)
}
```

Rules:

- Business logic calls `Check`; it never inspects role names.
- `Check` is called in **services**, not HTTP handlers, so workers and future
  gRPC/CLI paths cannot bypass it.
- A deny produces an `authz.denied` audit event and a `403` (or `404` when
  the resource's existence must not be disclosed — the service decides).
- `error` is only for infrastructure failures (e.g. membership lookup
  failed). Infrastructure failure → deny.

## Permission catalog

Permissions are string constants in `internal/authz/permissions.go`, grouped
by resource kind. Initial set:

```text
platform.manage            platform.audit.read
tenant.read                tenant.manage         tenant.members.manage
project.read               project.create        project.manage   project.members.manage
cluster.read               cluster.manage        cluster.assign
policy.read                policy.manage
workflow.read              workflow.create       workflow.publish  workflow.execute  workflow.approve
execution.read.self        execution.read.project execution.read.tenant execution.cancel.self  execution.cancel.any
job.submit                 job.read.self         job.read.project  job.read.tenant   job.cancel.self   job.cancel.any
secret.reference.read      secret.reference.create  secret.reference.use
accounting.read.self       accounting.read.project  accounting.read.tenant
audit.read.tenant
```

`*.self` permissions are satisfied only when `resource.OwnerID == principal.UserID`.

Shell tasks (`type: shell`) in workflows are gated by `workflow.publish`
plus the tenant `ValidationPolicy` flag `allowShellTasks: true`
(`docs/script-validation.md`); there is no separate `workflow.shell`
permission.
Script validation endpoints require `workflow.create` (mutating) or
`workflow.read` (reading results); legacy `#SBATCH` import additionally
requires the tenant `ValidationPolicy` flag `allowLegacySbatchImport`
(`docs/script-validation.md`). `ValidationPolicy` itself is edited under
`policy.manage`; platform admins own cluster-scope policies.

## Roles (RBAC v1)

Roles are named bundles of permissions. Bindings exist at three scopes:

| Scope | Binding table | Roles |
| --- | --- | --- |
| Platform | `platform_role_bindings` | `platform-admin`, `platform-auditor` |
| Tenant | `tenant_memberships.roles[]` | `tenant-admin`, `tenant-operator`, `workflow-author`, `researcher`, `viewer`, `auditor` |
| Project | `project_memberships.roles[]` | `project-admin`, `project-member`, `project-viewer` |
| Cluster (within tenant) | `cluster_role_bindings` (tenant_id, cluster_id, subject) | `cluster-operator`, `cluster-user` |

Role → permission mapping is data in Go (a table), not scattered code, and is
covered by a golden test so changes are visible in review.

Sketch:

```text
platform-admin   : platform.manage + everything
platform-auditor : platform.audit.read, *.read.* (all tenants), audit.read.tenant
tenant-admin     : tenant.*, project.*, policy.*, workflow.*, execution.*.any, job.*.any, secret.reference.*, accounting.read.tenant, audit.read.tenant
tenant-operator  : tenant.read, project.read, cluster.read, job.read.tenant, job.cancel.any, execution.read.tenant, execution.cancel.any, accounting.read.tenant
workflow-author  : workflow.read/create/publish, secret.reference.read/use, + researcher
researcher       : tenant.read, project.read, cluster.read, workflow.read, workflow.execute, job.submit, job.read.self, job.cancel.self, execution.*.self, accounting.read.self, secret.reference.use
viewer           : *.read.self, tenant.read, project.read, cluster.read, workflow.read
auditor          : tenant.read, project.read, cluster.read, workflow.read, audit.read.tenant, accounting.read.tenant, *.read.tenant
project-admin    : project.read/manage/members.manage, workflow.read/create/publish/execute,
                   job.submit/read.self/read.project/cancel.self, execution.read.self/read.project/cancel.self,
                   accounting.read.self/read.project (on that project)
project-member   : project.read, workflow.read/execute, job.submit/read.self/cancel.self,
                   execution.read.self/cancel.self, accounting.read.self
project-viewer   : project.read, workflow.read, job.read.self, execution.read.self
```

Evaluation order in the RBAC authorizer:

1. Platform bindings (checked against the platform role table).
2. If `resource.TenantID != ""`: load tenant membership for principal (from
   `TenantContext`, already verified). No membership → deny.
3. Union permissions from tenant roles, then — only when
   `resource.ProjectID` matches the project the request middleware resolved
   *and* the principal is a tenant member — the roles from the principal's
   `ProjectMembership` for that project (attached to the request context by
   the project middleware), cluster roles (if `resource.ClusterID`).
4. Match action, applying `*.self` owner check.

Membership lookups are per-request cached in the context (loaded once when
`TenantContext` is established).

## Policy vs authorization

`authz` answers "may this principal perform this action on this resource".
`policies` answers "is this *request content* within the tenant/project
limits" (max walltime, allowed partitions...). They are separate packages and
both must pass. Conflating them makes external PDP integration harder.

## External PDP path (future)

Because callers only see `Authorizer`, alternative implementations can be
swapped in via config:

| Option | Fit |
| --- | --- |
| Built-in RBAC (v1) | Simple, no extra service, sufficient for role-based tenants |
| OPA (Rego, sidecar or embedded) | Good for ABAC on `Resource.Attrs`; we would send principal + resource + memberships as input |
| Cedar | Strong typed policies; embed via `cedar-go` |
| SpiceDB / ReBAC | Best if relationship graphs (project→group→user→cluster) become deep |

Design consequence: `Resource` carries enough context (tenant, project,
owner, cluster, attrs) for an external PDP to decide *without* calling back
into Custos. A `CompositeAuthorizer` (built-in deny-overrides external) is
the intended migration path.

## Testing requirements

- Table-driven tests per permission: every role × action × ownership case.
- Negative tests: no membership, membership in another tenant, project
  member of another project, `*.self` with wrong owner.
- Tenant-isolation tests in each domain package's service tests (see
  `docs/tenancy.md`).
