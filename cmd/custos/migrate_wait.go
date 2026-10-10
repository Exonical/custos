package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/log"
)

type waitOptions struct {
	timeout  time.Duration
	interval time.Duration
}

// parseWaitArgs parses `custos migrate wait` flags.
func parseWaitArgs(args []string, stderr io.Writer) (waitOptions, error) {
	fs := flag.NewFlagSet("migrate wait", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := waitOptions{}
	fs.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "give up after this long")
	fs.DurationVar(&opts.interval, "interval", 2*time.Second, "poll interval")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if opts.timeout <= 0 {
		return opts, fmt.Errorf("--timeout must be positive")
	}
	if opts.interval <= 0 {
		return opts, fmt.Errorf("--interval must be positive")
	}
	return opts, nil
}

// cmdMigrateWait blocks until the database schema has reached the newest
// migration embedded in this binary, so pods of an orchestrated rollout can
// start only after the migration Job finished. It is read-only (no audit
// event) and works with the least-privilege app role, which can SELECT
// goose_db_version.
func cmdMigrateWait(ctx context.Context, configPath string, args []string, lookupEnv config.LookupEnv, stderr io.Writer) int {
	opts, err := parseWaitArgs(args, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "usage: custos migrate wait [--timeout 10m] [--interval 2s]: %v\n", err)
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
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	var lastErr error
	for {
		pool, openErr := db.OpenForMigrate(ctx, cfg.Database, logger)
		if openErr == nil {
			current, ahead, applied, latest, err := db.SchemaCurrent(ctx, pool)
			pool.Close()
			if err == nil && current {
				if ahead {
					logger.WarnContext(ctx, "database schema is newer than this binary",
						"applied", applied, "latest", latest)
				}
				logger.InfoContext(ctx, "database schema is current", "applied", applied, "latest", latest)
				return 0
			}
			if err == nil {
				logger.InfoContext(ctx, "waiting for migrations", "applied", applied, "latest", latest)
			}
			lastErr = err
		} else {
			lastErr = openErr
			logger.WarnContext(ctx, "database not reachable yet", "error", openErr)
		}
		select {
		case <-ctx.Done():
			logger.ErrorContext(context.Background(), "timed out waiting for migrations",
				"timeout", opts.timeout.String(), "last_error", fmt.Sprint(lastErr))
			return 1
		case <-time.After(opts.interval):
		}
	}
}
