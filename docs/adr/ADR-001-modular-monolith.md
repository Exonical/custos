# ADR-001: Modular monolith with serve/worker/migrate run modes

Status: Accepted
Date: 2026-09-14

## Context

Custos is a multi-tenant control plane: a REST API, background workers
(submit, reconcile, sync, collect), and one-shot migrations all share the
same domain logic, ports, and database. The team is small and the system
must remain operable by a single team ("one binary, one database, no
brokers" — docs/architecture.md §10). API and worker workloads do need to
scale independently, and domain code must stay decoupled from HTTP, pgx,
OpenBao, and Slinky so adapters can be swapped (docs/architecture.md §4).

## Decision

Ship one deployable Go binary (`custos`) with run modes selected by flags:
`serve`, `worker`, `migrate` (plus `version`). API and workers scale
independently as separate Deployments of the same image; `custos serve`
may optionally embed the worker (`--embed-worker`) for small deployments.

Inside the binary, enforce hexagonal packaging (docs/architecture.md §4–5):

- `internal/<domain>` packages own domain types, service logic, and the
  *ports* they consume; they import nothing from `http`, `pgx`, OpenBao,
  or Slinky (enforced by `depguard` in lint).
- Adapters live in `internal/<domain>/postgres`, `internal/slurm/slinky`,
  `internal/secrets/openbao`, `internal/api/...` and depend inward only.
- Wiring is plain constructors in `cmd/custos`; no DI framework.
- Interfaces are declared by the consumer and kept small.

## Alternatives considered

- **Microservices** — rejected: a service-per-domain adds network failure
  modes, distributed transactions, and operational overhead far beyond
  what the scale (thousands of work items/minute) or team size justifies.
- **Separate API and worker binaries** — rejected: duplicates domain code
  or forces a shared library package that drifts; a single image with run
  modes gives the same independent scaling without code split.
- **DI framework** — rejected: plain constructors in `cmd/custos` are
  explicit and greppable at this size.

## Consequences

- Independent scaling is achieved via Deployments of one image, not
  separate codebases.
- Compile-time enforcement (depguard) is required to keep the module
  boundaries real; without it the monolith will rot.
- A future split into services is still possible because ports are already
  consumer-owned and adapters are isolated.

Source: docs/architecture.md §4–5, §10; docs/workers.md "Worker process
model".
