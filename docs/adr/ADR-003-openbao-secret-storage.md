# ADR-003: OpenBao secret storage — tenant namespaces, per-user paths, references only

Status: Accepted
Date: 2026-09-14

## Context

Custos must handle secrets (cluster slurmrestd credentials, tenant service
credentials, user-owned tokens, workflow-scoped secrets) without becoming
a secrets store itself. Principles in docs/secrets.md: Custos stores
references, OpenBao stores values; values are read at the moment of use
and never enter logs, PostgreSQL, audit payloads, metrics, traces, or API
responses. Custos authenticates with workload identity — no root or
long-lived token is ever configured.

## Decision

- Namespace layout: a platform namespace `custos/` holding
  `kv/platform/*` and `kv/clusters/<cluster-id>/*`, plus **one child
  namespace per tenant** (`custos/tenants/<tenant-id>`) with path-level
  separation inside it (`kv/services/*`, `kv/users/<user-id>/*`,
  `kv/workflows/*`, optional transit/ssh engines).
- Custos authenticates via `auth/kubernetes`/`auth/jwt` workload identity
  (approle only for dev). Its token may only create bounded child tokens
  (`tenant-<id>-runtime` policy, TTL ≤ 10m) and read cluster credentials.
- Per-request secret access uses a **child token scoped to the tenant
  namespace**, so a bug in tenant A's path construction cannot read
  tenant B's KV.
- PostgreSQL stores only `SecretReference` metadata (provider, namespace,
  mount, path, key, version, kind, allowed uses, ACL) — never a value.
  The namespace is derived server-side, never accepted from clients.
- Job delivery: prefer a **response-wrapped, single-use, short-TTL
  OpenBao token** restricted to the referenced path; env injection of the
  raw value is the weakest option, allowed only for `Kind=generic` with
  `workflow_env` permitted. Jobs never receive Custos's OpenBao token,
  cluster credentials, or DB credentials.
- `secrets.Value` self-redacts (`[REDACTED]`) via Stringer/LogValuer/
  Marshaler.

## Alternatives considered

- **Namespace per tenant and per user** — rejected: tens of thousands of
  namespaces, each with auth mounts, policies, and token stores;
  operational/memory cost and slow listing.
- **Single namespace, path per tenant** — rejected: tenant admins cannot
  be delegated OpenBao admin; policy blast radius is platform-wide.
- **Storing encrypted secrets in PostgreSQL** — rejected: makes Custos a
  secrets store (key management, rotation, value-at-rest exposure via DB
  reads) contrary to the principles.

## Consequences

- User-level isolation rests on OpenBao policy templating plus Custos
  `SecretReference` ownership, not namespace boundaries — both must be
  tested.
- Tenant deletion must revoke the tenant namespace (staged teardown work
  item).
- OpenBao outage behavior is defined: fail closed for submissions,
  degraded (not down) readiness for the API, not-ready for workers.

Source: docs/secrets.md (all sections); docs/architecture.md §3 B3/B5.
