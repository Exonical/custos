package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/executions"
	execsvc "github.com/Exonical/custos/internal/executions/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/tenants"
)

// executionHandlers serves the workflow-execution routes.
type executionHandlers struct {
	svc *execsvc.Service
}

func execDTO(e executions.Execution) map[string]any {
	return map[string]any{
		"id":                e.ID,
		"tenantId":          e.TenantID,
		"projectId":         e.ProjectID,
		"workflowId":        e.WorkflowID,
		"workflowVersionId": e.WorkflowVersionID,
		"specHash":          "sha256:" + hexOf(e.SpecHash),
		"parameters":        json.RawMessage(e.Parameters),
		"strategy":          e.Strategy,
		"state":             e.State,
		"stateReason":       e.StateReason,
		"requestedBy":       e.RequestedBy,
		"createdAt":         e.CreatedAt,
		"startedAt":         e.StartedAt,
		"endedAt":           e.EndedAt,
		"updatedAt":         e.UpdatedAt,
		"version":           e.Version,
	}
}

func taskDTO(t executions.TaskExecution) map[string]any {
	out := map[string]any{
		"id":           t.ID,
		"executionId":  t.ExecutionID,
		"taskName":     t.TaskName,
		"index":        t.Index,
		"count":        t.Count,
		"attempt":      t.Attempt,
		"state":        t.State,
		"stateReason":  t.StateReason,
		"jobId":        t.JobID,
		"validationId": t.ValidationID,
		"createdAt":    t.CreatedAt,
		"updatedAt":    t.UpdatedAt,
		"version":      t.Version,
	}
	if t.SpecDigest != nil {
		out["executionSpecDigest"] = "sha256:" + hexOf(*t.SpecDigest)
	}
	return out
}

func hexOf(d [32]byte) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 64)
	for i, c := range d {
		b[2*i], b[2*i+1] = hex[c>>4], hex[c&0xf]
	}
	return string(b)
}

type executeRequest struct {
	WorkflowID string          `json:"workflow"`
	VersionID  string          `json:"version"`
	Parameters json.RawMessage `json:"parameters"`
}

func (h *executionHandlers) execute(w http.ResponseWriter, r *http.Request) {
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
	var in executeRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httpx.WriteError(ctx, w,
			apperr.New(apperr.Invalid, "MALFORMED", "malformed request body"))
		return
	}
	wid, err := parseUUID("workflow", in.WorkflowID)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var vid *uuid.UUID
	if in.VersionID != "" {
		v, err := parseUUID("version", in.VersionID)
		if err != nil {
			httpx.WriteError(ctx, w, err)
			return
		}
		vid = &v
	}
	if len(in.Parameters) > 0 && !json.Valid(in.Parameters) {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid, "MALFORMED",
			"parameters must be a JSON object"))
		return
	}
	res, err := h.svc.Execute(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), execsvc.ExecuteInput{
			WorkflowID: wid, VersionID: vid, Parameters: in.Parameters,
		}, key, sha256.Sum256(raw))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	if res.Replayed && len(res.Body) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, execDTO(res.Execution))
}

func (h *executionHandlers) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var f executions.ExecFilter
	if q := r.URL.Query().Get("workflow"); q != "" {
		id, err := parseUUID("workflow", q)
		if err != nil {
			httpx.WriteError(ctx, w, err)
			return
		}
		f.WorkflowID = &id
	}
	if q := r.URL.Query().Get("state"); q != "" {
		f.States = append(f.States, executions.ExecutionState(q))
	}
	es, next, err := h.svc.List(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), f, pageOf(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := make([]map[string]any, 0, len(es))
	for _, e := range es {
		out = append(out, execDTO(e))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out, "next_cursor": next,
	})
}

func (h *executionHandlers) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseUUID("execution", r.PathValue("execution"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	e, err := h.svc.Get(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), id)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, execDTO(e))
}

func (h *executionHandlers) cancel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseUUID("execution", r.PathValue("execution"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	if err := h.svc.Cancel(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), id); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *executionHandlers) ids(r *http.Request) (uuid.UUID, uuid.UUID, error) {
	eid, err := parseUUID("execution", r.PathValue("execution"))
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	tid, err := parseUUID("task", r.PathValue("task"))
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return eid, tid, nil
}

func (h *executionHandlers) tasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	eid, err := parseUUID("execution", r.PathValue("execution"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	ts, err := h.svc.ListTasks(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), eid)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := make([]map[string]any, 0, len(ts))
	for _, t := range ts {
		out = append(out, taskDTO(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

func (h *executionHandlers) taskSpec(w http.ResponseWriter,
	r *http.Request) {
	ctx := r.Context()
	eid, tid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	spec, err := h.svc.TaskSpec(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), eid, tid)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, spec)
}

func (h *executionHandlers) taskValidation(w http.ResponseWriter,
	r *http.Request) {
	ctx := r.Context()
	eid, tid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	sv, err := h.svc.TaskValidation(ctx, authn.MustPrincipal(ctx),
		tenants.MustTenantContext(ctx), eid, tid)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"valid":        sv.Valid,
		"validationId": sv.ID,
		"diagnostics":  sv.Diagnostics,
		"toolVersions": sv.ToolVersions,
	})
}
