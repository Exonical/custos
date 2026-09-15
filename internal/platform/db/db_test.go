package db_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestMapError(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505", Message: "duplicate"}
	err := db.MapError(pgErr)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.Conflict || ae.Code != "conflict" {
		t.Fatalf("23505: got %#v", err)
	}
	err = db.MapError(context.DeadlineExceeded)
	if !errors.As(err, &ae) || ae.Kind != apperr.Unavailable {
		t.Fatalf("deadline: got %#v", err)
	}
	other := errors.New("x")
	if db.MapError(other) != other {
		t.Fatal("plain error must pass through")
	}
	if db.MapError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestWithTx(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()

	if err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO work_items(id,kind,key,run_at,max_attempts,state) VALUES($1,'k','w1',now(),1,'pending')", uuid.New())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM work_items WHERE key='w1'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("commit: n=%d err=%v", n, err)
	}

	// Error rolls back.
	boom := errors.New("boom")
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO work_items(id,kind,key,run_at,max_attempts,state) VALUES($1,'k','w2',now(),1,'pending')", uuid.New()); err != nil {
			t.Fatal(err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM work_items WHERE key='w2'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback: n=%d err=%v", n, err)
	}

	// Panic rolls back and re-panics.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic must re-raise")
			}
		}()
		_ = db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			_, _ = tx.Exec(ctx, "INSERT INTO work_items(id,kind,key,run_at,max_attempts,state) VALUES($1,'k','w3',now(),1,'pending')", uuid.New())
			panic("explode")
		})
	}()
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM work_items WHERE key='w3'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("panic rollback: n=%d err=%v", n, err)
	}
}

func TestSetTenant(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	tid := uuid.New()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := db.SetTenant(ctx, tx, tid); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := tx.QueryRow(ctx,
		"SELECT coalesce(current_setting('app.tenant_id', true), '')").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != tid.String() {
		t.Fatalf("tenant = %q, want %q", got, tid)
	}

	// A different transaction sees an empty setting.
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback(ctx) }()
	if err := tx2.QueryRow(ctx,
		"SELECT coalesce(current_setting('app.tenant_id', true), '')").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("new tx tenant = %q, want empty", got)
	}
}

func TestAuditEventsImmutable(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		"INSERT INTO audit_streams(stream) VALUES ('platform')"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_events
		  (id, occurred_at, stream, seq, actor_type, actor_id, action, result, prev_hash, hash)
		VALUES ($1, now(), 'platform', 1, 'system', 't', 'a', 'allow', '', '')`, uuid.New()); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"UPDATE audit_events SET reason='x' WHERE stream='platform'",
		"DELETE FROM audit_events WHERE stream='platform'",
		"TRUNCATE audit_events",
	} {
		if _, err := pool.Exec(ctx, stmt); err == nil ||
			!strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: got %v", stmt, err)
		}
	}
}

func TestPreflight(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	// dbtest.Pool already ran Preflight; assert the scram check is real.
	var pw *string
	if err := pool.QueryRow(ctx,
		"SELECT rolpassword FROM pg_authid WHERE rolname=current_user").Scan(&pw); err != nil {
		t.Fatal(err)
	}
	if pw == nil || !strings.HasPrefix(*pw, "SCRAM-SHA-256$") {
		t.Fatalf("rolpassword not scram: %v", pw)
	}
	var ext int
	if err := pool.QueryRow(ctx,
		"SELECT 1 FROM pg_extension WHERE extname='pgcrypto'").Scan(&ext); err != nil {
		t.Fatal("pgcrypto missing")
	}
}

func TestGrantAppRole(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "CREATE ROLE t_app NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	if err := db.GrantAppRole(ctx, pool, "t_app"); err != nil {
		t.Fatal(err)
	}
	if err := db.GrantAppRole(ctx, pool, "bad;role"); err == nil {
		t.Fatal("invalid role accepted")
	}

	// SET ROLE on a dedicated conn; each statement is its own implicit
	// transaction so a denied statement doesn't poison the session.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET ROLE t_app"); err != nil {
		t.Fatal(err)
	}
	// Insert allowed on audit_events (parent grant propagates to the
	// current month's partition).
	if _, err := conn.Exec(ctx, `
		INSERT INTO audit_events
		  (id, occurred_at, stream, seq, actor_type, actor_id, action, result, prev_hash, hash)
		VALUES ($1, now(), 'platform', 1, 'system', 't', 'a', 'allow', '', '')`, uuid.New()); err != nil {
		t.Fatalf("insert as app role: %v", err)
	}
	// UPDATE blocked (trigger fires before any grant check anyway).
	if _, err := conn.Exec(ctx,
		"UPDATE audit_events SET reason='x'"); err == nil {
		t.Fatal("update as app role succeeded")
	}
	// No DDL for the app role.
	if _, err := conn.Exec(ctx, "CREATE TABLE nope(id int)"); err == nil {
		t.Fatal("create table as app role succeeded")
	}
	// work_items DML allowed.
	if _, err := conn.Exec(ctx,
		"INSERT INTO work_items(id,kind,key,run_at,max_attempts,state) VALUES($1,'k','g',now(),1,'pending')",
		uuid.New()); err != nil {
		t.Fatalf("work_items insert as app role: %v", err)
	}
}

func TestEnsureMonthlyPartitions(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()

	// audit_events gets fixed partitions in 0001; use the work_items
	// table? work_items is not partitioned. Create a test parent.
	if _, err := pool.Exec(ctx, `CREATE TABLE part_parent (id int, ts timestamptz, PRIMARY KEY(id, ts)) PARTITION BY RANGE (ts)`); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	if _, err := db.EnsureMonthlyPartitions(ctx, pool, "part_parent", from, 3); err != nil {
		t.Fatal(err)
	}
	// Idempotent.
	if _, err := db.EnsureMonthlyPartitions(ctx, pool, "part_parent", from, 3); err != nil {
		t.Fatal(err)
	}
	var names []string
	rows, err := pool.Query(ctx, `
		SELECT c.relname FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = 'part_parent' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		names = append(names, s)
	}
	want := []string{"part_parent_202603", "part_parent_202604", "part_parent_202605"}
	if len(names) != len(want) {
		t.Fatalf("partitions %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("partitions %v, want %v", names, want)
		}
	}

	if _, err := db.EnsureMonthlyPartitions(ctx, pool, "BAD;DROP", from, 1); err == nil {
		t.Fatal("invalid parent name must fail")
	}
}
