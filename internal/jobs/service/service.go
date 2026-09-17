// Package service is the job submission/read/cancel pipeline: the
// security-critical path joining scripts → validation → admission →
// durable job + idempotent enqueue (docs/script-validation.md
// §Pipeline, docs/workers.md).
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/jobs"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/workqueue"
	policiessvc "github.com/Exonical/custos/internal/policies/service"
	"github.com/Exonical/custos/internal/projects"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	"github.com/Exonical/custos/internal/scripts"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/envcheck"
	"github.com/Exonical/custos/internal/workflowspec"
)

// KindSubmit is the workqueue kind that runs the submit handler.
const KindSubmit = "job.submit"

// Deps wires the service.
type Deps struct {
	Jobs       jobs.Repository
	Scripts    scripts.Store
	Projects   *projectsvc.Service
	Policies   *policiessvc.Service
	Clusters   clusters.Repository
	Validators []validation.ScriptValidator
	AZ         authz.Authorizer
	Audit      audit.Recorder
}

// Service is the jobs application service.
type Service struct {
	d Deps
}

// New wires the service.
func New(d Deps) *Service { return &Service{d: d} }

// ScriptInput is either inline bytes or a reference to a stored script.
type ScriptInput struct {
	Language workflowspec.Language
	Body     []byte
	Ref      *validation.Digest
}

// SubmitInput is the POST /jobs body, decoded.
type SubmitInput struct {
	Name       string
	Cluster    string // cluster name or id
	Partition  string
	QoS        string
	Resources  workflowspec.Resources
	Script     ScriptInput
	Env        map[string]string
	WorkingDir string
	Args       []string
	Stdout     string
	Stderr     string
}

// Result is Submit's outcome.
type Result struct {
	Job      jobs.Job
	Replayed bool // idempotent replay of a stored response
}

// defaultValidationPolicy is the hardcoded effective validation policy
// until ValidationPolicy persistence lands in M5
// (docs/script-validation.md).
var defaultValidationPolicy = validation.EffectivePolicy{
	BlockAt: validation.SeverityError,
}

func (s *Service) audit(ctx context.Context, p authn.Principal,
	tenantID uuid.UUID, action, targetType, targetID, reason string,
	result string, details map[string]any) {
	actor := audit.ActorUser
	if p.Kind == authn.KindService {
		actor = audit.ActorService
	}
	_ = s.d.Audit.Record(ctx, audit.Event{
		Actor:    audit.Actor{Type: actor, ID: p.UserID.String()},
		Action:   action,
		Target:   audit.Target{Type: targetType, ID: targetID},
		Result:   result,
		Reason:   reason,
		TenantID: &tenantID,
		Details:  details,
	})
}

func jobResource(tenantID, projectID, jobID, ownerID uuid.UUID) authz.Resource {
	return authz.Resource{
		Kind:      "job",
		ID:        jobID.String(),
		TenantID:  tenantID.String(),
		ProjectID: projectID.String(),
		OwnerID:   ownerID.String(),
	}
}

// interpreterFor maps the payload language to a fixed interpreter;
// python is rejected until its sandbox story lands.
func interpreterFor(l workflowspec.Language) (admission.Interpreter, error) {
	switch l {
	case workflowspec.LanguageBash:
		return admission.InterpreterBash, nil
	case workflowspec.LanguageSh:
		return admission.InterpreterSh, nil
	default:
		return "", apperr.New(apperr.Validation, "LANGUAGE_UNSUPPORTED",
			"script language "+string(l)+" is not supported for jobs")
	}
}

// Submit runs the synchronous admission pipeline and persists the job
// + idempotency record + job.submit work item atomically.
func (s *Service) Submit(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext,
	in SubmitInput, idemKey string,
	requestHash [32]byte) (Result, []validation.Diagnostic, error) {
	tenantID, projectID := tc.Tenant.ID, pc.Project.ID
	scope := tenants.ScopeFor(&tc)

	// 1. Authorization and lifecycle gates.
	if err := authz.Require(ctx, s.d.AZ, p, authz.JobSubmit,
		authz.Resource{Kind: "project", ID: projectID.String(),
			TenantID:  tenantID.String(),
			ProjectID: projectID.String(),
			OwnerID:   p.UserID.String()}, s.d.Audit); err != nil {
		return Result{}, nil, err
	}
	if tc.Tenant.State == tenants.StateSuspended {
		return Result{}, nil, apperr.New(apperr.Conflict, "TENANT_SUSPENDED",
			"tenant is suspended")
	}
	if pc.Project.State == projects.StateArchived {
		return Result{}, nil, apperr.New(apperr.Conflict, "PROJECT_STATE",
			"project is archived")
	}
	if err := in.Resources.Validate(); err != nil {
		return Result{}, nil, apperr.New(apperr.Invalid, "RESOURCE_INVALID",
			err.Error())
	}

	// 2. Script: store inline bodies (limits checked inside Put) or
	// resolve a stored reference.
	interp, err := interpreterFor(in.Script.Language)
	if err != nil {
		return Result{}, nil, err
	}
	var digest validation.Digest
	var body []byte
	if in.Script.Ref != nil {
		digest = *in.Script.Ref
		body, err = s.d.Scripts.Get(ctx, scope, tenantID, digest)
		if err != nil {
			if apperr.Is(err, apperr.NotFound) {
				return Result{}, nil, apperr.New(apperr.Validation,
					"SCRIPT_UNKNOWN", "script digest is not stored in this tenant")
			}
			return Result{}, nil, err
		}
	} else {
		body = in.Script.Body
		if len(body) == 0 {
			return Result{}, nil, apperr.New(apperr.Invalid, "SCRIPT_REQUIRED",
				"script body or script_ref is required")
		}
		digest, err = s.d.Scripts.Put(ctx, scope, tenantID,
			in.Script.Language, body, p.UserID)
		if err != nil {
			return Result{}, nil, err
		}
	}

	// 3. Validation pipeline (in-process, synchronous).
	cluster, err := s.d.Clusters.GetByNameOrID(ctx, in.Cluster)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return Result{}, nil, apperr.New(apperr.Validation,
				"CLUSTER_UNKNOWN", "cluster is not registered")
		}
		return Result{}, nil, err
	}
	snap := clusterSnapshot(cluster)
	vin := validation.Input{
		Language:    in.Script.Language,
		Script:      body,
		Digest:      digest,
		Resources:   in.Resources,
		Environment: in.Env,
		Cluster:     snap,
		Policy:      defaultValidationPolicy,
	}
	var diags []validation.Diagnostic
	for _, v := range s.d.Validators {
		res, err := v.Validate(ctx, vin)
		if err != nil {
			return Result{}, nil, apperr.Wrap(err, apperr.Internal,
				"validation.tool_error", "validator "+v.Name()+" failed")
		}
		diags = append(diags, res.Diagnostics...)
	}
	diags = append(diags, envcheck.Validate(in.Env, envcheck.EnvPolicy{})...)
	diags, valid := validation.ApplyPolicy(diags, defaultValidationPolicy)
	if !valid {
		codes := make([]any, 0, len(diags))
		for _, d := range diags {
			codes = append(codes, d.Code+":"+d.Field)
		}
		s.audit(ctx, p, tenantID, "job.submit_rejected", "project",
			projectID.String(), "script validation failed",
			audit.ResultDeny, map[string]any{
				"script_digest": digest.String(),
				"diagnostics":   codes,
				"count":         len(diags),
			})
		return Result{}, diags, apperr.New(apperr.Validation,
			"SCRIPT_INVALID", "script failed validation")
	}

	// 4. Admission: effective policy ∩ binding ∩ capabilities.
	pol, err := s.d.Policies.Effective(ctx, scope, tenantID, projectID)
	if err != nil {
		return Result{}, nil, err
	}
	binding, err := s.d.Projects.ResolveBinding(ctx, scope, projectID, cluster.ID)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return Result{}, nil, apperr.New(apperr.Validation,
				"NO_CLUSTER_BINDING",
				"project has no enabled binding to this cluster")
		}
		return Result{}, nil, err
	}
	if snap == nil || cluster.Capabilities == nil ||
		cluster.State == clusters.StateDisabled {
		return Result{}, nil, apperr.New(apperr.Conflict, "CLUSTER_UNAVAILABLE",
			"cluster capabilities are unavailable")
	}
	jobID := uuid.Must(uuid.NewV7())
	name := in.Name
	if name == "" {
		name = "job"
	}
	spec := admission.ExecutionSpec{
		ID:          jobID,
		TenantID:    tenantID,
		ProjectID:   projectID,
		PrincipalID: p.UserID,
		TaskName:    "adhoc",
		Attempt:     1,
		Cluster: admission.ClusterRef{
			ID: cluster.ID, Name: cluster.Name, APIVersion: cluster.APIVersion,
		},
		Partition:   in.Partition,
		QoS:         in.QoS,
		Environment: admission.EnvSet{User: in.Env},
		Payload: admission.PayloadRef{
			ScriptID:    jobID,
			Digest:      digest,
			Language:    in.Script.Language,
			Interpreter: interp,
		},
		WorkingDir: in.WorkingDir,
		Stdout:     in.Stdout,
		Stderr:     in.Stderr,
		Security: admission.SecurityContext{
			SlurmUser:         cluster.ServiceUser,
			ImpersonationMode: string(cluster.IdentityMode),
		},
	}
	built, denial := admission.Build(admission.BuildInput{
		Spec: spec, Request: in.Resources, Policy: pol,
		Binding: binding, Cluster: *snap,
	})
	if denial != nil {
		s.audit(ctx, p, tenantID, "job.submit_denied", "project",
			projectID.String(), denial.Code, audit.ResultDeny,
			map[string]any{
				"field":         denial.Field,
				"script_digest": digest.String(),
			})
		return Result{}, nil, apperr.New(apperr.Validation,
			"POLICY_VIOLATION",
			fmt.Sprintf("%s: %s", denial.Field, denial.Message))
	}
	built.AdmittedAt = time.Now().UTC()

	// 5. Persist job + idempotency + enqueue in one transaction.
	now := time.Now().UTC()
	j := jobs.Job{
		ID: jobID, TenantID: tenantID, ProjectID: projectID,
		ClusterID: cluster.ID, CreatedBy: p.UserID, Name: name,
		State: jobs.StateSubmitting, ResourceRequest: in.Resources,
		ExecutionSpec: built, ExecutionSpecDigest: built.Digest,
		ScriptDigest: digest, ScriptLanguage: in.Script.Language,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	res, err := s.d.Jobs.CreateWithIdempotency(ctx, scope, j,
		jobs.IdemRecord{
			Key: idemKey, RequestHash: requestHash,
			Status: 202, ResourceID: jobID,
			ExpiresAt: now.Add(24 * time.Hour),
		}, func(ex jobs.Execer) error {
			_, err := workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{
				Kind:        KindSubmit,
				Key:         "job:" + jobID.String(),
				TenantID:    &tenantID,
				Payload:     map[string]string{"job_id": jobID.String()},
				MaxAttempts: 8,
			})
			return err
		})
	if err != nil {
		return Result{}, nil, err
	}
	if res.Replayed {
		return Result{Job: res.Job, Replayed: true}, nil, nil
	}
	s.audit(ctx, p, tenantID, "job.submitted", "job", jobID.String(), "",
		audit.ResultAllow, map[string]any{
			"spec_digest":   built.Digest.String(),
			"script_digest": digest.String(),
			"cluster":       cluster.Name,
			"account":       built.Account,
			"partition":     built.Partition,
		})
	return Result{Job: j}, nil, nil
}

// clusterSnapshot projects cluster capabilities into the validator view.
func clusterSnapshot(c clusters.Cluster) *validation.ClusterSnapshot {
	if c.Capabilities == nil {
		return nil
	}
	snap := &validation.ClusterSnapshot{
		GRESTypes:   c.Capabilities.GRESTypes,
		QoS:         c.Capabilities.QoSNames,
		MaxWalltime: map[string]time.Duration{},
	}
	for _, pt := range c.Capabilities.Partitions {
		snap.Partitions = append(snap.Partitions, pt.Name)
		if pt.MaxTime != nil {
			snap.MaxWalltime[pt.Name] = *pt.MaxTime
		}
	}
	return snap
}

// canRead reports whether p may read job j: owner via job.read.self,
// else job.read.project / job.read.tenant.
func (s *Service) canRead(ctx context.Context, p authn.Principal,
	j jobs.Job) error {
	if j.CreatedBy == p.UserID {
		return authz.Require(ctx, s.d.AZ, p, authz.JobReadSelf,
			jobResource(j.TenantID, j.ProjectID, j.ID, j.CreatedBy), s.d.Audit)
	}
	if err := authz.Require(ctx, s.d.AZ, p, authz.JobReadProject,
		jobResource(j.TenantID, j.ProjectID, j.ID, j.CreatedBy),
		nil); err == nil {
		return nil
	}
	return authz.Require(ctx, s.d.AZ, p, authz.JobReadTenant,
		jobResource(j.TenantID, j.ProjectID, j.ID, j.CreatedBy), s.d.Audit)
}

// Get returns one job (read permission as canRead).
func (s *Service) Get(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, id uuid.UUID) (jobs.Job, error) {
	j, err := s.d.Jobs.Get(ctx, tenants.ScopeFor(&tc), id)
	if err != nil {
		return jobs.Job{}, err
	}
	if err := s.canRead(ctx, p, j); err != nil {
		return jobs.Job{}, err
	}
	return j, nil
}

// List returns jobs; principals without job.read.project/tenant see
// only their own.
func (s *Service) List(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, f jobs.Filter,
	page tenants.Page, tenantWide bool) ([]jobs.Job, string, error) {
	scope := tenants.ScopeFor(&tc)
	res := authz.Resource{Kind: "job", TenantID: tc.Tenant.ID.String()}
	if f.ProjectID != nil {
		res.ProjectID = f.ProjectID.String()
	}
	if tenantWide {
		if err := authz.Require(ctx, s.d.AZ, p, authz.JobReadTenant,
			res, s.d.Audit); err != nil {
			return nil, "", err
		}
	} else if err := authz.Require(ctx, s.d.AZ, p, authz.JobReadProject,
		res, nil); err != nil {
		// Fall back to self-only listing.
		if err := authz.Require(ctx, s.d.AZ, p, authz.JobReadSelf,
			res, s.d.Audit); err != nil {
			return nil, "", err
		}
		f.CreatedBy = &p.UserID
	}
	return s.d.Jobs.List(ctx, scope, f, page)
}

// ExecutionSpec returns the persisted immutable spec (read permission
// as Get).
func (s *Service) ExecutionSpec(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, id uuid.UUID) (admission.ExecutionSpec, error) {
	j, err := s.Get(ctx, p, tc, id)
	if err != nil {
		return admission.ExecutionSpec{}, err
	}
	return j.ExecutionSpec, nil
}

// KindCancel is the workqueue kind for Slurm cancellation.
const KindCancel = "job.cancel"

// Cancel requests cancellation: SUBMITTING jobs go CANCELED directly
// (the submit handler re-reads state and skips); QUEUED/RUNNING jobs
// get a job.cancel work item; terminal jobs conflict.
func (s *Service) Cancel(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, id uuid.UUID,
	enqueue func(ctx context.Context) error) (jobs.Job, error) {
	scope := tenants.ScopeFor(&tc)
	j, err := s.d.Jobs.Get(ctx, scope, id)
	if err != nil {
		return jobs.Job{}, err
	}
	res := jobResource(j.TenantID, j.ProjectID, j.ID, j.CreatedBy)
	if j.CreatedBy == p.UserID {
		if err := authz.Require(ctx, s.d.AZ, p, authz.JobCancelSelf,
			res, s.d.Audit); err != nil {
			return jobs.Job{}, err
		}
	} else if err := authz.Require(ctx, s.d.AZ, p, authz.JobCancelAny,
		res, s.d.Audit); err != nil {
		return jobs.Job{}, err
	}
	switch j.State {
	case jobs.StateSubmitting:
		st := jobs.StateCanceled
		now := time.Now().UTC()
		out, err := s.d.Jobs.Transition(ctx, scope, j.ID, j.Version,
			jobs.Patch{State: &st, Reason: strptr("CANCELED"),
				EndedAt: &now})
		if err != nil {
			return jobs.Job{}, err
		}
		s.audit(ctx, p, j.TenantID, "job.canceled", "job", j.ID.String(),
			"canceled before submission", audit.ResultAllow, nil)
		return out, nil
	case jobs.StateQueued, jobs.StateRunning:
		if err := enqueue(ctx); err != nil {
			return jobs.Job{}, err
		}
		s.audit(ctx, p, j.TenantID, "job.cancel_requested", "job",
			j.ID.String(), "", audit.ResultAllow, nil)
		return j, nil
	default:
		return jobs.Job{}, apperr.New(apperr.Conflict, "JOB_STATE",
			"job is already "+string(j.State))
	}
}

func strptr(s string) *string { return &s }
