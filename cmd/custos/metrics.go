package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"

	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/platform/otel"
)

// runMetrics serves GET /metrics on cfg.Metrics.Listen inside g when
// enabled. Security headers only; no access log (scrape noise).
func runMetrics(ctx context.Context, g *errgroup.Group, cfg config.Config,
	reg *prometheus.Registry, logger *slog.Logger) error {
	if !cfg.Metrics.Enabled {
		return nil
	}
	scfg := config.Server{
		Listen:            cfg.Metrics.Listen,
		TLS:               cfg.Metrics.TLS,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   10 * time.Second,
	}
	srv, err := httpx.NewServer(scfg, httpx.SecurityHeaders(metricsMux(reg)), logger)
	if err != nil {
		return err
	}
	g.Go(func() error {
		logger.InfoContext(ctx, "metrics listening", "addr", cfg.Metrics.Listen)
		return httpx.Serve(ctx, srv, scfg)
	})
	return nil
}

func metricsMux(reg *prometheus.Registry) *http.ServeMux {
	m := http.NewServeMux()
	m.Handle("GET /metrics", otel.MetricsHandler(reg))
	return m
}
