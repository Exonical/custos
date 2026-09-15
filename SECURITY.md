# Security Policy

## Supported versions

None yet. Custos has no released versions (Milestone 0 — architecture).
This policy will be updated when the first release ships.

## Reporting a vulnerability

**Do not file public GitHub issues for security vulnerabilities.**

Report privately via GitHub Security Advisories at
`https://github.com/<org>/custos/security/advisories/new`
(placeholder — the org path will be finalized when the repository is
published).

Please include: affected component, reproduction steps or scenario,
potential impact, and whether you believe tenant isolation, credentials,
or the execution plane are affected.

## Scope

Custos is a **control plane**: it brokers authenticated, authorized
requests to Slurm clusters and stores metadata, references, usage, and
audit records. The execution plane (compute nodes, user jobs) is treated
as hostile by design — secrets delivered to jobs are scoped, short-lived,
and audited.

In scope for reports:

- Tenant isolation bypasses (cross-tenant read/write, enumeration)
- Authentication/authorization flaws (JWT confusion, authz bypass)
- Secret handling (leakage via logs, audit, errors, or job delivery)
- Command/option injection into Slurm submissions, SSRF via cluster
  endpoints
- CSRF/XSS in the BFF/UI

Out of scope: vulnerabilities in Slurm, OpenBao, or the OIDC provider
themselves; issues requiring already-trusted platform-admin compromise;
compute-node-level isolation that belongs to site Slurm configuration.

## Design reference

The security model, assets, threat actors, and mitigations are documented
in [docs/threat-model.md](docs/threat-model.md) — a living document
revisited before each milestone that adds attack surface. Residual risks
operators must accept are listed there too.
