package db_test

import (
	"context"
	"testing"

	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
)

func TestLatestVersionMatchesEmbeddedMigrations(t *testing.T) {
	latest, err := db.LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	if latest < 19 {
		t.Fatalf("latest = %d, want >= 19", latest)
	}
}

func TestSchemaCurrent(t *testing.T) {
	ctx := context.Background()
	latest, err := db.LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	pool := dbtest.Pool(t)
	current, ahead, applied, gotLatest, err := db.SchemaCurrent(ctx, pool)
	if err != nil || !current || ahead || applied != latest || gotLatest != latest {
		t.Fatalf("migrated: current=%v ahead=%v applied=%d latest=%d err=%v", current, ahead, applied, gotLatest, err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)", latest+5); err != nil {
		t.Fatal(err)
	}
	current, ahead, _, _, err = db.SchemaCurrent(ctx, pool)
	if err != nil || !current || !ahead {
		t.Fatalf("ahead: current=%v ahead=%v err=%v", current, ahead, err)
	}
	if _, err := pool.Exec(ctx, "DROP TABLE goose_db_version"); err != nil {
		t.Fatal(err)
	}
	v, err := db.AppliedVersion(ctx, pool)
	if err != nil || v != 0 {
		t.Fatalf("missing goose table must read as version 0, got %d, %v", v, err)
	}
}

func TestAppRoleCanReadSchemaVersion(t *testing.T) {
	ctx := context.Background()
	latest, _ := db.LatestVersion()
	app := dbtest.AppPool(t)
	v, err := db.AppliedVersion(ctx, app)
	if err != nil || v != latest {
		t.Fatalf("app role applied version = %d, %v; want %d", v, err, latest)
	}
}
