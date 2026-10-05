package audit

import (
	"context"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	failuresOnce sync.Once
	failures     metric.Int64Counter
)

func failureCounter() metric.Int64Counter {
	failuresOnce.Do(func() {
		failures, _ = otel.Meter("custos/audit").Int64Counter(
			"custos_audit_record_failures_total",
			metric.WithDescription("Audit events whose best-effort record failed."))
	})
	return failures
}

// RecordBestEffort records e on rec for an action that has already taken
// effect. Audit here is best-effort: a sink outage must not fail or undo
// the action, so a failure is logged (logger, or slog.Default when nil)
// and counted in custos_audit_record_failures_total{action} instead of
// returned. Call rec.Record directly where the action must fail closed
// (e.g. users.Provision). A nil rec is a no-op.
func RecordBestEffort(ctx context.Context, logger *slog.Logger, rec Recorder, e Event) {
	if rec == nil {
		return
	}
	err := rec.Record(ctx, e)
	if err == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.WarnContext(ctx, "audit record failed", "action", e.Action, "error", err)
	if c := failureCounter(); c != nil {
		c.Add(ctx, 1, metric.WithAttributes(attribute.String("action", e.Action)))
	}
}
