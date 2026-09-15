package pgaudit_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func ev(action string) audit.Event {
	return audit.Event{
		Actor:   audit.Actor{Type: audit.ActorSystem, ID: "test"},
		Action:  action,
		Result:  audit.ResultAllow,
		Details: map[string]any{"k": "v"},
	}
}

func TestRecordAndVerify(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	sink := pgaudit.New(pool)

	for i := 0; i < 3; i++ {
		if err := sink.Record(ctx, ev(fmt.Sprintf("a%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	n, err := pgaudit.Verify(ctx, pool, "platform")
	if err != nil || n != 3 {
		t.Fatalf("verify n=%d err=%v", n, err)
	}
}

func TestTamperDetected(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	sink := pgaudit.New(pool)
	for i := 0; i < 3; i++ {
		if err := sink.Record(ctx, ev(fmt.Sprintf("t%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	// The append-only trigger (ADR-013) blocks UPDATE even for the owner;
	// a tampering superuser would disable it — simulate that here.
	if _, err := pool.Exec(ctx,
		"ALTER TABLE audit_events DISABLE TRIGGER audit_events_immutable_row"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE audit_events SET reason='forged' WHERE stream='platform' AND seq=2"); err != nil {
		t.Fatal(err)
	}
	n, err := pgaudit.Verify(ctx, pool, "platform")
	var ce *pgaudit.ChainError
	if err == nil || !errors.As(err, &ce) {
		t.Fatalf("expected ChainError, got n=%d err=%v", n, err)
	}
	if ce.Seq != 2 {
		t.Fatalf("break at seq %d, want 2", ce.Seq)
	}
}

func TestIndependentStreams(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	sink := pgaudit.New(pool)
	tid := uuid.New()

	if err := sink.Record(ctx, ev("platform-op")); err != nil {
		t.Fatal(err)
	}
	e := ev("tenant-op")
	e.TenantID = &tid
	if err := sink.Record(ctx, e); err != nil {
		t.Fatal(err)
	}
	if got := e.Stream(); got != "tenant:"+tid.String() {
		t.Fatalf("stream %q", got)
	}
	n1, err := pgaudit.Verify(ctx, pool, "platform")
	if err != nil || n1 != 1 {
		t.Fatalf("platform n=%d err=%v", n1, err)
	}
	n2, err := pgaudit.Verify(ctx, pool, "tenant:"+tid.String())
	if err != nil || n2 != 1 {
		t.Fatalf("tenant n=%d err=%v", n2, err)
	}
}

func TestDetailsRoundTrip(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	sink := pgaudit.New(pool)

	e := ev("roundtrip")
	e.Details = map[string]any{
		"count":  3,
		"ratio":  1.5,
		"nested": map[string]any{"a": "b", "n": 7},
		"list":   []any{"x", 2, true},
	}
	if err := sink.Record(ctx, e); err != nil {
		t.Fatal(err)
	}
	n, err := pgaudit.Verify(ctx, pool, "platform")
	if err != nil || n != 1 {
		t.Fatalf("verify n=%d err=%v", n, err)
	}
}

func TestConcurrentRecords(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	sink := pgaudit.New(pool)

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- sink.Record(ctx, ev(fmt.Sprintf("c%d", i)))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	n, err := pgaudit.Verify(ctx, pool, "platform")
	if err != nil || n != 10 {
		t.Fatalf("verify n=%d err=%v", n, err)
	}
	var cnt int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_events WHERE stream='platform'").Scan(&cnt); err != nil || cnt != 10 {
		t.Fatalf("rows=%d err=%v", cnt, err)
	}
}
