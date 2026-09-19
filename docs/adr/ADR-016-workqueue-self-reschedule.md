# ADR-016: Workqueue self-reschedule via RescheduleAt, never self-enqueue

Status: Accepted
Date: 2026-09-19

## Context

The work queue (ADR-007) dedupes active items with a partial unique
index on `(kind, key) WHERE state IN ('pending','leased')`. Periodic
handlers (`cluster.sync`, `job.reconcile`, `jobs.sweep`,
`idempotency.expire`) originally scheduled their next run by calling
`Enqueue` with their own `(kind, key)` from inside the handler — while
their own row was still `leased`. The insert hit the dedupe index and
was ignored, so every periodic chain silently died after its first run
(observed live: `cluster.sync` ran once and the cluster never reached
`active`).

## Decision

A handler that wants the same item to run again returns
`workqueue.RescheduleAt(t)` — a typed `Reschedule` error. The queue's
completion path detects it with `errors.As` and `UPDATE`s the leased row
back to `pending` in place: new `run_at`, cleared `leased_by`/
`leased_until`, `attempt=0`, `last_error=NULL` — keyed by
`id + leased_by + state='leased'` so a lost lease can't resurrect the
row. The dedupe index is never involved because no new row is inserted.
Metrics count the outcome as `rescheduled`, not `done`/`failed`. Only
self-reschedules use it; cross-kind scheduling (submit → reconcile,
task → advance) remains ordinary `Enqueue`.

## Alternatives considered

- Insert-with-different-key (e.g. append a timestamp): defeats dedupe
  entirely — two overlapping runs would each spawn a chain.
- Complete-then-enqueue in two statements: a crash between them loses
  the chain; the single update is atomic with the lease release.
- A `EnqueueIfAbsent` for periodic kinds: same problem — the leased row
  itself makes the enqueue absent-fail.

## Consequences

The contract is documented in `docs/workers.md` and regression-tested:
a handler returning `RescheduleAt` leaves the same row `pending` with
the requested `run_at` and runs again; enqueue-time dedupe still holds
for all other paths. Any new periodic handler must use `RescheduleAt`;
self-`Enqueue` remains a silent no-op by design.
