# Custos

Custos is a multi-tenant control plane / Slurm gateway: it sits between
people and automation on one side and one or more Slurm clusters on the
other, owning identity mapping, tenancy, authorization, policy, workflow
orchestration, job bookkeeping, accounting visibility, and audit. Slurm
remains the scheduler and execution authority — Custos never schedules
work itself.

## Status

**Milestones 1–6 implemented.** The `custos` binary serves, works and
migrates today:

- **M1** — foundation binary on hardened PostgreSQL (SCRAM-only, TLS
  verify-full, RLS, append-only audit, split roles).
- **M2** — OIDC authentication, JIT user provisioning, tenants,
  projects and RBAC.
- **M3** — cluster registry and sync; Slurm 26.05 adapter (slurmrestd
  `v0.0.45`) with conformance fixtures.
- **M4** — secure job submission: content-addressed scripts,
  `ExecutionSpec`, admission and the submission wrapper.
- **M5** — validation pipeline with persisted results, ValidationPolicy,
  ShellCheck sidecar, workflow versions with a publish gate, and the
  execution engine (`execution.advance`, `task.admit`, structured argv).
- **M6** — secrets: OpenBao provider with tenant namespaces and one-use
  child tokens, a default connector per tenant plus bring-your-own
  OpenBao connectors, metadata-only `SecretReference` objects, and
  submit-time delivery to jobs as environment values or response-wrapped
  tokens.

An end-to-end stack (`scripts/e2e.sh`, `deploy/e2e/`) runs the whole
thing against a real Slurm 26.05 cluster, Keycloak 26.7 and OpenBao
under Podman; `test/e2e` exercises authentication through secret
delivery and workflow cancellation.

Next up: **M7** accounting/policy sync, **M8** the Next.js UI.

## Documentation

- [Architecture](docs/architecture.md) — system overview, trust
  boundaries, modular monolith, domain model, PostgreSQL strategy
- [Authentication](docs/authentication.md) — OIDC relying party, JWT
  verification, JIT provisioning
- [Authorization](docs/authorization.md) — Authorizer port, permission
  catalog, RBAC
- [Tenancy](docs/tenancy.md) — isolation model, tenant context, test
  matrix
- [Secrets](docs/secrets.md) — OpenBao namespaces, SecretReference, job
  delivery
- [Slurm integration](docs/slurm.md) — adapter ports, Slinky clients,
  reconciliation
- [Workflows](docs/workflows.md) — `custos.io/v1alpha1` spec, state
  machines, engine
- [Script validation](docs/script-validation.md) — payload validation,
  `ExecutionSpec` admission, submission wrapper
- [Workers](docs/workers.md) — durable PostgreSQL work queue,
  reconciliation loops
- [Observability](docs/observability.md) — metrics, logs, accounting
  pipeline, health
- [End-to-end stack](docs/e2e.md) — real Slurm 26.05 + Keycloak 26.7
  under Podman, `test/e2e` suite
- [Frontend](docs/frontend.md) — Next.js BFF, sessions, editor
- [API conventions](docs/api.md) — OpenAPI contract, routing, envelopes
- [Threat model](docs/threat-model.md) — assets, actors, STRIDE threats
- [Milestone 1 plan](docs/milestone-1.md) — foundation implementation plan
- [Architecture Decision Records](docs/adr/README.md) — ADR-001 … ADR-017

## Planned stack

- **Backend**: Go 1.27, one binary (`custos serve` / `worker` /
  `migrate`), pgx/v5 + PostgreSQL (goose migrations), OpenBao for
  secrets, coreos/go-oidc for OIDC, SlinkyProject slurm-client generated
  clients (baseline Slurm 26.05 / slurmrestd v0.0.45; Slurm 25.11 /
  v0.0.44 also supported), OpenTelemetry + Prometheus.
- **Frontend**: Next.js (App Router, TypeScript strict) as a BFF;
  shadcn/ui + Tailwind; React Flow + Monaco (workflow editor, script
  editing with backend diagnostics).
- **Deploy**: container image, Helm chart; API and workers as separate
  Deployments of the same image.

## Quick start (local, Docker/Podman)

```sh
bash deploy/compose/init-secrets.sh   # generates .secrets/ (gitignored)
docker compose -f deploy/compose/compose.yaml up -d --wait
```

Then:

- API: `http://localhost:8080` (`/health/live`, `/health/ready`)
- OpenAPI document: `http://localhost:8080/api/v1/openapi.json`
- Metrics: `http://localhost:9090/metrics`

The compose stack runs a hardened PostgreSQL 18 (SCRAM-only auth, TLS —
see [ADR-013](docs/adr/ADR-013-postgresql-hardening-baseline.md)), a
one-shot `migrate` job as the `custos_migrate` role, `serve`, and a
`worker`, both connecting as the least-privilege `custos_app` role with
`ssl_mode: verify-full`; config lives in `deploy/compose/custos.yaml`
(dev_mode relaxes only the HTTP listener, never the database path).

With `auth.oidc` configured, bootstrap the first platform admin (nothing
can create a tenant until one exists):

```sh
custos admin platform-role grant \
  --issuer https://idp.example.com --subject <sub> --role platform-admin
```

See [docs/authentication.md](docs/authentication.md#bootstrapping-the-first-platform-admin).

For Kubernetes, see the Helm skeleton in `deploy/helm/custos`.

## Non-goals

- Replacing or wrapping every slurmrestd endpoint.
- Acting as an identity provider.
- Acting as a general secrets store.
- Meta-scheduling in the first milestones (interfaces only).
