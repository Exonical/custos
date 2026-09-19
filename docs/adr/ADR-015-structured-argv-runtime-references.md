# ADR-015: Structured argv with allow-listed runtime references in ExecutionSpec

Status: Accepted
Date: 2026-09-19

## Context

`command` tasks (ADR-005) need arguments that reach Slurm without ever
becoming shell text: argv is interpolated through the restricted
expression language, and the generated wrapper must never interpolate
raw user text into an evaluable position. Array tasks additionally need
a value that does not exist at admit time — the per-element task index —
which `sbatch` exposes as `$SLURM_ARRAY_TASK_ID`. Rendering that into a
literal string is wrong (the placeholder must survive into the runtime
environment), yet allowing arbitrary `$VAR` text in argv would reopen
the injection surface the ExecutionSpec design closed.

## Decision

`admission.ExecutionSpec` carries `Argv []ArgvElement` where each
element is exactly one of `{Literal string}` or `{Runtime string}`, and
`Runtime` is allow-listed to the single value `SLURM_ARRAY_TASK_ID` —
what `{{ array.taskId }}` renders to. The same shape applies to
environment values (`EnvSet.Runtime`). `admission.Build` rejects any
other runtime name and any element with both or neither field set;
static spec validation rejects `{{ array.taskId }}` anywhere except as
the entire argv element / env value (`REF_ARRAY_RUNTIME_WHOLE`). In the
wrapper, literals are emitted single-quoted through `q`; a `Runtime`
element is emitted as the Custos-authored token `"$SLURM_ARRAY_TASK_ID"`
— never user text. The digest covers `Argv`; command-mode tasks exec
`[srun --ntasks=N] <argv>` with no payload block, and `Payload` is
optional in the spec (`jobs.script_digest` is nullable) — the job worker
fails closed if both payload and argv are absent.

## Alternatives considered

- String argv with a placeholder: `{{ array.taskId }}` →
  `"$SLURM_ARRAY_TASK_ID"` as literal text is indistinguishable from
  user-typed `$SLURM_ARRAY_TASK_ID`, so quoting can't tell them apart —
  the tag has to be structured.
- Free-form runtime-variable references: an allow-list of one keeps the
  invariant "no unquoted user text in the wrapper" mechanically
  checkable (the tests re-parse the generated script and assert every
  exec-line word is a single-quoted literal or the exact runtime token).
- Pre-materializing array values into N literal argv lists: loses the
  single `sbatch --array` submission and its atomic scheduling.

## Consequences

`ArgvElement` has exactly two fields (reflection-tested). Any future
runtime reference (e.g. `$SLURM_JOB_ID`) is a deliberate, reviewable
allow-list addition plus spec-validation change, not a syntax change.
Cross-cutting: `workflowspec/expr` rendering, `admission.Build`,
`submission` wrapper, and the M5 engine's task snapshot all agree on the
same two-shape representation.
