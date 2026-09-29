# ADR-024: Built-in workflow template catalog

Status: Accepted  
Date: 2026-09-28

## Context

Users coming from sbatch scripts need starting points for common job
shapes (OpenMP, multi-process, MPI, hybrid, per-core, GPU, long-running).
Custos documents never contain `#SBATCH` directives; scheduler settings
live in structured `resources`, and site values (account, partition,
QoS, modules) come from bindings and cluster configuration.

## Decision

- Templates are YAML files embedded in the binary
  (`internal/workflowtemplates/catalog/*.yaml`), each with metadata
  (`id`, `title`, `summary`, `description`, `order`, optional `tags`) and
  a complete `custos.io/v1alpha1` workflow document.
- Every template is a workflow template; there are no template types or
  categories. `tags` is a free-form `key: value` map (empty by default)
  so users can later label templates without a fixed taxonomy.
- Every template is decoded strictly and passes static validation
  (steps 1-4) at load; the package tests fail on an invalid template.
- `GET /api/v1/workflow-templates` and `/workflow-templates/{id}` expose
  the catalog to any authenticated principal. Using a template is an
  ordinary `createWorkflow` + `createWorkflowVersion` under the caller's
  permissions; the engine never reads templates.
- Templates stay site-neutral: account, partition and QoS come from the
  project binding; software is declared structurally and resolved through
  the cluster's module map (ADR-023); MPI ranks launch via `srun`.

## Alternatives considered

- **Frontend-only catalog**: no API change, but invisible to the CLI and
  other API clients and not validated by the backend pipeline.
- **Database-backed, tenant-editable templates**: the likely next step;
  deferred until tenants need their own catalogs.

## Consequences

Adding or changing a template is a code change reviewed with the tests.
Tenant- or site-specific templates need a later, storage-backed design.
