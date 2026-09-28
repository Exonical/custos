# ADR-020: Custos administers binding policy in slurmdbd

Status: Accepted  
Supersedes: ADR-019  
Date: 2026-09-27

## Context

Custos now materializes binding policy in Slurm as well as enforcing it at
admission. This gives Slurm a backstop for jobs submitted outside Custos, but
moves privileged write capability into the control plane and can affect
site-owned Slurm accounting state.

## Decision

Custos reconciles policy through the slurmrestd `/slurmdb/v0.0.45` API. The
current write adapter is v0.0.45; clusters on the read-only v0.0.44 adapter
must select report mode until write parity is implemented. The platform configuration `slurm.policy_management` defaults to `enforce`;
platform administrators can set each cluster to `inherit`, `enforce`, or
`report`, with `inherit` resolving to the configured default. Report mode is
the escape hatch and performs reads and drift reporting only.

For each enabled binding, enforce mode reconciles the described account and
service-user associations exactly. It creates a missing account, configures
per-partition service-user associations with the binding's exact QoS allow-list
and default QoS, removes extra associations for that service user, and
materializes active hard allocations as association `GrpTRESMins`. Slurm's
`IsDefault` service-user association is an exception: it grants all partitions
and cannot be deleted, so Custos retains it and reports
`DEFAULT_ASSOCIATION_RETAINED`. Sites that require native partition restriction
must give the service user a different default account. Unknown
site QoS and partitions are never created; affected association writes are
skipped and the corresponding drift remains visible.

This full-reconcile authority includes pre-existing site-created objects when
they are part of a binding's service-user association model. **Associations
for other users on the account are outside the binding model and are never
read-modify-written or deleted by Custos.** `slurm_managed_objects` records
only accounts and associations Custos created. Unbinding cleanup may delete
only those recorded objects; a managed account is deleted only after a fresh
slurmdbd read confirms that no associations remain on it.

| Object | Ownership and reconcile rule |
| --- | --- |
| Missing bound account | Custos-created; set description, organization `custos`, and parent; record ownership; delete on unbind only when association-free |
| Pre-existing account | Site-created; leave account metadata intact; reconcile only the binding's account/service-user associations |
| Binding service-user association | Reconcile exact allowed partition/QoS/default QoS even if pre-existing; record only when Custos creates it; delete extra entries for an active binding and clean up recorded entries on unbind |
| Slurm default service-user association | Never delete `IsDefault`; it grants all partitions. Retain it, report `DEFAULT_ASSOCIATION_RETAINED`, and require the site to assign a different default account for native partition restriction |
| Account-level association (`user` empty) | Reconcile only supported `GrpTRESMins` (including clearing a site-set unit limit when no hard allocation is active); preserve QoS/default QoS and parent/comment; record only when Custos creates it; do not delete a site-created association on unbind |
| Other users' associations | Site-owned and never touched |
| QoS and partitions | Site-owned definitions; read-only inputs, never created or deleted by Custos |

A single run applies at most 200 planned operations; remaining operations are
left for the next run. Every attempted operation is audited, and every
successful operation includes before/after state. Slurm authorization failures
stop further writes in the run and surface `PERMISSION_DENIED`; unavailable
slurmdbd never clears prior drift. Cluster bindings and allocation mutations
enqueue the existing deduplicated `policy.sync` item.

Hard budgets map to `GrpTRESMins`: CPU-hours to `cpu`, GPU-hours to
`gres/gpu`, and node-hours to `node`, using `ceil(limit × 60)` and the minimum
of active hard allocations for a unit. When a binding has no active hard
allocation for a supported unit, enforce mode clears that TRES limit even if it
was set by a site administrator; this is a deliberate consequence of full
reconcile. Clearing an unused supported TRES sends count `-1`. Slurm's group-minute accounting is subject to its own
`UsageResetPeriod`/decay semantics; it counts from the Slurm reset window, not
Custos `period_start`, and can therefore deny earlier than Custos. Custos
admission remains authoritative for the configured allocation period. Slurm
limits are a backstop for submissions that bypass Custos.

## Privilege boundary and mitigations

The e2e stack verified on Slurm **26.05.4** that the configured service
identity with `AdminLevel=Operator` can create accounts, write associations
(including `GrpTRESMins`), and remove the managed records. Its bootstrap grants
that level via `sacctmgr -i modify user name=custos set AdminLevel=Operator`.
Sites must restrict the slurmrestd network path to Custos. The deployment can
select `report` globally or per cluster to disable writes without losing drift
visibility. Managed-object ownership, an operation cap, transition/error
metrics, and per-operation audit records constrain and expose the impact of
write authority.

## Consequences

- Custos owns desired state only for accounts/bindings and the dedicated
  service-user associations described above; site definitions for QoS,
  partitions, and other users remain site-owned.
- Existing service-user association drift is corrected in enforce mode,
  even when a site operator created that association.
- Slurm-native limits complement rather than replace Custos period-aware
  admission and accounting.
- A site can opt into report mode during rollout or incident response while
  retaining the same periodic chain and dry-run plan endpoint.
