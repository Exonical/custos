# ADR-013: PostgreSQL hardening baseline

Status: Accepted
Date: 2026-09-15

## Context

ADR-002 chose PostgreSQL (pgx/v5, goose, RLS defense-in-depth) but left
the connection-level security posture implicit: authentication method,
transport encryption, schema-vs-runtime privileges, and tamper evidence
for `audit_events` were conventions, not enforced invariants. Custos is
the audit and control point for the HPC estate (TM-22, TM-8); a database
that accepts md5 passwords, plaintext transport, or mutable audit rows
undermines the threat model even when application code is correct. Test
stacks that run weaker settings also produce false confidence.

## Decision

- **SCRAM-only authentication.** `database.require_scram` (default true,
  relaxable only in `dev_mode`) makes startup fail unless
  `password_encryption = scram-sha-256` and — when readable — the
  connecting role's `pg_authid` entry is a `SCRAM-SHA-256$` hash. Test
  stacks (the Podman postgres:18 test compose, the CI service) are
  hardened to the same baseline rather than exempted.
- **TLS verify-full outside development.** `database.ssl_mode` is
  applied in `db.Open`; TLS query parameters in the URL are rejected so
  the configured mode cannot be silently downgraded. Optional CA and
  client-certificate material comes from `database.tls.*` files.
  Preflight asserts `ssl = on` whenever `ssl_mode != disable`.
- **`pgcrypto` required.** Created by the first migration; preflight
  fails with "run custos migrate up" when the schema is absent.
- **Append-only audit at the database layer.** `audit_events` gets
  `BEFORE UPDATE OR DELETE ... FOR EACH ROW` and `BEFORE TRUNCATE`
  triggers that raise `audit_events is append-only` — enforced even for
  the table owner, independent of grants. Triggers on the partitioned
  parent cover all partitions.
- **Split roles.** `custos_migrate` owns the schema and runs
  migrations; `custos_app` is a NOLOGIN-granted least-privilege runtime
  role (`GrantAppRole` in `custos migrate up`): DML on `work_items` /
  `idempotency_keys` / `audit_streams`, SELECT+INSERT on `audit_events`,
  no DDL, no audit mutation. `REVOKE ALL ON audit_events FROM PUBLIC`.
- **Fail-closed preflight.** `db.Open` runs `Preflight` after `Ping`;
  any failed check aborts startup (`db.preflight`, per-check details,
  no DSN in messages). `OpenForMigrate` skips only the schema-presence
  check.

## Alternatives considered

- **Application-level audit immutability** (grants + code review) —
  weaker: the migration/table-owner role could still rewrite history.
  Triggers close that hole for every role.
- **pg_hba/managed-PG enforcement outside the app** — operationally
  necessary but unverifiable from code; preflight makes the invariant
  executable and tested.
- **Exempting test stacks** — rejected; the test compose and CI service
  boot with `--auth-local=scram-sha-256` /
  `--auth-host=scram-sha-256` so `go test` exercises the same path
  production takes.

## Consequences

- Every deployment — including `docker/podman compose` and CI — must
  provision SCRAM credentials, TLS material, and the two roles;
  `deploy/compose/init-secrets.sh` automates the local case.
- Forged audit fixtures in tests must explicitly `DISABLE TRIGGER`
  first, making tampering loud.
- CockroachDB compatibility is unaffected: SCRAM and TLS settings are
  cluster-level concerns, and the trigger syntax is plain SQL.
