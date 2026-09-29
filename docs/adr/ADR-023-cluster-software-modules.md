# ADR-023: Platform-curated software-to-module map per cluster

Status: Accepted  
Date: 2026-09-28

## Context

Workflow tasks declare structured `software` requirements, and the job
wrapper already emits `module load` lines from `ExecutionSpec.Software[].ModuleSpec`.
Nothing populated `ModuleSpec`: the synced `software_catalog` described in
`docs/script-validation.md` was never built, so requirements were accepted
and silently ignored. Tasks with `launch: srun` (including the legacy
`type: mpi` alias) run their payload inside an srun step; the wrapper loads
site modules before launch rather than putting module names into the user
payload. Positive `resources.tasks` adds `--ntasks=N`; otherwise srun
inherits the allocation (see ADR-025).

## Decision

- Each cluster carries a platform-admin-curated `software_modules` list
  (`clusters.software_modules` JSONB), set through `POST/PATCH /clusters`.
  An entry maps `name` (and optionally `version`) to 1-16 module names.
- Admission resolves every task requirement against the placement
  cluster's list: an exact `name@version` entry wins, otherwise a
  version-less entry for the name matches any version. The resolved
  modules are frozen into `ExecutionSpec.Software` (and so into the spec
  digest) and emitted by the wrapper in declaration order before launch.
- Unresolvable requirements fail closed with `SOFTWARE_UNAVAILABLE`.
- Names, versions and module strings are validated against conservative
  character classes at configuration time; the wrapper still single-quotes
  them. Users never supply module strings (TM-35).

## Alternatives considered

- **Synced catalog from Lmod/Spack exports** (the original plan): richer,
  but needs a site manifest format and sync worker. The admin-curated list
  is its first, manual source; a later sync can populate the same field.
- **Module loads inside script payloads**: repeats setup inside each
  srun-launched rank and puts site module names into user documents.
- **Accept and ignore unknown software**: the prior behavior; jobs failed
  far from the cause.

## Consequences

Workflows that declare `software` now fail admission on clusters without
a matching entry, which is the intended fail-closed behavior. Sites must
curate the list; the `software` requirement becomes portable across
clusters while module names stay site-specific.
