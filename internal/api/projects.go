package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	policiesvc "github.com/Exonical/custos/internal/policies/service"
	"github.com/Exonical/custos/internal/projects"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	"github.com/Exonical/custos/internal/tenants"
)

// projectHandlers serves the project routes.
type projectHandlers struct {
	svc      *projectsvc.Service
	policies *policiesvc.Service
}

func projectDTO(p projects.Project) map[string]any {
	return map[string]any{
		"id": p.ID, "tenant_id": p.TenantID, "slug": p.Slug,
		"name": p.Name, "description": p.Description,
		"state": p.State, "settings": p.Settings, "version": p.Version,
		"created_at": p.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": p.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func pmemberDTO(m projects.Membership) map[string]any {
	return map[string]any{
		"project_id": m.ProjectID, "user_id": m.UserID,
		"roles": m.Roles, "source": m.Source,
		"created_at": m.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": m.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func clusterBindingDTO(b projects.ClusterBinding) map[string]any {
	return map[string]any{
		"id": b.ID, "project_id": b.ProjectID, "cluster_id": b.ClusterID,
		"slurm_account":      b.SlurmAccount,
		"default_partition":  b.DefaultPartition,
		"allowed_partitions": b.AllowedPartitions,
		"default_qos":        b.DefaultQoS,
		"allowed_qos":        b.AllowedQoS,
		"enabled":            b.Enabled, "version": b.Version,
		"created_at": b.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": b.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func projectTC(r *http.Request) (tenants.TenantContext, projects.ProjectContext) {
	return tenants.MustTenantContext(r.Context()), projects.MustProjectContext(r.Context())
}

func (h *projectHandlers) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tc := tenants.MustTenantContext(ctx)
	items, next, err := h.svc.List(ctx, authn.MustPrincipal(ctx), tc, pageOf(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := make([]any, 0, len(items))
	for _, p := range items {
		out = append(out, projectDTO(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

func (h *projectHandlers) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in projectsvc.CreateProject
	if !decodeDTO(w, r, &in) {
		return
	}
	tc := tenants.MustTenantContext(ctx)
	p, err := h.svc.Create(ctx, authn.MustPrincipal(ctx), tc, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, projectDTO(p))
}

func (h *projectHandlers) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tc, pc := projectTC(r)
	p, err := h.svc.Get(ctx, authn.MustPrincipal(ctx), tc, pc)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, projectDTO(p))
}

func (h *projectHandlers) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in projectsvc.UpdateProject
	if !decodeDTO(w, r, &in) {
		return
	}
	tc, pc := projectTC(r)
	p, err := h.svc.Update(ctx, authn.MustPrincipal(ctx), tc, pc, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, projectDTO(p))
}

func (h *projectHandlers) archive(w http.ResponseWriter, r *http.Request) {
	h.setState(w, r, true)
}

func (h *projectHandlers) unarchive(w http.ResponseWriter, r *http.Request) {
	h.setState(w, r, false)
}

func (h *projectHandlers) setState(w http.ResponseWriter, r *http.Request, archive bool) {
	ctx := r.Context()
	tc, pc := projectTC(r)
	p := authn.MustPrincipal(ctx)
	var proj projects.Project
	var err error
	if archive {
		proj, err = h.svc.Archive(ctx, p, tc, pc)
	} else {
		proj, err = h.svc.Unarchive(ctx, p, tc, pc)
	}
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, projectDTO(proj))
}

func userParam(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		return uuid.Nil, apperr.New(apperr.Validation, "USER_REF_INVALID", "user must be a uuid")
	}
	return id, nil
}

func (h *projectHandlers) listMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tc, pc := projectTC(r)
	items, next, err := h.svc.ListMembers(ctx, authn.MustPrincipal(ctx), tc, pc, pageOf(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := make([]any, 0, len(items))
	for _, m := range items {
		out = append(out, pmemberDTO(m))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

func (h *projectHandlers) addMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in projectsvc.UpsertMember
	if !decodeDTO(w, r, &in) {
		return
	}
	tc, pc := projectTC(r)
	m, err := h.svc.AddMember(ctx, authn.MustPrincipal(ctx), tc, pc, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, pmemberDTO(m))
}

func (h *projectHandlers) updateMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid, err := userParam(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var in struct {
		Roles []string `json:"roles"`
	}
	if !decodeDTO(w, r, &in) {
		return
	}
	tc, pc := projectTC(r)
	m, err := h.svc.UpdateRoles(ctx, authn.MustPrincipal(ctx), tc, pc, uid, in.Roles)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pmemberDTO(m))
}

func (h *projectHandlers) removeMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid, err := userParam(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	tc, pc := projectTC(r)
	if err := h.svc.RemoveMember(ctx, authn.MustPrincipal(ctx), tc, pc, uid); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func bindingParam(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("binding"))
	if err != nil {
		return uuid.Nil, apperr.New(apperr.Validation, "BINDING_REF_INVALID", "binding must be a uuid")
	}
	return id, nil
}

func (h *projectHandlers) listBindings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tc, pc := projectTC(r)
	items, err := h.svc.ListBindings(ctx, authn.MustPrincipal(ctx), tc, pc)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := make([]any, 0, len(items))
	for _, b := range items {
		out = append(out, clusterBindingDTO(b))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *projectHandlers) createBinding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in projectsvc.UpsertBinding
	if !decodeDTO(w, r, &in) {
		return
	}
	tc, pc := projectTC(r)
	b, err := h.svc.CreateBinding(ctx, authn.MustPrincipal(ctx), tc, pc, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, clusterBindingDTO(b))
}

func (h *projectHandlers) getBinding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bid, err := bindingParam(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	tc, pc := projectTC(r)
	b, err := h.svc.GetBinding(ctx, authn.MustPrincipal(ctx), tc, pc, bid)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, clusterBindingDTO(b))
}

func (h *projectHandlers) updateBinding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bid, err := bindingParam(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var in projectsvc.UpsertBinding
	if !decodeDTO(w, r, &in) {
		return
	}
	tc, pc := projectTC(r)
	b, err := h.svc.UpdateBinding(ctx, authn.MustPrincipal(ctx), tc, pc, bid, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, clusterBindingDTO(b))
}

func (h *projectHandlers) deleteBinding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bid, err := bindingParam(r)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	tc, pc := projectTC(r)
	if err := h.svc.DeleteBinding(ctx, authn.MustPrincipal(ctx), tc, pc, bid); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// policyDTO wraps a policy body + version.
func policyDTO(body any, version int) map[string]any {
	return map[string]any{"policy": body, "version": version}
}

func (h *projectHandlers) getTenantPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tc := tenants.MustTenantContext(ctx)
	pol, ver, err := h.policies.GetTenantPolicy(ctx, authn.MustPrincipal(ctx), tc)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, policyDTO(pol, ver))
}

func (h *projectHandlers) setTenantPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Policy admission.ResourcePolicy `json:"policy"`
	}
	if !decodeDTO(w, r, &in) {
		return
	}
	tc := tenants.MustTenantContext(ctx)
	ver, err := h.policies.SetTenantPolicy(ctx, authn.MustPrincipal(ctx), tc, in.Policy)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, policyDTO(in.Policy, ver))
}

func (h *projectHandlers) getProjectPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tc, pc := projectTC(r)
	pol, ver, err := h.policies.GetProjectPolicy(ctx, authn.MustPrincipal(ctx), tc, pc.Project.ID)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	eff, err := h.policies.Effective(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, pc.Project.ID)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := policyDTO(pol, ver)
	out["effective"] = eff
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *projectHandlers) setProjectPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Policy admission.ResourcePolicy `json:"policy"`
	}
	if !decodeDTO(w, r, &in) {
		return
	}
	tc, pc := projectTC(r)
	ver, err := h.policies.SetProjectPolicy(ctx, authn.MustPrincipal(ctx), tc, pc.Project.ID, in.Policy)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, policyDTO(in.Policy, ver))
}

// mountProjectRoutes registers project routes. pr wraps with
// bearer→tenant→project middleware.
func mountProjectRoutes(mux *http.ServeMux, h *projectHandlers,
	repo projects.Repository, members projects.MembershipRepository,
	logger *slog.Logger, _ func(http.Handler) http.Handler, tr func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/tenants/{tenant}/projects", tr(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/v1/tenants/{tenant}/projects", tr(http.HandlerFunc(h.create)))
	mux.Handle("GET /api/v1/tenants/{tenant}/policies/resource", tr(http.HandlerFunc(h.getTenantPolicy)))
	mux.Handle("PUT /api/v1/tenants/{tenant}/policies/resource", tr(http.HandlerFunc(h.setTenantPolicy)))

	projectMW := projects.Require(repo, members, logger)
	pr := func(h http.Handler) http.Handler { return tr(projectMW(h)) }
	mux.Handle("GET /api/v1/tenants/{tenant}/projects/{project}", pr(http.HandlerFunc(h.get)))
	mux.Handle("PATCH /api/v1/tenants/{tenant}/projects/{project}", pr(http.HandlerFunc(h.update)))
	mux.Handle("POST /api/v1/tenants/{tenant}/projects/{project}/archive", pr(http.HandlerFunc(h.archive)))
	mux.Handle("POST /api/v1/tenants/{tenant}/projects/{project}/unarchive", pr(http.HandlerFunc(h.unarchive)))
	mux.Handle("GET /api/v1/tenants/{tenant}/projects/{project}/members", pr(http.HandlerFunc(h.listMembers)))
	mux.Handle("POST /api/v1/tenants/{tenant}/projects/{project}/members", pr(http.HandlerFunc(h.addMember)))
	mux.Handle("PATCH /api/v1/tenants/{tenant}/projects/{project}/members/{user}", pr(http.HandlerFunc(h.updateMember)))
	mux.Handle("DELETE /api/v1/tenants/{tenant}/projects/{project}/members/{user}", pr(http.HandlerFunc(h.removeMember)))
	mux.Handle("GET /api/v1/tenants/{tenant}/projects/{project}/cluster-bindings", pr(http.HandlerFunc(h.listBindings)))
	mux.Handle("POST /api/v1/tenants/{tenant}/projects/{project}/cluster-bindings", pr(http.HandlerFunc(h.createBinding)))
	mux.Handle("GET /api/v1/tenants/{tenant}/projects/{project}/cluster-bindings/{binding}", pr(http.HandlerFunc(h.getBinding)))
	mux.Handle("PATCH /api/v1/tenants/{tenant}/projects/{project}/cluster-bindings/{binding}", pr(http.HandlerFunc(h.updateBinding)))
	mux.Handle("DELETE /api/v1/tenants/{tenant}/projects/{project}/cluster-bindings/{binding}", pr(http.HandlerFunc(h.deleteBinding)))
	mux.Handle("GET /api/v1/tenants/{tenant}/projects/{project}/policies/resource", pr(http.HandlerFunc(h.getProjectPolicy)))
	mux.Handle("PUT /api/v1/tenants/{tenant}/projects/{project}/policies/resource", pr(http.HandlerFunc(h.setProjectPolicy)))
}
