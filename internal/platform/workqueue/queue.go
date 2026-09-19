// Package workqueue implements the durable PostgreSQL work queue from
// docs/workers.md: SKIP LOCKED leases, enqueue-time dedupe via a partial
// unique index, jittered exponential backoff, and a dead-letter state.
package workqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/config"
)

// Item is a leased work item handed to a Handler.
type Item struct {
	ID          uuid.UUID
	Kind        string
	Key         string
	TenantID    *uuid.UUID
	Payload     json.RawMessage
	Attempt     int
	MaxAttempts int
	RunAt       time.Time
	Priority    int16
}

// Handler processes one leased item. It must be idempotent: the same item
// may be delivered more than once.
type Handler func(ctx context.Context, item Item) error

// EnqueueRequest describes a work item to enqueue.
type EnqueueRequest struct {
	Kind        string
	Key         string
	TenantID    *uuid.UUID
	Payload     any // json-marshalled; nil -> {}
	RunAt       time.Time
	Priority    int16
	MaxAttempts int // 0 -> kind limit or default
}

// Execer is satisfied by pgx.Tx and *pgxpool.Pool so enqueue can join a
// domain transaction.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Enqueue inserts a work item, deduplicated on (kind, key) while an
// active item exists. On conflict the existing item's run_at is pulled
// earlier if the new request is sooner; inserted=false in that case.
func Enqueue(ctx context.Context, ex Execer, req EnqueueRequest) (bool, error) {
	if req.Kind == "" || req.Key == "" {
		return false, apperr.New(apperr.Invalid, "workqueue.enqueue",
			"kind and key are required")
	}
	payload := json.RawMessage("{}")
	if req.Payload != nil {
		b, err := json.Marshal(req.Payload)
		if err != nil {
			return false, apperr.Wrap(err, apperr.Invalid, "workqueue.enqueue", "payload marshal")
		}
		payload = b
	}
	runAt := req.RunAt
	if runAt.IsZero() {
		runAt = time.Now()
	}
	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	// INSERT-or-noop first (CRDB-compatible: no xmax tricks); on conflict
	// pull run_at earlier if the row is still pending.
	var id uuid.UUID
	err := ex.QueryRow(ctx, `
		INSERT INTO work_items (id, kind, key, tenant_id, payload, run_at, priority, max_attempts, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
		ON CONFLICT (kind, key) WHERE state IN ('pending','leased')
		DO NOTHING
		RETURNING id
	`, uuid.Must(uuid.NewV7()), req.Kind, req.Key, req.TenantID, payload,
		runAt, req.Priority, maxAttempts).Scan(&id)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, mapErr(err)
	}
	if _, err := ex.Exec(ctx, `
		UPDATE work_items
		SET run_at = LEAST(run_at, $3), updated_at = now()
		WHERE kind = $1 AND key = $2 AND state = 'pending'`,
		req.Kind, req.Key, runAt); err != nil {
		return false, mapErr(err)
	}
	return false, nil
}

func mapErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return apperr.New(apperr.Conflict, "conflict", "resource conflict")
	}
	return err
}

// Reschedule is returned by a handler to run the same item again at At.
// Periodic handlers use it instead of Enqueue: the leased row is moved
// back to pending in place, so the (kind, key) dedupe index is never
// involved and the chain survives.
type Reschedule struct{ At time.Time }

// Error implements error.
func (r Reschedule) Error() string { return "reschedule at " + r.At.String() }

// RescheduleAt returns a Reschedule for time t.
func RescheduleAt(t time.Time) error { return Reschedule{At: t} }

// Permanent marks a handler error as non-retryable: the item goes
// straight to the dead state.
type Permanent struct{ Err error }

// Error implements error.
func (p *Permanent) Error() string { return p.Err.Error() }

// Unwrap returns the wrapped cause.
func (p *Permanent) Unwrap() error { return p.Err }

// Perm wraps err as permanent.
func Perm(err error) error {
	return &Permanent{Err: err}
}

// Backoff returns min(cap, base*2^(attempt-1)) * (0.5 + 0.5*rnd()).
func Backoff(base, limit time.Duration, attempt int, rnd func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := float64(base) * math.Pow(2, float64(attempt-1))
	if d > float64(limit) {
		d = float64(limit)
	}
	return time.Duration(d * (0.5 + 0.5*rnd()))
}

// Option customizes a Queue.
type Option func(*Queue)

// WithInstanceID overrides the worker identity recorded on leases.
func WithInstanceID(id string) Option { return func(q *Queue) { q.instance = id } }

// WithHeartbeat enables/disables the lease heartbeat goroutine
// (tests disable it to exercise expiry).
func WithHeartbeat(on bool) Option { return func(q *Queue) { q.heartbeat = on } }

// WithNow overrides the clock (tests).
func WithNow(fn func() time.Time) Option { return func(q *Queue) { q.now = fn } }

// WithMeterProvider sets the otel MeterProvider for queue metrics.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(q *Queue) { q.meter = mp }
}

// WithAuditor sets the Recorder used for workqueue.item.dead events.
func WithAuditor(r audit.Recorder) Option {
	return func(q *Queue) { q.auditor = r }
}

type kindCfg struct {
	concurrency int
	maxAttempts int
	baseBackoff time.Duration
	maxBackoff  time.Duration
	slots       chan struct{}
}

// Queue is the work-queue worker.
type Queue struct {
	pool      *pgxpool.Pool
	cfg       config.Worker
	logger    *slog.Logger
	instance  string
	heartbeat bool
	now       func() time.Time

	mu       sync.Mutex
	handlers map[string]Handler
	kinds    map[string]*kindCfg
	wg       sync.WaitGroup
	runCtx   context.Context
	runStop  context.CancelFunc

	meter        metric.MeterProvider
	itemsTotal   metric.Int64Counter
	duration     metric.Float64Histogram
	leaseExpired metric.Int64Counter

	gaugeMu    sync.Mutex
	gaugeAt    time.Time
	gaugeCache map[[2]string]int64 // (kind, state) -> count
	auditor    audit.Recorder
}

// New creates a Queue.
func New(pool *pgxpool.Pool, cfg config.Worker, logger *slog.Logger, opts ...Option) *Queue {
	host, _ := os.Hostname()
	q := &Queue{
		pool:      pool,
		cfg:       cfg,
		logger:    logger,
		instance:  host + "-" + uuid.Must(uuid.NewV7()).String()[:8],
		heartbeat: true,
		now:       time.Now,
		handlers:  map[string]Handler{},
		kinds:     map[string]*kindCfg{},
		meter:     metricnoop.NewMeterProvider(),
	}
	for _, o := range opts {
		o(q)
	}
	q.initMetrics()
	return q
}

// initMetrics registers the instruments; with the noop provider these
// are cheap no-ops.
func (q *Queue) initMetrics() {
	m := q.meter.Meter("custos")
	q.itemsTotal, _ = m.Int64Counter("custos_work_items_total",
		metric.WithDescription("Work items by terminal/transition result"))
	q.duration, _ = m.Float64Histogram("custos_work_item_duration_seconds",
		metric.WithDescription("Handler duration per item"))
	q.leaseExpired, _ = m.Int64Counter("custos_work_item_lease_expirations_total",
		metric.WithDescription("Lease takeovers after expiry"))
	pending, _ := m.Int64ObservableGauge("custos_work_items_pending",
		metric.WithDescription("Pending work items per kind"))
	dead, _ := m.Int64ObservableGauge("custos_work_items_dead",
		metric.WithDescription("Dead-lettered work items per kind"))
	_, _ = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		counts, err := q.stateCounts(ctx)
		if err != nil {
			q.logger.ErrorContext(ctx, "gauge query failed", "error", err)
			return nil
		}
		for k, c := range counts {
			attrs := metric.WithAttributes(attribute.String("kind", k[0]))
			switch k[1] {
			case "pending":
				o.ObserveInt64(pending, c, attrs)
			case "dead":
				o.ObserveInt64(dead, c, attrs)
			}
		}
		return nil
	}, pending, dead)
}

// stateCounts runs the pending/dead aggregate at most every 15s.
func (q *Queue) stateCounts(ctx context.Context) (map[[2]string]int64, error) {
	q.gaugeMu.Lock()
	defer q.gaugeMu.Unlock()
	if q.now().Sub(q.gaugeAt) < 15*time.Second && q.gaugeCache != nil {
		return q.gaugeCache, nil
	}
	rows, err := q.pool.Query(ctx, `
		SELECT kind, state, count(*) FROM work_items
		WHERE state IN ('pending','dead') GROUP BY 1,2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]string]int64{}
	for rows.Next() {
		var kind, state string
		var n int64
		if err := rows.Scan(&kind, &state, &n); err != nil {
			return nil, err
		}
		out[[2]string{kind, state}] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	q.gaugeCache = out
	q.gaugeAt = q.now()
	return out, nil
}

// Register binds a handler to a kind. Panics on duplicate kind.
func (q *Queue) Register(kind string, h Handler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.handlers[kind]; exists {
		panic("workqueue: duplicate handler for " + kind)
	}
	kc := &kindCfg{
		concurrency: q.cfg.DefaultConcurrency,
		maxAttempts: 5,
		baseBackoff: time.Second,
		maxBackoff:  5 * time.Minute,
	}
	if lim, ok := q.cfg.Kinds[kind]; ok {
		kc.concurrency = lim.Concurrency
		kc.maxAttempts = lim.MaxAttempts
		kc.baseBackoff = lim.BaseBackoff
		kc.maxBackoff = lim.MaxBackoff
	}
	kc.slots = make(chan struct{}, kc.concurrency)
	q.handlers[kind] = h
	q.kinds[kind] = kc
}

// Run leases and executes items until ctx is done. Handler contexts
// derive from a per-Run context (not the caller's ctx): while the
// heartbeat holds the lease a handler has no deadline; if heartbeat is
// disabled (tests) the handler deadline is leased_until-1s. On ctx done,
// leasing stops, in-flight handlers drain for up to
// cfg.ShutdownTimeout, then runCtx is canceled (canceling all handler
// contexts) and Run returns once everything finishes.
func (q *Queue) Run(ctx context.Context) error {
	q.runCtx, q.runStop = context.WithCancel(context.Background())
	defer q.runStop()

	for {
		select {
		case <-ctx.Done():
			q.drain()
			return nil
		default:
		}
		leased := q.leaseOnce(ctx)
		if leased == 0 {
			select {
			case <-ctx.Done():
				q.drain()
				return nil
			case <-time.After(q.cfg.PollInterval):
			}
		}
	}
}

// drain waits up to ShutdownTimeout for in-flight handlers, then cancels
// runCtx so heartbeat-less or long-running handlers are interrupted.
func (q *Queue) drain() {
	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(q.cfg.ShutdownTimeout):
		q.runStop()
		<-done
	}
}

type kindSnap struct {
	name string
	kc   *kindCfg
}

// leaseOnce performs one poll round across all kinds; returns count leased.
func (q *Queue) leaseOnce(ctx context.Context) int {
	q.mu.Lock()
	kinds := make([]kindSnap, 0, len(q.kinds))
	for k, kc := range q.kinds {
		kinds = append(kinds, kindSnap{k, kc})
	}
	q.mu.Unlock()

	total := 0
	for _, ks := range kinds {
		kind, kc := ks.name, ks.kc
		free := kc.concurrency - len(kc.slots)
		if free <= 0 {
			continue
		}
		items, err := q.lease(ctx, kind, free)
		if err != nil {
			q.logger.ErrorContext(ctx, "lease failed", "kind", kind, "error", err)
			continue
		}
		for _, it := range items {
			kc.slots <- struct{}{}
			total++
			q.wg.Add(1)
			// Handlers run on a detached ctx by design (lease deadline),
			// not the poll ctx.
			go q.execute(kind, kc, it) // #nosec G118
		}
	}
	return total
}

// lease marks up to n due items leased under this instance in one tx.
func (q *Queue) lease(ctx context.Context, kind string, n int) ([]Item, error) {
	tx, err := q.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		WITH picked AS (
			SELECT id, state AS prev_state FROM work_items
			WHERE kind = $1
			  AND ((state = 'pending' AND run_at <= now())
			    OR (state = 'leased' AND leased_until < now()))
			ORDER BY priority DESC, run_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE work_items w
		SET state = 'leased', leased_by = $3, leased_until = now() + $4 * interval '1 second',
		    attempt = attempt + 1, updated_at = now()
		FROM picked
		WHERE w.id = picked.id
		RETURNING w.id, w.kind, w.key, w.tenant_id, w.payload, w.attempt,
		          w.max_attempts, w.run_at, w.priority, w.leased_until, picked.prev_state
	`, kind, n, q.instance, q.cfg.LeaseDuration.Seconds())
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var it Item
		var leasedUntil time.Time
		var prev string
		if err := rows.Scan(&it.ID, &it.Kind, &it.Key, &it.TenantID, &it.Payload,
			&it.Attempt, &it.MaxAttempts, &it.RunAt, &it.Priority,
			&leasedUntil, &prev); err != nil {
			return nil, mapErr(err)
		}
		if prev == "leased" {
			q.logger.WarnContext(ctx, "took over expired lease",
				"kind", it.Kind, "key", it.Key, "id", it.ID)
			q.leaseExpired.Add(ctx, 1,
				metric.WithAttributes(attribute.String("kind", it.Kind)))
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapErr(err)
	}
	return items, nil
}

// execute runs one item to completion: handler, heartbeat, outcome.
func (q *Queue) execute(kind string, kc *kindCfg, it Item) {
	defer q.wg.Done()
	defer func() { <-kc.slots }()

	h := q.handlers[kind]
	var ctx context.Context
	var cancel context.CancelFunc
	if q.heartbeat {
		// Heartbeat keeps the DB lease alive; the handler ctx has no
		// deadline — heartbeat loss or drain cancellation ends it.
		ctx, cancel = context.WithCancel(q.runCtx)
	} else {
		ctx, cancel = context.WithDeadline(q.runCtx,
			q.now().Add(q.cfg.LeaseDuration-time.Second))
	}
	defer cancel()

	stopHb := make(chan struct{})
	if q.heartbeat {
		go q.heartbeatLoop(it.ID, stopHb, cancel)
		defer close(stopHb)
	}

	start := q.now()
	err := callHandler(ctx, h, it)
	// Outcome updates use a fresh ctx: the handler ctx may be at deadline.
	out := context.Background()
	kindAttr := metric.WithAttributes(attribute.String("kind", kind))
	q.duration.Record(out, q.now().Sub(start).Seconds(), kindAttr)
	if err != nil {
		var rs Reschedule
		if errors.As(err, &rs) {
			q.reschedule(out, it, rs.At)
			return
		}
		q.fail(out, kc, it, err)
		return
	}
	tag, err := q.pool.Exec(out, `
		UPDATE work_items SET state='done', finished_at=now(), updated_at=now()
		WHERE id=$1 AND leased_by=$2 AND state='leased'`, it.ID, q.instance)
	if err != nil {
		q.logger.Error("mark done failed", "id", it.ID, "error", err)
		return
	}
	if tag.RowsAffected() == 0 {
		q.logger.Warn("lease lost on completion", "id", it.ID, "kind", it.Kind)
		return
	}
	q.itemsTotal.Add(out, 1, metric.WithAttributes(
		attribute.String("kind", kind), attribute.String("result", "done")))
}

// reschedule returns a leased item to pending at a new run_at: the same
// row is reused, so the (kind, key) dedupe index never sees a duplicate.
func (q *Queue) reschedule(ctx context.Context, it Item, at time.Time) {
	tag, err := q.pool.Exec(ctx, `
		UPDATE work_items
		SET state='pending', run_at=$3, leased_by=NULL, leased_until=NULL,
		    attempt=0, last_error=NULL, updated_at=now()
		WHERE id=$1 AND leased_by=$2 AND state='leased'`,
		it.ID, q.instance, at)
	if err != nil {
		q.logger.Error("mark rescheduled failed", "id", it.ID, "error", err)
		return
	}
	if tag.RowsAffected() == 0 {
		q.logger.Warn("lease lost on reschedule", "id", it.ID,
			"kind", it.Kind)
		return
	}
	q.itemsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", it.Kind),
		attribute.String("result", "rescheduled")))
}

func callHandler(ctx context.Context, h Handler, it Item) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("handler panic: %v\n%s", rec, debug.Stack())
		}
	}()
	return h(ctx, it)
}

// fail applies retry/dead-letter semantics to a failed item.
func (q *Queue) fail(ctx context.Context, kc *kindCfg, it Item, err error) {
	lastErr := err.Error()
	if len(lastErr) > 1024 {
		lastErr = lastErr[:1024]
	}
	var perm *Permanent
	// max_attempts is the lower of the item's and the operator's per-kind
	// ceiling.
	dead := errors.As(err, &perm) ||
		it.Attempt >= it.MaxAttempts || it.Attempt >= kc.maxAttempts
	var tag pgconn.CommandTag
	var uerr error
	if dead {
		tag, uerr = q.pool.Exec(ctx, `
			UPDATE work_items SET state='dead', last_error=$3, finished_at=now(), updated_at=now()
			WHERE id=$1 AND leased_by=$2 AND state='leased'`, it.ID, q.instance, lastErr)
	} else {
		next := Backoff(kc.baseBackoff, kc.maxBackoff, it.Attempt, rand.Float64)
		tag, uerr = q.pool.Exec(ctx, `
			UPDATE work_items SET state='pending', run_at=now() + $3 * interval '1 second',
			       last_error=$4, updated_at=now()
			WHERE id=$1 AND leased_by=$2 AND state='leased'`,
			it.ID, q.instance, next.Seconds(), lastErr)
	}
	if uerr != nil {
		q.logger.Error("mark failed outcome error", "id", it.ID, "error", uerr)
		return
	}
	if tag.RowsAffected() == 0 {
		q.logger.Warn("lease lost on failure", "id", it.ID, "kind", it.Kind)
		return
	}
	result := "retry"
	if dead {
		result = "dead"
	}
	q.itemsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", it.Kind), attribute.String("result", result)))
	if dead && q.auditor != nil {
		class := "max_attempts"
		if errors.As(err, &perm) {
			class = "permanent"
		}
		if rerr := q.auditor.Record(ctx, audit.Event{
			Actor:  audit.Actor{Type: audit.ActorSystem, ID: "custos"},
			Action: "workqueue.item.dead",
			Result: audit.ResultError,
			Details: map[string]any{
				"kind":        it.Kind,
				"key":         it.Key,
				"attempts":    it.Attempt,
				"error_class": class,
			},
		}); rerr != nil {
			q.logger.Error("audit workqueue.item.dead failed", "id", it.ID, "error", rerr)
		}
	}
}

// heartbeatLoop extends leased_until every lease/3; if the extension
// affects no row the lease was lost and the handler ctx is canceled.
func (q *Queue) heartbeatLoop(id uuid.UUID, stop <-chan struct{}, cancel context.CancelFunc) {
	tick := time.NewTicker(q.cfg.LeaseDuration / 3)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		tag, err := q.pool.Exec(context.Background(), `
			UPDATE work_items SET leased_until = now() + $2 * interval '1 second', updated_at=now()
			WHERE id=$1 AND leased_by=$3 AND state='leased'`,
			id, q.cfg.LeaseDuration.Seconds(), q.instance)
		if err != nil {
			q.logger.Error("heartbeat failed", "id", id, "error", err)
			continue
		}
		if tag.RowsAffected() == 0 {
			q.logger.Warn("lease lost during heartbeat", "id", id)
			cancel()
			return
		}
	}
}
