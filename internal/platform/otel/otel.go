// Package otel wires OpenTelemetry tracing and metrics: a TracerProvider
// (no-op exporter or OTLP/gRPC), a Prometheus exporter on a private
// registry (Go runtime + process collectors + custos_build_info), and
// the W3C propagator + trace-id log hook.
package otel

import (
	"context"
	"net/http"
	"os"
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/config"
	custoslog "github.com/Exonical/custos/internal/platform/log"
)

// Providers bundles the otel SDK objects a process needs.
type Providers struct {
	Tracer       trace.TracerProvider
	Meter        *sdkmetric.MeterProvider
	PromRegistry *prometheus.Registry
	Shutdown     func(context.Context) error
}

// Setup builds providers from cfg. Tracing always runs (even with
// exporter=none) so trace ids exist for log correlation.
func Setup(ctx context.Context, cfg config.Telemetry, version, commit string) (*Providers, error) {
	hostname, _ := os.Hostname()
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(version),
			semconv.ServiceInstanceID(hostname),
		))
	if err != nil {
		return nil, apperr.Wrap(err, apperr.Internal, "otel.setup", "resource")
	}

	tp, err := tracerProvider(ctx, cfg, res)
	if err != nil {
		return nil, err
	}
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	custoslog.TraceIDFunc = spanIDs

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "custos_build_info",
		Help: "Build metadata for the custos binary.",
	}, []string{"version", "commit", "go_version"})
	reg.MustRegister(buildInfo)
	buildInfo.WithLabelValues(version, commit, runtime.Version()).Set(1)

	exp, err := promexporter.New(
		promexporter.WithRegisterer(reg),
		promexporter.WithTranslationStrategy(
			otlptranslator.UnderscoreEscapingWithoutSuffixes),
		promexporter.WithoutScopeInfo(),
	)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.Internal, "otel.setup", "prometheus exporter")
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(exp),
	)
	otel.SetMeterProvider(mp)

	return &Providers{
		Tracer:       tp,
		Meter:        mp,
		PromRegistry: reg,
		Shutdown: func(sctx context.Context) error {
			mErr := mp.Shutdown(sctx)
			tErr := tp.Shutdown(sctx)
			if mErr != nil {
				return mErr
			}
			return tErr
		},
	}, nil
}

func tracerProvider(ctx context.Context, cfg config.Telemetry, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(
			sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	}
	if cfg.Exporter == "otlp" {
		expOpts := []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(cfg.Endpoint),
		}
		if cfg.Insecure {
			expOpts = append(expOpts, otlptracegrpc.WithInsecure())
		}
		exp, err := otlptracegrpc.New(ctx, expOpts...)
		if err != nil {
			return nil, apperr.Wrap(err, apperr.Unavailable, "otel.setup", "otlp exporter")
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	return sdktrace.NewTracerProvider(opts...), nil
}

// spanIDs implements log.TraceIDFunc: pull trace/span ids from the
// current span context for log correlation.
func spanIDs(ctx context.Context) (traceID, spanID string) {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if sc.HasTraceID() {
		traceID = sc.TraceID().String()
	}
	if sc.HasSpanID() {
		spanID = sc.SpanID().String()
	}
	return traceID, spanID
}

// MetricsHandler serves the private Prometheus registry.
func MetricsHandler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

// RouteLabeler tags the otelhttp duration metric with http.route from
// r.Pattern. Place innermost (after the mux sees the route).
func RouteLabeler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.Pattern != "" {
			if l, ok := otelhttp.LabelerFromContext(r.Context()); ok {
				l.Add(semconv.HTTPRoute(r.Pattern))
			}
		}
	})
}

// Instrument wraps the API handler with otelhttp server instrumentation.
func Instrument(h http.Handler, p *Providers) http.Handler {
	return otelhttp.NewHandler(h, "http.server",
		otelhttp.WithMeterProvider(p.Meter),
		otelhttp.WithTracerProvider(p.Tracer))
}
