package db

import (
	"context"
	"fmt"
	"io"

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
