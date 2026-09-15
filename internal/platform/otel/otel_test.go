package otel_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/otel"
	"github.com/Exonical/custos/internal/platform/otel/oteltest"
	"github.com/Exonical/custos/internal/platform/workqueue"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func names(t *testing.T, reg *prometheus.Registry) []string {
	t.Helper()
	var out []string
	for _, f := range oteltest.Families(t, reg) {
		out = append(out, f.GetName())
	}
	return out
}

func hasSeries(t *testing.T, reg *prometheus.Registry, prefix string) bool {
	t.Helper()
	for _, f := range oteltest.Families(t, reg) {
		if strings.HasPrefix(f.GetName(), prefix) {
			return true
		}
	}
	return false
}

func TestSetupBuildInfoAndGather(t *testing.T) {
	cfg := config.Default().Telemetry
	p, err := otel.Setup(context.Background(), cfg, "test", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()
	if !hasSeries(t, p.PromRegistry, "custos_build_info") {
		t.Fatalf("custos_build_info missing; got %v", names(t, p.PromRegistry))
	}
	oteltest.AssertLowCardinality(t, p.PromRegistry)
}

func TestHTTPRouteLabel(t *testing.T) {
	p, err := otel.Setup(context.Background(), config.Default().Telemetry, "t", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()

	mux := http.NewServeMux()
	mux.Handle("GET /health/live", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	h := otel.Instrument(otel.RouteLabeler(mux), p)
	req := httptest.NewRequest("GET", "/health/live", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	// Find the request-duration series and its http.route label.
	found := false
	for _, f := range oteltest.Families(t, p.PromRegistry) {
		if !strings.HasPrefix(f.GetName(), "http_server_request_duration") {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "http_route" && lp.GetValue() == "/health/live" {
					found = true
				}
			}
		}
		t.Logf("family %s", f.GetName())
	}
	if !found {
		t.Fatalf("no http_server_request_duration* series with http_route=/health/live; families: %v",
			names(t, p.PromRegistry))
	}
	oteltest.AssertLowCardinality(t, p.PromRegistry)
}

func TestQueueMetrics(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p, err := otel.Setup(ctx, config.Default().Telemetry, "t", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()

	cfg := config.Worker{
		PollInterval:       20 * time.Millisecond,
		LeaseDuration:      30 * time.Second,
		HeartbeatInterval:  10 * time.Second,
		ShutdownTimeout:    time.Second,
		DefaultConcurrency: 2,
	}
	q := workqueue.New(pool, cfg, testLogger(),
		workqueue.WithMeterProvider(p.Meter),
		workqueue.WithHeartbeat(false))
	done := make(chan struct{}, 2)
	q.Register("m.done", func(_ context.Context, _ workqueue.Item) error {
		done <- struct{}{}
		return nil
	})
	q.Register("m.dead", func(_ context.Context, _ workqueue.Item) error {
		done <- struct{}{}
		return workqueue.Perm(errors.New("bad"))
	})
	go func() { _ = q.Run(ctx) }()

	// One item -> done; one item -> dead; one future item stays pending.
	for i, req := range []workqueue.EnqueueRequest{
		{Kind: "m.done", Key: "d1"},
		{Kind: "m.dead", Key: "x1"},
		{Kind: "m.done", Key: "p1", RunAt: time.Now().Add(time.Hour)},
	} {
		if _, err := workqueue.Enqueue(ctx, pool, req); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for len(done) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let outcome writes commit
	cancel()

	// Force a fresh gauge read: gauges are observable at gather time, but
	// the 15s cache may be empty if a callback ran earlier; wait past it.
	want := []string{
		"custos_work_items_total",
		"custos_work_item_duration_seconds",
		"custos_work_items_pending",
		"custos_work_items_dead",
	}
	for _, name := range want {
		if !hasSeries(t, p.PromRegistry, name) {
			t.Fatalf("series %q missing; families: %v", name, names(t, p.PromRegistry))
		}
	}
	oteltest.AssertLowCardinality(t, p.PromRegistry)
}
