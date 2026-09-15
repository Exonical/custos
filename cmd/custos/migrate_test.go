package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestMigrateStatus(t *testing.T) {
	dsn := dbtest.URL(t)
	var stdout, stderr bytes.Buffer
	env := map[string]string{
		"CUSTOS_DEV_MODE":           "true",
		"CUSTOS_SERVER__TLS__MODE":  "disabled",
		"CUSTOS_METRICS__TLS__MODE": "disabled",
		"CUSTOS_DATABASE__URL":      dsn,
		"CUSTOS_DATABASE__SSL_MODE": "disable",
	}
	code := run(context.Background(), []string{"migrate", "status"},
		&stdout, &stderr, func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		})
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "VERSION") ||
		!strings.Contains(stdout.String(), "1") {
		t.Fatalf("unexpected status output: %s", stdout.String())
	}
}

func TestMigrateUpAudits(t *testing.T) {
	dsn := dbtest.URL(t)
	var stdout, stderr bytes.Buffer
	env := map[string]string{
		"CUSTOS_DEV_MODE":           "true",
		"CUSTOS_SERVER__TLS__MODE":  "disabled",
		"CUSTOS_METRICS__TLS__MODE": "disabled",
		"CUSTOS_DATABASE__URL":      dsn,
		"CUSTOS_DATABASE__SSL_MODE": "disable",
	}
	lookup := func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
	if code := run(context.Background(), []string{"migrate", "up"},
		&stdout, &stderr, lookup); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var action string
	if err := pool.QueryRow(context.Background(),
		"SELECT action FROM audit_events WHERE action='schema.migrate'").
		Scan(&action); err != nil {
		t.Fatalf("schema.migrate audit row missing: %v", err)
	}
	if _, err := pgaudit.Verify(context.Background(), pool, "platform"); err != nil {
		t.Fatalf("audit chain: %v", err)
	}
}
