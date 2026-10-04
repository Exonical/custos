# ADR-031: Slurm-native service tasks

Status: Draft  
Date: 2026-10-04

## Context

Some workflows need a long-running service, such as a database, to be
available while later Slurm jobs run. Custos must preserve its engine-driven
DAG and failure policy while letting Slurm order dependent jobs after a
service starts. Custos does not have a service readiness probe and must not
pretend that job start proves application health.

## Decision

- A task with `service` is a service task and must request `resources.walltime`.
  It cannot use arrays, fan-out, retry, `when`, or outputs, and cannot be a
  condition or approval task. Images and multinode remain valid.
- Normal task dependencies still wait for terminal state. A dependency on a
  service is satisfied once its Slurm job ID is persisted (the task is
  QUEUED/RUNNING or later). Custos submits the dependent with
  `--dependency=after:<service-job-id>`, so Slurm makes it eligible after the
  service job starts. There are no Custos readiness probes. If a service
  fails before dependent submission, the dependent is canceled with
  `DEPENDENCY_FAILED` by default; `onDependencyFailure: run` submits without
  the `after` dependency. A service failure after dependent submission does
  not cause the engine to cancel that dependent; normal execution failure
  policy still applies.
- The service and dependent must resolve to the same Slurm cluster. Static
  validation rejects differing explicit placements with
  `SERVICE_CLUSTER_MISMATCH`; admission compares their resolved cluster IDs
  again before freezing the dependent. Foreign-cluster job IDs are never
  submitted as Slurm dependencies or exposed in service environment variables.
- Each service dependency contributes controlled environment variables
  `CUSTOS_SERVICE_<NAME>_JOBID` and `CUSTOS_SERVICE_<NAME>_HOST`. NAME is the
  uppercased service task name with non-`[A-Z0-9]` characters replaced by
  underscores; collisions are rejected. The Slurm job ID is validated as
  numeric before being frozen. The wrapper resolves HOST best-effort from
  `squeue` and `scontrol` when the dependent starts. The variables are
  reserved from user overrides and included in container environment
  forwarding.
- `autoStop` defaults to true. Custos requests cancellation once every direct
  dependent is terminal, or when all non-service tasks are terminal. The
  resulting Slurm cancellation maps to task COMPLETED with
  `SERVICE_STOPPED`; a service stopped before submission completes with that
  reason without creating a Slurm job. A service with no dependents and no
  non-service tasks runs until it exits or reaches walltime. With `autoStop: false`, service
  jobs run until their own exit/walltime; Slurm TIMEOUT maps to task COMPLETED
  with `SERVICE_WALLTIME`. An independent nonzero exit remains a task failure.
- Workflow execution cancellation continues to cancel services through the
  ordinary job-cancel path. Service stops initiated by the engine emit
  `workflow.execution.service_stopped`.
- Sbatch export emits service tasks as ordinary scripts with a comment that
  the Custos service lifecycle is not represented. Dependency behavior is
  already not represented by standalone export.

## Alternatives considered

- **Poll service ports or run readiness probes:** rejected; application
  protocols and endpoints are not part of the workflow contract.
- **Wait for the service job to complete before dependent work:** rejected;
  it would defeat the purpose of a long-running service.
- **Use `afterok` for service dependencies:** rejected; dependents should
  become eligible when the service job starts, not when it exits successfully.
- **Cancel every service when all dependent jobs finish:** rejected for
  `autoStop: false`; explicit opt-out allows services to finish naturally or
  reach their declared walltime.

## Consequences

- Slurm owns the start ordering for service-dependent jobs, while Custos owns
  dependency readiness, failure policy, service lifetime decisions, and
  execution settlement.
- `CUSTOS_SERVICE_<NAME>_HOST` is best-effort and may be empty if Slurm no
  longer lists the service job. Users needing health checks must implement
  them in their task logic.
- Slurm job IDs and service environment names become part of each dependent
  task's immutable `ExecutionSpec` and its derived structured submission.
- Submit/cancel races are fenced after a successful Slurm submission: if a
  canceled job row wins the persistence race, Custos immediately cancels and
  audits the Slurm job. Delayed name/comment-verified sweeps at 30 seconds and
  five minutes cover process-crash windows where the submit result was never
  persisted.
