package admission_test

import (
	"testing"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/workflowspec"
)

func TestEstimateCost(t *testing.T) {
	cases := []struct {
		name string
		r    admission.ResolvedResources
		want map[string]float64
	}{
		{"nodes", admission.ResolvedResources{Nodes: 2, TasksPerNode: 3, CPUsPerTask: 4, WalltimeSeconds: 3600, GPUCount: 2}, map[string]float64{"cpu_hours": 24, "gpu_hours": 2, "node_hours": 2}},
		{"tasks fallback", admission.ResolvedResources{Tasks: 4, CPUsPerTask: 2, WalltimeSeconds: 1800}, map[string]float64{"cpu_hours": 4, "gpu_hours": 0, "node_hours": 0.5}},
		{"nodes with unset tasks per node", admission.ResolvedResources{Nodes: 4, Tasks: 64, CPUsPerTask: 8, WalltimeSeconds: 3600}, map[string]float64{"cpu_hours": 512, "gpu_hours": 0, "node_hours": 4}},
		{"unset CPUs per task", admission.ResolvedResources{Tasks: 64, WalltimeSeconds: 3600}, map[string]float64{"cpu_hours": 64, "gpu_hours": 0, "node_hours": 1}},
		{"node task product exceeds tasks", admission.ResolvedResources{Nodes: 4, Tasks: 64, TasksPerNode: 32, CPUsPerTask: 2, WalltimeSeconds: 3600}, map[string]float64{"cpu_hours": 256, "gpu_hours": 0, "node_hours": 4}},
		{"minimum", admission.ResolvedResources{WalltimeSeconds: 3600}, map[string]float64{"cpu_hours": 1, "gpu_hours": 0, "node_hours": 1}},
		{"array", admission.ResolvedResources{Nodes: 1, TasksPerNode: 1, CPUsPerTask: 2, WalltimeSeconds: 3600, Array: &workflowspec.ArraySpec{Start: 1, End: 3, Step: 1}}, map[string]float64{"cpu_hours": 6, "gpu_hours": 0, "node_hours": 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := admission.EstimateCost(tc.r)
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("%s=%v want %v (%v)", k, got[k], v, got)
				}
			}
		})
	}
}

func TestAdmissionEstimateIsInDigest(t *testing.T) {
	a := admission.ExecutionSpec{}
	if err := a.Freeze(); err != nil {
		t.Fatal(err)
	}
	before := a.Digest
	a.Admission.EstimatedCost = map[string]float64{"cpu_hours": 1}
	if err := a.Freeze(); err != nil {
		t.Fatal(err)
	}
	if before == a.Digest {
		t.Fatal("admission estimate did not affect digest")
	}
}
