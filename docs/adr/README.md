# Architecture Decision Records

Each ADR records one architectural decision, the alternatives considered,
and its consequences. ADRs are immutable once Accepted — superseded
decisions get a new ADR that references the old one.

| ADR | Decision | Source doc |
| --- | --- | --- |
| [ADR-001](ADR-001-modular-monolith.md) | Modular monolith, single binary with `serve`/`worker`/`migrate` modes | [architecture.md](../architecture.md) §4 |
| [ADR-002](ADR-002-postgresql-pgx-goose.md) | PostgreSQL, pgx/v5 + hand-written SQL, goose migrations, UUIDv7, optimistic concurrency, RLS defense-in-depth | [architecture.md](../architecture.md) §7 |
| [ADR-003](ADR-003-openbao-secret-storage.md) | OpenBao: tenant namespaces + per-user KV paths, workload identity, SecretReference-only in DB | [secrets.md](../secrets.md) |
| [ADR-004](ADR-004-oidc-relying-party-go-oidc.md) | OIDC relying party via coreos/go-oidc/v3 + allow-list wrapper; JIT provisioning; bearer-only API | [authentication.md](../authentication.md) |
| [ADR-005](ADR-005-workflow-spec-format.md) | Own declarative `custos.io/v1alpha1` spec, Go-owned JSON Schema, immutable versions, restricted expressions | [workflows.md](../workflows.md) |
| [ADR-006](ADR-006-slurm-adapter-ports.md) | Consumer-owned `slurm.Cluster`/`slurm.Accounting` ports over Slinky per-version generated clients | [slurm.md](../slurm.md) |
| [ADR-007](ADR-007-postgres-work-queue.md) | Durable PostgreSQL work queue: SKIP LOCKED, dedupe index, jittered backoff, dead-letter | [workers.md](../workers.md) |
| [ADR-008](ADR-008-observability-stack.md) | OTel SDK, Prometheus on separate listener, slog JSON, strict label allow-list | [observability.md](../observability.md) |
| [ADR-009](ADR-009-api-contract-openapi.md) | Hand-maintained OpenAPI 3.1, oapi-codegen strict server, stdlib ServeMux, tenant-in-path | [api.md](../api.md) |
| [ADR-010](ADR-010-frontend-bff-auth.md) | Next.js BFF server-side session, HttpOnly cookies, CSRF double-submit | [frontend.md](../frontend.md) |
| [ADR-011](ADR-011-execution-spec-and-submission-envelope.md) | Immutable `ExecutionSpec` sole source of policy fields; `#SBATCH` rejected; base64 payload wrapper | [script-validation.md](../script-validation.md) |
| [ADR-012](ADR-012-script-validation-tooling.md) | mvdan/sh in-process; ShellCheck/ruff in credential-less sidecar; `ScriptValidator` port; persisted results | [script-validation.md](../script-validation.md) |
| [ADR-013](ADR-013-postgresql-hardening-baseline.md) | SCRAM-only, TLS verify-full outside dev, pgcrypto, append-only audit triggers, split migrate/app roles, fail-closed preflight | [architecture.md](../architecture.md) §7 |
| [ADR-014](ADR-014-cluster-credentials-file-provider.md) | Cluster credentials via `secrets.Resolver`; `file` provider until OpenBao (M6) | [secrets.md](../secrets.md), [slurm.md](../slurm.md) |
| [ADR-015](ADR-015-structured-argv-runtime-references.md) | Structured argv + allow-listed runtime references (`$SLURM_ARRAY_TASK_ID`) in ExecutionSpec | [workflows.md](../workflows.md), [script-validation.md](../script-validation.md) |
| [ADR-016](ADR-016-workqueue-self-reschedule.md) | Periodic handlers reschedule via `RescheduleAt`, never self-enqueue | [workers.md](../workers.md) |

## Process

Any new architectural decision (new external dependency category, new
trust boundary, change to a listed decision) requires an ADR before or
with the implementing change. Use the same template (`Context` /
`Decision` / `Alternatives considered` / `Consequences`).
