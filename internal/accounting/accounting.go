// Package accounting defines immutable usage facts and derivations.
package accounting

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/tenants"
)

// Record is one immutable ended Slurm job accounting fact.
type Record struct {
	ID                                                uuid.UUID
	ClusterID                                         uuid.UUID
	SlurmJobID                                        int64
	SlurmJobName                                      string
	JobID, TenantID, ProjectID, UserID                *uuid.UUID
	SlurmUser, Account, Partition, State              string
	ExitCode                                          *int
	SubmitTime, EligibleTime, StartTime, EndTime      time.Time
	ElapsedSeconds                                    int64
	NodeCount                                         int32
	CPUSeconds, GPUSeconds, NodeSeconds, MemGBSeconds int64
	WaitSeconds                                       *int64
	EnergyJoules                                      *int64
	TRESAlloc, TRESUsage                              map[string]int64
	CollectedAt                                       time.Time
	Failed                                            bool
}

// Derive converts a neutral Slurm record into immutable usage counters.
func Derive(clusterID uuid.UUID, in slurm.JobRecord, now time.Time) Record {
	elapsed := int64(in.Elapsed / time.Second)
	if elapsed < 0 {
		elapsed = 0
	}
	r := Record{ID: uuid.Must(uuid.NewV7()), ClusterID: clusterID,
		SlurmJobID: int64(in.ID.ID), SlurmJobName: in.Name, SlurmUser: in.User,
		Account: in.Account, Partition: in.Partition, State: string(in.State),
		SubmitTime: in.SubmitTime, EligibleTime: in.EligibleTime,
		StartTime: in.StartTime, EndTime: in.EndTime, ElapsedSeconds: elapsed,
		NodeCount: in.NodeCount, TRESAlloc: in.TRESAlloc, TRESUsage: in.TRESUsage,
		CollectedAt: now.UTC()}
	if in.ExitCode != nil {
		v := in.ExitCode.Code
		r.ExitCode = &v
	}
	r.CPUSeconds = in.TRESAlloc["cpu"] * elapsed
	r.GPUSeconds = in.TRESAlloc["gres/gpu"] * elapsed
	r.NodeSeconds = int64(in.NodeCount) * elapsed
	r.MemGBSeconds = in.TRESAlloc["mem"] * elapsed / 1024
	if !in.StartTime.IsZero() {
		base := in.SubmitTime
		if in.EligibleTime.After(base) {
			base = in.EligibleTime
		}
		wait := int64(in.StartTime.Sub(base) / time.Second)
		if wait < 0 {
			wait = 0
		}
		r.WaitSeconds = &wait
	}
	if v, ok := in.TRESUsage["energy"]; ok {
		r.EnergyJoules = &v
	}
	r.Failed = Failed(in.State, r.ExitCode)
	return r
}

// Failed applies the accounting failure definition.
func Failed(state slurm.JobState, exitCode *int) bool {
	switch state {
	case slurm.JobFailed, slurm.JobTimeout, slurm.JobNodeFail,
		slurm.JobOutOfMemory, slurm.JobBootFail, slurm.JobDeadline:
		return true
	case slurm.JobCompleted:
		return exitCode != nil && *exitCode != 0
	default:
		return false
	}
}

// Watermark is the collector cursor and status for a cluster.
type Watermark struct {
	ClusterID       uuid.UUID
	Watermark       time.Time
	LastCollectedAt *time.Time
	LastError       string
}

// Repository is the accounting persistence port.
type Repository interface {
	Watermark(context.Context, uuid.UUID) (Watermark, error)
	Store(context.Context, uuid.UUID, []Record, time.Time) (StoreResult, error)
	RecordError(context.Context, uuid.UUID, error) error
	AggregateDirty(context.Context, int) (int, error)
	ListDirty(context.Context, int) ([]DirtyDay, error)
	ListUsage(context.Context, tenants.Scope, UsageQuery) ([]Daily, error)
	Top(context.Context, tenants.Scope, TopQuery) ([]TopRow, error)
	ClusterStatus(context.Context, uuid.UUID) (Watermark, int64, error)
}

// StoreResult summarizes immutable inserts and attribution misses.
type StoreResult struct {
	Inserted              int64
	UnattributedAmbiguous int64
	UnattributedMissing   int64
}

// DirtyDay identifies one aggregate that must be recomputed.
type DirtyDay struct {
	ClusterID uuid.UUID
	Day       time.Time
}

// Daily is one materialized daily group.
type Daily struct {
	ID                                                                                             uuid.UUID  `json:"id"`
	TenantID                                                                                       *uuid.UUID `json:"tenant_id,omitempty"`
	ProjectID                                                                                      *uuid.UUID `json:"project_id,omitempty"`
	UserID                                                                                         *uuid.UUID `json:"user_id,omitempty"`
	ClusterID                                                                                      uuid.UUID  `json:"cluster_id"`
	Account                                                                                        string     `json:"account"`
	Partition                                                                                      string     `json:"partition"`
	Day                                                                                            time.Time  `json:"day"`
	Jobs, Failed, CPUSeconds, GPUSeconds, NodeSeconds, MemGBSeconds, WaitSecondsSum, RunSecondsSum int64
	EnergyJoules                                                                                   *int64
	WaitP50, WaitP90, WaitP99, RunP50, RunP90, RunP99                                              *float64
}

// UsageQuery scopes daily usage reads.
type UsageQuery struct {
	TenantID             uuid.UUID
	From, To             time.Time
	GroupBy              string
	ProjectID, ClusterID *uuid.UUID
	Limit                int
	Cursor               string
	UserID               uuid.UUID
	ProjectIDs           []uuid.UUID
	TenantWide           bool
}

// TopQuery describes a ranked usage request.
type TopQuery struct {
	UsageQuery
	Metric, By string
}

// TopRow is one ranked group.
type TopRow struct {
	Key   string `json:"key"`
	Value int64  `json:"value"`
}
