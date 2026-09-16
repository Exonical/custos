// Package dbtest provides a real PostgreSQL pool per test: it uses
// CUSTOS_TEST_DATABASE_URL when set (CI service container), else an
// embedded PostgreSQL 16 started once per test binary and hardened to
// the scram baseline (ADR-013). One migrated template database is
// created per binary; each test clones it with CREATE DATABASE TEMPLATE
// and drops its copy on cleanup, so tests may run t.Parallel() and no
// TRUNCATE is needed. All pools go through db.Open so Preflight runs in
// every DB test.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db"
)

// appRoleName is a non-superuser LOGIN role used by AppPool; it owns no
// tables and cannot read pg_authid, so RLS and grant tests see the real
// least-privilege path.
const appRoleName = "custos_test_app"

var (
	startOnce sync.Once
	embedErr  error
	embedDB   *embeddedpostgres.EmbeddedPostgres
	adminDSN  string
	appPass   string
	tempDir   string
	dataDir   string
	tmplName  string
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// Main runs m.Run() and tears down the embedded server and template
// database. Packages using Pool, AppPool or URL must call it:
//
//	func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
func Main(m *testing.M) int {
	code := m.Run()
	if adminDSN != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if ap, err := pgxpool.New(ctx, adminDSN); err == nil {
			_, _ = ap.Exec(ctx,
				fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, tmplName))
			ap.Close()
		}
	}
	if embedDB != nil {
		_ = embedDB.Stop()
	}
	if tempDir != "" {
		_ = os.RemoveAll(tempDir)
	}
	return code
}

func adminURL(tb testing.TB) string {
	tb.Helper()
	if u, ok := os.LookupEnv("CUSTOS_TEST_DATABASE_URL"); ok {
		adminDSN = u
		startOnce.Do(func() { embedErr = prepareTemplate() })
		if embedErr != nil {
			tb.Fatalf("prepare template db: %v", embedErr)
		}
		return adminDSN
	}
	startOnce.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			embedErr = err
			return
		}
		p := ln.Addr().(*net.TCPAddr).Port
		if p <= 0 || p > 65535 {
			embedErr = fmt.Errorf("listener port %d out of range", p)
			return
		}
		port := uint32(p)
		_ = ln.Close()

		tempDir, err = os.MkdirTemp("", "custos-pgtest-")
		if err != nil {
			embedErr = err
			return
		}
		dataDir = filepath.Join(tempDir, "data")
		// The downloaded .txz is cached once across test binaries;
		// binaries are extracted per-process because embedded-postgres'
		// presence check stats "bin/pg_ctl" without ".exe" on Windows and
		// would re-extract over a running server's locked files.
		cache := filepath.Join(mustUserCache(), "custos", "embedded-postgres")
		cfg := embeddedpostgres.DefaultConfig().
			Version(embeddedpostgres.V16).
			Port(port).
			RuntimePath(filepath.Join(tempDir, "runtime")).
			DataPath(dataDir).
			CachePath(cache).
			BinariesPath(filepath.Join(tempDir, "binaries")).
			Logger(io.Discard)
		embedDB = embeddedpostgres.NewDatabase(cfg)
		// Serialize the first tarball download across concurrent
		// `go test` package binaries with a lock file.
		if err := withLock(cache+".lock", embedDB.Start); err != nil {
			embedErr = err
			return
		}
		adminDSN = fmt.Sprintf(
			"postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
		if err := hardenScram(adminDSN); err != nil {
			embedErr = err
			return
		}
		embedErr = prepareTemplate()
	})
	if embedErr != nil {
		tb.Fatalf("start embedded postgres: %v", embedErr)
	}
	return adminDSN
}

// prepareTemplate creates the custos_test_app role (scram password) and
// one fully migrated template database. CREATE DATABASE ... TEMPLATE
// requires no connections to the template, so the migrate pool is
// closed before this returns.
func prepareTemplate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		return err
	}
	defer admin.Close()

	var pw [24]byte
	if _, err := rand.Read(pw[:]); err != nil {
		return err
	}
	appPass = hex.EncodeToString(pw[:])
	if _, err := admin.Exec(ctx,
		"SET password_encryption='scram-sha-256'; "+
			"CREATE ROLE "+appRoleName+" LOGIN PASSWORD '"+appPass+"'"); err != nil {
		// Role may already exist on a shared CI server.
		if _, err2 := admin.Exec(ctx,
			"ALTER ROLE "+appRoleName+" PASSWORD '"+appPass+"'"); err2 != nil {
			return fmt.Errorf("create app role: %w (alter: %v)", err, err2)
		}
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

var hbaAuthRE = regexp.MustCompile(`\b(password|md5|trust|scram-sha-256)\b`)

// hardenScram enforces the scram baseline on the embedded server:
// embedded-postgres runs initdb with `-A password`, so we flip
// password_encryption, re-hash the role password, rewrite pg_hba.conf,
// and reload.
func hardenScram(dsn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx,
		"ALTER SYSTEM SET password_encryption = 'scram-sha-256'"); err != nil {
		return fmt.Errorf("alter system: %w", err)
	}
	if _, err := pool.Exec(ctx, "SELECT pg_reload_conf()"); err != nil {
		return err
	}
	// Also SET in-session: pg_reload_conf signals SIGHUP asynchronously,
	// so ALTER SYSTEM may not be visible yet on the next statement.
	// Rehash under scram (password_encryption is a USERSET GUC).
	if _, err := pool.Exec(ctx,
		"SET password_encryption='scram-sha-256'; ALTER ROLE postgres PASSWORD 'postgres'"); err != nil {
		return fmt.Errorf("rehash role: %w", err)
	}

	hba := filepath.Join(dataDir, "pg_hba.conf")
	b, err := os.ReadFile(hba) // #nosec G304 -- test-owned data dir
	if err != nil {
		return fmt.Errorf("read pg_hba: %w", err)
	}
	b = hbaAuthRE.ReplaceAll(b, []byte("scram-sha-256"))
	if err := os.WriteFile(hba, b, 0o600); err != nil { // #nosec G703 -- test-owned data dir
		return fmt.Errorf("write pg_hba: %w", err)
	}
	if _, err := pool.Exec(ctx, "SELECT pg_reload_conf()"); err != nil {
		return err
	}
	return nil
}

func mustUserCache() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return os.TempDir()
	}
	return dir
}

// withLock runs fn holding a cross-process lockfile at path; locks older
// than 10 minutes are treated as stale (crashed holder) and removed.
func withLock(path string, fn func() error) error {
	for {
		// Path is derived from our own cache dir, not user input.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304
		if err == nil {
			_ = f.Close()
			break
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > 10*time.Minute {
			_ = os.Remove(path)
			continue
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer func() { _ = os.Remove(path) }()
	return fn()
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
		appRoleName, appPass, acfg.ConnConfig.Host, acfg.ConnConfig.Port,
		acfg.ConnConfig.Database)
	pool, err := db.Open(ctx, testConfig(appDSN), discard)
	if err != nil {
		tb.Fatalf("app pool: %v", err)
	}
	tb.Cleanup(pool.Close)
	return pool
}
