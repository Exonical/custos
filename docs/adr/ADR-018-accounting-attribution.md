# ADR-018: Accounting attribution

Status: Accepted
Date: 2026-09-20

## Context

Slurm accounting contains jobs submitted both through Custos and outside it.
Custos must retain complete cluster/account usage while exposing tenant data
only when attribution is unambiguous. Slurm usernames are not yet mapped to
Custos users.

## Decision

Each ended Slurm job becomes one immutable `usage_records` fact. Attribution is
applied in this order:

1. Match `jobs(cluster_id, slurm_job_id)`. Copy that job's tenant, project,
   creator, and Custos job UUID, and persist its derived `resource_usage`.
2. Otherwise, if exactly one enabled `ProjectClusterBinding` on the cluster has
   the record's Slurm account, copy that tenant and project; leave user NULL.
3. Otherwise leave tenant, project, user, and job NULL. Ambiguous accounts use
   this rule and increment the bounded `ambiguous_account` metric.

The raw Slurm username is always retained. NULL-tenant facts are visible only
under platform database scope. Daily aggregates are fully recomputed whenever
a late record marks a `(cluster, day)` dirty; they are never incrementally
patched.

## Consequences

- Direct-to-Slurm activity contributes to cluster/account totals without being
  incorrectly assigned to a user.
- Shared Slurm account names must be disambiguated operationally before their
  usage is tenant-visible.
- Adding an explicit Slurm-user mapping later can introduce a new attribution
  rule without rewriting immutable facts; dirty days can be reaggregated.
- Custos job rows gain value-free derived resource usage for job detail views.
