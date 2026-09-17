package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	scriptpg "github.com/Exonical/custos/internal/scripts/postgres"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func mkTenant(t *testing.T, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO tenants (id, slug, name, state, settings, version)
		VALUES ($1,$2,'T','active','{}',1)`, id, slug); err != nil {
		t.Fatal(err)
	}
	return id
}

func mkUser(t *testing.T, pool *pgxpool.Pool, sub string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, issuer, subject, kind) VALUES ($1,'iss',$2,'user')`,
		id, sub); err != nil {
		t.Fatal(err)
	}
	return id
}

func scope(tenantID uuid.UUID) tenants.Scope {
	return tenants.TenantScope(tenantID)
}

func TestPutGet(t *testing.T) {
	pool := dbtest.Pool(t)
	st := scriptpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "scr-a")
	uid := mkUser(t, pool, "scr-u")
	body := []byte("#!/bin/bash\necho hi\n")

	d1, err := st.Put(ctx, scope(tid), tid, workflowspec.LanguageBash, body, uid)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != validation.DigestOf(body) {
		t.Fatalf("digest %v != sha256(body)", d1)
	}
	// Idempotent: same body re-put returns the same digest.
	d2, err := st.Put(ctx, scope(tid), tid, workflowspec.LanguageBash, body, uid)
	if err != nil || d2 != d1 {
		t.Fatalf("re-put: %v %v", d2, err)
	}
	got, err := st.Get(ctx, scope(tid), tid, d1)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("body mismatch: %q", got)
	}
	if _, err := st.Get(ctx, scope(tid), tid,
		validation.DigestOf([]byte("nope"))); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestPutRejectsLimits(t *testing.T) {
	pool := dbtest.Pool(t)
	st := scriptpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "scr-b")
	uid := mkUser(t, pool, "scr-u2")

	big := make([]byte, validation.DefaultLimits.MaxScriptBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	if _, err := st.Put(ctx, scope(tid), tid,
		workflowspec.LanguageBash, big, uid); !apperr.Is(err, apperr.Validation) {
		t.Fatalf("want validation, got %v", err)
	}
	if _, err := st.Put(ctx, scope(tid), tid,
		workflowspec.LanguageBash, []byte("a\x00b"), uid); !apperr.Is(err, apperr.Validation) {
		t.Fatalf("want validation for NUL, got %v", err)
	}
}

// TestImmutability verifies the trigger blocks UPDATE/DELETE.
func TestImmutability(t *testing.T) {
	pool := dbtest.Pool(t)
	st := scriptpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "scr-c")
	body := []byte("echo 1")
	d, err := st.Put(ctx, scope(tid), tid, workflowspec.LanguageBash, body, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE scripts SET body='\x00' WHERE tenant_id=$1 AND sha256=$2`,
		`DELETE FROM scripts WHERE tenant_id=$1 AND sha256=$2`,
	} {
		_, err := pool.Exec(ctx, q, tid, d[:])
		if err == nil {
			t.Fatalf("%s: expected trigger error", q)
		}
	}
}

// TestIntegrityReverify: a tampered body (bypassing the trigger) must
// surface as scripts.integrity, never bytes.
func TestIntegrityReverify(t *testing.T) {
	pool := dbtest.Pool(t)
	st := scriptpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "scr-d")
	body := []byte("echo safe")
	d, err := st.Put(ctx, scope(tid), tid, workflowspec.LanguageBash, body, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a DB-side tamper: bypass the immutability trigger.
	if _, err := pool.Exec(ctx,
		`ALTER TABLE scripts DISABLE TRIGGER scripts_no_update`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx,
			`ALTER TABLE scripts ENABLE TRIGGER scripts_no_update`)
	}()
	if _, err := pool.Exec(ctx,
		`UPDATE scripts SET body=$3 WHERE tenant_id=$1 AND sha256=$2`,
		tid, d[:], []byte("echo evil")); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, scope(tid), tid, d)
	if err == nil {
		t.Fatalf("tampered body returned: %q", got)
	}
	ae, ok := err.(*apperr.Error)
	if !ok || ae.Code != "scripts.integrity" {
		t.Fatalf("want scripts.integrity, got %v", err)
	}
}

// TestRLS verifies tenant scoping under the app role.
func TestRLS(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ta := mkTenant(t, admin, "rls-sa")
	tb := mkTenant(t, admin, "rls-sb")
	st := scriptpg.New(admin)
	body := []byte("echo x")
	d, err := st.Put(ctx, tenants.PlatformScope(), ta,
		workflowspec.LanguageBash, body, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}

	app := dbtest.AppPoolOn(t, dsn)
	if err := db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, tb); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM scripts WHERE tenant_id=$1`, ta).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatalf("tenant B saw %d rows of A's scripts", n)
		}
		// And the app-role store cannot read it either.
		appSt := scriptpg.New(app)
		if _, err := appSt.Get(ctx, scope(tb), tb, d); !apperr.Is(err, apperr.NotFound) {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
