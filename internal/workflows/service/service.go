// Package service is the workflows application service: authoring
// CRUD, spec-version persistence, the full validation pipeline
// (steps 1-8) and the publish gate (docs/workflows.md §Validation,
// §Idempotency). Executions are M5-C.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/apperr"
	policiessvc "github.com/Exonical/custos/internal/policies/service"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	"github.com/Exonical/custos/internal/sbatchexport"
	"github.com/Exonical/custos/internal/scripts"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/submission"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/pipeline"
	vpolicy "github.com/Exonical/custos/internal/validation/policy"
	"github.com/Exonical/custos/internal/validation/sbatchimport"
	"github.com/Exonical/custos/internal/workflows"
	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/expr"
	wfvalidate "github.com/Exonical/custos/internal/workflowspec/validate"
)

// ValidationStore persists and queries ScriptValidation rows.
type ValidationStore interface {
	Put(ctx context.Context, scope tenants.Scope,
		sv validation.ScriptValidation) error
	Latest(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID,
		digest validation.Digest, policyVersion int64,
		inputHash uint64) (validation.ScriptValidation, error)
	ListByWorkflowVersion(ctx context.Context, scope tenants.Scope,
		tenantID, wfvID uuid.UUID) ([]validation.ScriptValidation, error)
}

// Deps wires the service.
type Deps struct {
	Repo            workflows.Repository
	SecretReference func(context.Context, uuid.UUID, string) (wfvalidate.SecretReferenceInfo, bool)
	Projects        *projectsvc.Service
	Policies        *policiessvc.Service
	Clusters        clusters.Repository
	Pipeline        *pipeline.Pipeline
	VPolicy         *vpolicy.Service
	Validations     ValidationStore
	Scripts         scripts.Store
	Metrics         *pipeline.Metrics // may be nil
	AZ              authz.Authorizer
	Audit           audit.Recorder
}

// Service is the workflows application service.
type Service struct {
	d Deps
}

// New wires the service. Pipeline, VPolicy, Validations and Scripts
// are mandatory: the publish gate must never run without the durable
// validation path.
func New(d Deps) *Service {
	if d.Repo == nil || d.Pipeline == nil || d.VPolicy == nil ||
		d.Validations == nil || d.Scripts == nil {
		panic("workflows service: Repo, Pipeline, VPolicy, Validations and Scripts are required")
	}
	return &Service{d: d}
}

func (s *Service) audit(ctx context.Context, p authn.Principal,
	tenantID uuid.UUID, action, targetID, reason, result string,
	details map[string]any) {
	actor := audit.ActorUser
	if p.Kind == authn.KindService {
		actor = audit.ActorService
	}
	_ = s.d.Audit.Record(ctx, audit.Event{
		Actor:    audit.Actor{Type: actor, ID: p.UserID.String()},
		Action:   action,
		Target:   audit.Target{Type: "workflow", ID: targetID},
		Result:   result,
		Reason:   reason,
		TenantID: &tenantID,
		Details:  details,
	})
}

func wfResource(w workflows.Workflow, owner uuid.UUID) authz.Resource {
	return authz.Resource{
		Kind:      "workflow",
		ID:        w.ID.String(),
		TenantID:  w.TenantID.String(),
		ProjectID: w.ProjectID.String(),
		OwnerID:   owner.String(),
	}
}

func fieldErrs(errs []workflowspec.FieldError) error {
	if len(errs) == 0 {
		return nil
	}
	e := apperr.New(apperr.Validation, "SPEC_INVALID",
		fmt.Sprintf("workflow spec failed validation (%d errors)", len(errs)))
	for _, fe := range errs {
		e.Details = append(e.Details, apperr.Detail{
			Field:  fe.Path,
			Reason: fe.Code + ": " + fe.Message,
		})
	}
	return e
}

// --- workflow records -------------------------------------------------------

// CreateInput is the POST /workflows body.
type CreateInput struct {
	ProjectID   uuid.UUID
	Name        string
	Description string
}

// Create persists a workflow record (workflow.create on the project).
func (s *Service) Create(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, in CreateInput) (workflows.Workflow, error) {
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowCreate,
		authz.Resource{Kind: "project", ID: in.ProjectID.String(),
			TenantID:  tc.Tenant.ID.String(),
			ProjectID: in.ProjectID.String(),
			OwnerID:   p.UserID.String()}, s.d.Audit); err != nil {
		return workflows.Workflow{}, err
	}
	now := time.Now().UTC()
	w := workflows.Workflow{
		ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID,
		ProjectID: in.ProjectID, Name: in.Name,
		Description: in.Description, State: workflows.StateActive,
		CreatedBy: p.UserID, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if err := s.d.Repo.Create(ctx, tenants.ScopeFor(&tc), w); err != nil {
		return workflows.Workflow{}, err
	}
	return w, nil
}

// Get returns one workflow (workflow.read).
func (s *Service) Get(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, id uuid.UUID) (workflows.Workflow, error) {
	w, err := s.d.Repo.Get(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, id)
	if err != nil {
		return workflows.Workflow{}, err
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowRead,
		wfResource(w, w.CreatedBy), s.d.Audit); err != nil {
		return workflows.Workflow{}, err
	}
	return w, nil
}

// List returns workflows (workflow.read on the tenant).
func (s *Service) List(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, projectID *uuid.UUID) ([]workflows.Workflow, error) {
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowRead,
		authz.Resource{Kind: "tenant", ID: tc.Tenant.ID.String(),
			TenantID: tc.Tenant.ID.String()}, s.d.Audit); err != nil {
		return nil, err
	}
	return s.d.Repo.List(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, projectID)
}

// PatchInput carries editable workflow fields.
type PatchInput struct {
	Name        *string
	Description *string
	Version     int64
}

// Patch applies metadata edits with optimistic locking.
func (s *Service) Patch(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, id uuid.UUID, in PatchInput) (workflows.Workflow, error) {
	w, err := s.Get(ctx, p, tc, id)
	if err != nil {
		return workflows.Workflow{}, err
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowCreate,
		wfResource(w, w.CreatedBy), s.d.Audit); err != nil {
		return workflows.Workflow{}, err
	}
	if in.Name != nil {
		w.Name = *in.Name
	}
	if in.Description != nil {
		w.Description = *in.Description
	}
	w.UpdatedAt = time.Now().UTC()
	if err := s.d.Repo.Update(ctx, tenants.ScopeFor(&tc), w, in.Version); err != nil {
		return workflows.Workflow{}, err
	}
	w.Version++
	return w, nil
}

// Archive soft-deletes the workflow.
func (s *Service) Archive(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, id uuid.UUID, expectVersion int64) error {
	w, err := s.Get(ctx, p, tc, id)
	if err != nil {
		return err
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowCreate,
		wfResource(w, w.CreatedBy), s.d.Audit); err != nil {
		return err
	}
	w.State = workflows.StateArchived
	w.UpdatedAt = time.Now().UTC()
	return s.d.Repo.Update(ctx, tenants.ScopeFor(&tc), w, expectVersion)
}

// --- versions ----------------------------------------------------------------

// decodeSpec strict-decodes a submission, validates steps 1-4, and
// content-addresses inline payloads without replacing their inline source.
func (s *Service) decodeSpec(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, body []byte, contentType string) (workflowspec.Workflow,
	[]byte, [32]byte, error) {
	w, err := workflowspec.Decode(body, contentType)
	if err != nil {
		return workflowspec.Workflow{}, nil, [32]byte{},
			apperr.New(apperr.Invalid, "MALFORMED", err.Error())
	}
	if err := fieldErrs(wfvalidate.Static(w)); err != nil {
		return workflowspec.Workflow{}, nil, [32]byte{}, err
	}
	scope := tenants.ScopeFor(&tc)
	for _, task := range w.Spec.Tasks {
		if task.Script == nil || !task.Script.HasInline() {
			continue
		}
		payload := []byte(task.Script.Inline)
		digest, err := s.d.Scripts.Put(ctx, scope, tc.Tenant.ID,
			task.Script.EffectiveLanguage(), payload, p.UserID)
		if err != nil {
			return workflowspec.Workflow{}, nil, [32]byte{}, err
		}
		if digest != validation.DigestOf(payload) {
			return workflowspec.Workflow{}, nil, [32]byte{}, apperr.New(
				apperr.Internal, "INTERNAL", "inline script store returned a mismatched digest")
		}
	}
	canon, err := workflowspec.Canonical(w)
	if err != nil {
		return workflowspec.Workflow{}, nil, [32]byte{}, err
	}
	hash, err := workflowspec.SpecHash(w)
	if err != nil {
		return workflowspec.Workflow{}, nil, [32]byte{}, err
	}
	return w, canon, hash, nil
}

// CreateVersion validates steps 1-4 and stores a new draft.
func (s *Service) CreateVersion(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID uuid.UUID,
	body []byte, contentType string) (workflows.Version, error) {
	w, err := s.Get(ctx, p, tc, workflowID)
	if err != nil {
		return workflows.Version{}, err
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowCreate,
		wfResource(w, w.CreatedBy), s.d.Audit); err != nil {
		return workflows.Version{}, err
	}
	if w.State == workflows.StateArchived {
		return workflows.Version{}, apperr.New(apperr.Conflict,
			"WORKFLOW_ARCHIVED", "workflow is archived")
	}
	_, canon, hash, err := s.decodeSpec(ctx, p, tc, body, contentType)
	if err != nil {
		return workflows.Version{}, err
	}
	scope := tenants.ScopeFor(&tc)
	n, err := s.d.Repo.NextVersionNumber(ctx, scope, workflowID)
	if err != nil {
		return workflows.Version{}, err
	}
	v := workflows.Version{
		ID: uuid.Must(uuid.NewV7()), WorkflowID: workflowID,
		TenantID: tc.Tenant.ID, Number: n,
		State:         workflows.VersionDraft,
		SchemaVersion: workflowspec.APIVersionV1Alpha1,
		Spec:          canon, SpecHash: hash, Layout: []byte(`{}`),
		CreatedBy: p.UserID, CreatedAt: time.Now().UTC(), Version: 1,
	}
	if err := s.d.Repo.CreateVersion(ctx, scope, v); err != nil {
		return workflows.Version{}, err
	}
	return v, nil
}

// GetVersion returns one version.
func (s *Service) GetVersion(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID) (workflows.Version, error) {
	w, err := s.Get(ctx, p, tc, workflowID)
	if err != nil {
		return workflows.Version{}, err
	}
	return s.d.Repo.GetVersion(ctx, tenants.ScopeFor(&tc), w.TenantID,
		workflowID, versionID)
}

// ListVersions returns a workflow's versions.
func (s *Service) ListVersions(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID uuid.UUID) ([]workflows.Version, error) {
	w, err := s.Get(ctx, p, tc, workflowID)
	if err != nil {
		return nil, err
	}
	return s.d.Repo.ListVersions(ctx, tenants.ScopeFor(&tc), w.TenantID,
		workflowID)
}

// UpdateDraft replaces a draft's spec (409 VERSION_IMMUTABLE
// otherwise).
func (s *Service) UpdateDraft(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID,
	body []byte, contentType string, expectVersion int64) (workflows.Version, error) {
	w, err := s.Get(ctx, p, tc, workflowID)
	if err != nil {
		return workflows.Version{}, err
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowCreate,
		wfResource(w, w.CreatedBy), s.d.Audit); err != nil {
		return workflows.Version{}, err
	}
	scope := tenants.ScopeFor(&tc)
	v, err := s.d.Repo.GetVersion(ctx, scope, w.TenantID, workflowID, versionID)
	if err != nil {
		return workflows.Version{}, err
	}
	if v.State != workflows.VersionDraft {
		return workflows.Version{}, apperr.New(apperr.Conflict,
			"VERSION_IMMUTABLE", "only draft versions may be edited")
	}
	if err := s.d.Repo.CheckDraftEditable(ctx, scope, v); err != nil {
		return workflows.Version{}, err
	}
	_, canon, hash, err := s.decodeSpec(ctx, p, tc, body, contentType)
	if err != nil {
		return workflows.Version{}, err
	}
	v.Spec, v.SpecHash = canon, hash
	if err := s.d.Repo.UpdateDraftSpec(ctx, scope, v, expectVersion); err != nil {
		return workflows.Version{}, err
	}
	v.Version++
	return v, nil
}

// UpdateLayout stores editor layout on draft or published versions.
func (s *Service) UpdateLayout(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID,
	layout []byte, expectVersion int64) error {
	w, err := s.Get(ctx, p, tc, workflowID)
	if err != nil {
		return err
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowCreate,
		wfResource(w, w.CreatedBy), s.d.Audit); err != nil {
		return err
	}
	scope := tenants.ScopeFor(&tc)
	v, err := s.d.Repo.GetVersion(ctx, scope, w.TenantID, workflowID, versionID)
	if err != nil {
		return err
	}
	if v.State == workflows.VersionDeprecated {
		return apperr.New(apperr.Conflict, "VERSION_IMMUTABLE",
			"deprecated versions are immutable")
	}
	v.Layout = layout
	return s.d.Repo.UpdateLayout(ctx, scope, v, expectVersion)
}

// --- validation context (steps 5-8) ------------------------------------------

// buildContext assembles the contextual validation inputs for a
// workflow (its project's effective policy + cluster bindings).
func (s *Service) buildContext(ctx context.Context, scope tenants.Scope,
	w workflows.Workflow) (wfvalidate.Context, error) {
	pol, err := s.d.Policies.Effective(ctx, scope, w.TenantID, w.ProjectID)
	if err != nil {
		return wfvalidate.Context{}, err
	}
	vpol, _, err := s.d.VPolicy.Effective(ctx, scope, w.TenantID, nil)
	if err != nil {
		return wfvalidate.Context{}, err
	}
	return wfvalidate.Context{
		ShellAllowed:   vpol.AllowShellTasks,
		ResourcePolicy: pol,
		SecretReference: func(name string) (wfvalidate.SecretReferenceInfo, bool) {
			if s.d.SecretReference == nil {
				return wfvalidate.SecretReferenceInfo{}, false
			}
			return s.d.SecretReference(ctx, w.TenantID, name)
		},
		Cluster: func(name string) (admission.Binding,
			validation.ClusterSnapshot, bool) {
			c, err := s.d.Clusters.GetByNameOrID(ctx, name)
			if err != nil {
				return admission.Binding{}, validation.ClusterSnapshot{}, false
			}
			b, err := s.d.Projects.ResolveBinding(ctx, scope,
				w.ProjectID, c.ID)
			if err != nil {
				return admission.Binding{}, validation.ClusterSnapshot{}, false
			}
			return b, workflows.ClusterSnapshot(&c), true
		},
	}, nil
}

// ValidateBody runs steps 1-8 on an unpersisted submission.
func (s *Service) ValidateBody(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID uuid.UUID,
	body []byte, contentType string) []workflowspec.FieldError {
	w, err := s.Get(ctx, p, tc, workflowID)
	if err != nil {
		return []workflowspec.FieldError{{Path: "", Code: "NOT_FOUND",
			Message: err.Error()}}
	}
	spec, err := workflowspec.Decode(body, contentType)
	if err != nil {
		return []workflowspec.FieldError{{Path: "", Code: "MALFORMED",
			Message: err.Error()}}
	}
	errs := wfvalidate.Static(spec)
	if len(errs) > 0 {
		return errs
	}
	vc, err := s.buildContext(ctx, tenants.ScopeFor(&tc), w)
	if err != nil {
		return []workflowspec.FieldError{{Path: "", Code: "INTERNAL",
			Message: err.Error()}}
	}
	return wfvalidate.Contextual(spec, vc)
}

// --- script-bearing tasks ---------------------------------------------------

// taskSnapshot resolves a task's validation input via the shared
// internal/workflows helper (same code path the engine's task.admit
// uses).
func (s *Service) taskSnapshot(ctx context.Context, scope tenants.Scope,
	w workflows.Workflow, spec workflowspec.Workflow,
	task workflowspec.Task) (validation.Input, *clusters.Cluster, error) {
	return workflows.TaskSnapshot(ctx, scope, s.d.Scripts, s.d.Clusters,
		w.TenantID, spec, task)
}

// runTaskValidation executes the pipeline for one task and persists
// the result scoped to (version, task).
func (s *Service) runTaskValidation(ctx context.Context,
	scope tenants.Scope, w workflows.Workflow, v workflows.Version,
	task workflowspec.Task, in validation.Input,
	policyVersion int64) (validation.ScriptValidation, error) {
	sv := s.d.Pipeline.Run(ctx, w.TenantID, pipeline.Request{
		In:                in,
		PolicyVersion:     policyVersion,
		WorkflowVersionID: &v.ID,
		TaskName:          task.Name,
	})
	if s.d.Metrics != nil {
		s.d.Metrics.Observe(ctx, sv)
	}
	if err := s.d.Validations.Put(ctx, scope, sv); err != nil {
		return validation.ScriptValidation{}, err
	}
	return sv, nil
}

// currentValidation returns the persisted validation for (version,
// task, digest, fingerprint, input context) when it exists, is valid
// and unexpired.
func (s *Service) currentValidation(ctx context.Context,
	scope tenants.Scope, w workflows.Workflow, v workflows.Version,
	taskName string, digest validation.Digest,
	policyVersion int64, inputHash uint64,
	now time.Time) (validation.ScriptValidation, bool) {
	sv, err := s.d.Validations.Latest(ctx, scope, w.TenantID, digest,
		policyVersion, inputHash)
	if err != nil || !sv.Valid || now.After(sv.ExpiresAt) {
		return validation.ScriptValidation{}, false
	}
	if sv.WorkflowVersionID == nil || *sv.WorkflowVersionID != v.ID ||
		sv.TaskName != taskName {
		return validation.ScriptValidation{}, false
	}
	return sv, true
}

// Publish runs the full gate: steps 1-8 plus a current valid
// ScriptValidation per script-bearing task; success flips state and
// latest_published_version_id atomically.
func (s *Service) Publish(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID,
	expectVersion int64) (workflows.Version, error) {
	w, err := s.Get(ctx, p, tc, workflowID)
	if err != nil {
		return workflows.Version{}, err
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowPublish,
		wfResource(w, w.CreatedBy), s.d.Audit); err != nil {
		return workflows.Version{}, err
	}
	scope := tenants.ScopeFor(&tc)
	v, err := s.d.Repo.GetVersion(ctx, scope, w.TenantID, workflowID, versionID)
	if err != nil {
		return workflows.Version{}, err
	}
	if v.State != workflows.VersionDraft {
		return workflows.Version{}, apperr.New(apperr.Conflict,
			"VERSION_IMMUTABLE", "only draft versions can be published")
	}
	spec, err := workflowspec.Decode(v.Spec, "application/json")
	if err != nil {
		return workflows.Version{}, err
	}
	if err := fieldErrs(wfvalidate.Static(spec)); err != nil {
		return workflows.Version{}, err
	}
	vc, err := s.buildContext(ctx, scope, w)
	if err != nil {
		return workflows.Version{}, err
	}
	if err := fieldErrs(wfvalidate.Contextual(spec, vc)); err != nil {
		return workflows.Version{}, err
	}
	// Script validations.
	now := time.Now().UTC()
	var failed []workflowspec.FieldError
	for i, task := range spec.Spec.Tasks {
		if task.Script == nil {
			continue
		}
		in, cluster, err := s.taskSnapshot(ctx, scope, w, spec, task)
		if err != nil {
			return workflows.Version{}, err
		}
		var cid *uuid.UUID
		if cluster != nil {
			cid = &cluster.ID
		}
		epol, efp, err := s.d.VPolicy.Effective(ctx, scope, w.TenantID, cid)
		if err != nil {
			return workflows.Version{}, err
		}
		in.Policy = epol.Effective()
		if _, ok := s.currentValidation(ctx, scope, w, v, task.Name,
			in.Digest, efp, pipeline.InputHash(in), now); ok {
			continue
		}
		sv, err := s.runTaskValidation(ctx, scope, w, v, task, in, efp)
		if err != nil {
			return workflows.Version{}, err
		}
		if !sv.Valid {
			for _, d := range sv.Diagnostics {
				if d.Severity.AtLeast(epol.BlockAt) ||
					d.Severity == validation.SeverityPolicy ||
					d.Severity == validation.SeveritySecurity {
					failed = append(failed, workflowspec.FieldError{
						Path:    fmt.Sprintf("spec.tasks[%d].script", i),
						Code:    d.Code,
						Message: d.Message})
				}
			}
		}
	}
	if len(failed) > 0 {
		s.audit(ctx, p, w.TenantID, "workflow.publish_denied",
			v.ID.String(), "task validation failed", audit.ResultDeny,
			map[string]any{"errors": len(failed)})
		return workflows.Version{}, fieldErrs(failed)
	}
	if err := s.d.Repo.SetVersionState(ctx, scope, v,
		workflows.VersionPublished, expectVersion); err != nil {
		return workflows.Version{}, err
	}
	s.audit(ctx, p, w.TenantID, "workflow.published", v.ID.String(), "",
		audit.ResultAllow, map[string]any{
			"spec_hash": fmt.Sprintf("%x", v.SpecHash),
			"number":    v.Number,
		})
	v.State = workflows.VersionPublished
	v.Version++
	return v, nil
}

// Deprecate moves a published version to deprecated.
func (s *Service) Deprecate(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID,
	expectVersion int64) error {
	w, err := s.Get(ctx, p, tc, workflowID)
	if err != nil {
		return err
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowPublish,
		wfResource(w, w.CreatedBy), s.d.Audit); err != nil {
		return err
	}
	scope := tenants.ScopeFor(&tc)
	v, err := s.d.Repo.GetVersion(ctx, scope, w.TenantID, workflowID, versionID)
	if err != nil {
		return err
	}
	if v.State != workflows.VersionPublished {
		return apperr.New(apperr.Conflict, "VERSION_STATE",
			"only published versions can be deprecated")
	}
	return s.d.Repo.SetVersionState(ctx, scope, v,
		workflows.VersionDeprecated, expectVersion)
}

// --- task-scoped routes ------------------------------------------------------

// findTask locates a task by name in a decoded spec.
func findTask(spec workflowspec.Workflow,
	name string) (workflowspec.Task, error) {
	for _, t := range spec.Spec.Tasks {
		if t.Name == name {
			return t, nil
		}
	}
	return workflowspec.Task{}, apperr.New(apperr.NotFound, "TASK_UNKNOWN",
		"task "+name+" is not in this version")
}

// TaskValidate runs the validation pipeline on one task's script and
// persists the result scoped to (version, task).
func (s *Service) TaskValidate(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID,
	taskName string) (validation.ScriptValidation, error) {
	w, v, spec, task, err := s.taskContext(ctx, p, tc,
		workflowID, versionID, taskName)
	if err != nil {
		return validation.ScriptValidation{}, err
	}
	scope := tenants.ScopeFor(&tc)
	in, cluster, err := s.taskSnapshot(ctx, scope, w, spec, task)
	if err != nil {
		return validation.ScriptValidation{}, err
	}
	var cid *uuid.UUID
	if cluster != nil {
		cid = &cluster.ID
	}
	pol, fp, err := s.d.VPolicy.Effective(ctx, scope, w.TenantID, cid)
	if err != nil {
		return validation.ScriptValidation{}, err
	}
	in.Policy = pol.Effective()
	sv, err := s.runTaskValidation(ctx, scope, w, v, task, in, fp)
	if err != nil {
		return validation.ScriptValidation{}, err
	}
	s.audit(ctx, p, w.TenantID, "workflow.task.validate", v.ID.String(),
		"", audit.ResultAllow, map[string]any{
			"task":          taskName,
			"script_digest": in.Digest.String(),
			"valid":         sv.Valid,
		})
	return sv, nil
}

// TaskImportSbatch converts an sbatch script for one task; the
// proposal is returned, never applied (the client PUTs the version).
func (s *Service) TaskImportSbatch(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID,
	taskName string, lang workflowspec.Language,
	script []byte) (sbatchimport.Proposal, error) {
	w, _, _, _, err := s.taskContext(ctx, p, tc,
		workflowID, versionID, taskName)
	if err != nil {
		return sbatchimport.Proposal{}, err
	}
	scope := tenants.ScopeFor(&tc)
	pol, _, err := s.d.VPolicy.Effective(ctx, scope, w.TenantID, nil)
	if err != nil {
		return sbatchimport.Proposal{}, err
	}
	if !pol.AllowLegacySbatchImport {
		s.audit(ctx, p, w.TenantID, "workflow.script.import", "",
			"legacy sbatch import disabled", audit.ResultDeny, nil)
		return sbatchimport.Proposal{}, apperr.New(apperr.Forbidden,
			"IMPORT_DISABLED",
			"legacy sbatch import is disabled by the effective validation policy")
	}
	prop, err := sbatchimport.Import(script, lang)
	if err != nil {
		return sbatchimport.Proposal{}, apperr.New(apperr.Validation,
			"IMPORT_FAILED", err.Error())
	}
	s.audit(ctx, p, w.TenantID, "workflow.script.import", "",
		"", audit.ResultAllow, map[string]any{
			"task":          taskName,
			"digest_before": validation.DigestOf(script).String(),
			"digest_after":  validation.DigestOf(prop.Rewritten).String(),
			"imported":      prop.Imported,
		})
	return prop, nil
}

const maxWorkflowImportScripts = 20

var workflowImportLabelInvalid = regexp.MustCompile(`[^a-z0-9-]+`)
var workflowImportLabelRuns = regexp.MustCompile(`-+`)

// SbatchImportFile is one named script in a multi-file import request.
type SbatchImportFile struct {
	Filename string
	Content  string
}

// SbatchWorkflowImportInput is the workflow-level sbatch import request.
type SbatchWorkflowImportInput struct {
	Name    string
	Scripts []SbatchImportFile
}

// SbatchImportedTask summarizes one imported script and its diagnostics.
type SbatchImportedTask struct {
	Filename     string                  `json:"filename"`
	Task         string                  `json:"task"`
	ScriptDigest string                  `json:"scriptDigest"`
	Imported     []string                `json:"imported"`
	Diagnostics  []validation.Diagnostic `json:"diagnostics"`
}

// SbatchWorkflowImportResult is the statically validated workflow import proposal.
type SbatchWorkflowImportResult struct {
	Spec  workflowspec.Workflow `json:"spec"`
	YAML  string                `json:"yaml"`
	Tasks []SbatchImportedTask  `json:"tasks"`
}

func sbatchLanguage(script []byte) workflowspec.Language {
	line, _, _ := strings.Cut(string(script), "\n")
	line = strings.TrimSuffix(line, "\r")
	fields := strings.Fields(line)
	if line == "#!/bin/sh" || strings.HasPrefix(line, "#!/bin/sh ") ||
		(len(fields) >= 2 && fields[0] == "#!/usr/bin/env" && fields[1] == "sh") {
		return workflowspec.LanguageSh
	}
	return workflowspec.LanguageBash
}

func sanitizeWorkflowLabel(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = workflowImportLabelInvalid.ReplaceAllString(value, "-")
	value = workflowImportLabelRuns.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		value = fallback
	}
	if len(value) > 63 {
		value = strings.TrimRight(value[:63], "-")
	}
	if value == "" {
		return "task"
	}
	return value
}

func uniqueWorkflowTaskName(value string, used map[string]bool) string {
	base := sanitizeWorkflowLabel(value, "task")
	if !used[base] {
		used[base] = true
		return base
	}
	for suffix := 2; ; suffix++ {
		tail := fmt.Sprintf("-%d", suffix)
		prefix := strings.TrimRight(base[:min(len(base), 63-len(tail))], "-")
		candidate := prefix + tail
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

func importedTaskResources(src workflowspec.Resources) (workflowspec.TaskResources, *workflowspec.ArraySpecYAML, []validation.Diagnostic) {
	var out workflowspec.TaskResources
	var array *workflowspec.ArraySpecYAML
	out.Nodes = src.Nodes
	out.Tasks = src.Tasks
	out.TasksPerNode = src.TasksPerNode
	out.CPUsPerTask = src.CPUsPerTask
	out.Constraints = src.Constraints
	out.Licenses = append([]string(nil), src.Licenses...)
	out.Exclusive = src.Exclusive
	if src.MemoryPerNodeMiB > 0 {
		out.Memory = fmt.Sprintf("%dMi", src.MemoryPerNodeMiB)
	}
	if src.MemoryPerCPUMiB > 0 {
		out.MemoryPerCPU = fmt.Sprintf("%dMi", src.MemoryPerCPUMiB)
	}
	if src.Walltime > 0 {
		out.Walltime = time.Duration(src.Walltime).String()
	}
	if src.GPU != nil {
		out.GPU = &workflowspec.GPURequest{Type: src.GPU.Type, Count: src.GPU.Count}
	}
	if src.Array != nil {
		array = &workflowspec.ArraySpecYAML{
			Start: src.Array.Start, End: src.Array.End,
			Step: src.Array.Step, MaxConcurrent: src.Array.MaxConcurrent,
		}
	}
	return out, array, nil
}

// ImportWorkflowSbatch proposes a new workflow made of independent script tasks.
// It stores only rewritten script bodies; workflow/version creation remains a
// separate client action so diagnostics can be reviewed before persistence.
func (s *Service) ImportWorkflowSbatch(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, in SbatchWorkflowImportInput) (SbatchWorkflowImportResult, error) {
	if err := authz.Require(ctx, s.d.AZ, p, authz.WorkflowCreate,
		authz.Resource{Kind: "tenant", ID: tc.Tenant.ID.String(), TenantID: tc.Tenant.ID.String()}, s.d.Audit); err != nil {
		return SbatchWorkflowImportResult{}, err
	}
	if len(in.Scripts) == 0 || len(in.Scripts) > maxWorkflowImportScripts {
		return SbatchWorkflowImportResult{}, apperr.New(apperr.Invalid,
			"WORKFLOW_IMPORT_COUNT", "scripts must contain between 1 and 20 files")
	}
	scope := tenants.ScopeFor(&tc)
	pol, _, err := s.d.VPolicy.Effective(ctx, scope, tc.Tenant.ID, nil)
	if err != nil {
		return SbatchWorkflowImportResult{}, err
	}
	if !pol.AllowLegacySbatchImport {
		s.audit(ctx, p, tc.Tenant.ID, "workflow.script.import", "",
			"legacy sbatch import disabled", audit.ResultDeny, nil)
		return SbatchWorkflowImportResult{}, apperr.New(apperr.Forbidden,
			"IMPORT_DISABLED", "legacy #SBATCH import is disabled by validation policy")
	}
	if len(in.Name) > 256 {
		return SbatchWorkflowImportResult{}, apperr.New(apperr.Invalid,
			"WORKFLOW_IMPORT_NAME", "workflow name exceeds 256 bytes")
	}

	used := make(map[string]bool, len(in.Scripts))
	out := SbatchWorkflowImportResult{
		Spec: workflowspec.Workflow{
			APIVersion: workflowspec.APIVersionV1Alpha1,
			Kind:       workflowspec.KindWorkflow,
			Spec:       workflowspec.Spec{Tasks: make([]workflowspec.Task, 0, len(in.Scripts))},
		},
		Tasks: make([]SbatchImportedTask, 0, len(in.Scripts)),
	}
	for i, file := range in.Scripts {
		if strings.TrimSpace(file.Filename) == "" {
			return SbatchWorkflowImportResult{}, apperr.New(apperr.Invalid,
				"WORKFLOW_IMPORT_FILENAME", fmt.Sprintf("scripts[%d].filename is required", i))
		}
		if len(file.Filename) > 1024 {
			return SbatchWorkflowImportResult{}, apperr.New(apperr.Invalid,
				"WORKFLOW_IMPORT_FILENAME", fmt.Sprintf("scripts[%d].filename exceeds 1024 bytes", i))
		}
		script := []byte(file.Content)
		if len(script) == 0 {
			return SbatchWorkflowImportResult{}, apperr.New(apperr.Invalid,
				"SCRIPT_REQUIRED", fmt.Sprintf("scripts[%d].content is empty", i))
		}
		if len(script) > validation.DefaultLimits.MaxScriptBytes {
			return SbatchWorkflowImportResult{}, apperr.New(apperr.Invalid,
				"SCRIPT_LIMITS", fmt.Sprintf("scripts[%d].content exceeds the 256 KiB script limit", i))
		}
		language := sbatchLanguage(script)
		proposal, err := sbatchimport.Import(script, language)
		if err != nil {
			return SbatchWorkflowImportResult{}, apperr.New(apperr.Validation,
				"IMPORT_FAILED", fmt.Sprintf("scripts[%d]: %v", i, err))
		}
		storedDigest, err := s.d.Scripts.Put(ctx, scope, tc.Tenant.ID,
			language, proposal.Rewritten, p.UserID)
		if err != nil {
			return SbatchWorkflowImportResult{}, err
		}
		stem := strings.TrimSuffix(path.Base(strings.ReplaceAll(file.Filename, "\\", "/")), path.Ext(path.Base(strings.ReplaceAll(file.Filename, "\\", "/"))))
		if proposal.Name != "" {
			stem = proposal.Name
		}
		if stem == "" {
			stem = fmt.Sprintf("task-%d", i+1)
		}
		taskName := uniqueWorkflowTaskName(stem, used)
		resources, array, resourceDiagnostics := importedTaskResources(proposal.Resources)
		diagnostics := make([]validation.Diagnostic, 0, len(proposal.Diagnostics)+len(resourceDiagnostics))
		diagnostics = append(diagnostics, proposal.Diagnostics...)
		diagnostics = append(diagnostics, resourceDiagnostics...)
		validation.SortDiagnostics(diagnostics)
		imported := append([]string{}, proposal.Imported...)
		task := workflowspec.Task{
			Name:             taskName,
			Launch:           workflowspec.LaunchSbatch,
			Resources:        resources,
			Array:            array,
			Script:           &workflowspec.ScriptRef{Digest: storedDigest.String(), Language: language},
			Partition:        proposal.Partition,
			QoS:              proposal.QoS,
			WorkingDirectory: proposal.WorkingDir,
			Stdout:           proposal.Stdout,
			Stderr:           proposal.Stderr,
		}
		out.Spec.Spec.Tasks = append(out.Spec.Spec.Tasks, task)
		out.Tasks = append(out.Tasks, SbatchImportedTask{
			Filename: file.Filename, Task: taskName,
			ScriptDigest: storedDigest.String(), Imported: imported,
			Diagnostics: diagnostics,
		})
		s.audit(ctx, p, tc.Tenant.ID, "workflow.script.import", "", "",
			audit.ResultAllow, map[string]any{
				"filename":      file.Filename,
				"task":          taskName,
				"digest_before": validation.DigestOf(script).String(),
				"digest_after":  storedDigest.String(),
				"imported":      imported,
			})
	}
	workflowName := sanitizeWorkflowLabel(in.Name, out.Spec.Spec.Tasks[0].Name)
	out.Spec.Metadata.Name = workflowName
	if errs := wfvalidate.Static(out.Spec); len(errs) != 0 {
		return SbatchWorkflowImportResult{}, fieldErrs(errs)
	}
	canonical, err := workflowspec.Canonical(out.Spec)
	if err != nil {
		return SbatchWorkflowImportResult{}, err
	}
	var yamlValue any
	if err := json.Unmarshal(canonical, &yamlValue); err != nil {
		return SbatchWorkflowImportResult{}, err
	}
	yamlBytes, err := yaml.Marshal(yamlValue)
	if err != nil {
		return SbatchWorkflowImportResult{}, err
	}
	out.YAML = string(yamlBytes)
	return out, nil
}

func mapExportError(err error) error {
	var unsupported *sbatchexport.UnsupportedError
	if !errors.As(err, &unsupported) {
		return err
	}
	apiErr := apperr.New(apperr.Validation, "EXPORT_UNSUPPORTED", unsupported.Error())
	apiErr.Details = append(apiErr.Details,
		apperr.Detail{Field: unsupported.Field, Reason: unsupported.Reason})
	return apiErr
}

// ExportTaskSbatch renders a workflow task without mutating the version.
func (s *Service) ExportTaskSbatch(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID, taskName string) (string, error) {
	w, version, spec, task, err := s.taskContext(ctx, p, tc, workflowID, versionID, taskName)
	if err != nil {
		return "", err
	}
	if task.Image != nil || task.Multinode != nil {
		_, err := sbatchexport.Render(spec, task, nil)
		return "", mapExportError(err)
	}
	var payload []byte
	if task.Script != nil {
		if task.Script.HasInline() {
			payload = []byte(task.Script.Inline)
		} else {
			digest, err := validation.ParseDigest(task.Script.Digest)
			if err != nil {
				return "", apperr.New(apperr.NotFound, "SCRIPT_UNKNOWN", "script payload not found")
			}
			payload, err = s.d.Scripts.Get(ctx, tenants.ScopeFor(&tc), w.TenantID, digest)
			if err != nil {
				return "", err
			}
		}
	}
	rendered, err := sbatchexport.RenderWithOptions(spec, task, payload,
		sbatchexport.RenderOptions{WorkflowVersion: &version.Number})
	if err != nil {
		return "", mapExportError(err)
	}
	return rendered, nil
}

// Preview is the read-only submission preview: the frozen
// ExecutionSpec, the wrapper script and the neutral JobSubmission.
type Preview struct {
	ExecutionSpec admission.ExecutionSpec `json:"executionSpec"`
	Wrapper       string                  `json:"wrapper"`
	JobSubmission slurm.JobSubmission     `json:"jobSubmission"`
}

// resolveSecretRef resolves a tenant SecretReference name to its id.
func (s *Service) resolveSecretRef(ctx context.Context, tenantID uuid.UUID,
	ref string) (uuid.UUID, error) {
	info, found := s.d.SecretReference(ctx, tenantID, ref)
	id, err := uuid.Parse(info.ID)
	if !found || err != nil {
		return uuid.UUID{}, apperr.New(apperr.Validation,
			"SECRET_REFERENCE_NOT_FOUND", "secret reference not found")
	}
	return id, nil
}

// PreviewSubmission builds — but never persists or submits — the
// submission for one task (attempt=1, caller as principal).
func (s *Service) PreviewSubmission(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID,
	taskName string) (Preview, error) {
	w, v, spec, task, err := s.taskContext(ctx, p, tc,
		workflowID, versionID, taskName)
	if err != nil {
		return Preview{}, err
	}
	if task.Script == nil {
		return Preview{}, apperr.New(apperr.Invalid, "TASK_NO_SCRIPT",
			"preview requires a script-bearing task")
	}
	scope := tenants.ScopeFor(&tc)
	in, cluster, err := s.taskSnapshot(ctx, scope, w, spec, task)
	if err != nil {
		return Preview{}, err
	}
	if cluster == nil {
		return Preview{}, apperr.New(apperr.Validation, "CLUSTER_UNKNOWN",
			"preview requires a resolved placement cluster")
	}
	pol, err := s.d.Policies.Effective(ctx, scope, w.TenantID, w.ProjectID)
	if err != nil {
		return Preview{}, err
	}
	binding, err := s.d.Projects.ResolveBinding(ctx, scope, w.ProjectID,
		cluster.ID)
	if err != nil {
		return Preview{}, apperr.New(apperr.Validation, "NO_CLUSTER_BINDING",
			"project has no enabled binding to this cluster")
	}
	interp, err := interpreterFor(in.Language)
	if err != nil {
		return Preview{}, err
	}
	env := map[string]string{}
	var secretRefs []admission.SecretEnvRef
	var wrapped []string
	for k, val := range task.Env {
		tpl, parseErr := expr.ParseTemplate(val)
		if parseErr == nil {
			if e, ok := tpl.SoleExpr(); ok {
				if path, sole := e.SoleRef(); sole && len(path) == 2 && path[0] == "secrets" {
					handle := path[1]
					use := spec.Spec.Secrets[handle]
					id, err := s.resolveSecretRef(ctx, w.TenantID, use.Ref)
					if err != nil {
						return Preview{}, err
					}
					secretRefs = append(secretRefs, admission.SecretEnvRef{
						Name: workflowspec.SecretEnvName(handle, use), ReferenceID: id,
						Mode: "env", Handle: handle})
					continue
				}
			}
		}
		env[k] = val
	}
	for handle, use := range spec.Spec.Secrets {
		if use.Use != "wrapped_token" {
			continue
		}
		id, err := s.resolveSecretRef(ctx, w.TenantID, use.Ref)
		if err != nil {
			return Preview{}, err
		}
		secretRefs = append(secretRefs, admission.SecretEnvRef{
			Name:        "CUSTOS_SECRET_" + workflowspec.SecretEnvName(handle, use) + "_WRAP_TOKEN",
			ReferenceID: id, Mode: use.Use, Handle: handle})
		wrapped = append(wrapped, handle)
	}
	slices.Sort(wrapped)
	slices.SortFunc(secretRefs, func(a, b admission.SecretEnvRef) int {
		if a.Handle < b.Handle {
			return -1
		}
		if a.Handle > b.Handle {
			return 1
		}
		return 0
	})
	specID := uuid.Must(uuid.NewV7())
	espec := admission.ExecutionSpec{
		ID:          specID,
		TenantID:    w.TenantID,
		ProjectID:   w.ProjectID,
		PrincipalID: p.UserID,
		TaskName:    task.Name,
		Launch:      task.EffectiveLaunch(),
		Attempt:     1,
		Cluster: admission.ClusterRef{
			ID: cluster.ID, Name: cluster.Name,
			APIVersion: cluster.APIVersion,
		},
		Partition:   task.Partition,
		QoS:         task.QoS,
		Environment: admission.EnvSet{User: env, SecretRefs: secretRefs},
		Payload: admission.PayloadRef{
			ScriptID:    specID,
			Digest:      in.Digest,
			Language:    in.Language,
			Interpreter: interp,
		},
		WorkingDir: task.WorkingDirectory,
		Stdout:     task.Stdout,
		Stderr:     task.Stderr,
		Security: admission.SecurityContext{
			SlurmUser:         cluster.ServiceUser,
			ImpersonationMode: string(cluster.IdentityMode),
			WrappedTokenRefs:  wrapped,
		},
	}
	var multinode *workflowspec.Multinode
	if task.Multinode != nil {
		resolved := *task.Multinode
		resolved.Implementation = resolved.EffectiveImplementation()
		resolved.ProcsPerNode = task.EffectiveProcsPerNode()
		multinode = &resolved
	}
	built, denial := admission.Build(admission.BuildInput{
		Spec: espec, Software: in.Software, Request: in.Resources,
		Image: task.Image, Multinode: multinode,
		CPUAffinity: task.Resources.CPUAffinity, ContainerEnv: env,
		Policy: pol, Binding: binding, Cluster: *in.Cluster,
	})
	if denial != nil {
		return Preview{}, apperr.New(apperr.Validation, "POLICY_VIOLATION",
			fmt.Sprintf("%s: %s", denial.Field, denial.Message))
	}
	built.AdmittedAt = time.Now().UTC()
	wrapper, err := submission.Wrapper(built, in.Script)
	if err != nil {
		return Preview{}, err
	}
	_ = v
	return Preview{
		ExecutionSpec: built,
		Wrapper:       wrapper,
		JobSubmission: submission.JobSubmission(built, wrapper),
	}, nil
}

// ListValidations returns the persisted validations for a version.
func (s *Service) ListValidations(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID) ([]validation.ScriptValidation, error) {
	w, err := s.Get(ctx, p, tc, workflowID)
	if err != nil {
		return nil, err
	}
	return s.d.Validations.ListByWorkflowVersion(ctx,
		tenants.ScopeFor(&tc), w.TenantID, versionID)
}

// taskContext resolves (workflow, version, decoded spec, task) with
// the workflow.read gate applied once.
func (s *Service) taskContext(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, workflowID, versionID uuid.UUID,
	taskName string) (workflows.Workflow, workflows.Version,
	workflowspec.Workflow, workflowspec.Task, error) {
	w, err := s.Get(ctx, p, tc, workflowID)
	if err != nil {
		return workflows.Workflow{}, workflows.Version{},
			workflowspec.Workflow{}, workflowspec.Task{}, err
	}
	v, err := s.d.Repo.GetVersion(ctx, tenants.ScopeFor(&tc), w.TenantID,
		workflowID, versionID)
	if err != nil {
		return workflows.Workflow{}, workflows.Version{},
			workflowspec.Workflow{}, workflowspec.Task{}, err
	}
	spec, err := workflowspec.Decode(v.Spec, "application/json")
	if err != nil {
		return workflows.Workflow{}, workflows.Version{},
			workflowspec.Workflow{}, workflowspec.Task{}, err
	}
	task, err := findTask(spec, taskName)
	if err != nil {
		return workflows.Workflow{}, workflows.Version{},
			workflowspec.Workflow{}, workflowspec.Task{}, err
	}
	return w, v, spec, task, nil
}

// interpreterFor maps payload language to a fixed interpreter.
func interpreterFor(l workflowspec.Language) (admission.Interpreter, error) {
	switch l {
	case workflowspec.LanguageBash:
		return admission.InterpreterBash, nil
	case workflowspec.LanguageSh:
		return admission.InterpreterSh, nil
	case workflowspec.LanguagePython:
		return admission.InterpreterPython, nil
	default:
		return "", apperr.New(apperr.Validation, "LANGUAGE_UNSUPPORTED",
			"script language "+string(l)+" is not supported for jobs")
	}
}
