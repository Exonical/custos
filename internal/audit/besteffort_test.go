package audit_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Exonical/custos/internal/audit"
)

type stubRecorder struct {
	err   error
	calls int
}

func (r *stubRecorder) Record(context.Context, audit.Event) error {
	r.calls++
	return r.err
}

func failureCounts(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "custos_audit_record_failures_total" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				v, _ := dp.Attributes.Value("action")
				got[v.AsString()] += dp.Value
			}
		}
	}
	return got
}

func TestRecordBestEffort(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	ctx := context.Background()

	// Nil recorder: no-op.
	audit.RecordBestEffort(ctx, nil, nil, audit.Event{Action: "x.nil"})

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	ok := &stubRecorder{}
	audit.RecordBestEffort(ctx, logger, ok, audit.Event{Action: "x.ok"})
	if ok.calls != 1 || logs.Len() != 0 {
		t.Fatalf("success: calls=%d logs=%q", ok.calls, logs.String())
	}

	bad := &stubRecorder{err: errors.New("audit sink down")}
	audit.RecordBestEffort(ctx, logger, bad, audit.Event{Action: "secret.accessed"})
	audit.RecordBestEffort(ctx, logger, bad, audit.Event{Action: "secret.accessed"})
	if bad.calls != 2 {
		t.Fatalf("failure: calls=%d, want 2", bad.calls)
	}
	out := logs.String()
	if !strings.Contains(out, "audit record failed") ||
		!strings.Contains(out, "secret.accessed") ||
		!strings.Contains(out, "audit sink down") {
		t.Fatalf("missing audit failure warning:\n%s", out)
	}

	got := failureCounts(t, reader)
	if len(got) != 1 || got["secret.accessed"] != 2 {
		t.Fatalf("custos_audit_record_failures_total = %v, want secret.accessed=2", got)
	}
}

func TestRecordBestEffortDefaultsLogger(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	audit.RecordBestEffort(context.Background(), nil,
		&stubRecorder{err: errors.New("boom")}, audit.Event{Action: "job.submitted"})
	if !strings.Contains(logs.String(), "audit record failed") ||
		!strings.Contains(logs.String(), "job.submitted") {
		t.Fatalf("default logger not used:\n%s", logs.String())
	}
}
