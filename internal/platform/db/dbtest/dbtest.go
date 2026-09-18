// Package dbtest provides a real PostgreSQL pool per test against the
// server named by CUSTOS_TEST_DATABASE_URL (the Podman test stack from
// scripts/testdb.sh locally, the CI service in CI; ADR-013). One
// migrated template database is created per test binary; each test
// clones it with CREATE DATABASE TEMPLATE and drops its copy on
// cleanup, so tests may run t.Parallel() and no TRUNCATE is needed.
// All pools go through db.Open so Preflight runs in every DB test.
//
// With the env var unset, Pool/URL/AppPoolOn t.Skip (unless
// CUSTOS_TEST_REQUIRE_DB=1, which fails fast for CI).
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db"
)

// appRoleName is a non-superuser LOGIN role used by AppPool; it owns no
// tables and cannot read pg_authid, so RLS and grant tests see the real
// least-privilege path.
const appRoleName = "custos_test_app"

// appRolePassword is fixed because roles are global to the shared test
// server: every test binary connecting concurrently must agree on one
// password. The role exists only on throwaway test servers.
const appRolePassword = "custos-test-app-pw" // #nosec G101 -- throwaway test-server role, not a production credential

const noDBMsg = `CUSTOS_TEST_DATABASE_URL is not set: database tests are skipped.
Start the test stack and export the URL first:

    bash scripts/testdb.sh up
    export CUSTOS_TEST_DATABASE_URL="$(bash scripts/testdb.sh url)"
    # PowerShell: $env:CUSTOS_TEST_DATABASE_URL = (bash scripts/testdb.sh url)
`

var (
	startOnce sync.Once
	prepErr   error
	adminDSN  string
	tmplName  string
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// Main runs m.Run() and tears down the template database. Packages
// using Pool, AppPool or URL must call it:
//
//	func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
func Main(m *testing.M) int {
	if _, ok := os.LookupEnv("CUSTOS_TEST_DATABASE_URL"); !ok &&
		os.Getenv("CUSTOS_TEST_REQUIRE_DB") != "1" {
		fmt.Fprint(os.Stderr, "dbtest: "+noDBMsg)
	}
	code := m.Run()
	if adminDSN != "" && tmplName != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ap, err := pgxpool.New(ctx, adminDSN); err == nil {
			_, _ = ap.Exec(ctx,
				fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, tmplName))
			ap.Close()
		}
	}
	return code
}

// requireDB returns false (after Skip or Fatal) when no test database
// is configured.
func requireDB(tb testing.TB) bool {
	tb.Helper()
	if _, ok := os.LookupEnv("CUSTOS_TEST_DATABASE_URL"); !ok {
		if os.Getenv("CUSTOS_TEST_REQUIRE_DB") == "1" {
			tb.Fatal("CUSTOS_TEST_REQUIRE_DB=1 but " + noDBMsg)
		}
		tb.Skip("CUSTOS_TEST_DATABASE_URL is not set: database test skipped")
		return false
	}
	return true
}

func adminURL(tb testing.TB) string {
	tb.Helper()
	if !requireDB(tb) {
		return ""
	}
	adminDSN = os.Getenv("CUSTOS_TEST_DATABASE_URL")
	startOnce.Do(func() { prepErr = prepareTemplate() })
	if prepErr != nil {
		tb.Fatalf("prepare template db: %v", prepErr)
	}
	return adminDSN
}

// prepareTemplate preflights the server (PostgreSQL >= 16 and the
// scram-sha-256 baseline, via db.Preflight), creates the
// custos_test_app role (scram password) and one fully migrated
// template database. CREATE DATABASE ... TEMPLATE requires no
// connections to the template, so the migrate pool is closed before
// this returns.
func prepareTemplate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		return err
	}
	defer admin.Close()
	if err := db.Preflight(ctx, admin, testConfig(adminDSN), true); err != nil {
		return fmt.Errorf("test database server failed preflight "+
			"(want PostgreSQL >= 16 with password_encryption=scram-sha-256; "+
			"see scripts/testdb.sh): %w", err)
	}

	var pw [4]byte
	if _, err := rand.Read(pw[:]); err != nil {
		return err
	}
	// Serialize role create/alter across concurrent test binaries:
	// CREATE ROLE has no IF NOT EXISTS, and racing writers on pg_authid
	// fail with "tuple concurrently updated".
	if _, err := admin.Exec(ctx,
		"SELECT pg_advisory_lock(hashtext('custos dbtest role'))"); err != nil {
		return fmt.Errorf("role lock: %w", err)
	}
	_, roleErr := admin.Exec(ctx,
		"SET password_encryption='scram-sha-256'; "+
			"CREATE ROLE "+appRoleName+" LOGIN PASSWORD '"+appRolePassword+"'")
	if roleErr != nil {
		// Role may already exist on a shared server.
		_, roleErr = admin.Exec(ctx,
			"ALTER ROLE "+appRoleName+" PASSWORD '"+appRolePassword+"'")
	}
	if _, err := admin.Exec(ctx,
		"SELECT pg_advisory_unlock(hashtext('custos dbtest role'))"); err != nil && roleErr == nil {
		roleErr = err
	}
	if roleErr != nil {
		return fmt.Errorf("create app role: %w", roleErr)
	}

	tmplName = "custos_tmpl_" + hex.EncodeToString(pw[:4])
	if _, err := admin.Exec(ctx,
		fmt.Sprintf(`CREATE DATABASE %q`, tmplName)); err != nil {
		return err
	}
	acfg, err := pgxpool.ParseConfig(adminDSN)
	if err != nil {
		return err
	}
	c := acfg.ConnConfig
	tmplDSN := fmt.Sprintf("postgres://%s:%s@%s:%d/%s",
		c.User, c.Password, c.Host, c.Port, tmplName)
	mp, err := db.OpenForMigrate(ctx, testConfig(tmplDSN), discard)
	if err != nil {
		return fmt.Errorf("template pool: %w", err)
	}
	_, err = db.Migrate(ctx, mp, db.Up, io.Discard)
	mp.Close()
	return err
}

// testConfig returns a config.Database pointing at dsn with the
// scram baseline enforced via Preflight.
func testConfig(dsn string) config.Database {
	return config.Database{
		URL:              config.Secret(dsn),
		SSLMode:          "disable",
		RequireSCRAM:     true,
		MaxConns:         4,
		MinConns:         1,
		ConnectTimeout:   10 * time.Second,
		StatementTimeout: 30 * time.Second,
	}
}

// createDB clones the migrated template into a fresh database and
// returns its superuser DSN (no ssl* params — config.Validate rejects
// them; TLS is disabled for the loopback/CI test target).
func createDB(tb testing.TB) string {
	tb.Helper()
	admin := adminURL(tb)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, admin)
	if err != nil {
		tb.Fatalf("admin pool: %v", err)
	}
	dbName := "t_" + uuid.NewString()[:12]
	if _, err := adminPool.Exec(ctx,
		fmt.Sprintf(`CREATE DATABASE %q TEMPLATE %q`, dbName, tmplName)); err != nil {
		adminPool.Close()
		tb.Fatalf("create database: %v", err)
	}
	adminPool.Close()

	tb.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dcancel()
		ap, err := pgxpool.New(dctx, admin)
		if err != nil {
			tb.Logf("cleanup: admin pool: %v", err)
			return
		}
		defer ap.Close()
		if _, err := ap.Exec(dctx,
			fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, dbName)); err != nil {
			tb.Logf("cleanup: drop database: %v", err)
		}
	})

	acfg, err := pgxpool.ParseConfig(admin)
	if err != nil {
		tb.Fatalf("parse admin dsn: %v", err)
	}
	c := acfg.ConnConfig
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s",
		c.User, c.Password, c.Host, c.Port, dbName)
}

// URL creates a fresh migrated database (cloned from the template) and
// returns its superuser DSN.
func URL(tb testing.TB) string {
	tb.Helper()
	if testing.Short() {
		tb.Skip("requires PostgreSQL")
	}
	return createDB(tb)
}

// Pool returns a migrated, preflighted superuser pool on a fresh
// database.
func Pool(tb testing.TB) *pgxpool.Pool {
	tb.Helper()
	dsn := URL(tb)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testConfig(dsn), discard)
	if err != nil {
		tb.Fatalf("test pool: %v", err)
	}
	tb.Cleanup(pool.Close)
	return pool
}

// AppPool returns a pool on a fresh migrated database connecting as
// custos_test_app — a non-superuser with GrantAppRole privileges and no
// BYPASSRLS, so tests exercise the same path production roles take.
func AppPool(tb testing.TB) *pgxpool.Pool {
	tb.Helper()
	return AppPoolOn(tb, createDB(tb))
}

// AppPoolOn is AppPool on an existing test database (e.g. the DSN from
// URL), so tests can seed data as superuser and read it as the app role
// against the same database.
func AppPoolOn(tb testing.TB, dsn string) *pgxpool.Pool {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Grants run as the table owner via the superuser DSN.
	admin, err := db.Open(ctx, testConfig(dsn), discard)
	if err != nil {
		tb.Fatalf("app test admin pool: %v", err)
	}
	if err := db.GrantAppRole(ctx, admin, appRoleName); err != nil {
		admin.Close()
		tb.Fatalf("grant app role: %v", err)
	}
	admin.Close()

	acfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		tb.Fatalf("parse dsn: %v", err)
	}
	appDSN := fmt.Sprintf("postgres://%s:%s@%s:%d/%s",
		appRoleName, appRolePassword, acfg.ConnConfig.Host, acfg.ConnConfig.Port,
		acfg.ConnConfig.Database)
	pool, err := db.Open(ctx, testConfig(appDSN), discard)
	if err != nil {
		tb.Fatalf("app pool: %v", err)
	}
	tb.Cleanup(pool.Close)
	return pool
}
