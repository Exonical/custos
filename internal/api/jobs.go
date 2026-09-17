package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/jobs"
	jobssvc "github.com/Exonical/custos/internal/jobs/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/platform/log"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// jobHandlers serves the job routes.
type jobHandlers struct {
	svc  *jobssvc.Service
	exec workqueue.Execer // cancel enqueue (out-of-tx)
}

// jobSubmitDTO is the POST /jobs body. Script is inline text; script_ref
// names a previously stored script by digest.
type jobSubmitDTO struct {
	Name      string                 `json:"name,omitempty"`
	Cluster   string                 `json:"cluster"`
	Partition string                 `json:"partition,omitempty"`
	QoS       string                 `json:"qos,omitempty"`
	Resources workflowspec.Resources `json:"resources"`
	Script    *struct {
		Language workflowspec.Language `json:"language"`
		Body     string                `json:"body"`
	} `json:"script,omitempty"`
	ScriptRef  string            `json:"script_ref,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Stdout     string            `json:"stdout,omitempty"`
	Stderr     string            `json:"stderr,omitempty"`
}

func jobDTO(j jobs.Job) map[string]any {
	out := map[string]any{
		"id": j.ID, "tenant_id": j.TenantID, "project_id": j.ProjectID,
		"cluster_id": j.ClusterID, "created_by": j.CreatedBy,
		"name": j.Name, "state": j.State, "version": j.Version,
		"resource_request":  j.ResourceRequest,
		"script_digest":     j.ScriptDigest.String(),
		"script_language":   j.ScriptLanguage,
		"execution_spec_id": j.ExecutionSpecDigest.String(),
		"created_at":        j.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":        j.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if j.StateReason != "" {
		out["state_reason"] = j.StateReason
	}
	if j.SlurmJobID != nil {
		out["slurm_job_id"] = *j.SlurmJobID
	}
	if j.SlurmState != "" {
		out["slurm_state"] = j.SlurmState
	}
	if j.ExitCode != nil {
		out["exit_code"] = *j.ExitCode
	}
	if j.ExitSignal != nil {
		out["exit_signal"] = *j.ExitSignal
	}
	for k, v := range map[string]*time.Time{
		"submitted_at": j.SubmittedAt, "started_at": j.StartedAt,
		"ended_at": j.EndedAt, "last_reconciled_at": j.LastReconciledAt,
	} {
		if v != nil {
			out[k] = v.UTC().Format(time.RFC3339Nano)
		}
	}
	return out
}

// writeValidationError writes the 422 body with diagnostics
// (docs/api.md): {error:{code,message,request_id}, diagnostics:[...]}.
func writeValidationError(w http.ResponseWriter, r *http.Request,
	err error, diags []validation.Diagnostic) {
	code, msg := "VALIDATION", "validation failed"
	if ae, ok := err.(*apperr.Error); ok {
		code = ae.Code
		msg = ae.Message
	}
	httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error": map[string]any{
			"code":       code,
			"message":    msg,
			"request_id": log.RequestIDFrom(r.Context()),
		},
		"diagnostics": diags,
	})
}

func (h *jobHandlers) submit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid,
			"IDEMPOTENCY_KEY_REQUIRED",
			"Idempotency-Key header is required (1-128 chars)"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var in jobSubmitDTO
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httpx.WriteError(ctx, w,
			apperr.New(apperr.Invalid, "MALFORMED", "malformed request body"))
		return
	}
	sin := jobssvc.SubmitInput{
		Name: in.Name, Cluster: in.Cluster, Partition: in.Partition,
		QoS: in.QoS, Resources: in.Resources, Env: in.Env,
		WorkingDir: in.WorkingDir, Args: in.Args,
		Stdout: in.Stdout, Stderr: in.Stderr,
	}
	switch {
	case in.Script != nil:
		sin.Script.Language = in.Script.Language
		sin.Script.Body = []byte(in.Script.Body)
	case in.ScriptRef != "":
		d, err := validation.ParseDigest(in.ScriptRef)
		if err != nil {
			httpx.WriteError(ctx, w, apperr.New(apperr.Invalid,
				"SCRIPT_REF_INVALID", "script_ref must be sha256:<hex>"))
			return
		}
		sin.Script.Ref = &d
		sin.Script.Language = workflowspec.LanguageBash // resolved rows store their own language
	default:
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid,
			"SCRIPT_REQUIRED", "script or script_ref is required"))
		return
	}
	tc, pc := projectTC(r)
	res, diags, err := h.svc.Submit(ctx, authn.MustPrincipal(ctx),
		tc, pc, sin, key, sha256.Sum256(raw))
	if err != nil {
		if diags != nil {
			writeValidationError(w, r, err, diags)
			return
		}
		httpx.WriteError(ctx, w, err)
		return
	}
	status := http.StatusAccepted
	if res.Replayed {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, jobDTO(res.Job))
}

func (h *jobHandlers) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := jobs.Filter{}
	pc := projects.MustProjectContext(ctx)
	f.ProjectID = &pc.Project.ID
	if s := r.URL.Query().Get("state"); s != "" {
		for _, st := range strings.Split(s, ",") {
			f.States = append(f.States, jobs.State(strings.ToUpper(st)))
		}
	}
	if c := r.URL.Query().Get("cluster"); c != "" {
		if id, err := uuid.Parse(c); err == nil {
			f.ClusterID = &id
		}
	}
	items, next, err := h.svc.List(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), f, pageOf(r), false)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := make([]any, 0, len(items))
	for _, j := range items {
		out = append(out, jobDTO(j))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

// listTenant serves GET /tenants/{t}/jobs (job.read.tenant).
func (h *jobHandlers) listTenant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := jobs.Filter{}
	if s := r.URL.Query().Get("state"); s != "" {
		for _, st := range strings.Split(s, ",") {
			f.States = append(f.States, jobs.State(strings.ToUpper(st)))
		}
	}
	if c := r.URL.Query().Get("cluster"); c != "" {
		if id, err := uuid.Parse(c); err == nil {
			f.ClusterID = &id
		}
	}
	items, next, err := h.svc.List(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), f, pageOf(r), true)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := make([]any, 0, len(items))
	for _, j := range items {
		out = append(out, jobDTO(j))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

// getJob resolves {job} under the project and checks read permission;
// jobs outside the project are hidden as 404.
func (h *jobHandlers) getJob(w http.ResponseWriter, r *http.Request) (jobs.Job, bool) {
	ctx := r.Context()
	id, err := uuid.Parse(r.PathValue("job"))
	if err != nil {
		httpx.WriteError(ctx, w,
			apperr.New(apperr.Invalid, "JOB_ID_INVALID", "invalid job id"))
		return jobs.Job{}, false
	}
	j, err := h.svc.Get(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), id)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return jobs.Job{}, false
	}
	if j.ProjectID != projects.MustProjectContext(ctx).Project.ID {
		httpx.WriteError(ctx, w, apperr.New(apperr.NotFound,
			"NOT_FOUND", "job not found"))
		return jobs.Job{}, false
	}
	return j, true
}

func (h *jobHandlers) get(w http.ResponseWriter, r *http.Request) {
	j, ok := h.getJob(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, jobDTO(j))
}

func (h *jobHandlers) executionSpec(w http.ResponseWriter, r *http.Request) {
	j, ok := h.getJob(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, j.ExecutionSpec)
}

func (h *jobHandlers) cancel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	j, ok := h.getJob(w, r)
	if !ok {
		return
	}
	tenantID := tenants.MustTenantContext(ctx).Tenant.ID
	out, err := h.svc.Cancel(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), j.ID,
		func(ctx context.Context) error {
			_, err := workqueue.Enqueue(ctx, h.exec, workqueue.EnqueueRequest{
				Kind:     jobssvc.KindCancel,
				Key:      "jobcancel:" + j.ID.String(),
				TenantID: &tenantID,
				Payload:  map[string]string{"job_id": j.ID.String()},
			})
			return err
		})
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, jobDTO(out))
}
