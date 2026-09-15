# ADR-006: Slurm adapter — consumer-owned ports over Slinky generated clients

Status: Accepted
Date: 2026-09-14

## Context

The SlinkyProject/slurm-client module (Apache-2.0, SchedMD) has two
layers: a controller-runtime-style `pkg/client` wrapper that only covers
Node/Job (plus ping/partition/stats in some versions), and fully
generated OpenAPI clients `api/v0042`–`api/v0045` including slurmdbd
accounting endpoints (docs/slurm.md). The wrapper's object coverage is
insufficient (accounting must use the generated clients, whose types
churn per Slurm version), and the module is 0.x — "may evolve
aggressively". Custos must support multiple slurmrestd versions per
registered cluster and keep Slurm churn out of domain code.

## Decision

- `internal/slurm` defines **neutral types** and two consumer-owned
  ports: `slurm.Cluster` (ping, capabilities, submit/get/list/cancel
  jobs, nodes, partitions, reservations) and `slurm.Accounting`
  (associations, accounts, QoS, windowed job records). They are separate
  interfaces because they target different daemons (slurmctld vs
  slurmdbd), fail independently, and accounting is optional on some
  sites. A `Factory` opens both for a registered cluster, resolving
  credentials through `secrets.Resolver` at call time.
- Nothing outside `internal/slurm/slinky` imports Slinky or generated
  types. One sub-package per supported slurmrestd version:
  `internal/slurm/slinky/v0045` (primary, Slurm 26.05) and `v0044`
  (Slurm 25.11), each implementing the port with that version's generated
  client. `Factory.Open` picks by `cluster.api_version` (discovered via
  `/openapi/v3` or configured).
- The Slinky wrapper's **cache/informer layer is NOT used** — Custos owns
  persistence and reconciliation; a second cache with different staleness
  rules would confuse state.
- Correctness is pinned by a **conformance test suite**
  (`internal/slurm/conformance`) running the same tests against every
  adapter version and the in-memory `internal/slurm/fake`, with recorded
  slurmrestd JSON fixtures from real clusters.
- Cluster credentials and JWT minting stay in OpenBao; TLS CA pinning per
  cluster, optional mTLS, no `InsecureSkipVerify`; SSRF protections on
  registered endpoints.

## Alternatives considered

- **Use the Slinky wrapper client directly** — rejected: covers only
  Node/Job, embeds a cache we don't want, and leaks its types into domain
  code at the exact boundary that churns.
- **Hand-written HTTP client to slurmrestd** — rejected: reimplements
  what the generated clients already cover correctly (including slurmdb),
  at high maintenance cost.
- **One combined interface for ctld+dbd** — rejected for the sketch's
  single interface: different daemons, failure modes, latency, and
  optionality argue for two ports.

## Consequences

- Version mapping code is "mechanical and boring on purpose"; adding a
  new slurmrestd version is a new sub-package plus fixtures.
- The conformance suite and fake are mandatory infrastructure — all
  job/workflow/worker tests run against the fake; real-slurmrestd
  integration runs in CI nightly/optional via a containerized Slurm.
- Neutral types (`JobSubmission`, `Job`, `JobID`, ...) are the stable
  contract the rest of the codebase builds on.

Source: docs/slurm.md (all sections); docs/architecture.md §10.
