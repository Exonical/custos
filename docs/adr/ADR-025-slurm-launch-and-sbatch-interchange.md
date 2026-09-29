# ADR-025: Slurm launch model and sbatch interchange

Status: Draft  
Date: 2026-09-28

## Context

Slurm workload execution has two distinct layers: `sbatch` allocates
resources and starts one batch script; `srun` starts a step inside that
allocation. Inferring `srun` from `resources.tasks > 1` is incorrect: a
legacy sbatch script can request many tasks and itself launch MPI (for
example through `mpirun`), so an inferred srun would repeat the imported
script once per rank.

Users also need a safe path to bring existing sbatch scripts into Custos
and to take an individual workflow task back to a portable sbatch script.
Neither interchange operation should create or publish a workflow on the
user's behalf, and scheduler-controlled values must continue to originate
from structured workflow fields rather than arbitrary script directives.

## Decision

### Launch semantics

- A Slurm-backed task has an optional `launch` field: `sbatch` or `srun`.
  Omitted launch means `sbatch`; only an explicit `srun` runs the payload
  under an srun step.
- Legacy task `type` values (`batch`, `mpi`, `gpu`, `array`) remain accepted
  for stored versions. `EffectiveLaunch` maps legacy `type: mpi` with no
  explicit launch to `srun`; other legacy aliases use `sbatch`. New specs
  omit `type` except for `shell` and `condition`.
- Decode, canonicalization, and storage do not normalize these legacy
  fields. The `launch` field is `omitempty`, so adding it does not change
  the canonical bytes or hash of an existing spec. The resolved launch is
  copied into the immutable `ExecutionSpec` at admission.
- The wrapper uses the frozen launch value. An empty launch in an already
  persisted legacy `ExecutionSpec` retains the historical `tasks > 1`
  srun behavior; ad-hoc submissions explicitly use `sbatch`. Srun without
  a positive `tasks` value inherits the Slurm allocation.

### Sbatch import and export

- `POST /workflow-imports/sbatch` accepts 1-20 bounded scripts and returns
  an independently assembled, statically validated workflow proposal. It
  stores rewritten payloads by digest, but does not create a Workflow or
  WorkflowVersion. The existing per-task import and ad-hoc import endpoints
  remain proposal/gating compatible.
- Legacy import is enabled by the built-in policy default. Tenant and
  cluster policy can still disable it. Unmappable directives remain
  `CUSTOS201` errors in the per-file diagnostics. A parsed resource value
  that cannot be represented, such as `--mem-per-cpu`, is omitted only with
  a field-scoped `CUSTOS202` warning.
- Import audit records contain filenames, before/after digests, task names,
  and imported fields, never script bytes.
- A task export endpoint renders a portable sbatch script. Account is
  intentionally site-managed. Workflow-only behavior that cannot be
  represented in one script is documented in comments, while unsupported
  expressions or task kinds fail with `EXPORT_UNSUPPORTED` rather than
  being silently dropped.

## Alternatives considered

- **Continue inferring launch from task count:** rejected because the count
  is an allocation request, not an instruction to run the executable once
  per task.
- **Rewrite imported scripts or create a workflow automatically:** rejected;
  the importer returns a proposal so the user can review diagnostics and
  then use the ordinary workflow/version creation flow.
- **Submit arbitrary sbatch scripts directly:** rejected because it would
  bypass structured admission and the `ExecutionSpec` invariant.

## Consequences

- Old published workflow specs keep their hashes and legacy `mpi` execution
  behavior. New authors can separately express allocation shape and launch
  behavior.
- `--mem-per-cpu` has no equivalent in the current `TaskResources` schema;
  import reports the loss rather than inventing a memory conversion.
- Export is intentionally partial: dependency scheduling, conditions,
  fan-out, retries, and secret values are not represented by a single
  sbatch script. Unsupported templating fails closed.
- Future launchers and container runtimes should compose around the
  explicit `EffectiveLaunch` decision rather than infer behavior from
  resource counts.
