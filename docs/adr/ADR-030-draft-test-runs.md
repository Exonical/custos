# ADR-030: Draft workflow test runs

Status: Draft  
Date: 2026-10-04

## Context

Workflow authors need to exercise a draft through the real execution engine
before publishing it. A test run is not a dry run: it creates ordinary task
executions and Slurm jobs, and must use the same authorization, policy,
allocation, rate-limit, audit, and accounting paths as a normal execution.
The version must remain immutable for the lifetime of the test so the queued
execution cannot observe a spec different from the hash it captured.

## Decision

- `POST /workflow-executions` accepts optional `test` (default `false`). A
  normal execution uses only a published version and requires
  `workflow.execute`. A test execution requires `test: true`, an explicit
  draft version, and both `workflow.create` and `workflow.execute` on the
  project. Published or deprecated versions are not accepted for a new test
  run; a draft version is not accepted for a normal run.
- The execution persists `is_test`; the API exposes it as `test` on execution
  detail/list responses. The list endpoint may filter with `test=true|false`;
  omitting the filter returns both kinds.
- While a non-terminal test execution references a draft, spec updates fail
  with `409 DRAFT_LOCKED` and identify an active execution. Layout updates do
  not change the spec and remain allowed. Publishing a locked draft is
  permitted.
- Test creation and draft-spec updates lock the same `workflow_versions` row
  inside their transactions. Test creation checks that the version is still
  a draft and that its stored hash matches the hash captured by the request
  before inserting the execution. Draft updates check for an active test
  after acquiring the row lock. This serializes the hash capture/insert
  against spec updates.
- The engine always verifies the pinned `spec_hash`. Normal executions require
  `published`; tests may continue against the same hash while the version is
  draft, published, or deprecated. This permits an author to publish or
  deprecate a draft while an already-pinned test is still running without
  changing its execution semantics.
- Retry attempts remain part of the same test execution and use the same
  pinned version/hash. There is no separate execution-rerun endpoint.

## Alternatives considered

- **Run draft specs without marking them:** rejected because execution lists,
  lifecycle locks, and audit records could not distinguish these runs.
- **Treat a test run as validation-only:** rejected because authors need the
  real admission and scheduler path; validation remains separately available
  without creating an execution.
- **Block publishing while a test is active:** rejected. The test is pinned
  to its hash, and the engine accepts that hash in draft, published, or
  deprecated state; publishing does not mutate the spec.
- **Lock only in service code:** rejected because concurrent save and execute
  requests could interleave between reading the hash and inserting the test
  execution. Both transactions lock the version row.

## Consequences

- A test run may consume real cluster resources and incur accounting charges.
  It is subject to normal resource policies and allocations.
- Active test executions temporarily prevent draft spec edits. Authors can
  cancel a test or wait for it to become terminal before editing.
- Historical normal executions retain `is_test = false` through the database
  default. Test-run state and the captured spec hash remain available for
  audit and troubleshooting.
