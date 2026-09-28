# ADR-019: Policy sync is read-only drift detection

Status: Superseded by ADR-020
Date: 2026-09-27

## Context

Custos binds projects to Slurm accounts, partitions, and QoS values. Those
values are administered by the HPC site, while Custos is a control plane and
must not silently become an administrator of slurmdbd policy.

## Decision

`policy.sync` only reads the cluster capability snapshot and slurmdbd accounts,
associations, and QoS. It records binding drift (`ok`, `drift`, `unknown`) and
emits transition audit events. It never writes accounts, associations, or QoS
to Slurm. Operators and site administrators reconcile mismatches manually.

A future decision to write slurmdbd state would require a separate ADR and an
explicit per-cluster opt-in; it is not part of this milestone.

## Consequences

- Periodic checks and an operational trigger improve visibility without
  transferring ownership of site accounting configuration to Custos.
- Slurm accounting unavailability marks the check `unknown` and preserves the
  previous drift list; it never falsely clears a known mismatch.
