package workqueue_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/workqueue"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func testCfg() config.Worker {
	return config.Worker{
		PollInterval:       20 * time.Millisecond,
		LeaseDuration:      30 * time.Second,
		HeartbeatInterval:  10 * time.Second,
		DefaultConcurrency: 2,
		Kinds:              map[string]config.KindLimits{},
	}
}

func logger() *slog.Logger { return slog.New(slog.NewTextHandler(os.Stderr, nil)) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func itemState(t *testing.T, pool *pgxpool.Pool, key string) (string, int, time.Time) {
	t.Helper()
	var state string
	var attempt int
	var runAt time.Time
	if err := pool.QueryRow(context.Background(),
		"SELECT state, attempt, run_at FROM work_items WHERE key=$1", key).
		Scan(&state, &attempt, &runAt); err != nil {
		t.Fatal(err)
	}
	return state, attempt, runAt
}

func runQueue(ctx context.Context, q *workqueue.Queue) *sync.WaitGroup {
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() { defer wg.Done(); _ = q.Run(ctx) }()
	return wg
}

func TestEnqueueDedupe(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()

	ok, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "dedupe", Key: "k1"})
	if err != nil || !ok {
		t.Fatalf("first enqueue: ok=%v err=%v", ok, err)
	}
	ok, err = workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "dedupe", Key: "k1"})
	if err != nil || ok {
		t.Fatalf("dup enqueue: ok=%v err=%v", ok, err)
	}

	// run_at is pulled earlier.
	later := time.Now().Add(time.Hour)
	earlier := time.Now().Add(time.Minute)
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "dedupe", Key: "k2", RunAt: later}); err != nil {
		t.Fatal(err)
	}
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "dedupe", Key: "k2", RunAt: earlier}); err != nil {
		t.Fatal(err)
	}
	_, _, runAt := itemState(t, pool, "k2")
	if runAt.Sub(earlier) > 5*time.Second || runAt.After(later) {
		t.Fatalf("run_at %v not pulled near %v", runAt, earlier)
	}

	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{Kind: "", Key: "x"}); err == nil {
		t.Fatal("empty kind must fail")
	}
}

func TestHappyPath(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan workqueue.Item, 1)
	q := workqueue.New(pool, testCfg(), logger())
	q.Register("happy", func(_ context.Context, it workqueue.Item) error {
		done <- it
		return nil
	})
	runQueue(ctx, q)
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "happy", Key: "h1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case it := <-done:
		if it.Attempt != 1 {
			t.Fatalf("attempt = %d, want 1", it.Attempt)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("item not processed")
	}
	waitFor(t, "done state", func() bool {
		s, a, _ := itemState(t, pool, "h1")
		return s == "done" && a == 1
	})
	cancel()
}

func TestRetryThenDone(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := testCfg()
	cfg.Kinds["retry"] = config.KindLimits{
		Concurrency: 1, MaxAttempts: 3,
		BaseBackoff: 50 * time.Millisecond, MaxBackoff: time.Second,
	}
	var calls int32
	q := workqueue.New(pool, cfg, logger())
	q.Register("retry", func(_ context.Context, _ workqueue.Item) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return errors.New("transient")
		}
		return nil
	})
	runQueue(ctx, q)
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "retry", Key: "r1"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "retry to done", func() bool {
		s, _, _ := itemState(t, pool, "r1")
		return s == "done"
	})
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	_, attempt, runAt := itemState(t, pool, "r1")
	if attempt != 2 {
		t.Fatalf("attempt = %d, want 2", attempt)
	}
	if runAt.After(time.Now()) {
		t.Fatalf("run_at in future after done: %v", runAt)
	}
}

// TestRescheduleRerunsSameRow covers the periodic-handler contract: a
// handler returning RescheduleAt moves its own leased row back to
// pending — no new row, no dedupe loss — and runs again.
func TestRescheduleRerunsSameRow(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	next := time.Now().Add(200 * time.Millisecond)
	q := workqueue.New(pool, testCfg(), logger())
	q.Register("periodic", func(_ context.Context, _ workqueue.Item) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return workqueue.RescheduleAt(next)
		}
		return nil
	})
	runQueue(ctx, q)
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "periodic", Key: "p1"}); err != nil {
		t.Fatal(err)
	}

	// After the first run: same row is pending at ~next, attempt reset.
	waitFor(t, "first run rescheduled", func() bool {
		return atomic.LoadInt32(&calls) >= 1
	})
	waitFor(t, "pending again", func() bool {
		s, _, _ := itemState(t, pool, "p1")
		return s == "pending"
	})
	state, attempt, runAt := itemState(t, pool, "p1")
	if state != "pending" {
		t.Fatalf("state = %s, want pending", state)
	}
	if attempt != 0 {
		t.Fatalf("attempt = %d, want 0 after reschedule", attempt)
	}
	if d := runAt.Sub(next); d < -time.Second || d > 5*time.Second {
		t.Fatalf("run_at %v not within window of %v", runAt, next)
	}
	var n int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM work_items WHERE kind='periodic' AND key='p1'").
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1 (same row rescheduled)", n)
	}

	// The handler runs a second time and the item completes.
	waitFor(t, "second run", func() bool { return atomic.LoadInt32(&calls) >= 2 })
	waitFor(t, "done", func() bool {
		s, _, _ := itemState(t, pool, "p1")
		return s == "done"
	})
}

func TestPermanentGoesDead(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := workqueue.New(pool, testCfg(), logger())
	q.Register("perm", func(_ context.Context, _ workqueue.Item) error {
		return workqueue.Perm(errors.New("bad input"))
	})
	runQueue(ctx, q)
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "perm", Key: "p1", MaxAttempts: 5}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "dead state", func() bool {
		s, _, _ := itemState(t, pool, "p1")
		return s == "dead"
	})
}

// fakeRecorder captures audit events for assertion.
type fakeRecorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (f *fakeRecorder) Record(_ context.Context, e audit.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func TestDeadLetterAudit(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := &fakeRecorder{}
	q := workqueue.New(pool, testCfg(), logger(),
		workqueue.WithAuditor(rec))
	q.Register("deadaudit", func(_ context.Context, _ workqueue.Item) error {
		return workqueue.Perm(errors.New("bad input"))
	})
	runQueue(ctx, q)
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "deadaudit", Key: "da1", MaxAttempts: 5}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "dead audit event", func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.events) > 0
	})
	e := rec.events[0]
	if e.Action != "workqueue.item.dead" || e.Result != audit.ResultError {
		t.Fatalf("unexpected event: %+v", e)
	}
	d := e.Details
	if d["kind"] != "deadaudit" || d["key"] != "da1" ||
		d["error_class"] != "permanent" {
		t.Fatalf("bad details: %v", d)
	}
	if _, has := d["payload"]; has {
		t.Fatal("payload leaked into audit details")
	}
}

func TestMaxAttemptsDead(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := testCfg()
	cfg.Kinds["maxatt"] = config.KindLimits{
		Concurrency: 1, MaxAttempts: 1,
		BaseBackoff: time.Millisecond, MaxBackoff: time.Second,
	}
	q := workqueue.New(pool, cfg, logger())
	q.Register("maxatt", func(_ context.Context, _ workqueue.Item) error {
		return errors.New("always fails")
	})
	runQueue(ctx, q)
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "maxatt", Key: "m1", MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "dead state", func() bool {
		s, _, _ := itemState(t, pool, "m1")
		return s == "dead"
	})
}

func TestLeaseExpiryRecovery(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := testCfg()
	cfg.LeaseDuration = 300 * time.Millisecond
	cfg.PollInterval = 20 * time.Millisecond
	// Concurrency 1 so worker A cannot re-lease the row itself while its
	// blocked handler is still holding the first lease.
	cfg.Kinds["exp"] = config.KindLimits{Concurrency: 1}

	block := make(chan struct{})
	leased := make(chan struct{})
	a := workqueue.New(pool, cfg, logger(),
		workqueue.WithHeartbeat(false), workqueue.WithInstanceID("worker-a"))
	a.Register("exp", func(_ context.Context, _ workqueue.Item) error {
		close(leased)
		<-block // hold past lease expiry; ignore ctx
		return nil
	})
	runQueue(ctx, a)

	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "exp", Key: "e1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-leased:
	case <-time.After(15 * time.Second):
		t.Fatal("worker A never leased")
	}

	// Worker B picks up the expired lease and completes.
	b := workqueue.New(pool, cfg, logger(),
		workqueue.WithHeartbeat(false), workqueue.WithInstanceID("worker-b"))
	b.Register("exp", func(_ context.Context, _ workqueue.Item) error {
		return nil
	})
	runQueue(ctx, b)

	waitFor(t, "B completes item", func() bool {
		s, _, _ := itemState(t, pool, "e1")
		return s == "done"
	})
	close(block)
	_, attempt, _ := itemState(t, pool, "e1")
	if attempt != 2 {
		t.Fatalf("attempt = %d, want 2 (A then B)", attempt)
	}
	cancel()
}

func TestPerKindConcurrency(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := testCfg()
	cfg.Kinds["bounded"] = config.KindLimits{
		Concurrency: 2, MaxAttempts: 1,
		BaseBackoff: time.Millisecond, MaxBackoff: time.Second,
	}
	var cur, peak, done int32
	q := workqueue.New(pool, cfg, logger())
	q.Register("bounded", func(_ context.Context, _ workqueue.Item) error {
		c := atomic.AddInt32(&cur, 1)
		for {
			m := atomic.LoadInt32(&peak)
			if c <= m || atomic.CompareAndSwapInt32(&peak, m, c) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		atomic.AddInt32(&done, 1)
		return nil
	})
	runQueue(ctx, q)
	for i := 0; i < 6; i++ {
		if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
			Kind: "bounded", Key: fmt.Sprintf("b%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "all done", func() bool { return atomic.LoadInt32(&done) == 6 })
	if atomic.LoadInt32(&peak) > 2 {
		t.Fatalf("peak concurrency = %d, want <=2", peak)
	}
}

func TestTwoQueuesOnePool(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := testCfg()
	cfg.DefaultConcurrency = 4

	counts := sync.Map{}
	var total int32
	h := func(_ context.Context, it workqueue.Item) error {
		v, _ := counts.LoadOrStore(it.ID, new(int32))
		atomic.AddInt32(v.(*int32), 1)
		atomic.AddInt32(&total, 1)
		return nil
	}
	q1 := workqueue.New(pool, cfg, logger(), workqueue.WithInstanceID("q1"))
	q2 := workqueue.New(pool, cfg, logger(), workqueue.WithInstanceID("q2"))
	q1.Register("dup2", h)
	q2.Register("dup2", h)
	runQueue(ctx, q1)
	runQueue(ctx, q2)

	for i := 0; i < 50; i++ {
		if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
			Kind: "dup2", Key: fmt.Sprintf("d%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "50 processed", func() bool { return atomic.LoadInt32(&total) >= 50 })
	bad := 0
	counts.Range(func(_, v any) bool {
		if atomic.LoadInt32(v.(*int32)) != 1 {
			bad++
		}
		return true
	})
	if bad != 0 {
		t.Fatalf("%d items ran != 1 time", bad)
	}
	cancel()
}

func TestDrainCancelsHandlers(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())

	cfg := testCfg()
	cfg.ShutdownTimeout = 200 * time.Millisecond
	cfg.LeaseDuration = 30 * time.Second

	q := workqueue.New(pool, cfg, logger())
	q.Register("drain", func(ctx context.Context, _ workqueue.Item) error {
		<-ctx.Done() // block until drain cancels
		return ctx.Err()
	})
	wg := runQueue(ctx, q)
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "drain", Key: "d1"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "item leased", func() bool {
		s, _, _ := itemState(t, pool, "d1")
		return s == "leased"
	})
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after ShutdownTimeout")
	}
	// ctx.Err() is a retryable error -> item is pending again, not lost.
	waitFor(t, "item pending again", func() bool {
		s, _, _ := itemState(t, pool, "d1")
		return s == "pending"
	})
}

func TestKindCeilingMaxAttempts(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := testCfg()
	cfg.Kinds["ceiling"] = config.KindLimits{
		Concurrency: 1, MaxAttempts: 2, // operator ceiling below item's 5
		BaseBackoff: time.Millisecond, MaxBackoff: time.Second,
	}
	q := workqueue.New(pool, cfg, logger())
	q.Register("ceiling", func(_ context.Context, _ workqueue.Item) error {
		return errors.New("always fails")
	})
	runQueue(ctx, q)
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "ceiling", Key: "c1", MaxAttempts: 5}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "dead state", func() bool {
		s, _, _ := itemState(t, pool, "c1")
		return s == "dead"
	})
	_, attempt, _ := itemState(t, pool, "c1")
	if attempt != 2 {
		t.Fatalf("attempt = %d, want 2 (kind ceiling)", attempt)
	}
}

func TestBackoff(t *testing.T) {
	base, limit := 100*time.Millisecond, 500*time.Millisecond
	cases := []struct {
		attempt int
		rnd     float64
		min, mx time.Duration
	}{
		{1, 0, 50 * time.Millisecond, 100 * time.Millisecond},
		{1, 1, 100 * time.Millisecond, 100 * time.Millisecond},
		{2, 0, 100 * time.Millisecond, 200 * time.Millisecond},
		{3, 0, 200 * time.Millisecond, 400 * time.Millisecond},
		{4, 0, 250 * time.Millisecond, 500 * time.Millisecond}, // capped
		{9, 1, 500 * time.Millisecond, 500 * time.Millisecond}, // capped
	}
	for _, c := range cases {
		got := workqueue.Backoff(base, limit, c.attempt, func() float64 { return c.rnd })
		if got < c.min || got > c.mx {
			t.Fatalf("attempt %d rnd %v: %v not in [%v,%v]", c.attempt, c.rnd, got, c.min, c.mx)
		}
	}
}
