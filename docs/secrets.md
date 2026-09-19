# Secrets and connectors

## Principles

- Custos stores **references**; OpenBao stores **values**.
- Custos authenticates to platform OpenBao with workload identity. A root token
  is used only by stack bootstrap and is revoked immediately.
- Values are read at use time, held only for the operation, and wiped where the
  type allows. They never enter PostgreSQL, logs, audit details, metrics,
  traces, or API responses.
- M6-A implements providers, connectors, references, cluster credentials, and
  safe connectivity tests. Job delivery is M6-B and remains fail-closed with
  `SECRETS_NOT_AVAILABLE`.

## Platform OpenBao

The recommended layout is:

```text
custos/                                  platform namespace
  kv/clusters/<cluster-id>/...           slurmrestd credentials
  tenants/<tenant-id>/                   one child namespace per tenant
    kv/connectors/<connector-id>          BYO connector login credential
    kv/services/<name>                    tenant service credentials
    kv/users/<user-id>/<name>             user-owned secrets
    kv/workflows/<workflow-id>/...        workflow-scoped secrets
```

Tenant namespaces plus user paths provide a bounded namespace count while
retaining an OpenBao boundary between tenants (ADR-003). Custos idempotently
creates the tenant namespace, KV v2 mount, and `tenant-<id>-runtime` policy when
a tenant is created, and lazily on first default-reference creation.

### Authentication and token lifecycle

The adapter is a thin, typed HTTP client over the small auth/token/namespace/
policy/KV v2 surface used by Custos. This avoids pulling OpenBao's full API
module and transitive helper tree into the control plane while keeping request
and error handling directly testable with `httptest`.

The platform provider supports:

- JWT auth using either a mounted workload JWT file or an OIDC
  client-credentials token (the e2e stack uses Keycloak for this shape); and
- AppRole with the secret-id in a mounted file, only when `dev_mode` is true.

Exactly one JWT source is required. Plain HTTP and AppRole fail configuration
validation outside `dev_mode`. Login yields a short-lived parent token. The
provider renews it at approximately two-thirds of its TTL and re-authenticates
with jittered backoff after renewal failure. Shutdown revokes the provider-owned
token.

Tenant resolution derives the namespace from the persisted reference, creates
a one-use orphan token in that tenant namespace with only
`tenant-<id>-runtime` and a TTL of at most ten minutes, performs the KV v2 read,
and attempts token revocation. Child tokens and values are not cached; only the
parent token is cached. **The implemented provider does not fall back to parent
reads for tenant values**: failure to create the child token fails the request,
preserving the isolation boundary.

Cluster credentials are platform-owned and are read with the parent token from
`custos/kv/clusters/<cluster-id>` (the e2e fixture uses `clusters/e2e` because
the UUID is not known before registration).

## Connectors

A `SecretConnector` selects the manager used by tenant references:

- `platform-openbao` is the automatically provisioned `default` connector when
  platform OpenBao is configured. It uses the platform address, CA, workload
  auth, tenant namespace, and child-token isolation described above.
- `openbao` connects to a tenant-managed OpenBao/Vault-compatible endpoint.
  Its non-secret configuration contains address, pinned CA, namespace, mount,
  and auth shape. M6-A supports static token, AppRole (`role_id` plus a
  separately stored `secret_id`), and JWT (`role` plus a separately stored
  workload JWT). The factory remains additive for future AWS, Azure, and GCP
  secret-manager kinds.

BYO credentials supplied during create/rotate are written directly to the
platform tenant namespace at `kv/connectors/<connector-id>`. PostgreSQL stores
only the platform `credential_ref`; API responses never echo the credential.
Consequently, BYO connector creation requires platform OpenBao and otherwise
returns `PLATFORM_SECRETS_REQUIRED`.

Connector addresses are tenant-controlled. Create, update, test, and resolution
use the same `platform/safehttp` transport as Slurm: TLS 1.3 minimum, optional
pinned CA, no redirects, all DNS answers vetted, and dialing by vetted address
to prevent rebinding. Cloud metadata, link-local, loopback, unspecified, and
policy-disallowed private addresses fail closed. `slurm.dial_policy.allow_private`
also controls private BYO endpoints; HTTP remains forbidden for BYO connectors.
Connectors are cached by `(id, version)` and invalidated on update or deletion.
A connector with references cannot be deleted (`CONNECTOR_IN_USE`).

## SecretReference

Tenant references persist only metadata:

```text
id, tenant_id, owner_id?, project_id?, name, connector_id,
namespace, mount, path, key, secret_version?, kind, allowed_uses,
created_by, timestamps, optimistic version
```

Create accepts a connector name or UUID and defaults to `default`. The platform
connector namespace is server-derived as `custos/tenants/<tenant-id>`; clients
cannot select a foreign namespace. Paths match `^[A-Za-z0-9_./-]+$` and may not
contain empty, `.` or `..` segments. Kinds are `ssh_key`, `slurm_token`,
`api_token`, `generic`, and `storage_credential`. Owned references are visible
only to their owner and tenant administrators.

`POST .../secret-references/{reference}/test` resolves and immediately wipes the
value, returning only `{ok, kind, version, resolved_at}`. Connector tests return
only `{ok, kind, latency_ms}`. Tests and access are audited without values or
tokens.

## Platform provider references

Platform-owned cluster references retain `provider` because they do not belong
to a tenant connector:

| Provider | Use |
| --- | --- |
| `file` | Dev/test mounted files. Absolute paths must resolve inside `secrets.file_roots`; symlink and `..` escapes are denied. Files are limited to 64 KiB. |
| `openbao` | Production KV v2 references with `namespace`, `mount`, `path`, `key`, and optional `version`. |

A recognized but unconfigured provider returns HTTP 422
`SECRET_PROVIDER_UNAVAILABLE`. OpenBao 403 and 404 map to
`secrets.forbidden`/`secrets.not_found`; transport and 5xx failures map to
`SECRETS_UNAVAILABLE`.

## Readiness and metrics

The API registers optional readiness check `openbao`: an outage produces
`degraded` with HTTP 200 so state reads remain available. The worker metrics
listener exposes `/health/ready` with OpenBao as a required check, so the same
outage reports not-ready there. Workers also authenticate at startup and fail
secret-dependent work closed; resolution failures retain durable-work backoff. Resolution emits
`custos_secrets_resolve_total{kind,result}` with bounded connector kinds and
results; labels never contain tenant, path, connector, or secret identifiers.

## Delivery to jobs (M6-B)

Environment injection and response-wrapped short-lived tokens are deliberately
not implemented in M6-A. Workflow `spec.secrets` names are available to the
contextual lookup hook, but static validation continues to reject every use
with `SECRETS_NOT_AVAILABLE`. Jobs never receive the platform token, cluster
credentials, connector credentials, or database credentials.
