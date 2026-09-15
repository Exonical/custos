package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Exonical/custos/internal/api"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/health"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/platform/log"
	"github.com/Exonical/custos/internal/platform/otel"
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

	mux := http.NewServeMux()
	api.Mount(mux, api.Deps{Health: reg, ReadyBudget: 500 * time.Millisecond, Logger: logger})

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
