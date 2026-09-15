# ADR-005: Own declarative workflow format `custos.io/v1alpha1`

Status: Accepted
Date: 2026-09-14

## Context

Custos needs a workflow representation that is validatable end-to-end,
auditable, expresses a DAG of Slurm-bound tasks, and is editor-friendly
for the UI's visual/YAML dual view (docs/workflows.md). Security
constraints (TM-05, TM-17) forbid free-form code and demand strict
validation of references, resources, and expressions. UI, CLI, and
backend must share one schema to prevent drift.

## Decision

Define our own declarative YAML/JSON spec, `apiVersion:
custos.io/v1alpha1`, `kind: Workflow`:

- Authoritative definition: Go types in `pkg/workflowspec` (public, so
  CLI and UI share it) with a JSON Schema **generated from the Go types**
  (`pkg/workflowspec/schema/v1alpha1.json`); YAML is converted to
  canonical JSON before hashing/storage.
- `WorkflowVersion` is immutable once `published`; it stores the
  canonical spec, `spec_hash`, `schema_version`, and a **separate**
  `layout` JSON that the engine never reads. Editing a published version
  creates a new draft.
- Templating uses a **restricted expression language** (mustache-like
  `{{ }}` with dotted lookups, comparison/boolean operators, integer
  arithmetic for `when`/`fanOut.count`; no functions, loops, or
  string-to-code), implemented as a ~500-line hand-written parser in
  `pkg/workflowspec/expr` — the small grammar is a security feature.
  Substitutions only land in argv elements or env values, never shell
  text.
- Tasks are not materialized as a table; they live in the immutable spec
  (`TaskExecution` is the per-run row).
- Validation is always backend-side, returns all path-addressed errors,
  and resolves `secrets.*`/`cluster` references only within the caller's
  tenant scope.

## Alternatives considered

- **Free-form shell script per workflow** — rejected: unvalidatable,
  unauditable, no DAG.
- **CWL / WDL / Snakemake / Nextflow DSL** — rejected as the core format:
  each is a full language with its own runtime expectations; running them
  is a separate integration ("run a Nextflow pipeline as a task").
- **Argo Workflows CRD shape** — rejected: Kubernetes-flavored, too many
  Pod concepts.
- **General template engine** — rejected: a Turing-complete-ish template
  language cannot be statically validated the way the restricted grammar
  can, and invites injection.

## Consequences

- The spec is versioned (`v1alpha1`), so schema evolution is explicit;
  `schema_version` is stored per version.
- Native Slurm dependency batching vs engine-driven execution is an
  engine strategy detail (`spec.execution.strategy: auto|engine|native`),
  not a spec-format concern.
- The JSON Schema must stay generated from Go types — a CI freshness
  check is required to avoid drift between UI, CLI, and backend.

Source: docs/workflows.md (Objects, Specification, Templating,
Validation, Alternatives); docs/architecture.md §6/§10.
