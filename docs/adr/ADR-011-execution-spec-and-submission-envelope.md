# ADR-011: ExecutionSpec as the sole source of policy-sensitive Slurm fields

Status: Accepted
Date: 2026-09-14

## Context

User-authored workload scripts ("payloads") are untrusted input that ends
up inside a batch script handed to Slurm. Historically, `#SBATCH`
directives in a script are how resources are requested — if Custos passed
payloads through, a user could bypass every policy check (GPUs, QoS,
accounts, partitions, walltime) by writing directives directly (TM-28 in
docs/threat-model.md). The invariant in docs/script-validation.md: every
policy-sensitive attribute sent to Slurm MUST originate from a validated,
persisted Custos `ExecutionSpec`, never from script contents.

## Decision

- **Immutable `ExecutionSpec`**: all policy-sensitive Slurm fields
  (account, partition, QoS, reservation, resources, placement,
  environment classes, security context) come from an `ExecutionSpec`
  produced by a fixed admission pipeline — authn → authz → ResourcePolicy
  → entitlement → allocation → admission → placement — then frozen with a
  SHA-256 digest over its canonical JSON and persisted on
  `task_executions` (never updated). `slurm.JobSubmission` is a pure
  function of `ExecutionSpec`; the pipeline never receives script bodies,
  only `ScriptRef{ID, Digest, Language}`.
- **Content-addressed payloads**: scripts are stored tenant-scoped keyed
  by SHA-256 of exact bytes; tasks reference `script: {ref: sha256:…}`;
  the immutable `WorkflowVersion` spec hash covers every digest.
- **Directive rejection**: `#SBATCH` (and `#PBS`/`#$`/`#BSUB`/`#COBALT`)
  directives in payloads are rejected by default as `SECURITY_VIOLATION`
  by `sbatchscan`, a parser-based canonicalizing scanner (mvdan/sh AST +
  line fallback + shell-word tokenizer + generated option table).
  Opt-in `allowLegacySbatchImport` converts recognized directives into an
  ordinary resource *request* that still passes policy, with the payload
  rewritten to neutralized comment lines.
- **Trusted wrapper**: Custos generates the batch script; it contains no
  `#SBATCH` lines, embeds the payload as base64 behind a nonce-checked
  heredoc, verifies it with `sha256sum -c` on the node, and executes it
  as a file via an interpreter enum — payload text is never interpolated.
- **Explicit, classified environment**: nothing is inherited; env vars are
  controlled / generated / secret-injected / filtered / user-configurable,
  with `--export=NONE` semantics.
- **Severity floor**: `POLICY_VIOLATION` and `SECURITY_VIOLATION` always
  block; they cannot be disabled, downgraded, or overridden by users or
  tenant admins.

## Alternatives considered

- **Trust or parse `#SBATCH` as the resource source** — rejected: puts
  the untrusted payload in charge of policy fields; parsing to "honor"
  them makes every tokenizer bug a policy bypass.
- **Strip directives silently** — rejected: silently changing user input
  hides intent and breaks reproducibility; rejection/import is explicit
  and audited.
- **Embed payload text directly in the wrapper** — rejected: heredoc and
  quoting collisions become injection vectors; base64 makes the payload
  inert and enables the runtime digest check.
- **Single monolithic validator** — rejected: the pipeline separates
  parse, static lint, directive scan, env, software, and policy stages so
  each is testable and versioned independently.
- **Frontend validation as authoritative** — rejected: Monaco diagnostics
  are usability only; all enforcement is backend-side.

## Consequences

- **Custos policy is not a sandbox.** Static scanning cannot stop a
  hostile payload from running `sbatch`/`salloc`/`srun` inside a job;
  "consume only what you were granted" is enforced by native Slurm limits
  — associations/QoS limits, cgroup constraints, `job_submit` plugins —
  which remain required for hostile-workload sites (TM-31/TM-32).
  `policy.sync` mirrors Custos limits into slurmdbd associations (M7+).
- Integrity is checked three times: publish (validation ↔ digest),
  admission (spec ↔ digest), runtime (bytes ↔ digest on the node).
- Every new scheduler field must enter via the `ExecutionSpec` builder and
  the sbatchscan option table — both are test-pinned (bypass corpus,
  invariant/property tests).
- Import mode is a reviewed, audited transformation
  (`workflow.script.import`), never automatic.

Source: docs/script-validation.md (invariant, pipeline, sbatchscan,
ExecutionSpec, wrapper, environment); docs/threat-model.md TM-28–TM-32;
docs/workflows.md (ADMITTING state); docs/workers.md (`task.admit`).
