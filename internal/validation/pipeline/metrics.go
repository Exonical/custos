package pipeline

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Exonical/custos/internal/validation"
)

// Metrics holds custos_validation_security_violations_total{field}
// (docs/script-validation.md §Audit). The field label uses the bounded
// canonical-field set produced by the validators.
type Metrics struct {
	violations metric.Int64Counter
}

// NewMetrics registers validation instruments on mp (may be nil).
func NewMetrics(mp metric.MeterProvider) *Metrics {
	m := &Metrics{}
	if mp == nil {
		return m
	}
	meter := mp.Meter("custos/validation")
	m.violations, _ = meter.Int64Counter(
		"custos_validation_security_violations_total",
		metric.WithDescription("security-violation diagnostics by field"))
	return m
}

// Observe counts SECURITY_VIOLATION diagnostics in a pipeline result.
func (m *Metrics) Observe(ctx context.Context, sv validation.ScriptValidation) {
	if m == nil || m.violations == nil {
		return
	}
	for _, d := range sv.Diagnostics {
		if d.Severity == validation.SeveritySecurity {
			m.violations.Add(ctx, 1, metric.WithAttributes(
				attribute.String("field", d.Field)))
		}
	}
}
