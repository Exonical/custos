package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/workflows"
	wfsvc "github.com/Exonical/custos/internal/workflows/service"
	"github.com/Exonical/custos/internal/workflowspec"
	wfschema "github.com/Exonical/custos/internal/workflowspec/schema"
)

// workflowBodyLimit bounds workflow documents (docs/workflows.md §API).
const workflowBodyLimit = 4 << 20

// workflowHandlers serves the workflow authoring/version/publish
// routes.
type workflowHandlers struct {
	svc *wfsvc.Service
}

func wfDTO(w workflows.Workflow) map[string]any {
	return map[string]any{
		"id":                       w.ID,
		"tenantId":                 w.TenantID,
		"projectId":                w.ProjectID,
		"name":                     w.Name,
		"description":              w.Description,
		"state":                    w.State,
		"latestPublishedVersionId": w.LatestPublishedVersion,
		"version":                  w.Version,
		"createdAt":                w.CreatedAt,
		"updatedAt":                w.UpdatedAt,
	}
}

func versionDTO(v workflows.Version) map[string]any {
	return map[string]any{
		"id":            v.ID,
		"workflowId":    v.WorkflowID,
		"number":        v.Number,
		"state":         v.State,
		"schemaVersion": v.SchemaVersion,
		"specHash":      fmt.Sprintf("sha256:%x", v.SpecHash),
		"layout":        json.RawMessage(v.Layout),
		"version":       v.Version,
		"createdAt":     v.CreatedAt,
		"publishedAt":   v.PublishedAt,
	}
}

func parseUUID(path, s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, apperr.New(apperr.Invalid, "MALFORMED",
			path+" must be a uuid")
	}
	return id, nil
}

// readSpecBody reads up to 4MiB of a YAML/JSON workflow document.
func readSpecBody(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	ct := r.Header.Get("Content-Type")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, workflowBodyLimit))
	if err != nil {
		httpx.WriteError(r.Context(), w, apperr.New(apperr.Invalid,
			"BODY_TOO_LARGE", "workflow document exceeds 4 MiB"))
		return nil, "", false
	}
	return body, ct, true
}

type createWorkflowRequest struct {
	ProjectID   string `json:"project"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *workflowHandlers) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	var in createWorkflowRequest
	if !decodeDTO(w, r, &in) {
		return
	}
	pid, err := parseUUID("project", in.ProjectID)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	wf, err := h.svc.Create(ctx, p, tc, wfsvc.CreateInput{
		ProjectID: pid, Name: in.Name, Description: in.Description,
	})
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, wfDTO(wf))
}

func (h *workflowHandlers) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	var project *uuid.UUID
	if q := r.URL.Query().Get("project"); q != "" {
		id, err := parseUUID("project", q)
		if err != nil {
			httpx.WriteError(ctx, w, err)
			return
		}
		project = &id
	}
	ws, err := h.svc.List(ctx, p, tc, project)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := make([]map[string]any, 0, len(ws))
	for _, x := range ws {
		out = append(out, wfDTO(x))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"workflows": out})
}

func (h *workflowHandlers) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	id, err := parseUUID("workflow", r.PathValue("workflow"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	wf, err := h.svc.Get(ctx, p, tc, id)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wfDTO(wf))
}

type patchWorkflowRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Version     int64   `json:"version"`
}

func (h *workflowHandlers) patch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	id, err := parseUUID("workflow", r.PathValue("workflow"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var in patchWorkflowRequest
	if !decodeDTO(w, r, &in) {
		return
	}
	wf, err := h.svc.Patch(ctx, p, tc, id, wfsvc.PatchInput{
		Name: in.Name, Description: in.Description, Version: in.Version,
	})
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wfDTO(wf))
}

func (h *workflowHandlers) archive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	id, err := parseUUID("workflow", r.PathValue("workflow"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var in patchWorkflowRequest
	_ = decodeDTO(w, r, &in)
	if err := h.svc.Archive(ctx, p, tc, id, in.Version); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- versions ----------------------------------------------------------------

func (h *workflowHandlers) ids(r *http.Request) (uuid.UUID, uuid.UUID, error) {
	wid, err := parseUUID("workflow", r.PathValue("workflow"))
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	vid, err := parseUUID("version", r.PathValue("version"))
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return wid, vid, nil
}

func (h *workflowHandlers) createVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, err := parseUUID("workflow", r.PathValue("workflow"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	body, ct, ok := readSpecBody(w, r)
	if !ok {
		return
	}
	v, err := h.svc.CreateVersion(ctx, p, tc, wid, body, ct)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, versionDTO(v))
}

func (h *workflowHandlers) listVersions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, err := parseUUID("workflow", r.PathValue("workflow"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	vs, err := h.svc.ListVersions(ctx, p, tc, wid)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := make([]map[string]any, 0, len(vs))
	for _, x := range vs {
		out = append(out, versionDTO(x))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"versions": out})
}

func (h *workflowHandlers) getVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, vid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	v, err := h.svc.GetVersion(ctx, p, tc, wid, vid)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	if r.Header.Get("Accept") == "application/yaml" {
		var m map[string]any
		if err := json.Unmarshal(v.Spec, &m); err != nil {
			httpx.WriteError(ctx, w, err)
			return
		}
		y, err := yaml.Marshal(m)
		if err != nil {
			httpx.WriteError(ctx, w, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(y)
		return
	}
	out := versionDTO(v)
	out["spec"] = json.RawMessage(v.Spec)
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *workflowHandlers) updateDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, vid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	body, ct, ok := readSpecBody(w, r)
	if !ok {
		return
	}
	v, err := h.svc.UpdateDraft(ctx, p, tc, wid, vid, body, ct,
		expectVersion(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, versionDTO(v))
}

// expectVersion reads the optimistic-lock version from the
// X-Expected-Version header (0 = unchecked).
func expectVersion(r *http.Request) int64 {
	var n int64
	_, _ = fmt.Sscan(r.Header.Get("X-Expected-Version"), &n)
	return n
}

func (h *workflowHandlers) updateLayout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, vid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var layout json.RawMessage
	if !decodeDTO(w, r, &layout) {
		return
	}
	if !json.Valid(layout) {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid, "MALFORMED",
			"layout must be a JSON object"))
		return
	}
	if err := h.svc.UpdateLayout(ctx, p, tc, wid, vid, layout,
		expectVersion(r)); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *workflowHandlers) validateVersion(w http.ResponseWriter,
	r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, err := parseUUID("workflow", r.PathValue("workflow"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	body, ct, ok := readSpecBody(w, r)
	if !ok {
		return
	}
	errs := h.svc.ValidateBody(ctx, p, tc, wid, body, ct)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"valid":  len(errs) == 0,
		"errors": errs,
	})
}

func (h *workflowHandlers) publish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, vid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	v, err := h.svc.Publish(ctx, p, tc, wid, vid, expectVersion(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, versionDTO(v))
}

func (h *workflowHandlers) deprecate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, vid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	if err := h.svc.Deprecate(ctx, p, tc, wid, vid,
		expectVersion(r)); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *workflowHandlers) taskValidate(w http.ResponseWriter,
	r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, vid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	sv, err := h.svc.TaskValidate(ctx, p, tc, wid, vid,
		r.PathValue("task"))
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

func (h *workflowHandlers) taskImportSbatch(w http.ResponseWriter,
	r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, vid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var in importRequest
	if !decodeDTO(w, r, &in) {
		return
	}
	prop, err := h.svc.TaskImportSbatch(ctx, p, tc, wid, vid,
		r.PathValue("task"), in.Language, []byte(in.Script))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"resources":   prop.Resources,
		"partition":   prop.Partition,
		"qos":         prop.QoS,
		"name":        prop.Name,
		"stdout":      prop.Stdout,
		"stderr":      prop.Stderr,
		"working_dir": prop.WorkingDir,
		"rewritten":   string(prop.Rewritten),
		"imported":    prop.Imported,
		"diagnostics": prop.Diagnostics,
	})
}

func (h *workflowHandlers) preview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, vid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	prev, err := h.svc.PreviewSubmission(ctx, p, tc, wid, vid,
		r.PathValue("task"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, prev)
}

func (h *workflowHandlers) listValidations(w http.ResponseWriter,
	r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	wid, vid, err := h.ids(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	svs, err := h.svc.ListValidations(ctx, p, tc, wid, vid)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"validations": svs})
}

// --- scripts upload + schema ---------------------------------------------------

type uploadScriptRequest struct {
	Language workflowspec.Language `json:"language"`
	Script   string                `json:"script"`
}

func (h *scriptHandlers) upload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	if err := authz.Require(ctx, h.az, p, authz.WorkflowCreate,
		tenantResOf(tc), h.audit); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var in uploadScriptRequest
	if !decodeDTO(w, r, &in) {
		return
	}
	if len(in.Script) == 0 {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid,
			"SCRIPT_REQUIRED", "script is required"))
		return
	}
	digest, err := h.scripts.Put(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID,
		in.Language, []byte(in.Script), p.UserID)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"digest": digest.String(),
		"size":   len(in.Script),
	})
}

// schemaHandler serves the workflow JSON Schema unauthenticated with
// cache headers (no tenant data).
func schemaHandler() http.Handler {
	sum := sha256.Sum256(wfschema.V1Alpha1)
	etag := fmt.Sprintf("\"%x\"", sum[:8])
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/schema+json")
		h.Set("ETag", etag)
		h.Set("Cache-Control", "public, max-age=3600")
		_, _ = io.Copy(w, bytes.NewReader(wfschema.V1Alpha1))
	})
}
