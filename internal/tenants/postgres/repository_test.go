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
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func mkTenant(slug string) tenants.Tenant {
	return tenants.Tenant{
		ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: "T " + slug,
		State: tenants.StateActive, Settings: map[string]any{},
	}
}

func TestTenantCRUD(t *testing.T) {
	repo := tenantpg.New(dbtest.Pool(t))
	ctx := context.Background()

	ten := mkTenant("acme-labs")
	if err := repo.Create(ctx, ten); err != nil {
		t.Fatal(err)
	}
	// By slug and by uuid.
	for _, ref := range []string{ten.Slug, ten.ID.String()} {
		got, err := repo.GetBySlugOrID(ctx, tenants.PlatformScope(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != ten.ID || got.Slug != ten.Slug || got.Version != 1 {
			t.Fatalf("got %+v", got)
		}
	}
	// Missing ref -> NotFound.
	if _, err := repo.GetBySlugOrID(ctx, tenants.PlatformScope(), "nope-nope"); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want NotFound, got %v", err)
	}
	// Update with right version; conflict with stale version.
	ten.Name = "Renamed"
	ten.Version = 1
	if err := repo.Update(ctx, tenants.PlatformScope(), ten); err != nil {
		t.Fatal(err)
	}
	ten.Version = 1 // stale
	if err := repo.Update(ctx, tenants.PlatformScope(), ten); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want Conflict, got %v", err)
	}
	// Unique slug.
	dup := mkTenant("acme-labs")
	if err := repo.Create(ctx, dup); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want Conflict, got %v", err)
	}
}

func TestTenantPagination(t *testing.T) {
	repo := tenantpg.New(dbtest.Pool(t))
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		tn := mkTenant("pg" + string(rune('a'+i)))
		if err := repo.Create(ctx, tn); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[uuid.UUID]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		ts, next, err := repo.List(ctx, tenants.PlatformScope(), tenants.Page{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, tn := range ts {
			if seen[tn.ID] {
				t.Fatalf("duplicate row %s", tn.ID)
			}
			seen[tn.ID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d tenants, want 5", len(seen))
	}
	// Bad cursor.
	if _, _, err := repo.List(ctx, tenants.PlatformScope(), tenants.Page{Cursor: "%%%bogus"}); !apperr.Is(err, apperr.Invalid) {
		t.Fatalf("want CURSOR_INVALID, got %v", err)
	}
}

// TestRLS exercises the real least-privilege role: custos_test_app is
// subject to RLS (no BYPASSRLS, not the table owner).
func TestRLS(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	repo := tenantpg.New(admin)

	a, b := mkTenant("rls-a"), mkTenant("rls-b")
	for _, tn := range []tenants.Tenant{a, b} {
		if err := repo.Create(ctx, tn); err != nil {
			t.Fatal(err)
		}
	}
	ua, ub := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	// Users rows aren't strictly needed for the RLS assertion but keep
	// FK integrity honest.
	for i, u := range []uuid.UUID{ua, ub} {
		if _, err := admin.Exec(ctx,
			`INSERT INTO users (id, issuer, subject, kind) VALUES ($1,'iss',$2,'user')`,
			u, "sub-"+string(rune('0'+i))); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []tenants.Membership{
		{TenantID: a.ID, UserID: ua, Roles: []string{"viewer"}, Source: tenants.SourceManual},
		{TenantID: b.ID, UserID: ub, Roles: []string{"viewer"}, Source: tenants.SourceManual},
	} {
		if err := repo.UpsertMembership(ctx, tenants.PlatformScope(), m); err != nil {
			t.Fatal(err)
		}
	}

	app := dbtest.AppPoolOn(t, dsn)
	scopeA := func(tx pgx.Tx) error { return db.SetTenant(ctx, tx, a.ID) }

	// Scoped to A: only A's rows are visible, even without WHERE.
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := scopeA(tx); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM tenant_memberships`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("scoped count = %d, want 1", n)
		}
		var tcount int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM tenants`).Scan(&tcount); err != nil {
			return err
		}
		if tcount != 1 {
			t.Fatalf("scoped tenants = %d, want 1", tcount)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// INSERT of a B row under scope A fails the WITH CHECK (own tx: the
	// error aborts it).
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := scopeA(tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO tenant_memberships (tenant_id, user_id, roles, source)
			 VALUES ($1,$2,'{viewer}','manual')`, b.ID, ua)
		if err == nil {
			t.Fatal("cross-tenant insert succeeded")
		}
		return err
	})
	if err == nil {
		t.Fatal("expected aborted tx")
	}

	// No scope at all: zero rows on both tables.
	var n, tn int
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM tenant_memberships`).Scan(&n); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM tenants`).Scan(&tn)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || tn != 0 {
		t.Fatalf("unscoped counts = %d memberships, %d tenants; want 0", n, tn)
	}

	// Platform scope ('*') sees all rows and may write.
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		var all int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM tenant_memberships`).Scan(&all); err != nil {
			return err
		}
		if all != 2 {
			t.Fatalf("platform scope sees %d rows, want 2", all)
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO tenant_memberships (tenant_id, user_id, roles, source)
			 VALUES ($1,$2,'{viewer}','manual')`, b.ID, ua)
		return err
	})
	if err != nil {
		t.Fatalf("platform-scope insert: %v", err)
	}
}
