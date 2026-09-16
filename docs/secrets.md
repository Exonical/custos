# Secrets (OpenBao)

## Principles

- Custos stores **references**, OpenBao stores **values**.
- Custos authenticates to OpenBao with workload identity; no root or
  long-lived token is ever configured.
- Secret values are read at the moment of use, held in memory for the
  duration of the operation, and zeroed where the type allows.
- Values never enter: logs, PostgreSQL, audit payloads, metrics, traces, API
  responses (except the narrow, explicitly audited "reveal" path a future
  milestone may add for user-owned secrets).

## Namespace strategy

Recommended layout (OpenBao namespaces are hierarchical):

```text
custos/                                   (platform namespace)
  ├─ kv/platform/...                       Custos-internal (e.g. cluster service JWT signing material references)
  ├─ kv/clusters/<cluster-id>/...          slurmrestd credentials, mTLS client cert refs
  └─ tenants/<tenant-id>/                  ONE child namespace per tenant
        ├─ kv/services/<name>              tenant service credentials
        ├─ kv/users/<user-id>/<name>       user-owned secrets (path-separated, not namespace)
        ├─ kv/workflows/<workflow-id>/...  workflow-scoped secrets
        └─ transit/, ssh/ (optional engines) per tenant
```

Tradeoff analysis — per-user namespaces vs paths:

| Option | Pros | Cons |
| --- | --- | --- |
| Namespace per tenant, **path per user** (recommended) | Bounded namespace count (tens–hundreds); one policy template per tenant with templated paths (`kv/users/{{identity.entity.aliases...}}`); simple backup/rotation | Requires careful policy templating for user-level isolation |
| Namespace per tenant **and** per user | Hard isolation per user; per-user audit device possible | Tens of thousands of namespaces; each has its own auth mounts, policies, token stores — operational and memory cost; slow listing |
| Single namespace, path per tenant | Simplest | Tenant admins cannot be delegated OpenBao admin; policy blast radius platform-wide |

Decision: tenant namespaces + user paths (ADR-003). User-level isolation is
enforced by OpenBao policies *and* by Custos (`SecretReference` ownership),
not by namespace boundaries.

## Custos authentication to OpenBao

- Kubernetes: `auth/kubernetes` or `auth/jwt` with the pod's projected
  service-account token → short-lived OpenBao token (TTL minutes), renewed
  by a background goroutine owned by the adapter, re-login on failure.
- Non-Kubernetes: `auth/jwt` with a workload JWT from the platform, or
  `auth/approle` with the secret-id delivered via a mounted file (dev only).

Custos's own token has policies allowing:

- token creation in tenant namespaces with **bounded** policies and TTLs
  (`create child token with policy tenant-<id>-runtime, ttl <= 10m`), and
- KV read on `custos/kv/clusters/*`.

Per-request secret access uses a **child token scoped to the tenant
namespace**, so a bug in tenant A's code path cannot read tenant B's KV
even if it constructs the path.

## SecretReference

```go
type SecretReference struct {
    ID        uuid.UUID
    TenantID  uuid.UUID
    OwnerID   *uuid.UUID     // user-owned when set
    ProjectID *uuid.UUID
    Name      string          // human handle used in workflow specs: secrets.<name>
    Provider  string          // "openbao"
    Namespace string          // "custos/tenants/<tenant-id>"
    Mount     string          // "kv"
    Path      string          // "users/<uid>/hf-token"
    Key       string          // field within the KV entry
    Version   *int            // pin, or nil = latest
    Kind      string          // ssh_key | slurm_token | api_token | generic | storage_credential
    AllowedUses []string      // "workflow_env", "stage_in", ...
    CreatedBy uuid.UUID
    CreatedAt time.Time
}
```

Validation on create: namespace must equal the tenant's namespace (the API
never accepts a namespace from the client — it is derived); path must match
`^[A-Za-z0-9_./-]+$`, no `..` segments; the caller must hold
`secret.reference.create`; if `OwnerID` is set it must be the caller unless
tenant-admin.

## Secrets port

```go
package secrets

type Resolver interface {
    // Resolve returns the value for ref. ctx must carry TenantContext; the
    // adapter derives the namespace-scoped token from it.
    Resolve(ctx context.Context, ref SecretReference) (Value, error)
}

type Value struct{ bytes []byte }     // unexported; String() returns "[REDACTED]"
func (v *Value) Bytes() []byte
func (v *Value) Zero()
```

`Value` implements `fmt.Stringer`, `slog.LogValuer`, and `json.Marshaler` to
redact itself, so accidental logging prints `[REDACTED]`.

## Providers

`secrets.Resolver` dispatches on `Reference.Provider`:

| Provider | Status | Use |
| --- | --- | --- |
| `file` | implemented (M3) | Dev/test and interim deployments that mount cluster credentials as files. Paths must resolve — after `filepath.Clean` + `EvalSymlinks` — inside an allow-listed root from `secrets.file_roots` (absolute paths only); `..` escapes and symlinks escaping the root are denied (`secrets.path_outside_root`). Files ≤ 64 KiB, trailing newline trimmed; `Key` extracts a string field from a JSON document. |
| `openbao` | M6 | OpenBao KV, the production provider (this doc). |

## Delivery to jobs (execution plane)

Jobs are untrusted; delivering a secret to a job is a deliberate, audited
act. Milestone 6 supports:

1. **Environment injection at submit time** — value placed in the Slurm job
   environment. Weakest option (visible in `scontrol show job` to the user,
   and to root on nodes); allowed only for `Kind=generic` and only when the
   policy permits `workflow_env`.
2. **Response-wrapped token** (preferred) — Custos creates a wrapped,
   single-use, short-TTL OpenBao token restricted to the referenced path;
   the job unwraps it with the `bao` CLI. The wrapped token, not the secret,
   is in the env.

Never delivered to jobs: Custos's own OpenBao token, cluster slurmrestd
credentials, PostgreSQL credentials.

## Cluster credentials

`clusters.credential_ref` points at `custos/kv/clusters/<id>` holding the
slurmrestd JWT (or the key material to mint them) and optional mTLS client
certificate. The Slurm adapter's `TokenProvider` reads through
`secrets.Resolver` with a short in-memory TTL cache (bounded to the token's
own lifetime). Rotating the secret in OpenBao is picked up without restart.

## Outage behavior

- Secret resolution failure → the operation fails with `SECRETS_UNAVAILABLE`
  (5xx), work items back off with jitter, nothing is retried more than the
  work item's policy allows.
- Readiness: OpenBao unreachable marks `/health/ready` as **degraded** (still
  200 with body detail) for the API — reads of Custos state still work — and
  **not ready** for the `worker` mode, so submitters stop taking leases.

## Audit

`secret.reference.created/deleted`, `secret.accessed` (ref id, purpose,
job/execution id, result). Never the value, never the OpenBao token.
