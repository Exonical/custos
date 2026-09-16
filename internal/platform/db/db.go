// Package db owns the pgx pool, transaction helper, tenant-scope setting,
// migration runner, and health checker. It is platform infrastructure;
// domain repositories build on it.
package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/config"
)

// Open builds, pings, and preflights a pool from cfg. The DSN is never
// logged.
func Open(ctx context.Context, cfg config.Database, logger *slog.Logger) (*pgxpool.Pool, error) {
	return open(ctx, cfg, logger, false)
}

// OpenForMigrate is Open without the schema-presence check (the point of
// migrate is to create it).
func OpenForMigrate(ctx context.Context, cfg config.Database, logger *slog.Logger) (*pgxpool.Pool, error) {
	return open(ctx, cfg, logger, true)
}

func open(ctx context.Context, cfg config.Database, logger *slog.Logger, skipSchema bool) (*pgxpool.Pool, error) {
	dsn := cfg.URL.Reveal()
	// Validation guarantees no ssl* params in the URL; set sslmode here so
	// the fail-closed rule cannot be bypassed.
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.Invalid, "db.config", "invalid database url")
	}
	q := u.Query()
	q.Set("sslmode", cfg.SSLMode)
	u.RawQuery = q.Encode()

	pcfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, apperr.Wrap(err, apperr.Invalid, "db.config", "invalid database config")
	}
	pcfg.MaxConns = cfg.MaxConns
	pcfg.MinConns = cfg.MinConns
	pcfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	pcfg.ConnConfig.RuntimeParams["application_name"] = "custos"
	pcfg.ConnConfig.RuntimeParams["statement_timeout"] =
		fmt.Sprintf("%d", cfg.StatementTimeout.Milliseconds())
	pcfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"

	if cfg.TLS.RootCAFile != "" || cfg.TLS.CertFile != "" {
		tlsCfg, err := buildTLS(cfg.TLS, cfg.SSLMode)
		if err != nil {
			return nil, err
		}
		// verify-full needs an explicit ServerName once we own TLSConfig.
		if cfg.SSLMode == "verify-full" {
			tlsCfg.ServerName = pcfg.ConnConfig.Host
		}
		pcfg.ConnConfig.TLSConfig = tlsCfg
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.Unavailable, "db.unavailable", "cannot create connection pool")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, apperr.Wrap(err, apperr.Unavailable, "db.unavailable", "database unreachable")
	}
	if err := Preflight(ctx, pool, cfg, skipSchema); err != nil {
		pool.Close()
		return nil, err
	}
	logger.DebugContext(ctx, "database pool opened",
		"max_conns", cfg.MaxConns, "ssl_mode", cfg.SSLMode)
	return pool, nil
}

// buildTLS assembles the client TLS config from PEM files. sslmode was
// already applied to the URL; here we attach the material.
func buildTLS(t config.DatabaseTLS, sslMode string) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS13}
	if t.RootCAFile != "" {
		pem, err := os.ReadFile(t.RootCAFile) // #nosec G304 -- operator-supplied config path
		if err != nil {
			return nil, apperr.Wrap(err, apperr.Invalid, "db.config", "cannot read database.tls.root_ca_file")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, apperr.New(apperr.Invalid, "db.config", "root_ca_file has no PEM certificates")
		}
		tc.RootCAs = pool
	}
	if t.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile) // #nosec G304 -- operator paths
		if err != nil {
			return nil, apperr.Wrap(err, apperr.Invalid, "db.config", "cannot load database.tls cert/key")
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	// verify-ca/verify-full semantics: pgx checks the server name itself
	// when TLSConfig.ServerName is empty it uses the host. verify-ca skips
	// the hostname check.
	if sslMode == "verify-ca" {
		tc.InsecureSkipVerify = true // #nosec G402 -- verify-ca: CA verified via VerifyConnection below
		// VerifyConnection runs on session resumption too, so it cannot
		// bypass the CA check (unlike VerifyPeerCertificate).
		tc.VerifyConnection = func(cs tls.ConnectionState) error {
			return verifyCAOnly(tc.RootCAs, cs.PeerCertificates)
		}
	}
	return tc, nil
}

// verifyCAOnly verifies the chain against roots without checking the
// server hostname (libpq verify-ca semantics).
func verifyCAOnly(roots *x509.CertPool, certs []*x509.Certificate) error {
	if len(certs) == 0 {
		return errors.New("db: server presented no certificate")
	}
	opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
	for _, c := range certs[1:] {
		opts.Intermediates.AddCert(c)
	}
	_, err := certs[0].Verify(opts)
	return err
}

// Preflight enforces the hardened-PostgreSQL baseline (ADR-013) at
// startup. Failures are Kind Unavailable, code "db.preflight", with one
// Detail per check; messages never include the DSN.
func Preflight(ctx context.Context, pool *pgxpool.Pool, cfg config.Database, skipSchema bool) error {
	var fails []apperr.Detail

	var verStr string
	if err := pool.QueryRow(ctx, "SHOW server_version_num").Scan(&verStr); err != nil {
		fails = append(fails, apperr.Detail{Field: "server_version", Reason: err.Error()})
	} else if ver, perr := strconv.Atoi(verStr); perr != nil || ver < 160000 {
		fails = append(fails, apperr.Detail{Field: "server_version", Reason: "PostgreSQL >= 16 required"})
	}

	if cfg.RequireSCRAM {
		var enc string
		if err := pool.QueryRow(ctx, "SHOW password_encryption").Scan(&enc); err != nil {
			fails = append(fails, apperr.Detail{Field: "password_encryption", Reason: err.Error()})
		} else if enc != "scram-sha-256" {
			fails = append(fails, apperr.Detail{Field: "password_encryption", Reason: "server password_encryption is " + enc + "; want scram-sha-256"})
		}
		// Our own credential must be scram-hashed when we can see it
		// (superuser). App roles can't read pg_authid -> skip silently.
		var rolpw *string
		err := pool.QueryRow(ctx,
			"SELECT rolpassword FROM pg_authid WHERE rolname = current_user").Scan(&rolpw)
		if err == nil && rolpw != nil && !strings.HasPrefix(*rolpw, "SCRAM-SHA-256$") {
			fails = append(fails, apperr.Detail{Field: "credential", Reason: "role password is not scram-sha-256 hashed"})
		}
	}

	if cfg.SSLMode != "disable" {
		var ssl string
		if err := pool.QueryRow(ctx,
			"SELECT current_setting('ssl', true)").Scan(&ssl); err == nil && ssl != "on" {
			fails = append(fails, apperr.Detail{Field: "ssl", Reason: "server ssl is off but ssl_mode=" + cfg.SSLMode})
		}
	}

	if !skipSchema {
		var one int
		if err := pool.QueryRow(ctx,
			"SELECT 1 FROM pg_extension WHERE extname='pgcrypto'").Scan(&one); err != nil {
			fails = append(fails, apperr.Detail{Field: "schema", Reason: "run custos migrate up"})
		}
	}

	if len(fails) > 0 {
		return &apperr.Error{
			Kind:    apperr.Unavailable,
			Code:    "db.preflight",
			Message: "database preflight failed",
			Details: fails,
		}
	}
	return nil
}

var roleRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// GrantAppRole grants the least-privilege runtime role its DML rights
// (ADR-013). role is validated and identifier-quoted.
func GrantAppRole(ctx context.Context, pool *pgxpool.Pool, role string) error {
	if !roleRE.MatchString(role) {
		return apperr.New(apperr.Invalid, "db.grant", "invalid role name")
	}
	q := pgx.Identifier{role}.Sanitize()
	stmts := []string{
		"GRANT USAGE ON SCHEMA public TO " + q,
		// ALL TABLES covers every table present now; grants on the
		// partitioned parent propagate to partitions. The default
		// privileges below cover tables the granting role creates later.
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO " + q,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public " +
			"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO " + q,
		// Append-only audit: no mutation, ever (trigger also enforces).
		"REVOKE UPDATE, DELETE, TRUNCATE ON audit_events FROM " + q,
		// Migration bookkeeping is read-only for the app role.
		"REVOKE ALL ON goose_db_version FROM " + q,
		"GRANT SELECT ON goose_db_version TO " + q,
		"REVOKE ALL ON audit_events FROM PUBLIC",
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			return MapError(err)
		}
	}
	return nil
}

// WithTx runs fn inside a transaction, committing on success and rolling
// back on error or panic (the panic is re-raised after rollback).
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MapError(err)
	}
	defer func() {
		// Rollback is a no-op after Commit; on panic it still runs because
		// deferred functions execute during unwinding.
		_ = tx.Rollback(ctx)
	}()
	if err := fn(tx); err != nil {
		return err
	}
	return MapError(tx.Commit(ctx))
}

// SetTenant sets the transaction-local tenant id used by RLS policies.
// set_config with is_local=true is the bind-parameter-safe form of
// SET LOCAL.
func SetTenant(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error {
	_, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID.String())
	return MapError(err)
}

// SetPlatformScope sets the platform-wide RLS scope ('*') on tx.
func SetPlatformScope(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', '*', true)")
	return MapError(err)
}

// MapError translates driver errors into apperr kinds: unique_violation
// -> Conflict, context deadline/cancel -> Unavailable. Anything else is
// returned unchanged.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return apperr.New(apperr.Conflict, "conflict", "resource conflict")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return apperr.Wrap(err, apperr.Unavailable, "db.unavailable", "database operation timed out")
	}
	return err
}

// Checker is the required health check for the database.
type Checker struct {
	pool *pgxpool.Pool
}

// NewChecker returns a health.Checker named "db" running SELECT 1.
func NewChecker(pool *pgxpool.Pool) *Checker { return &Checker{pool: pool} }

// Name implements health.Checker.
func (c *Checker) Name() string { return "db" }

// Check implements health.Checker.
func (c *Checker) Check(ctx context.Context) error {
	var one int
	return c.pool.QueryRow(ctx, "SELECT 1").Scan(&one)
}

var identRE = regexp.MustCompile(`^[a-z_]+$`)

// ExecQueryer is satisfied by pgx.Tx and *pgxpool.Pool.
type ExecQueryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// EnsureMonthlyPartitions creates monthly partitions of parent covering
// the month containing `from` (UTC) through monthsAhead-1 further months
// and returns the names of partitions it created. parent is the only
// interpolated identifier and is regex-guarded.
func EnsureMonthlyPartitions(ctx context.Context, ex ExecQueryer,
	parent string, from time.Time, monthsAhead int) ([]string, error) {
	if !identRE.MatchString(parent) {
		return nil, apperr.New(apperr.Invalid, "db.partition", "invalid partition parent name")
	}
	from = from.UTC()
	y, m, _ := from.Date()
	start := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	var created []string
	for i := 0; i < monthsAhead; i++ {
		lo := start.AddDate(0, i, 0)
		hi := lo.AddDate(0, 1, 0)
		name := fmt.Sprintf("%s_%04d%02d", parent, lo.Year(), int(lo.Month()))
		var exists *string
		if err := ex.QueryRow(ctx,
			"SELECT to_regclass($1)::text", "public."+name).Scan(&exists); err != nil {
			return created, MapError(err)
		}
		if exists != nil {
			continue
		}
		stmt := fmt.Sprintf(
			"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
			name, parent,
			lo.Format("2006-01-02"), hi.Format("2006-01-02"))
		if _, err := ex.Exec(ctx, stmt); err != nil {
			return created, MapError(err)
		}
		created = append(created, name)
	}
	return created, nil
}
