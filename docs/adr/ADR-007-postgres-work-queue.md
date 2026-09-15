# ADR-007: Durable PostgreSQL work queue with SKIP LOCKED leases

Status: Accepted
Date: 2026-09-14

## Context

Custos needs a durable queue for job submit/reconcile/cancel, execution
advance, cluster sync, accounting collection, and maintenance (docs/
workers.md). Requirements: leases, retries with backoff, attempt counts,
dead-lettering, idempotency, restart survival, small team — at volumes of
thousands of items/minute. The critical property is that enqueueing is
**transactional with domain state changes**, so "job row created but work
item lost" is impossible.

## Decision

Implement the queue as a PostgreSQL table `work_items` in
`internal/workqueue`:

- `SELECT ... FOR UPDATE SKIP LOCKED` lease loop: order by priority/run_at,
  batch-limit, mark `leased` with `leased_by`/`leased_until` and
  `attempt+1` in one transaction; a heartbeat goroutine extends the lease
  while a handler runs; expired leases are naturally re-picked.
- **Enqueue-time dedup** via a partial unique index on `(kind, key) WHERE
  state IN ('pending','leased')` and `ON CONFLICT DO NOTHING`.
- Retry classification: `Retryable` → back to `pending` with jittered
  exponential backoff `min(cap, base*2^attempt) * rand(0.5,1.0)`; permanent
  errors or `attempt >= max_attempts` → `dead` (visible/retriable via API).
- Handlers are idempotent by construction (read state in tx, guarded
  transition).
- Poll interval 250ms–2s adaptive; **no LISTEN/NOTIFY** (CockroachDB
  compatibility, simplicity).
- Per-kind concurrency limits, graceful shutdown (stop leasing, drain to
  `shutdown_timeout`, leases expire naturally), `done` rows kept 7 days.

## Alternatives considered

- **Kafka / NATS / Redis** — rejected: adds an at-least-once boundary
  between the DB and the broker that would have to be reconciled anyway,
  plus another system to operate.
- **River / asynq-style libraries** — rejected in favor of an owned,
  ~small implementation whose semantics (dedupe index, guarded
  transitions, tenant field, trace propagation via payload) match the
  domain exactly. (Noted: not explicitly enumerated in the doc, but the
  design rationale — transactional enqueue + CRDB constraints — rules
  them out for the same reasons.)
- **In-memory goroutines/channels** — rejected: not durable, no restart
  survival, no multi-instance leasing.

## Consequences

- Queue depth is bounded by DB capacity; indexed partials keep it fast at
  the stated volume.
- The dedupe index semantics make "enqueue if absent" a no-op — callers
  rely on this for reconciliation fan-in.
- No LISTEN/NOTIFY means worst-case latency is the poll interval, which
  is acceptable and adaptive.

Source: docs/workers.md (all sections); docs/architecture.md §7/§10.
