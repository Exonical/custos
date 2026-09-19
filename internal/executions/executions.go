// Package executions holds the workflow-execution domain: durable
// execution records, per-task executions, and the transition tables
// of docs/workflows.md §Execution state machines. The tables are data
// and are tested against the mermaid diagrams in the doc.
package executions

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/jobs"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
)

// ExecutionState is the WorkflowExecution lifecycle.
type ExecutionState string

// Execution states (docs/workflows.md mermaid).
const (
	ExecPending        ExecutionState = "PENDING"
	ExecValidating     ExecutionState = "VALIDATING"
	ExecQueued         ExecutionState = "QUEUED"
	ExecRunning        ExecutionState = "RUNNING"
	ExecSucceeded      ExecutionState = "SUCCEEDED"
	ExecFailed         ExecutionState = "FAILED"
	ExecPartialFailure ExecutionState = "PARTIAL_FAILURE"
	ExecCanceling      ExecutionState = "CANCELING"
	ExecCanceled       ExecutionState = "CANCELED"
)

// TaskState is the TaskExecution lifecycle.
type TaskState string

// Task states (docs/workflows.md mermaid).
const (
	TaskPending    TaskState = "PENDING"
	TaskBlocked    TaskState = "BLOCKED"
	TaskReady      TaskState = "READY"
	TaskAdmitting  TaskState = "ADMITTING"
	TaskSubmitting TaskState = "SUBMITTING"
	TaskQueued     TaskState = "QUEUED"
	TaskRunning    TaskState = "RUNNING"
	TaskCompleted  TaskState = "COMPLETED"
	TaskFailed     TaskState = "FAILED"
	TaskSkipped    TaskState = "SKIPPED"
	TaskCanceled   TaskState = "CANCELED"
)

// ExecutionTransitions is the WorkflowExecution edge set; it must
// equal the mermaid diagram in docs/workflows.md.
var ExecutionTransitions = map[ExecutionState][]ExecutionState{
	ExecPending:    {ExecValidating, ExecCanceling},
	ExecValidating: {ExecQueued, ExecFailed},
	ExecQueued:     {ExecRunning, ExecCanceling},
	ExecRunning: {ExecSucceeded, ExecFailed, ExecPartialFailure,
		ExecCanceling},
	ExecCanceling: {ExecCanceled},
}

// TaskTransitions is the TaskExecution edge set; it must equal the
// mermaid diagram in docs/workflows.md.
var TaskTransitions = map[TaskState][]TaskState{
	TaskPending:    {TaskBlocked, TaskReady},
	TaskBlocked:    {TaskReady, TaskSkipped, TaskCanceled},
	TaskReady:      {TaskAdmitting, TaskCanceled},
	TaskAdmitting:  {TaskSubmitting, TaskReady, TaskFailed},
	TaskSubmitting: {TaskQueued, TaskReady, TaskFailed},
	TaskQueued:     {TaskRunning, TaskCanceled},
	TaskRunning:    {TaskCompleted, TaskFailed, TaskCanceled},
	TaskFailed:     {TaskReady},
}

// Terminal reports whether s is a terminal task state.
func Terminal(s TaskState) bool {
	switch s {
	case TaskCompleted, TaskFailed, TaskSkipped, TaskCanceled:
		return true
	}
	return false
}

// TerminalOK reports whether s is a successful terminal state
// (dependency satisfied).
func TerminalOK(s TaskState) bool {
	return s == TaskCompleted || s == TaskSkipped
}

// Execution is the durable WorkflowExecution record.
type Execution struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	ProjectID         uuid.UUID
	WorkflowID        uuid.UUID
	WorkflowVersionID uuid.UUID
	SpecHash          [32]byte
	Parameters        []byte // jsonb
	Strategy          string
	State             ExecutionState
	StateReason       string
	RequestedBy       uuid.UUID
	CreatedAt         time.Time
	StartedAt         *time.Time
	EndedAt           *time.Time
	UpdatedAt         time.Time
	Version           int64
}

// TaskExecution is one DAG node instance (index < count under
// fan-out).
type TaskExecution struct {
	ID           uuid.UUID
	ExecutionID  uuid.UUID
	TenantID     uuid.UUID
	TaskName     string
	Index        int
	Count        int
	Attempt      int
	State        TaskState
	StateReason  string
	JobID        *uuid.UUID
	Spec         *admission.ExecutionSpec // frozen at admit; nil before
	SpecDigest   *validation.Digest
	ValidationID *uuid.UUID
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Version      int64
}

// TaskPatch is the mutable-field update applied by TransitionTask.
type TaskPatch struct {
	State        *TaskState
	Reason       *string
	JobID        *uuid.UUID
	ClearJob     bool // retry: job_id -> NULL
	ClearSpec    bool // retry: execution_spec{,_digest} -> NULL
	Attempt      *int
	Spec         *admission.ExecutionSpec // write-once
	SpecDigest   *validation.Digest
	ValidationID *uuid.UUID
}

// ExecPatch is the mutable-field update applied by TransitionExec.
type ExecPatch struct {
	State     *ExecutionState
	Reason    *string
	StartedAt *time.Time
	EndedAt   *time.Time
}

// ExecFilter scopes List.
type ExecFilter struct {
	WorkflowID  *uuid.UUID
	States      []ExecutionState
	RequestedBy *uuid.UUID // self-only listing
}

// Execer is the in-transaction handle handed to enqueue hooks —
// aliased from workqueue so the domain need not import pgx.
type Execer = workqueue.Execer

// EnqueueFunc enqueues a work item inside a transaction.
type EnqueueFunc func(ex Execer) error

// IdemRecord is the idempotency_keys row written with the execution
// insert (same table/semantics as jobs).
type IdemRecord struct {
	Key         string
	RequestHash [32]byte
	Status      int
	Body        []byte
	ResourceID  uuid.UUID
	ExpiresAt   time.Time
}

// CreateResult is CreateWithIdempotency's outcome.
type CreateResult struct {
	Execution Execution
	Replayed  bool
	Status    int
	Body      []byte
}

// Repository is the executions persistence port.
type Repository interface {
	// CreateWithIdempotency inserts the execution, the idempotency
	// row, and runs enqueue — atomically. A same-key replay returns
	// the stored response; a different request hash is Conflict
	// IDEMPOTENCY_MISMATCH.
	CreateWithIdempotency(ctx context.Context, scope tenants.Scope,
		e Execution, idem IdemRecord, enqueue EnqueueFunc) (CreateResult, error)
	Get(ctx context.Context, scope tenants.Scope, tenantID,
		id uuid.UUID) (Execution, error)
	List(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID,
		f ExecFilter, page tenants.Page) ([]Execution, string, error)
	// TransitionExec applies p guarded by (id, fromState,
	// fromVersion); a zero-row update returns ErrTransitionStale so
	// the worker reloads and re-evaluates.
	TransitionExec(ctx context.Context, scope tenants.Scope,
		id uuid.UUID, fromState ExecutionState, fromVersion int64,
		p ExecPatch, enqueue EnqueueFunc) (Execution, error)
	// Materialize inserts the materialized tasks and transitions the
	// execution (VALIDATING → QUEUED) with enqueue — atomically.
	Materialize(ctx context.Context, scope tenants.Scope, execID uuid.UUID,
		fromState ExecutionState, fromVersion int64,
		tasks []TaskExecution, p ExecPatch,
		enqueue EnqueueFunc) (Execution, error)
	// AdmitTask freezes the task's spec, sets job_id, transitions to
	// SUBMITTING and inserts the job + enqueue — atomically.
	AdmitTask(ctx context.Context, scope tenants.Scope,
		taskID uuid.UUID, fromState TaskState, fromVersion int64,
		p TaskPatch, j jobs.Job, enqueue EnqueueFunc) (TaskExecution, error)
	// CancelExec moves a cancelable execution to CANCELING with
	// enqueue; zero rows → ErrTransitionStale.
	CancelExec(ctx context.Context, scope tenants.Scope,
		id uuid.UUID, fromVersion int64,
		enqueue EnqueueFunc) (Execution, error)
	ListTasks(ctx context.Context, scope tenants.Scope, tenantID,
		executionID uuid.UUID) ([]TaskExecution, error)
	GetTask(ctx context.Context, scope tenants.Scope, tenantID,
		id uuid.UUID) (TaskExecution, error)
	GetTaskByJob(ctx context.Context, scope tenants.Scope,
		jobID uuid.UUID) (TaskExecution, error)
	// TransitionTask applies p guarded by (id, fromState,
	// fromVersion); zero rows → ErrTransitionStale.
	TransitionTask(ctx context.Context, scope tenants.Scope,
		id uuid.UUID, fromState TaskState, fromVersion int64,
		p TaskPatch, enqueue EnqueueFunc) (TaskExecution, error)
}

// ErrTransitionStale is returned by Transition* when the guarded
// UPDATE matched zero rows — the worker reloads and re-evaluates.
var ErrTransitionStale = apperr.New(apperr.Conflict, "TRANSITION_STALE",
	"state or version changed underneath the worker")

// IsStale reports whether err is a stale-transition conflict.
func IsStale(err error) bool { return apperr.Is(err, apperr.Conflict) }
