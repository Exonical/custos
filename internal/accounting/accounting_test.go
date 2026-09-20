package accounting_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/accounting"
	"github.com/Exonical/custos/internal/slurm"
)

func TestDerive(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	exit0 := 0
	cases := []struct {
		name   string
		record slurm.JobRecord
		wait   *int64
		failed bool
	}{{"normal", slurm.JobRecord{State: slurm.JobCompleted, ExitCode: &slurm.ExitCode{}, SubmitTime: base, EligibleTime: base.Add(time.Minute), StartTime: base.Add(3 * time.Minute), EndTime: base.Add(13 * time.Minute), Elapsed: 10 * time.Minute, NodeCount: 2, TRESAlloc: map[string]int64{"cpu": 4, "mem": 2048, "gres/gpu": 1}, TRESUsage: map[string]int64{"energy": 42}}, ptr(120), false}, {"wait clamp", slurm.JobRecord{State: slurm.JobCompleted, ExitCode: &slurm.ExitCode{}, SubmitTime: base, EligibleTime: base.Add(5 * time.Minute), StartTime: base.Add(time.Minute), Elapsed: time.Minute}, ptr(0), false}, {"no start", slurm.JobRecord{State: slurm.JobFailed, Elapsed: time.Minute}, nil, true}, {"completed nonzero", slurm.JobRecord{State: slurm.JobCompleted, ExitCode: &slurm.ExitCode{Code: 1}, Elapsed: time.Minute}, nil, true}}
	_ = exit0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := accounting.Derive(uuid.New(), tc.record, base)
			if (got.WaitSeconds == nil) != (tc.wait == nil) || got.WaitSeconds != nil && *got.WaitSeconds != *tc.wait {
				t.Fatalf("wait=%v", got.WaitSeconds)
			}
			if got.Failed != tc.failed {
				t.Fatalf("failed=%v", got.Failed)
			}
			if tc.name == "normal" {
				if got.CPUSeconds != 2400 || got.GPUSeconds != 600 || got.NodeSeconds != 1200 || got.MemGBSeconds != 1200 || got.EnergyJoules == nil || *got.EnergyJoules != 42 {
					t.Fatalf("derived=%+v", got)
				}
			}
		})
	}
}
func ptr(v int64) *int64 { return &v }
