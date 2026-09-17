package admission_test

import (
	"testing"
	"time"

	"github.com/Exonical/custos/internal/workflowspec"

	"github.com/Exonical/custos/internal/admission"
)

func TestIntersectPolicies(t *testing.T) {
	tenant := admission.ResourcePolicy{
		MaxNodes: 8, MaxGPUsPerJob: 8, MaxWalltime: workflowspec.Duration(4 * time.Hour),
		AllowedPartitions: []string{"gpu", "batch"}, AllowedQoS: []string{"normal", "high"},
		AllowedGPUTypes: []string{"h100", "a100"}, AllowExclusive: true,
	}
	cases := []struct {
		name    string
		project admission.ResourcePolicy
		check   func(t *testing.T, out admission.ResourcePolicy)
	}{
		{"project tightens",
			admission.ResourcePolicy{MaxGPUsPerJob: 4},
			func(t *testing.T, o admission.ResourcePolicy) {
				if o.MaxGPUsPerJob != 4 || o.MaxNodes != 8 {
					t.Fatalf("%+v", o)
				}
			}},
		{"project cannot loosen",
			admission.ResourcePolicy{MaxGPUsPerJob: 16},
			func(t *testing.T, o admission.ResourcePolicy) {
				if o.MaxGPUsPerJob != 8 {
					t.Fatalf("%+v", o)
				}
			}},
		{"lists intersect",
			admission.ResourcePolicy{AllowedPartitions: []string{"gpu"}},
			func(t *testing.T, o admission.ResourcePolicy) {
				if len(o.AllowedPartitions) != 1 || o.AllowedPartitions[0] != "gpu" {
					t.Fatalf("%+v", o.AllowedPartitions)
				}
			}},
		{"nil project list = tenant list",
			admission.ResourcePolicy{},
			func(t *testing.T, o admission.ResourcePolicy) {
				if len(o.AllowedPartitions) != 2 {
					t.Fatalf("%+v", o.AllowedPartitions)
				}
			}},
		{"empty intersection",
			admission.ResourcePolicy{AllowedQoS: []string{"debug"}},
			func(t *testing.T, o admission.ResourcePolicy) {
				if o.AllowedQoS == nil || len(o.AllowedQoS) != 0 {
					t.Fatalf("%+v", o.AllowedQoS)
				}
			}},
		{"bools AND",
			admission.ResourcePolicy{AllowExclusive: false},
			func(t *testing.T, o admission.ResourcePolicy) {
				if o.AllowExclusive {
					t.Fatal("exclusive stayed allowed")
				}
			}},
		{"walltime min",
			admission.ResourcePolicy{MaxWalltime: workflowspec.Duration(time.Hour)},
			func(t *testing.T, o admission.ResourcePolicy) {
				if o.MaxWalltime != workflowspec.Duration(time.Hour) {
					t.Fatalf("%v", time.Duration(o.MaxWalltime))
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.check(t, admission.IntersectPolicies(tenant, c.project))
		})
	}
}
