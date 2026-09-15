// Package oteltest provides the shared metric-cardinality assertions.
package oteltest

import (
	"regexp"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// forbiddenLabels are label names that would explode cardinality or leak
// identifiers into metrics.
var forbiddenLabels = map[string]bool{
	"tenant_id": true, "tenant": true, "user_id": true, "user": true,
	"job_id": true, "execution_id": true, "request_id": true,
	"trace_id": true, "path": true, "url": true, "url_path": true,
	"http_target": true, "client_ip": true, "remote_addr": true,
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// AssertLowCardinality fails the test if any metric family in reg uses a
// forbidden label name or a UUID-looking label value.
func AssertLowCardinality(t testing.TB, reg *prometheus.Registry) {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range fams {
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				name := lp.GetName()
				if forbiddenLabels[name] {
					t.Errorf("metric %q uses forbidden label %q", f.GetName(), name)
				}
				if uuidRE.MatchString(lp.GetValue()) {
					t.Errorf("metric %q label %q has a UUID value %q",
						f.GetName(), name, lp.GetValue())
				}
			}
		}
	}
}

// Families returns the gathered families for name-level assertions.
func Families(t testing.TB, reg *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	return fams
}
