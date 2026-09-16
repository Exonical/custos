# ADR-014: Cluster credentials via secrets.Resolver with a file provider before OpenBao

Status: Accepted
Date: 2026-09-16

## Context

Milestone 3 adds the Slurm adapter, which needs a JWT token (and
optionally an mTLS client certificate) per cluster. OpenBao — the
production secret store (ADR-003) — is not integrated until M6. The
choices were to (a) defer all credential handling to M6, (b) store
tokens in the `clusters` row, or (c) build the secrets port now with an
interim file provider.

## Decision

(c). `internal/secrets` defines `Reference`, `Value` (self-redacting,
wipeable), and `Resolver`. `slinky.Factory.Open` resolves credentials
through the resolver at call time; `clusters` rows will store references
only, never values — the same contract OpenBao satisfies later. Until
then, `FileResolver` serves secrets from allow-listed filesystem roots
(`secrets.file_roots`), with `EvalSymlinks`-checked path confinement and
a 64 KiB cap. Unknown providers are rejected; `openbao` arrives in M6.

## Alternatives considered

- Defer credentials to M6: leaves the adapter untestable end-to-end and
  would have invited a temporary credentials-in-DB design that later
  needs migrating away from.
- Store tokens on the `clusters` row: violates docs/secrets.md
  (PostgreSQL holds references only) and creates a plaintext-at-rest
  credential store.

## Consequences

- Tests and dev deployments can run the full adapter path (e.g. a
  slurmrestd container with `auth/jwt`) today.
- The file provider is a deliberate interim measure: roots are
  allow-listed and absolute, not user-writable in production images;
  it carries no revocation or rotation semantics.
- When `openbao` lands, only the `Multi` dispatch table and cluster
  seed data change — no port or adapter changes.
