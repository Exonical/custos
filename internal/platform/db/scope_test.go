package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/tenants"
)

func TestScopeTenant(t *testing.T) {
	scoped, explicit := uuid.New(), uuid.New()
	if got := db.ScopeTenant(tenants.TenantScope(scoped), explicit); got != scoped {
		t.Fatalf("tenant scope: got %s, want %s", got, scoped)
	}
	if got := db.ScopeTenant(tenants.PlatformScope(), explicit); got != explicit {
		t.Fatalf("platform scope: got %s, want %s", got, explicit)
	}
}

func TestApplyScope(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	tid := uuid.New()
	for _, tc := range []struct {
		name  string
		scope tenants.Scope
		want  string
	}{
		{"tenant", tenants.TenantScope(tid), tid.String()},
		{"platform", tenants.PlatformScope(), "*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if err := db.ApplyScope(ctx, tx, tc.scope); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := tx.QueryRow(ctx,
				"SELECT coalesce(current_setting('app.tenant_id', true), '')").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("app.tenant_id = %q, want %q", got, tc.want)
			}
		})
	}
}
