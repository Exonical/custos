package main

import (
	"context"
	"fmt"
	"io"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/log"
)

func cmdMigrate(ctx context.Context, configPath string, args []string, lookupEnv config.LookupEnv, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: custos migrate <up|down|status>")
		return 2
	}
	var cmd db.MigrateCommand
	switch args[0] {
	case "up":
		cmd = db.Up
	case "down":
		cmd = db.Down
	case "status":
		cmd = db.Status
	default:
		_, _ = fmt.Fprintf(stderr, "unknown migrate command %q\n", args[0])
		return 2
	}

	cfg, err := config.Load(configPath, lookupEnv)
	if err != nil {
		reportConfigError(stderr, err)
		return 1
	}
	logger, lerr := log.New(cfg.Log, stderr)
	if lerr != nil {
		_, _ = fmt.Fprintf(stderr, "log setup: %v\n", lerr)
		return 1
	}
	// Migrate opens the pool without the schema-presence check.
	pool, err := db.OpenForMigrate(ctx, cfg.Database, logger)
	if err != nil {
		logger.ErrorContext(ctx, "database unavailable", "error", err)
		return 1
	}
	defer pool.Close()

	recorder := audit.Multi{pgaudit.New(pool), audit.SlogRecorder{Logger: logger}}

	var fromVersion int64
	if cmd != db.Status {
		_ = pool.QueryRow(ctx,
			"SELECT coalesce(max(version_id),0) FROM goose_db_version WHERE is_applied").
			Scan(&fromVersion) // table may not exist yet on first run
	}

	applied, err := db.Migrate(ctx, pool, cmd, stdout)
	if err != nil {
		recordMigrate(ctx, recorder, audit.Event{
			Action: "schema.migrate",
			Result: audit.ResultError,
			Details: map[string]any{
				"direction":    args[0],
				"from_version": fromVersion,
				"error_class":  apperr.KindOf(err).String(),
			},
		}, logger)
		logger.ErrorContext(ctx, "migrate failed", "error", err)
		return 1
	}
	if cmd != db.Status {
		var toVersion int64
		if n := len(applied); n > 0 {
			toVersion = applied[n-1]
			if cmd == db.Down {
				toVersion = fromVersion // rolled back applied[0]
			}
		} else {
			toVersion = fromVersion
		}
		var verList []any
		for _, v := range applied {
			verList = append(verList, v)
		}
		recordMigrate(ctx, recorder, audit.Event{
			Action: "schema.migrate",
			Result: audit.ResultAllow,
			Details: map[string]any{
				"direction":    args[0],
				"from_version": fromVersion,
				"to_version":   toVersion,
				"applied":      verList,
			},
		}, logger)
	}

	if cmd == db.Up && cfg.Database.AppRole != "" {
		if err := db.GrantAppRole(ctx, pool, cfg.Database.AppRole); err != nil {
			logger.ErrorContext(ctx, "grant app role", "error", err)
			return 1
		}
		recordMigrate(ctx, recorder, audit.Event{
			Action:  "schema.grant",
			Result:  audit.ResultAllow,
			Details: map[string]any{"role": cfg.Database.AppRole},
		}, logger)
	}
	return 0
}

// recordMigrate writes the audit event; failures are logged, not fatal —
// the migration itself already succeeded/failed.
func recordMigrate(ctx context.Context, r audit.Recorder, e audit.Event, logger interface {
	ErrorContext(context.Context, string, ...any)
}) {
	e.Actor = audit.Actor{Type: audit.ActorSystem, ID: "custos"}
	if err := r.Record(ctx, e); err != nil {
		logger.ErrorContext(ctx, "audit record failed", "error", err)
	}
}
