package allocations_test

import (
	"testing"

	"github.com/Exonical/custos/internal/allocations"
)

func TestHardSoftAndMultiple(t *testing.T) {
	items := []allocations.Allocation{{Name: "cpu", Unit: "cpu_hours", Enforcement: "hard", LimitAmount: 10, ConsumedAmount: 4}, {Name: "gpu", Unit: "gpu_hours", Enforcement: "soft", LimitAmount: 1, ConsumedAmount: .8}}
	estimate := map[string]float64{"cpu_hours": 5, "gpu_hours": .3}
	r := allocations.Check(items, map[string]float64{"cpu_hours": 1}, estimate)
	if r.Denial != nil {
		t.Fatalf("exact boundary denied: %+v", r)
	}
	if len(r.Soft) != 1 || len(r.Warnings) != 1 || r.Warnings[0].Severity != "WARN" {
		t.Fatalf("soft result=%+v", r)
	}
	r = allocations.Check(items, map[string]float64{"cpu_hours": 1.001}, estimate)
	if r.Denial == nil || r.Denial.Code != "ALLOCATION_EXHAUSTED" {
		t.Fatalf("expected hard denial: %+v", r)
	}
}
