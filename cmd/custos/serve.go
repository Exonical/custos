package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Exonical/custos/internal/api"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	clustersvc "github.com/Exonical/custos/internal/clusters/service"
	clustersync "github.com/Exonical/custos/internal/clusters/sync"
	execpg "github.com/Exonical/custos/internal/executions/postgres"
	execsvc "github.com/Exonical/custos/internal/executions/service"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	jobssvc "github.com/Exonical/custos/internal/jobs/service"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/health"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/platform/log"
	"github.com/Exonical/custos/internal/platform/otel"
	"github.com/Exonical/custos/internal/platform/safehttp"
	policypg "github.com/Exonical/custos/internal/policies/postgres"
	policiesvc "github.com/Exonical/custos/internal/policies/service"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	scriptpg "github.com/Exonical/custos/internal/scripts/postgres"
	"github.com/Exonical/custos/internal/secretrefs"
	secretpg "github.com/Exonical/custos/internal/secretrefs/postgres"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
	tenantsvc "github.com/Exonical/custos/internal/tenants/service"
	"github.com/Exonical/custos/internal/users"
	userpg "github.com/Exonical/custos/internal/users/postgres"
	wfpg "github.com/Exonical/custos/internal/workflows/postgres"
	wfsvc "github.com/Exonical/custos/internal/workflows/service"
)

func cmdServe(parent context.Context, configPath string, lookupEnv config.LookupEnv, stderr io.Writer) int {
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
	slog.SetDefault(logger)
	logger.InfoContext(ctx, "effective config", "config", cfg.Redacted())

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

	// Fail closed: no pool, no serve.
	pool, err := db.Open(ctx, cfg.Database, logger)
	if err != nil {
		logger.ErrorContext(ctx, "database unavailable", "error", err)
		return 1
	}
	defer pool.Close()

	reg := health.NewRegistry()
	reg.Register(db.NewChecker(pool), true)

	recorder := audit.Multi{
		pgaudit.New(pool),
		audit.SlogRecorder{Logger: logger},
	}
	if err := recorder.Record(ctx, audit.Event{
		Actor:  audit.Actor{Type: audit.ActorSystem, ID: "custos"},
		Action: "system.started",
		Result: audit.ResultAllow,
		Details: map[string]any{
			"version": version,
			"commit":  commit,
		},
	}); err != nil {
		logger.ErrorContext(ctx, "audit system.started failed", "error", err)
		return 1
	}

	// Bearer verification: real OIDC verifier when configured or required;
	// DenyAll in dev_mode without an issuer so bearer routes fail closed.
	var verifier authn.Verifier = authn.DenyAll{}
	if !cfg.DevMode || cfg.Auth.OIDC.Issuer != "" {
		oidcClient, err := oidcHTTPClient(cfg.Auth.OIDC.CAFile)
		if err != nil {
			logger.ErrorContext(ctx, "oidc ca_file", "error", err)
			return 1
		}
		v, err := authn.NewOIDCVerifier(ctx, cfg.Auth.OIDC, oidcClient, logger)
		if err != nil {
			logger.ErrorContext(ctx, "oidc verifier setup", "error", err)
			return 1
		}
		defer v.Close()
		reg.Register(v, true)
		verifier = v
	} else {
		logger.WarnContext(ctx,
			"authentication disabled: dev_mode without auth.oidc.issuer")
	}

	userRepo := userpg.New(pool)
	tenantRepo := tenantpg.New(pool)
	provisioner := users.NewService(userRepo, recorder,
		users.WithAuthorizer(authz.RBAC{}),
		users.WithGroupsClaim(cfg.Auth.OIDC.Claims.Groups),
		users.WithMeterProvider(prov.Meter),
		users.WithLogger(logger))
	sdeps, err := newSlurmDeps(ctx, cfg, logger, prov.Meter)
	if err != nil {
		logger.ErrorContext(ctx, "slurm setup", "error", err)
		return 1
	}
	if sdeps.OpenBao != nil {
		defer func() { _ = sdeps.OpenBao.Close() }()
		reg.Register(sdeps.OpenBao, false)
	}
	secretRepo := secretpg.New(pool)
	connectorFactory := &secretrefs.ConnectorFactory{
		Platform: sdeps.OpenBao,
		Policy: safehttp.DialPolicy{AllowPrivate: sdeps.Policy.AllowPrivate,
			AllowLoopback: sdeps.Policy.AllowLoopback, AllowHTTP: sdeps.Policy.AllowHTTP,
			DenyCIDRs: sdeps.Policy.DenyCIDRs},
		Logger: logger, Meter: prov.Meter,
	}
	secretRuntime := secretrefs.NewRuntime(secretRepo, connectorFactory, sdeps.Resolver)
	defer func() { _ = secretRuntime.Close() }()
	platformNS := ""
	if cfg.Secrets.OpenBao != nil {
		platformNS = cfg.Secrets.OpenBao.Namespace
	}
	secretSvc := secretrefs.NewService(secretRepo, secretRuntime, authz.RBAC{},
		recorder, sdeps.OpenBao, platformNS)
	tenantSvc := tenantsvc.NewService(tenantRepo, tenantRepo, tenantRepo,
		userRepo, authz.RBAC{}, recorder, secretSvc)
	clusterRepo := clusterpg.New(pool)
	clusterSvc := clustersvc.New(clustersvc.Deps{
		Repository: clusterRepo, Tenants: tenantRepo,
		Factory: sdeps.Factory, Authorizer: authz.RBAC{},
		Recorder: recorder, DialPolicy: sdeps.Policy,
		Resolver: sdeps.Resolver, Enqueuer: pool,
	})
	reg.Register(clustersync.UnreachableChecker(clusterRepo), false)

	projectRepo := projectpg.New(pool)
	projectSvc := projectsvc.NewService(projectRepo, projectRepo, projectRepo,
		tenantRepo, clusterRepo, authz.RBAC{}, recorder)
	policySvc := policiesvc.NewService(policypg.New(pool), authz.RBAC{}, recorder)

	// Validation pipeline: in-process validators always; ShellCheck via
	// the loopback sidecar when enabled (docs/script-validation.md).
	vdeps, err := newValidationDeps(cfg, pool, clusterRepo, prov, recorder)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	pipe, vpolSvc, vstore, vMetrics := vdeps.Pipeline, vdeps.VPolicy,
		vdeps.Store, vdeps.Metrics

	// Jobs: the synchronous admission pipeline; actual Slurm submission
	// happens in the worker (docs/workers.md).
	jobSvc := jobssvc.New(jobssvc.Deps{
		Jobs:        jobpg.New(pool),
		Scripts:     scriptpg.New(pool),
		Projects:    projectSvc,
		Policies:    policySvc,
		Clusters:    clusterRepo,
		Pipeline:    pipe,
		VPolicy:     vpolSvc,
		Validations: vstore,
		Metrics:     vMetrics,
		AZ:          authz.RBAC{},
		Audit:       recorder,
	})

	wfSvc := wfsvc.New(wfsvc.Deps{
		Repo:            wfpg.New(pool),
		SecretReference: secretSvc.ReferenceExists,
		Projects:        projectSvc,
		Policies:        policySvc,
		Clusters:        clusterRepo,
		Pipeline:        pipe,
		VPolicy:         vpolSvc,
		Validations:     vstore,
		Scripts:         scriptpg.New(pool),
		Metrics:         vMetrics,
		AZ:              authz.RBAC{},
		Audit:           recorder,
	})

	execSvc := execsvc.New(execsvc.Deps{
		Repo:        execpg.New(pool),
		Workflows:   wfpg.New(pool),
		Validations: vstore,
		AZ:          authz.RBAC{},
		Audit:       recorder,
	})

	mux := http.NewServeMux()
	api.Mount(mux, api.Deps{
		Health:         reg,
		ReadyBudget:    500 * time.Millisecond,
		Logger:         logger,
		Verifier:       verifier,
		Provisioner:    provisioner,
		Audit:          recorder,
		Tenants:        tenantSvc,
		TenantRepo:     tenantRepo,
		Users:          provisioner,
		Clusters:       clusterSvc,
		Projects:       projectSvc,
		ProjectRepo:    projectRepo,
		ProjectMembers: projectRepo,
		Policies:       policySvc,
		Jobs:           jobSvc,
		JobExec:        pool,
		Scripts:        scriptpg.New(pool),
		Pipeline:       pipe,
		VPolicy:        vpolSvc,
		VStore:         vstore,
		VMetrics:       vMetrics,
		VLimiter:       httpx.NewPrincipalRateLimiter(30, 10, 0),
		Workflows:      wfSvc,
		Executions:     execSvc,
		SecretRefs:     secretSvc,
		AZ:             authz.RBAC{},
	})

	handler := otel.Instrument(httpx.Chain(
		httpx.Recover(logger),
		httpx.RequestID,
		httpx.RealIP(trustedProxies(logger, cfg)),
		httpx.SecurityHeaders,
		httpx.CORS(cfg.Server.CORS.AllowedOrigins),
		httpx.BodyLimit(cfg.Server.MaxBodyBytes),
		httpx.Timeout(cfg.Server.RequestTimeout),
		httpx.AccessLog(logger),
		otel.RouteLabeler,
	)(mux), prov)

	srv, err := httpx.NewServer(cfg.Server, handler, logger)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "server setup: %v\n", err)
		return 1
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		logger.InfoContext(gctx, "listening", "addr", cfg.Server.Listen,
			"tls", cfg.Server.TLS.Mode)
		return httpx.Serve(gctx, srv, cfg.Server)
	})
	if err := runMetrics(gctx, g, cfg, prov.PromRegistry, logger); err != nil {
		logger.ErrorContext(gctx, "metrics setup", "error", err)
		return 1
	}
	gerr := g.Wait()
	reason, result := "signal", audit.ResultAllow
	if gerr != nil {
		reason, result = "error", audit.ResultError
	}
	// Stopped event on a fresh ctx — the signal ctx is already canceled.
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := recorder.Record(sctx, audit.Event{
		Actor:  audit.Actor{Type: audit.ActorSystem, ID: "custos"},
		Action: "system.stopped",
		Result: result,
		Details: map[string]any{
			"reason":    reason,
			"component": "serve",
		},
	}); err != nil {
		logger.ErrorContext(sctx, "audit system.stopped failed", "error", err)
	}
	if gerr != nil {
		logger.Error("server exited", "error", gerr)
		return 1
	}
	return 0
}

// trustedProxies parses the validated CIDR list; config.Validate already
// guarantees they parse, so failures are logged and skipped.
func trustedProxies(logger *slog.Logger, cfg config.Config) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range cfg.Server.TrustedProxies {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			logger.Warn("ignoring invalid trusted proxy", "cidr", p)
			continue
		}
		out = append(out, pfx)
	}
	return out
}

// oidcHTTPClient builds the HTTP client the OIDC verifier uses for
// discovery and JWKS fetches. caFile pins a private CA (e.g. the e2e
// stack's CA) appended to the system roots; empty uses the defaults.
func oidcHTTPClient(caFile string) (*http.Client, error) {
	if caFile == "" {
		return nil, nil // verifier falls back to http.DefaultClient
	}
	pem, err := os.ReadFile(caFile) // #nosec G304 -- path comes from validated operator config
	if err != nil {
		return nil, fmt.Errorf("auth.oidc.ca_file: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf(
			"auth.oidc.ca_file: no parseable certificates in %s", caFile)
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}}, nil
}
