# ADR-017: Tenant secret connectors

Status: Accepted
Date: 2026-09-18

## Context

ADR-003 makes the platform OpenBao the default secret store, but tenants may
already operate a separate OpenBao-compatible manager. Custos must support that
without storing connector credentials or allowing tenant-controlled URLs to
become an SSRF path.

## Decision

A tenant secret reference names a `SecretConnector`; platform-owned cluster
credentials continue to use the platform `file` or `openbao` provider directly.
When platform OpenBao is configured, tenant creation idempotently provisions a
`default` connector of kind `platform-openbao`, its tenant namespace, KV mount,
and runtime policy. The only bring-your-own kind in M6 is `openbao`; future
cloud secret-manager kinds are additive factory implementations.

Connector configuration in PostgreSQL is non-secret. Its login credential is
written directly to platform OpenBao and PostgreSQL stores only a platform
secret reference. Connector responses never echo supplied credentials.
Connectors are cached by connector id and optimistic-concurrency version and
are invalidated on update or deletion.

Every bring-your-own endpoint uses the shared `platform/safehttp` transport:
TLS 1.3, pinned tenant CA when supplied, redirects disabled, all resolved
addresses vetted before dialing, and dialing by the vetted address to prevent
DNS rebinding. Cloud metadata, link-local, loopback, and disallowed private
addresses fail closed. Slurm endpoints use the same transport.

## Consequences

- Bring-your-own connectors require platform OpenBao; otherwise Custos returns
  `PLATFORM_SECRETS_REQUIRED` because it has nowhere safe to store credentials.
- Connector deletion is restricted while references exist.
- Custos checks connectivity and authentication when a connector is created,
  updated, or explicitly tested.
- Tenant isolation for the default connector retains ADR-003's per-request,
  one-use child-token boundary.
