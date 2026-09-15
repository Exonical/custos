// Package dbtest provides a real PostgreSQL pool per test: it uses
// CUSTOS_TEST_DATABASE_URL when set (CI service container), else an
// embedded PostgreSQL 16 started once per test binary and hardened to
// the scram baseline (ADR-013). Each test gets its own database
// (create, migrate, drop on cleanup), so tests may run t.Parallel() and
// no TRUNCATE is needed. All pools go through db.Open so Preflight runs
// in every DB test.
package dbtest

import (
	"context"
	"errors"
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

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db"
)

var (
	startOnce sync.Once
	embedErr  error
	embedDB   *embeddedpostgres.EmbeddedPostgres
	adminDSN  string
	tempDir   string
	dataDir   string
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// Main runs m.Run() and tears down the embedded server. Packages using
// Pool or URL must call it from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
func Main(m *testing.M) int {
	code := m.Run()
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
		return u
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
		if err := hardenScram(adminDSN, dataDir); err != nil {
			embedErr = err
		}
	})
	if embedErr != nil {
		tb.Fatalf("start embedded postgres: %v", embedErr)
	}
	return adminDSN
}

var hbaAuthRE = regexp.MustCompile(`\b(password|md5|trust|scram-sha-256)\b`)

// hardenScram enforces the scram baseline on the embedded server:
// embedded-postgres runs initdb with `-A password`, so we flip
// password_encryption, re-hash the role password, rewrite pg_hba.conf,
// and reload.
func hardenScram(dsn, dataDir string) error {
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

// URL creates a fresh migrated database and returns its DSN without any
// ssl* query parameters (config.Validate rejects those); TLS is disabled
// for the loopback/CI test target.
func URL(tb testing.TB) string {
	tb.Helper()
	if testing.Short() {
		tb.Skip("requires PostgreSQL")
	}
	admin := adminURL(tb)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, admin)
	if err != nil {
		tb.Fatalf("admin pool: %v", err)
	}
	dbName := "t_" + uuid.NewString()[:12]
	if _, err := adminPool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", dbName)); err != nil {
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
			fmt.Sprintf("DROP DATABASE IF EXISTS %q WITH (FORCE)", dbName)); err != nil {
			tb.Logf("cleanup: drop database: %v", err)
		}
	})

	acfg, err := pgxpool.ParseConfig(admin)
	if err != nil {
		tb.Fatalf("parse admin dsn: %v", err)
	}
	c := acfg.ConnConfig
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s",
		c.User, c.Password, c.Host, c.Port, dbName)

	// Migrate via OpenForMigrate (preflight minus the schema check).
	mp, err := db.OpenForMigrate(ctx, testConfig(dsn), discard)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			tb.Fatalf("migrate pool: %v details=%+v", err, ae.Details)
		}
		tb.Fatalf("migrate pool: %v", err)
	}
	defer mp.Close()
	if _, err := db.Migrate(ctx, mp, db.Up, io.Discard); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	return dsn
}

// Pool returns a migrated, preflighted pool on a fresh database.
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
