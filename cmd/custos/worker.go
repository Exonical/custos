package main

import (
	"context"
	"fmt"
	"io"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/accounting"
	accountpg "github.com/Exonical/custos/internal/accounting/postgres"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/authz"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	clustersync "github.com/Exonical/custos/internal/clusters/sync"
	"github.com/Exonical/custos/internal/executions/engine"
	execpg "github.com/Exonical/custos/internal/executions/postgres"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	jobssvc "github.com/Exonical/custos/internal/jobs/service"
	jobsworker "github.com/Exonical/custos/internal/jobs/worker"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/health"
	"github.com/Exonical/custos/internal/platform/log"
	"github.com/Exonical/custos/internal/platform/otel"
	"github.com/Exonical/custos/internal/platform/safehttp"
	"github.com/Exonical/custos/internal/platform/workqueue"
	policypg "github.com/Exonical/custos/internal/policies/postgres"
	policiesvc "github.com/Exonical/custos/internal/policies/service"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	scriptpg "github.com/Exonical/custos/internal/scripts/postgres"
	"github.com/Exonical/custos/internal/secretrefs"
	secretpg "github.com/Exonical/custos/internal/secretrefs/postgres"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
	wfpg "github.com/Exonical/custos/internal/workflows/postgres"
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

	// Cluster sync: self-rescheduling chain per cluster; bootstrap
	// enqueues every non-disabled cluster (dedupe makes it idempotent).
	sdeps, err := newSlurmDeps(ctx, cfg, logger, prov.Meter)
	if err != nil {
		logger.ErrorContext(ctx, "slurm setup", "error", err)
		return 1
	}
	if sdeps.OpenBao != nil {
		defer func() { _ = sdeps.OpenBao.Close() }()
	}
	secretRepo := secretpg.New(pool)
	tenantRepo := tenantpg.New(pool)
	secretRuntime := secretrefs.NewRuntime(secretRepo, &secretrefs.ConnectorFactory{
		Platform: sdeps.OpenBao,
		Policy: safehttp.DialPolicy{AllowPrivate: sdeps.Policy.AllowPrivate,
			AllowLoopback: sdeps.Policy.AllowLoopback, AllowHTTP: sdeps.Policy.AllowHTTP,
			DenyCIDRs: sdeps.Policy.DenyCIDRs}, Logger: logger, Meter: prov.Meter,
	}, sdeps.Resolver)
	defer func() { _ = secretRuntime.Close() }()
	platformNS := ""
	if cfg.Secrets.OpenBao != nil {
		platformNS = cfg.Secrets.OpenBao.Namespace
	}
	secretSvc := secretrefs.NewService(secretRepo, secretRuntime, authz.RBAC{},
		recorder, sdeps.OpenBao, platformNS, tenantRepo)
	secretSvc.SetMeterProvider(prov.Meter)
	clusterRepo := clusterpg.New(pool)
	q.Register(clustersync.Kind, clustersync.Handler(clusterRepo,
		sdeps.Factory, cfg.Worker.ClusterSyncInterval,
		clustersync.NewMetrics(prov.Meter)))
	if err := clustersync.Bootstrap(ctx, pool, clusterRepo); err != nil {
		logger.ErrorContext(ctx, "cluster.sync bootstrap", "error", err)
		return 1
	}
	accountDeps := accounting.WorkerDeps{Repo: accountpg.New(pool), Clusters: clusterRepo,
		Factory: sdeps.Factory, Metrics: accounting.NewMetrics(prov.Meter)}
	q.Register(accounting.KindCollect, accounting.Collect(accountDeps))
	q.Register(accounting.KindAggregate, accounting.Aggregate(accountDeps))
	if err := accounting.Bootstrap(ctx, pool, clusterRepo); err != nil {
		logger.ErrorContext(ctx, "accounting bootstrap", "error", err)
		return 1
	}

	// Job handlers (docs/workers.md): submit, reconcile, cancel, sweep.
	jdeps := jobsworker.Deps{
		Jobs:     jobpg.New(pool),
		Scripts:  scriptpg.New(pool),
		Clusters: clusterRepo,
		Factory:  sdeps.Factory,
		Exec:     pool,
		Execs:    execpg.New(pool),
		Audit:    recorder,
		Metrics:  jobsworker.NewMetrics(prov.Meter, jobpg.New(pool)),
		Secrets:  secretSvc,
	}
	q.Register(jobssvc.KindSubmit, jobsworker.Submit(jdeps))
	q.Register(jobsworker.KindReconcile, jobsworker.Reconcile(jdeps))
	q.Register(jobssvc.KindCancel, jobsworker.Cancel(jdeps))
	q.Register(jobsworker.KindSweep, jobsworker.Sweep(jdeps))
	q.Register(jobsworker.KindIdemExpire, jobsworker.IdempotencyExpire(jdeps))

	// Workflow execution engine (docs/workflows.md §Engine strategy).
	vdeps, err := newValidationDeps(cfg, pool, clusterRepo, prov, recorder)
	if err != nil {
		logger.ErrorContext(ctx, "validation setup", "error", err)
		return 1
	}
	projectRepo := projectpg.New(pool)
	execDeps := engine.Deps{
		Execs:           execpg.New(pool),
		Workflows:       wfpg.New(pool),
		SecretReference: secretSvc.ReferenceInfo,
		AuthorizeSecretUse: func(ctx context.Context, tenantID, projectID,
			userID uuid.UUID, name string) error {
			_, err := secretSvc.AuthorizeUserUse(ctx, tenantID, projectID, userID, name)
			return err
		},
		Policies:    policiesvc.NewService(policypg.New(pool), authz.RBAC{}, recorder),
		VPolicy:     vdeps.VPolicy,
		Clusters:    clusterRepo,
		Projects:    projectsvc.NewService(projectRepo, projectRepo, projectRepo, tenantRepo, clusterRepo, authz.RBAC{}, recorder),
		Pipeline:    vdeps.Pipeline,
		Validations: vdeps.Store,
		Scripts:     scriptpg.New(pool),
		Jobs:        jobpg.New(pool),
		Audit:       recorder,
		Metrics:     vdeps.Metrics,
	}
	q.Register(engine.KindAdvance, engine.Advance(execDeps))
	q.Register(engine.KindAdmit, engine.Admit(execDeps))
	if err := jobsworker.BootstrapSweep(ctx, pool, clusterRepo); err != nil {
		logger.ErrorContext(ctx, "jobs.sweep bootstrap", "error", err)
		return 1
	}
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: jobsworker.KindIdemExpire,
		Key:  "singleton",
	}); err != nil {
		logger.ErrorContext(ctx, "enqueue idempotency.expire", "error", err)
		return 1
	}

	// Ensure audit partitions stay ahead; dedupe makes this idempotent.
	if _, err := workqueue.Enqueue(ctx, pool, workqueue.EnqueueRequest{
		Kind: "maintenance.partitions",
		Key:  "audit_events",
	}); err != nil {
		logger.ErrorContext(ctx, "enqueue maintenance.partitions", "error", err)
		return 1
	}

	workerHealth := health.NewRegistry()
	workerHealth.Register(db.NewChecker(pool), true)
	if sdeps.OpenBao != nil {
		workerHealth.Register(sdeps.OpenBao, true)
	}
	g, gctx := errgroup.WithContext(ctx)
	if err := runMetrics(gctx, g, cfg, prov.PromRegistry, logger, workerHealth); err != nil {
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
	q.Register("tenant.delete", tenantDeleteHandler(pool, recorder))
	q.Register("maintenance.partitions", func(ctx context.Context, _ workqueue.Item) error {
		var all []string
		for _, table := range []string{"audit_events", "usage_records"} {
			created, err := db.EnsureMonthlyPartitions(ctx, pool, table, time.Now(), 3)
			if err != nil {
				return err
			}
			all = append(all, created...)
		}
		dropped, err := db.DropMonthlyPartitionsBefore(ctx, pool, "usage_records",
			time.Now().Add(-400*24*time.Hour))
		if err != nil {
			return err
		}
		all = append(all, dropped...)
		if len(all) == 0 {
			return nil
		}
		names := make([]any, len(all))
		for i, n := range all {
			names[i] = n
		}
		return recorder.Record(ctx, audit.Event{
			Actor:  audit.Actor{Type: audit.ActorSystem, ID: "custos"},
			Action: "maintenance.partitions", Result: audit.ResultAllow,
			Details: map[string]any{"tables": []string{"audit_events", "usage_records"}, "created": names},
		})
	})
}

// tenantDeleteHandler purges a tenant's rows and tombstones it. The
// tenants row (and slug) is retained — slugs are never reused
// (docs/tenancy.md lifecycle). Idempotent: re-running on a deleted
// tenant is a no-op. Item key: "tenant:<uuid>".
func tenantDeleteHandler(pool *pgxpool.Pool, recorder audit.Recorder) workqueue.Handler {
	repo := tenantpg.New(pool)
	return func(ctx context.Context, it workqueue.Item) error {
		id, err := uuid.Parse(it.Key[len("tenant:"):])
		if err != nil {
			return fmt.Errorf("tenant.delete key %q: %w", it.Key, err)
		}
		if err := repo.PurgeTenantData(ctx, id); err != nil {
			return err
		}
		return recorder.Record(ctx, audit.Event{
			Actor:   audit.Actor{Type: audit.ActorSystem, ID: "custos"},
			Action:  "tenant.deleted",
			Result:  audit.ResultAllow,
			Target:  audit.Target{Type: "tenant", ID: id.String()},
			Details: map[string]any{"key": it.Key},
		})
	}
}
