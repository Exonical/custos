# Durable Work Queue and Reconciliation

## Why PostgreSQL

Requirements: leases, retries with backoff, attempt counts, dead-lettering,
idempotency, survive restarts, small team. Volume: thousands of items per
minute at the top end (tens of clusters × sync + a few submissions per
second). A `SKIP LOCKED` queue table handles this comfortably and keeps
enqueue transactional with domain state changes — the property that makes
"job row created but work item lost" impossible. Kafka/NATS/Redis would add
an at-least-once boundary *between* the DB and the broker that we would
then have to reconcile anyway. ADR-007.

## Schema

```sql
CREATE TABLE work_items (
    id            UUID PRIMARY KEY,
    kind          TEXT NOT NULL,             -- 'job.submit', 'job.reconcile', 'cluster.sync', ...
    key           TEXT NOT NULL,             -- dedupe key: kind-specific, e.g. 'job:<uuid>'
    tenant_id     UUID,                      -- nullable for platform work
    payload       JSONB NOT NULL,
    run_at        TIMESTAMPTZ NOT NULL,
    priority      SMALLINT NOT NULL DEFAULT 0,
    attempt       INT NOT NULL DEFAULT 0,
    max_attempts  INT NOT NULL,
    leased_until  TIMESTAMPTZ,
    leased_by     TEXT,                      -- worker instance id
    state         TEXT NOT NULL,             -- 'pending' | 'leased' | 'done' | 'dead'
    last_error    TEXT,                      -- redacted, bounded length
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX work_items_active_key ON work_items (kind, key) WHERE state IN ('pending','leased');
CREATE INDEX work_items_ready ON work_items (run_at, priority DESC) WHERE state = 'pending';
```

The partial unique index gives **enqueue-time deduplication**: enqueueing
`job.reconcile` for a job that already has a pending reconcile is a no-op
(`ON CONFLICT DO NOTHING`, optionally pulling `run_at` earlier).

**Self-rescheduling handlers** (`cluster.sync`, `job.reconcile`,
`jobs.sweep`, `idempotency.expire`) must return
`workqueue.RescheduleAt(t)` to schedule their next run. A handler's own
item is still `leased` while it runs, so an `Enqueue` with the same
`(kind, key)` dedupes to nothing and silently ends the periodic chain.
`RescheduleAt` instead moves the same row back to `pending` in place
(`run_at=t`, lease cleared, `attempt=0`, `last_error=NULL`), which never
touches the dedupe index; it is counted as `rescheduled`, not
`done`/`failed`. Cross-kind enqueue (`job.submit` → `job.reconcile`,
`task.admit` → `job.submit`, job transition → `execution.advance`) still
uses `Enqueue` — the target row is different, so dedupe is correct.

## Lease loop

```text
loop:
  BEGIN
  SELECT ... FROM work_items
   WHERE (state='pending' AND run_at <= now())
      OR (state='leased' AND leased_until < now())   -- expired leases
   ORDER BY priority DESC, run_at
   LIMIT $batch FOR UPDATE SKIP LOCKED;
  UPDATE ... SET state='leased', leased_by=$me, leased_until=now()+$lease, attempt=attempt+1
  COMMIT
  for each item (bounded concurrency per kind):
     ctx with deadline = leased_until - margin
     heartbeat goroutine extends leased_until every lease/3 while handler runs
     err := handler(ctx, item)
     switch:
       nil                    -> state='done'  (or DELETE; we keep 'done' rows 7 days for debugging, then purge)
       Retryable(err)         -> state='pending', run_at = now() + backoff(attempt) with full jitter
       Permanent(err)         -> state='dead', last_error
       attempt>=max_attempts  -> state='dead'
  sleep poll interval (250ms–2s, adaptive) — no LISTEN/NOTIFY (CRDB compat, simpler)
```

Backoff: `min(cap, base * 2^attempt) * rand(0.5, 1.0)`; per-kind `base`,
`cap`, `max_attempts`. Expired leases (`leased_until < now()`) are picked up
by the next `SELECT` because the predicate treats them as pending — a
crashed worker loses nothing.

Handlers are **idempotent by construction**: they read current domain state
inside a transaction, decide, and apply a guarded transition; they never
assume they are the first execution of that item.

Dead items are visible at `/api/v1/admin/work-items?state=dead` with a
`retry` action, and counted in Prometheus (`custos_work_items_dead{kind}`).

## Work item kinds (initial)

Implemented in M4-C: `job.submit`, `job.reconcile`, `job.cancel`,
`jobs.sweep`, `idempotency.expire` (plus `cluster.sync`,
`maintenance.partitions`, `tenant.delete` from earlier slices). M5-A
moved script validation into the durable pipeline
(`internal/validation/pipeline`): submissions run concurrent validators
(panic/timeout → `CUSTOS900`, fail closed) under the effective
`ValidationPolicy`, persist a `script_validations` row, and store its ID
on `jobs.script_validation_id`. External ShellCheck runs via the
loopback-only `custos-validator` sidecar (127.0.0.1:8481). M5-C adds the
execution engine handlers: `execution.advance` (DAG evaluation, unblock/
skip/retry/cancel, execution terminal accounting) and `task.admit`
(validation currency → `ExecutionSpec` freeze → linked `jobs` row +
`job.submit`, all in one transaction). Task-linked job transitions also
enqueue `execution.advance` from inside the jobs worker. The rest lands
with the workflow milestones.

| Kind | Trigger | Handler outline |
| --- | --- | --- |
| `job.submit` | job admitted (ad-hoc jobs go through the same admission step) | reconcile-by-name first; build `JobSubmission` from the persisted `ExecutionSpec`; resolve credentials; `SubmitJob`; persist `slurm_job_id`; transition to QUEUED; enqueue `job.reconcile` |
| `job.reconcile` | after submit; periodic while non-terminal; on demand | `GetJob`; map state; guarded transition; reschedule with interval growing 5s→60s (age-based); if Slurm says "unknown job" and accounting has a record → terminal from accounting; if unknown everywhere for >10 minutes after submit → FAILED (`LOST`) |
| `job.cancel` | cancel request | `CancelJob` (`ErrNotFound` = done); SUBMITTING jobs go CANCELED directly and the submit handler skips them; enqueue reconcile ≈+2s |
| `jobs.sweep` | periodic per cluster (60s) | `ListJobs(name prefix custos-)` once; bulk-reconcile all non-terminal jobs on that cluster; jobs absent for >10min → FAILED (`LOST`) (see cadence below) |
| `task.admit` | task READY | verify script digest ↔ `ScriptValidation` currency (enqueue `script.validate` if stale, requeue self); build + persist `ExecutionSpec`; transition `ADMITTING → SUBMITTING`; enqueue `job.submit` |
| `script.validate` | validate endpoint (async for large scripts), publish, stale validation at admission | run validator pipeline (external tools via sidecar); persist `ScriptValidation` |
| `policy.sync` | ResourcePolicy / binding change; periodic per cluster (M7+) | mirror binding limits to slurmdbd associations via `slurm.Accounting` write ops; report drift |
| `execution.advance` | any task terminal transition | evaluate DAG: unblock READY tasks, evaluate `when`, fan-out, decide execution terminal state |
| `cluster.sync` | periodic per cluster | see `docs/slurm.md` |
| `accounting.collect` | periodic per cluster (window) | `GetJobRecords(since=watermark)`; upsert `usage_records`; advance watermark |
| `usage.aggregate` | hourly | roll up `usage_records` into `usage_daily` |
| `tenant.delete` | tenant deletion | staged teardown |
| `maintenance.partitions` | daily | create next month partitions; drop expired |
| `idempotency.expire` | hourly | purge expired keys |

## Reconciliation of the "lost submit" case

```text
SubmitJob issued ──► response lost
job.submit handler retried:
   1. ListJobs(name = "custos-<job-id>")           (slurmctld, recent jobs)
   2. if not found and accounting enabled: GetJobRecords(name=..., since=submit-24h)
   3. found      -> adopt slurm_job_id, transition to QUEUED/RUNNING/terminal accordingly
   4. not found  -> submit again (this is safe because steps 1-2 are authoritative
                    for the retention window, and the job name is unique)
```

Residual risk: slurmctld accepted the job but has not yet flushed it to
slurmdbd and it already left the ctld job list (`MinJobAge`, default 300s)
— extremely narrow (retry happens within seconds). We also read the
`Comment` field as a second correlation key.

## Job state reconciler cadence

- Per non-terminal job: reconcile item with adaptive interval (5s while
  `SUBMITTING/QUEUED` young, up to 60s for long-running jobs).
- Per cluster: a `jobs.sweep` item every 60s that lists all Slurm jobs and
  filters `custos-%` names client-side in one call (slurmrestd has no
  server-side name filter) and reconciles in bulk — this is the
  primary path at scale; per-job items are the fallback for freshness on
  user-facing operations (e.g. right after submit or cancel).

## Cluster state reconciler

`cluster.sync` also drives `clusters.state`: `active` ⇄ `degraded` ⇄
`unreachable` with hysteresis (3 consecutive failures → unreachable, 2
successes → active). Submissions to `unreachable` clusters are held in
`READY` with the reason surfaced on the execution.

## Worker process model

- `custos worker` runs the lease loop with per-kind concurrency limits from
  config (`worker.kinds.job.submit.concurrency: 8`).
- `custos serve` can optionally embed the worker (`--embed-worker`) for
  small deployments; production runs them separately.
- Graceful shutdown: stop leasing, let in-flight handlers finish until
  `shutdown_timeout`, then cancel contexts; leases expire naturally.
- One `errgroup` per process; no goroutine is started outside the worker
  pool, the heartbeat, or explicitly owned background refreshers (JWKS,
  OpenBao token renewal, OTel exporters).

## Observability

Prometheus: `custos_work_items_total{kind,result}`, `custos_work_item_duration_seconds{kind}`,
`custos_work_items_pending{kind}`, `custos_work_item_lease_expirations_total{kind}`,
`custos_work_items_dead{kind}`. Traces: one span per handler invocation
linked to the enqueuing request's trace via `payload.trace_context`.
