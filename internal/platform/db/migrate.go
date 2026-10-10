package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db/migrations"
)

// MigrateCommand selects the migration direction.
type MigrateCommand int

// Migration commands.
const (
	Up MigrateCommand = iota
	Down
	Status
)

// Migrate runs goose migrations and reports applied versions. Down rolls
// back exactly one migration. Status prints the migration table to out.
func Migrate(ctx context.Context, pool *pgxpool.Pool, cmd MigrateCommand, out io.Writer) ([]int64, error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.Internal, "db.migrate", "cannot create migration provider")
	}
	switch cmd {
	case Up:
		res, err := provider.Up(ctx)
		if err != nil {
			return nil, apperr.Wrap(err, apperr.Unavailable, "db.migrate", "migration up failed")
		}
		var applied []int64
		for _, r := range res {
			applied = append(applied, r.Source.Version)
		}
		return applied, nil
	case Down:
		res, err := provider.Down(ctx)
		if err != nil {
			return nil, apperr.Wrap(err, apperr.Unavailable, "db.migrate", "migration down failed")
		}
		return []int64{res.Source.Version}, nil
	case Status:
		statuses, err := provider.Status(ctx)
		if err != nil {
			return nil, apperr.Wrap(err, apperr.Unavailable, "db.migrate", "migration status failed")
		}
		_, _ = fmt.Fprintf(out, "%-8s %-24s %s\n", "VERSION", "APPLIED", "STATE")
		for _, s := range statuses {
			applied := "-"
			if s.State == goose.StateApplied {
				applied = s.AppliedAt.Format("2006-01-02 15:04:05")
			}
			_, _ = fmt.Fprintf(out, "%-8d %-24s %s\n",
				s.Source.Version, applied, s.State)
		}
		return nil, nil
	}
	return nil, apperr.New(apperr.Invalid, "db.migrate", "unknown command")
}

// LatestVersion returns the highest migration version embedded in this
// binary. Goose versions are the numeric file-name prefixes.
func LatestVersion() (int64, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return 0, apperr.Wrap(err, apperr.Internal, "db.migrate", "cannot read embedded migrations")
	}
	var latest int64
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			continue
		}
		latest = max(latest, v)
	}
	if latest == 0 {
		return 0, apperr.New(apperr.Internal, "db.migrate", "no embedded migrations")
	}
	return latest, nil
}

// AppliedVersion returns the highest applied goose version, or 0 when the
// goose table does not exist yet (nothing migrated). It only needs SELECT
// on goose_db_version, which the least-privilege app role holds.
func AppliedVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var v int64
	err := pool.QueryRow(ctx,
		"SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied").Scan(&v)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table
			return 0, nil
		}
		return 0, MapError(err)
	}
	return v, nil
}

// SchemaCurrent reports whether the applied version has reached the latest
// embedded one. ahead is true when the database is newer than this binary.
func SchemaCurrent(ctx context.Context, pool *pgxpool.Pool) (current, ahead bool, applied, latest int64, err error) {
	latest, err = LatestVersion()
	if err != nil {
		return false, false, 0, 0, err
	}
	applied, err = AppliedVersion(ctx, pool)
	if err != nil {
		return false, false, 0, latest, err
	}
	return applied >= latest, applied > latest, applied, latest, nil
}
