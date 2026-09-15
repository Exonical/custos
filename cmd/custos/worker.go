package main

import (
	"context"
	"fmt"
	"io"
	"os/signal"
	"syscall"
	"time"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/log"
	"github.com/Exonical/custos/internal/platform/otel"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

func cmdWorker(parent context.Context, configPath string, lookupEnv config.LookupEnv, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(configPath, lookupEnv)
	if err != nil {
		reportConfigError(stderr, err)
		return 1
	}
	logger, err := log.New(cfg.Log, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "log setup: %v\n", err)
		return 1
	}
	prov, err := otel.Setup(ctx, cfg.Telemetry, version, commit)
	if err != nil {
		logger.ErrorContext(ctx, "telemetry setup", "error", err)
		return 1
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = prov.Shutdown(sctx)
	}()

	pool, err := db.Open(ctx, cfg.Database, logger)
	if err != nil {
		logger.ErrorContext(ctx, "database unavailable", "error", err)
		return 1
	}
	defer pool.Close()

	recorder := audit.Multi{pgaudit.New(pool), audit.SlogRecorder{Logger: logger}}

	q := workqueue.New(pool, cfg.Worker, logger,
		workqueue.WithMeterProvider(prov.Meter),
		workqueue.WithAuditor(recorder))
	registerBuiltins(q, pool, recorder)

	// Ensure audit partitions stay ahead; dedupe makes this idempotent.
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "maintenance.partitions",
		Key:  "audit_events",
	}); err != nil {
		logger.ErrorContext(ctx, "enqueue maintenance.partitions", "error", err)
		return 1
	}

	g, gctx := errgroup.WithContext(ctx)
	if err := runMetrics(gctx, g, cfg, prov.PromRegistry, logger); err != nil {
		logger.ErrorContext(ctx, "metrics setup", "error", err)
		return 1
	}
	g.Go(func() error { return q.Run(gctx) })

	logger.InfoContext(ctx, "worker started")
	gerr := g.Wait()

	reason, result := "signal", audit.ResultAllow
	if gerr != nil {
		reason, result = "error", audit.ResultError
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := recorder.Record(sctx, audit.Event{
		Actor:  audit.Actor{Type: audit.ActorSystem, ID: "custos"},
		Action: "system.stopped",
		Result: result,
		Details: map[string]any{
			"reason":    reason,
			"component": "worker",
		},
	}); err != nil {
		logger.ErrorContext(sctx, "audit system.stopped failed", "error", err)
	}
	if gerr != nil {
		logger.ErrorContext(ctx, "worker exited", "error", gerr)
		return 1
	}
	logger.InfoContext(ctx, "worker stopped")
	return 0
}

// registerBuiltins wires the built-in maintenance handlers.
func registerBuiltins(q *workqueue.Queue, pool *pgxpool.Pool, recorder audit.Recorder) {
	q.Register("maintenance.noop", func(_ context.Context, _ workqueue.Item) error {
		return nil
	})
	q.Register("maintenance.partitions", func(ctx context.Context, _ workqueue.Item) error {
		created, err := db.EnsureMonthlyPartitions(ctx, pool, "audit_events", time.Now(), 3)
		if err != nil {
			return err
		}
		if len(created) == 0 {
			return nil
		}
		names := make([]any, len(created))
		for i, n := range created {
			names[i] = n
		}
		return recorder.Record(ctx, audit.Event{
			Actor:  audit.Actor{Type: audit.ActorSystem, ID: "custos"},
			Action: "maintenance.partitions",
			Result: audit.ResultAllow,
			Details: map[string]any{
				"table":   "audit_events",
				"created": names,
			},
		})
	})
}
