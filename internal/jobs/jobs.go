// Package jobs is the job domain: the durable record of a submitted
// workload plus the idempotency/transition rules. The worker
// (internal/jobs/worker) is the only place script bytes meet the
// wrapper (docs/workers.md).
package jobs

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// State is the Custos-side job lifecycle state.
type State string

// Job states (docs/workers.md).
const (
	StateSubmitting State = "SUBMITTING"
	StateQueued     State = "QUEUED"
	StateRunning    State = "RUNNING"
	StateCompleted  State = "COMPLETED"
	StateFailed     State = "FAILED"
	StateCanceled   State = "CANCELED"
)

// Terminal reports whether s is a final state.
func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateCanceled:
		return true
	}
	return false
}

// Result is the metrics label value for a terminal state.
func (s State) Result() string {
	switch s {
	case StateCompleted:
		return "completed"
	case StateFailed:
		return "failed"
	case StateCanceled:
		return "canceled"
	}
	return "unknown"
}

// MapSlurmState maps a neutral Slurm state onto a Custos state. The
// second return is false when the Slurm state is unrecognized (the
// caller records the raw state as the reason and fails the job).
func MapSlurmState(s slurm.JobState) (State, bool) {
	switch s {
	case slurm.JobPending:
		return StateQueued, true
	case slurm.JobRunning, slurm.JobCompleting, slurm.JobSuspended:
		return StateRunning, true
	case slurm.JobCompleted:
		return StateCompleted, true
	case slurm.JobCancelled:
		return StateCanceled, true
	case slurm.JobFailed, slurm.JobTimeout, slurm.JobNodeFail,
		slurm.JobPreempted, slurm.JobBootFail, slurm.JobDeadline,
		slurm.JobOutOfMemory:
		return StateFailed, true
	default:
		return StateFailed, false
	}
}

// TransitionOK guards the allowed state edges (docs/workers.md).
func TransitionOK(from, to State) bool {
	switch from {
	case StateSubmitting:
		return to == StateQueued || to == StateFailed || to == StateCanceled
	case StateQueued:
		return to == StateRunning || to == StateCompleted ||
			to == StateFailed || to == StateCanceled
	case StateRunning:
		return to == StateCompleted || to == StateFailed || to == StateCanceled
	}
	return false
}

// Job is the durable record. ExecutionSpec is written once and is
// immutable (enforced by a DB trigger and by Patch having no spec fields).
type Job struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	ProjectID           uuid.UUID
	ClusterID           uuid.UUID
	CreatedBy           uuid.UUID
	Name                string
	State               State
	StateReason         string
	SlurmJobID          *int64
	SlurmState          string
	ExitCode            *int
	ExitSignal          *int
	ResourceRequest     workflowspec.Resources
	ExecutionSpec       admission.ExecutionSpec
	ExecutionSpecDigest validation.Digest
	ScriptDigest        validation.Digest
	ScriptLanguage      workflowspec.Language
	SubmittedAt         *time.Time
	StartedAt           *time.Time
	EndedAt             *time.Time
	LastReconciledAt    *time.Time
	Version             int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// SlurmJobIDRef returns the neutral JobID for the stored slurm_job_id.
func (j Job) SlurmJobIDRef() (slurm.JobID, bool) {
	if j.SlurmJobID == nil {
		return slurm.JobID{}, false
	}
	// #nosec G115 -- slurm_job_id originates from a uint32 Slurm job id
	return slurm.JobID{ID: uint32(*j.SlurmJobID)}, true
}

// Patch is the mutable-field update applied by Transition. There is no
// way to patch execution_spec/script fields — they are immutable.
type Patch struct {
	State            *State
	Reason           *string
	SlurmJobID       *int64
	SlurmState       *string
	ExitCode         *int
	ExitSignal       *int
	SubmittedAt      *time.Time
	StartedAt        *time.Time
	EndedAt          *time.Time
	LastReconciledAt *time.Time
}

// Filter scopes List.
type Filter struct {
	ProjectID *uuid.UUID
	States    []State
	ClusterID *uuid.UUID
	CreatedBy *uuid.UUID // self-only listing
}

// Execer is the in-transaction handle handed to the enqueue hook —
// aliased from workqueue so the domain need not import pgx.
type Execer = workqueue.Execer

// EnqueueFunc enqueues a work item inside the job's transaction.
type EnqueueFunc func(ex Execer) error

// IdemRecord is the idempotency_keys row written with the job insert.
type IdemRecord struct {
	Key         string
	RequestHash [32]byte // sha256 of the raw request body
	Status      int
	Body        []byte // serialized response
	ResourceID  uuid.UUID
	ExpiresAt   time.Time
}

// CreateResult is CreateWithIdempotency's outcome: either the new job
// (Replayed=false) or the stored response (Replayed=true).
type CreateResult struct {
	Job      Job
	Replayed bool
	Status   int
	Body     []byte
}

// Repository is the jobs persistence contract.
type Repository interface {
	// CreateWithIdempotency inserts the job, the idempotency row, and
	// runs enqueue — atomically. A same-key replay returns the stored
	// response; a key with a different request hash is Conflict
	// IDEMPOTENCY_MISMATCH.
	CreateWithIdempotency(ctx context.Context, scope tenants.Scope,
		j Job, idem IdemRecord, enqueue EnqueueFunc) (CreateResult, error)
	Get(ctx context.Context, scope tenants.Scope, id uuid.UUID) (Job, error)
	List(ctx context.Context, scope tenants.Scope, f Filter,
		page tenants.Page) ([]Job, string, error)
	// ListActiveByCluster returns non-terminal jobs on a cluster
	// (platform scope; sweep worker).
	ListActiveByCluster(ctx context.Context, clusterID uuid.UUID) ([]Job, error)
	// Transition applies p guarded by fromVersion (optimistic
	// concurrency) and returns the updated job. Conflict on version
	// mismatch, NotFound when the job is gone.
	Transition(ctx context.Context, scope tenants.Scope, id uuid.UUID,
		fromVersion int, p Patch) (Job, error)
	// CountActiveByState counts non-terminal jobs by state (platform
	// scope) for the active-jobs gauge.
	CountActiveByState(ctx context.Context) (map[State]int64, error)
	// ExpireIdempotency deletes expired idempotency rows; returns the
	// count (platform scope; idempotency.expire worker).
	ExpireIdempotency(ctx context.Context, now time.Time) (int64, error)
}
