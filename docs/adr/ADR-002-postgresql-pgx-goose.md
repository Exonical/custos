# ADR-002: PostgreSQL with pgx/v5, hand-written SQL, and goose migrations

Status: Accepted
Date: 2026-09-14

## Context

Custos needs durable, transactional state where enqueueing work happens in
the same transaction as domain mutations (docs/workers.md). The schema
strategy (docs/architecture.md §7) requires: tenant-scoped tables with
`tenant_id` in every unique constraint, RLS as defense-in-depth,
optimistic concurrency via `version` columns, `SKIP LOCKED` work-queue
leasing, monthly range partitioning of append-only tables, keyset
pagination, and a path to CockroachDB later.

## Decision

- Driver: `github.com/jackc/pgx/v5` (`pgxpool`) with hand-written SQL and
  typed scanning helpers. No ORM.
- Migrations: `pressly/goose/v3` with plain SQL up/down files embedded via
  `embed.FS`, run by `custos migrate` as a separate step — never on API
  startup.
- Primary keys: UUIDv7 generated in Go (no sequences) — time-ordered
  inserts keep B-trees local and avoid sequence dependence.
- Concurrency: optimistic `UPDATE ... WHERE id AND version` on state-
  machine aggregates; `FOR UPDATE SKIP LOCKED` for leases; no advisory
  locks.
- RLS on tenant-owned tables via `SET LOCAL app.tenant_id` per
  transaction, with a `BYPASSRLS` role for platform operations. RLS is a
  safety net, not the authorization system.
- CockroachDB-compatible constraints: no `SERIAL`, no advisory locks, no
  `LISTEN/NOTIFY`, no DDL mixed with DML in a transaction; `SKIP LOCKED`
  is supported in CRDB 23.2+. PG-specific partitioning syntax is isolated
  in migrations marked as such.

## Alternatives considered

- **ORM (GORM/ent)** — rejected: hides the transactional/guarded-update
  semantics the design relies on (SKIP LOCKED, version-guarded updates,
  RLS session settings) and obscures per-query tenant scoping.
- **sqlc** — rejected: adds a codegen step and still hand-writes SQL;
  typed scanning helpers give the same safety without the toolchain.
- **`database/sql` stdlib** — rejected: loses pg types and pgx
  performance; pgx is the de-facto driver (docs/milestone-1.md).
- **CockroachDB from day one** — rejected: adds operational and dialect
  cost now; the constraint list keeps it a viable future target instead.

## Consequences

- Every repository method must take a `tenants.Scope` value type and set
  `app.tenant_id` per transaction; forgetting it is what RLS catches.
- Migration authoring discipline (no DDL+DML in one tx, no advisory
  locks, partitioned parents pre-created by a worker) is permanent.
- Partitioning migrations are marked PG-specific and would be the main
  porting cost if CockroachDB is ever adopted.

Source: docs/architecture.md §7; docs/milestone-1.md "Dependencies
introduced in M1"; docs/workers.md "Why PostgreSQL".
