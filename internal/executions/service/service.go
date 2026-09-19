// Package service is the executions application service: the
// idempotent execute endpoint, read/cancel operations and the
// per-task reads (frozen ExecutionSpec, ScriptValidation).
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/executions"
	"github.com/Exonical/custos/internal/executions/engine"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflows"
	"github.com/Exonical/custos/internal/workflowspec"
)

// ValidationReader fetches a persisted ScriptValidation by id.
type ValidationReader interface {
	Get(ctx context.Context, scope tenants.Scope, tenantID,
		id uuid.UUID) (validation.ScriptValidation, error)
}

// Deps wires the service.
type Deps struct {
	Repo        executions.Repository
	Workflows   workflows.Repository
	Validations ValidationReader // may be nil → task validation 404s
	AZ          authz.Authorizer
	Audit       audit.Recorder
}

// Service is the executions application service.
type Service struct {
	d Deps
}

// New wires the service.
func New(d Deps) *Service {
	if d.Repo == nil || d.Workflows == nil {
		panic("executions service: Repo and Workflows are required")
	}
	return &Service{d: d}
}

func (s *Service) auditEvent(ctx context.Context, p authn.Principal,
	tenantID uuid.UUID, action, targetID, reason, result string,
	details map[string]any) {
	if s.d.Audit == nil {
		return
	}
	actor := audit.ActorUser
	if p.Kind == authn.KindService {
		actor = audit.ActorService
	}
	_ = s.d.Audit.Record(ctx, audit.Event{
		Actor:    audit.Actor{Type: actor, ID: p.UserID.String()},
		Action:   action,
		Target:   audit.Target{Type: "workflow_execution", ID: targetID},
		Result:   result,
		Reason:   reason,
		TenantID: &tenantID,
		Details:  details,
	})
}

func execResource(e executions.Execution) authz.Resource {
	return authz.Resource{
		Kind:      "execution",
		ID:        e.ID.String(),
		TenantID:  e.TenantID.String(),
		ProjectID: e.ProjectID.String(),
		OwnerID:   e.RequestedBy.String(),
	}
}

// --- execute -----------------------------------------------------------------

// ExecuteInput is the POST /workflow-executions body.
type ExecuteInput struct {
	WorkflowID uuid.UUID
	VersionID  *uuid.UUID // nil → latest published
	Parameters json.RawMessage
}

// Result is Execute's outcome.
type Result struct {
	Execution executions.Execution
	Replayed  bool
	Status    int
	Body      []byte
}

// Execute creates a PENDING execution and enqueues
// execution.advance, idempotently on (tenant, Idempotency-Key).
func (s *Service) Execute(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, in ExecuteInput, idemKey string,
	requestHash [32]byte) (Result, error) {
	scope := tenants.ScopeFor(&tc)
	wf, err := s.d.Workflows.Get(ctx, scope, tc.Tenant.ID, in.WorkflowID)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return Result{}, apperr.New(apperr.NotFound, "WORKFLOW_UNKNOWN",
				"workflow is not in this tenant")
		}
		return Result{}, err
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowExecute,
		authz.Resource{
			Kind:      "project",
			ID:        wf.ProjectID.String(),
			TenantID:  wf.TenantID.String(),
			ProjectID: wf.ProjectID.String(),
		}, s.d.Audit); err != nil {
		return Result{}, err
	}
	var v workflows.Version
	if in.VersionID != nil {
		v, err = s.d.Workflows.GetVersion(ctx, scope, tc.Tenant.ID,
			wf.ID, *in.VersionID)
		if err != nil {
			return Result{}, err
		}
		if v.State != workflows.VersionPublished {
			return Result{}, apperr.New(apperr.Conflict, "VERSION_STATE",
				"only published versions can be executed")
		}
	} else {
		if wf.LatestPublishedVersion == nil {
			return Result{}, apperr.New(apperr.Conflict,
				"NO_PUBLISHED_VERSION",
				"workflow has no published version")
		}
		v, err = s.d.Workflows.GetVersion(ctx, scope, tc.Tenant.ID,
			wf.ID, *wf.LatestPublishedVersion)
		if err != nil {
			return Result{}, err
		}
	}
	strategy := "auto"
	if spec, derr := workflowspec.Decode(v.Spec, "application/json"); derr == nil &&
		spec.Spec.Execution != nil && spec.Spec.Execution.Strategy != "" {
		strategy = spec.Spec.Execution.Strategy
	}
	params := in.Parameters
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	now := time.Now().UTC()
	e := executions.Execution{
		ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID,
		ProjectID: wf.ProjectID, WorkflowID: wf.ID,
		WorkflowVersionID: v.ID, SpecHash: v.SpecHash,
		Parameters: params, Strategy: strategy,
		State: executions.ExecPending, RequestedBy: p.UserID,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	res, err := s.d.Repo.CreateWithIdempotency(ctx, scope, e,
		executions.IdemRecord{
			Key: idemKey, RequestHash: requestHash,
			Status: 202, ResourceID: e.ID,
			ExpiresAt: now.Add(24 * time.Hour),
		}, func(ex executions.Execer) error {
			_, err := workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{
				Kind:     engine.KindAdvance,
				Key:      "execution:" + e.ID.String(),
				TenantID: &e.TenantID,
				Payload: map[string]string{
					"execution_id": e.ID.String()},
			})
			return err
		})
	if err != nil {
		return Result{}, err
	}
	if res.Replayed {
		return Result{Execution: res.Execution, Replayed: true,
			Status: res.Status, Body: res.Body}, nil
	}
	s.auditEvent(ctx, p, e.TenantID, "workflow.execution.submitted",
		e.ID.String(), "", audit.ResultAllow, map[string]any{
			"workflow":  wf.Name,
			"version":   v.Number,
			"spec_hash": fmt.Sprintf("%x", e.SpecHash),
			"strategy":  strategy,
		})
	return Result{Execution: e}, nil
}

// --- reads -------------------------------------------------------------------

// Get returns an execution the principal may read.
func (s *Service) Get(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, id uuid.UUID) (executions.Execution, error) {
	e, err := s.d.Repo.Get(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, id)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return e, apperr.New(apperr.NotFound, "EXECUTION_UNKNOWN",
				"execution is not in this tenant")
		}
		return e, err
	}
	if err := s.canRead(ctx, p, e); err != nil {
		return executions.Execution{}, err
	}
	return e, nil
}

// canRead permits owner (execution.read.self), else
// execution.read.project, else execution.read.tenant.
func (s *Service) canRead(ctx context.Context, p authn.Principal,
	e executions.Execution) error {
	res := execResource(e)
	if e.RequestedBy == p.UserID {
		return authz.Require(ctx, s.d.AZ, p, authz.ExecutionReadSelf,
			res, s.d.Audit)
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.ExecutionReadProject,
		res, nil); err == nil {
		return nil
	}
	return authz.Require(ctx, s.d.AZ, p, authz.ExecutionReadTenant,
		res, s.d.Audit)
}

// List returns executions; tenant readers see all, others only their
// own (execution.read.self).
func (s *Service) List(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, f executions.ExecFilter,
	page tenants.Page) ([]executions.Execution, string, error) {
	res := authz.Resource{Kind: "tenant", ID: tc.Tenant.ID.String(),
		TenantID: tc.Tenant.ID.String()}
	if err := authz.Require(ctx, s.d.AZ, p, authz.ExecutionReadTenant,
		res, nil); err != nil {
		if err := authz.Require(ctx, s.d.AZ, p, authz.ExecutionReadSelf,
			res, s.d.Audit); err != nil {
			return nil, "", err
		}
		f.RequestedBy = &p.UserID
	}
	return s.d.Repo.List(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, f, page)
}

// ListTasks returns an execution's task rows (read-gated on the
// execution).
func (s *Service) ListTasks(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, execID uuid.UUID) (
	[]executions.TaskExecution, error) {
	e, err := s.Get(ctx, p, tc, execID)
	if err != nil {
		return nil, err
	}
	return s.d.Repo.ListTasks(ctx, tenants.ScopeFor(&tc), e.TenantID, e.ID)
}

// GetTask returns one task execution (read-gated on the execution).
func (s *Service) GetTask(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, execID,
	taskID uuid.UUID) (executions.TaskExecution, error) {
	e, err := s.Get(ctx, p, tc, execID)
	if err != nil {
		return executions.TaskExecution{}, err
	}
	t, err := s.d.Repo.GetTask(ctx, tenants.ScopeFor(&tc), e.TenantID,
		taskID)
	if err != nil || t.ExecutionID != e.ID {
		return executions.TaskExecution{}, apperr.New(apperr.NotFound,
			"TASK_UNKNOWN", "task execution is not in this execution")
	}
	return t, nil
}

// TaskSpec returns the task's frozen ExecutionSpec.
func (s *Service) TaskSpec(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, execID,
	taskID uuid.UUID) (*admission.ExecutionSpec, error) {
	t, err := s.GetTask(ctx, p, tc, execID, taskID)
	if err != nil {
		return nil, err
	}
	if t.Spec == nil {
		return nil, apperr.New(apperr.NotFound, "SPEC_NOT_FROZEN",
			"the task has not been admitted yet")
	}
	return t.Spec, nil
}

// TaskValidation returns the ScriptValidation recorded at admit.
func (s *Service) TaskValidation(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, execID,
	taskID uuid.UUID) (validation.ScriptValidation, error) {
	t, err := s.GetTask(ctx, p, tc, execID, taskID)
	if err != nil {
		return validation.ScriptValidation{}, err
	}
	if t.ValidationID == nil || s.d.Validations == nil {
		return validation.ScriptValidation{}, apperr.New(apperr.NotFound,
			"VALIDATION_UNKNOWN", "the task has no validation record")
	}
	return s.d.Validations.Get(ctx, tenants.ScopeFor(&tc), t.TenantID,
		*t.ValidationID)
}

// --- cancel ------------------------------------------------------------------

// Cancel moves a cancelable execution to CANCELING and enqueues the
// advance that drives job cancels.
func (s *Service) Cancel(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, id uuid.UUID) error {
	scope := tenants.ScopeFor(&tc)
	e, err := s.Get(ctx, p, tc, id)
	if err != nil {
		return err
	}
	res := execResource(e)
	if e.RequestedBy == p.UserID {
		err = authz.Require(ctx, s.d.AZ, p, authz.ExecutionCancelSelf,
			res, s.d.Audit)
	} else {
		err = authz.Require(ctx, s.d.AZ, p, authz.ExecutionCancelAny,
			res, s.d.Audit)
	}
	if err != nil {
		return err
	}
	switch e.State {
	case executions.ExecPending, executions.ExecQueued,
		executions.ExecRunning:
	default:
		return apperr.New(apperr.Conflict, "EXECUTION_TERMINAL",
			"execution is already terminal")
	}
	_, err = s.d.Repo.CancelExec(ctx, scope, e.ID, e.Version,
		func(ex executions.Execer) error {
			_, err := workqueue.Enqueue(ctx, ex,
				workqueue.EnqueueRequest{
					Kind:     engine.KindAdvance,
					Key:      "execution:" + e.ID.String(),
					TenantID: &e.TenantID,
					Payload: map[string]string{
						"execution_id": e.ID.String()},
				})
			return err
		})
	if err != nil {
		if executions.IsStale(err) {
			return apperr.New(apperr.Conflict, "EXECUTION_STATE",
				"execution changed state concurrently")
		}
		return err
	}
	s.auditEvent(ctx, p, e.TenantID, "workflow.execution.canceled",
		e.ID.String(), "", audit.ResultAllow, nil)
	return nil
}
