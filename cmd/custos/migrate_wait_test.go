package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/db/dbtest"
)

func TestParseWaitArgs(t *testing.T) {
	var sink bytes.Buffer
	opts, err := parseWaitArgs(nil, &sink)
	if err != nil || opts.timeout != 10*time.Minute || opts.interval != 2*time.Second {
		t.Fatalf("defaults: %+v %v", opts, err)
	}
	opts, err = parseWaitArgs([]string{"--timeout", "30s", "--interval=500ms"}, &sink)
	if err != nil || opts.timeout != 30*time.Second || opts.interval != 500*time.Millisecond {
		t.Fatalf("flags: %+v %v", opts, err)
	}
	for _, bad := range [][]string{
		{"--timeout", "0"}, {"--interval", "-1s"}, {"--timeout", "soon"}, {"extra"}, {"--nope"},
	} {
		if _, err := parseWaitArgs(bad, &sink); err == nil {
			t.Fatalf("args %v accepted", bad)
		}
	}
}

func TestMigrateWaitUsageAndConfigErrors(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"migrate", "wait", "--timeout", "0"}, &out, &errBuf, noEnv); code != 2 {
		t.Fatalf("bad flag exit %d, want 2", code)
	}
	errBuf.Reset()
	start := time.Now()
	// No database URL configured: a config error must fail immediately, not poll.
	if code := run(context.Background(), []string{"migrate", "wait", "--timeout", "30s"}, &out, &errBuf, noEnv); code != 1 {
		t.Fatalf("config error exit %d, want 1: %s", code, errBuf.String())
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("config errors must not wait for the timeout")
	}
}

func waitRun(t *testing.T, dsn string, args ...string) (int, string) {
	t.Helper()
	env := testEnv(dsn)
	var out, errBuf bytes.Buffer
	code := run(context.Background(), append([]string{"migrate", "wait"}, args...), &out, &errBuf,
		func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	return code, errBuf.String()
}

func blankDatabase(t *testing.T) string {
	t.Helper()
	_ = dbtest.URL(t) // skips (or requires) the test database
	admin := os.Getenv("CUSTOS_TEST_DATABASE_URL")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	name := "t_wait_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p, err := pgxpool.New(ctx, admin)
		if err != nil {
			return
		}
		defer p.Close()
		_, _ = p.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, name))
	})
	cfg := pool.Config().ConnConfig
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s", cfg.User, cfg.Password, cfg.Host, cfg.Port, name)
}

func TestMigrateWaitUnmigratedTimesOutThenSucceedsAfterUp(t *testing.T) {
	dsn := blankDatabase(t)
	code, stderr := waitRun(t, dsn, "--timeout", "1500ms", "--interval", "200ms")
	if code != 1 || !strings.Contains(stderr, "timed out waiting for migrations") {
		t.Fatalf("unmigrated: exit %d, stderr %s", code, stderr)
	}

	done := make(chan int, 1)
	go func() {
		c, _ := waitRun(t, dsn, "--timeout", "20s", "--interval", "200ms")
		done <- c
	}()
	time.Sleep(600 * time.Millisecond)
	env := testEnv(dsn)
	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"migrate", "up"}, &out, &errBuf,
		func(k string) (string, bool) { v, ok := env[k]; return v, ok }); code != 0 {
		t.Fatalf("migrate up exit %d: %s", code, errBuf.String())
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("wait returned %d after migrate up", code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("wait did not return after migrate up")
	}
}

func TestMigrateWaitMigratedAndAhead(t *testing.T) {
	dsn := dbtest.URL(t)
	if code, stderr := waitRun(t, dsn, "--timeout", "5s"); code != 0 {
		t.Fatalf("migrated database: exit %d: %s", code, stderr)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "INSERT INTO goose_db_version (version_id, is_applied) VALUES (99999, true)"); err != nil {
		t.Fatal(err)
	}
	code, stderr := waitRun(t, dsn, "--timeout", "5s")
	if code != 0 || !strings.Contains(stderr, "newer than this binary") {
		t.Fatalf("ahead database: exit %d, stderr %s", code, stderr)
	}
}

func TestMigrateWaitAsLeastPrivilegeAppRole(t *testing.T) {
	dsn := dbtest.URL(t)
	app := dbtest.AppPoolOn(t, dsn) // grants the runtime role as in production
	c := app.Config().ConnConfig
	appDSN := fmt.Sprintf("postgres://%s:%s@%s:%d/%s", c.User, c.Password, c.Host, c.Port, c.Database)
	if code, stderr := waitRun(t, appDSN, "--timeout", "5s"); code != 0 {
		t.Fatalf("app role: exit %d: %s", code, stderr)
	}
}
