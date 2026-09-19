// Package api mounts the v1 HTTP routes on the ServeMux. Route
// registration lives here (not in cmd) so all commands mount the same
// surface.
package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	apiv1 "github.com/Exonical/custos/pkg/api/v1"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	clustersvc "github.com/Exonical/custos/internal/clusters/service"
	execsvc "github.com/Exonical/custos/internal/executions/service"
	jobssvc "github.com/Exonical/custos/internal/jobs/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/health"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/platform/workqueue"
	policiesvc "github.com/Exonical/custos/internal/policies/service"
	"github.com/Exonical/custos/internal/projects"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	"github.com/Exonical/custos/internal/scripts"
	"github.com/Exonical/custos/internal/tenants"
	tenantsvc "github.com/Exonical/custos/internal/tenants/service"
	"github.com/Exonical/custos/internal/users"
	"github.com/Exonical/custos/internal/validation/pipeline"
	vpolicy "github.com/Exonical/custos/internal/validation/policy"
	wfsvc "github.com/Exonical/custos/internal/workflows/service"
)

// specJSON is computed once at init from the spec embedded in the
// generated package (single source of truth: api/openapi/v1.yaml).
var specJSON []byte
var specETag string

func init() {
	doc, err := apiv1.GetSpec()
	if err != nil {
		panic("api: embedded spec does not parse: " + err.Error())
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic("api: spec JSON encode: " + err.Error())
	}
	specJSON = b
	sum := sha256.Sum256(b)
	specETag = fmt.Sprintf("\"%x\"", sum[:8])
}

// Deps are the dependencies Mount needs.
type Deps struct {
	Health         *health.Registry
	ReadyBudget    time.Duration
	Logger         *slog.Logger
	Verifier       authn.Verifier                // required for bearer routes
	Provisioner    authn.Provisioner             // optional; enriches principal
	Audit          audit.Recorder                // may be nil (skips audit records)
	Tenants        *tenantsvc.Service            // nil disables tenant routes
	TenantRepo     tenants.Repository            // required when Tenants is set
	Users          *users.Service                // enables platform role-binding routes
	Clusters       *clustersvc.Service           // enables cluster registry routes
	Projects       *projectsvc.Service           // enables project routes
	ProjectRepo    projects.Repository           // required when Projects is set
	ProjectMembers projects.MembershipRepository // required when Projects is set
	Policies       *policiesvc.Service           // enables resource-policy routes
	Jobs           *jobssvc.Service              // enables job routes
	JobExec        workqueue.Execer              // cancel enqueue (pool)
	Scripts        scripts.Store                 // enables validate/import routes
	Pipeline       *pipeline.Pipeline            // enables validate/import routes
	VPolicy        *vpolicy.Service              // enables validation-policy routes
	VStore         ValidationStore               // persists ScriptValidations
	VMetrics       *pipeline.Metrics             // may be nil
	VLimiter       *httpx.PrincipalRateLimiter   // may be nil (no limit)
	Workflows      *wfsvc.Service                // enables workflow routes
	Executions     *execsvc.Service              // enables execution routes
	AZ             authz.Authorizer              // required for validate routes
}

// Mount registers the v1 routes on mux. The generated types in
// pkg/api/v1 describe the shapes; the health routes delegate to the
// health package handlers directly since they already emit the exact
// documented JSON.
func Mount(mux *http.ServeMux, deps Deps) {
	mux.Handle("GET /api/v1/openapi.json", openapiHandler())
	mux.Handle("GET /api/v1/schemas/workflow/v1alpha1", schemaHandler())
	mux.Handle("GET /health/live", deps.Health.LiveHandler())
	mux.Handle("GET /health/ready",
		deps.Health.ReadyHandler(deps.ReadyBudget, deps.Logger))
	if deps.Verifier != nil {
		bearer := authn.RequireBearer(deps.Verifier, deps.Audit, deps.Logger,
			authn.WithProvisioner(deps.Provisioner))
		mux.Handle("GET /api/v1/me", bearer(meHandler(deps)))

		if deps.Tenants != nil {
			h := &tenantHandlers{svc: deps.Tenants}
			tenantMW := tenants.Require(deps.TenantRepo, deps.Logger, deps.Audit)
			tr := func(h http.Handler) http.Handler { return bearer(tenantMW(h)) }

			mux.Handle("GET /api/v1/tenants", bearer(http.HandlerFunc(h.list)))
			mux.Handle("POST /api/v1/tenants", bearer(http.HandlerFunc(h.create)))
			mux.Handle("GET /api/v1/tenants/{tenant}", tr(http.HandlerFunc(h.get)))
			mux.Handle("PATCH /api/v1/tenants/{tenant}", tr(http.HandlerFunc(h.update)))
			mux.Handle("GET /api/v1/tenants/{tenant}/members", tr(http.HandlerFunc(h.listMembers)))
			mux.Handle("POST /api/v1/tenants/{tenant}/members", tr(http.HandlerFunc(h.addMember)))
			mux.Handle("PATCH /api/v1/tenants/{tenant}/members/{user}", tr(http.HandlerFunc(h.updateMember)))
			mux.Handle("DELETE /api/v1/tenants/{tenant}/members/{user}", tr(http.HandlerFunc(h.removeMember)))
			mux.Handle("DELETE /api/v1/tenants/{tenant}", tr(http.HandlerFunc(h.deleteTenant)))
			mux.Handle("GET /api/v1/tenants/{tenant}/users/lookup", tr(http.HandlerFunc(h.lookupUsers)))
			mux.Handle("GET /api/v1/tenants/{tenant}/groups", tr(http.HandlerFunc(h.listGroups)))
			mux.Handle("POST /api/v1/tenants/{tenant}/groups", tr(http.HandlerFunc(h.createGroup)))
			mux.Handle("GET /api/v1/tenants/{tenant}/groups/{group}", tr(http.HandlerFunc(h.getGroup)))
			mux.Handle("PATCH /api/v1/tenants/{tenant}/groups/{group}", tr(http.HandlerFunc(h.updateGroup)))
			mux.Handle("DELETE /api/v1/tenants/{tenant}/groups/{group}", tr(http.HandlerFunc(h.deleteGroup)))
			mux.Handle("GET /api/v1/tenants/{tenant}/groups/{group}/members", tr(http.HandlerFunc(h.listGroupMembers)))
			mux.Handle("POST /api/v1/tenants/{tenant}/groups/{group}/members", tr(http.HandlerFunc(h.addGroupMember)))
			mux.Handle("DELETE /api/v1/tenants/{tenant}/groups/{group}/members/{user}", tr(http.HandlerFunc(h.removeGroupMember)))
			mux.Handle("GET /api/v1/tenants/{tenant}/claim-rules", tr(http.HandlerFunc(h.listClaimRules)))
			mux.Handle("POST /api/v1/tenants/{tenant}/claim-rules", tr(http.HandlerFunc(h.createClaimRule)))
			mux.Handle("GET /api/v1/tenants/{tenant}/claim-rules/{rule}", tr(http.HandlerFunc(h.getClaimRule)))
			mux.Handle("PATCH /api/v1/tenants/{tenant}/claim-rules/{rule}", tr(http.HandlerFunc(h.updateClaimRule)))
			mux.Handle("DELETE /api/v1/tenants/{tenant}/claim-rules/{rule}", tr(http.HandlerFunc(h.deleteClaimRule)))
		}

		if deps.Clusters != nil {
			ch := &clusterHandlers{svc: deps.Clusters}
			tenantMW := tenants.Require(deps.TenantRepo, deps.Logger, deps.Audit)
			tr := func(h http.Handler) http.Handler { return bearer(tenantMW(h)) }
			mountClusterRoutes(mux, ch, bearer, tr)
		}

		if deps.Projects != nil && deps.Policies != nil {
			ph := &projectHandlers{svc: deps.Projects, policies: deps.Policies}
			tenantMW := tenants.Require(deps.TenantRepo, deps.Logger, deps.Audit)
			tr := func(h http.Handler) http.Handler { return bearer(tenantMW(h)) }
			mountProjectRoutes(mux, ph, deps.ProjectRepo, deps.ProjectMembers,
				deps.Logger, bearer, tr)
		}

		if deps.Jobs != nil && deps.JobExec != nil {
			jh := &jobHandlers{svc: deps.Jobs, exec: deps.JobExec}
			tenantMW := tenants.Require(deps.TenantRepo, deps.Logger, deps.Audit)
			tr := func(h http.Handler) http.Handler { return bearer(tenantMW(h)) }
			mux.Handle("GET /api/v1/tenants/{tenant}/jobs",
				tr(http.HandlerFunc(jh.listTenant)))
			projectMW := projects.Require(deps.ProjectRepo, deps.ProjectMembers,
				deps.Logger)
			pr := func(h http.Handler) http.Handler { return tr(projectMW(h)) }
			base := "/api/v1/tenants/{tenant}/projects/{project}/jobs"
			mux.Handle("POST "+base, pr(http.HandlerFunc(jh.submit)))
			mux.Handle("GET "+base, pr(http.HandlerFunc(jh.list)))
			mux.Handle("GET "+base+"/{job}", pr(http.HandlerFunc(jh.get)))
			mux.Handle("POST "+base+"/{job}/cancel", pr(http.HandlerFunc(jh.cancel)))
			mux.Handle("GET "+base+"/{job}/execution-spec",
				pr(http.HandlerFunc(jh.executionSpec)))
		}

		if deps.Pipeline != nil && deps.VPolicy != nil && deps.Scripts != nil {
			sh := &scriptHandlers{
				pipe: deps.Pipeline, vpol: deps.VPolicy,
				vstore: deps.VStore, scripts: deps.Scripts,
				clusterSvc: deps.Clusters, metrics: deps.VMetrics,
				az: deps.AZ, audit: deps.Audit,
			}
			tenantMW := tenants.Require(deps.TenantRepo, deps.Logger, deps.Audit)
			tr := func(h http.Handler) http.Handler { return bearer(tenantMW(h)) }
			guard := func(h http.Handler) http.Handler {
				h = httpx.BodyLimit(validateBodyLimit)(h)
				if deps.VLimiter != nil {
					h = deps.VLimiter.Middleware(func(r *http.Request) string {
						return authn.MustPrincipal(r.Context()).UserID.String()
					})(h)
				}
				return h
			}
			base := "/api/v1/tenants/{tenant}/scripts"
			mux.Handle("POST "+base,
				tr(guard(http.HandlerFunc(sh.upload))))
			mux.Handle("POST "+base+"/validate",
				tr(guard(http.HandlerFunc(sh.validate))))
			mux.Handle("POST "+base+"/import-sbatch",
				tr(guard(http.HandlerFunc(sh.importSbatch))))
			mux.Handle("GET /api/v1/tenants/{tenant}/policies/validation",
				tr(http.HandlerFunc(sh.getTenantPolicy)))
			mux.Handle("PUT /api/v1/tenants/{tenant}/policies/validation",
				tr(http.HandlerFunc(sh.putTenantPolicy)))
			mux.Handle("GET /api/v1/clusters/{cluster}/policies/validation",
				bearer(http.HandlerFunc(sh.getClusterPolicy)))
			mux.Handle("PUT /api/v1/clusters/{cluster}/policies/validation",
				bearer(http.HandlerFunc(sh.putClusterPolicy)))
		}

		if deps.Workflows != nil {
			wh := &workflowHandlers{svc: deps.Workflows}
			tenantMW := tenants.Require(deps.TenantRepo, deps.Logger, deps.Audit)
			tr := func(h http.Handler) http.Handler { return bearer(tenantMW(h)) }
			base := "/api/v1/tenants/{tenant}/workflows"
			mux.Handle("POST "+base, tr(http.HandlerFunc(wh.create)))
			mux.Handle("GET "+base, tr(http.HandlerFunc(wh.list)))
			mux.Handle("GET "+base+"/{workflow}", tr(http.HandlerFunc(wh.get)))
			mux.Handle("PATCH "+base+"/{workflow}", tr(http.HandlerFunc(wh.patch)))
			mux.Handle("DELETE "+base+"/{workflow}", tr(http.HandlerFunc(wh.archive)))
			vb := base + "/{workflow}/versions"
			mux.Handle("POST "+vb, tr(http.HandlerFunc(wh.createVersion)))
			mux.Handle("GET "+vb, tr(http.HandlerFunc(wh.listVersions)))
			mux.Handle("POST "+vb+"/validate", tr(http.HandlerFunc(wh.validateVersion)))
			mux.Handle("GET "+vb+"/{version}", tr(http.HandlerFunc(wh.getVersion)))
			mux.Handle("PUT "+vb+"/{version}", tr(http.HandlerFunc(wh.updateDraft)))
			mux.Handle("PUT "+vb+"/{version}/layout", tr(http.HandlerFunc(wh.updateLayout)))
			mux.Handle("POST "+vb+"/{version}/publish", tr(http.HandlerFunc(wh.publish)))
			mux.Handle("POST "+vb+"/{version}/deprecate", tr(http.HandlerFunc(wh.deprecate)))
			mux.Handle("GET "+vb+"/{version}/validations", tr(http.HandlerFunc(wh.listValidations)))
			tb := vb + "/{version}/tasks/{task}"
			mux.Handle("POST "+tb+"/validate", tr(http.HandlerFunc(wh.taskValidate)))
			mux.Handle("POST "+tb+"/import-sbatch", tr(http.HandlerFunc(wh.taskImportSbatch)))
			mux.Handle("POST "+tb+"/preview-submission", tr(http.HandlerFunc(wh.preview)))
		}

		if deps.Executions != nil {
			eh := &executionHandlers{svc: deps.Executions}
			tenantMW := tenants.Require(deps.TenantRepo, deps.Logger, deps.Audit)
			tr := func(h http.Handler) http.Handler { return bearer(tenantMW(h)) }
			base := "/api/v1/tenants/{tenant}/workflow-executions"
			mux.Handle("POST "+base, tr(http.HandlerFunc(eh.execute)))
			mux.Handle("GET "+base, tr(http.HandlerFunc(eh.list)))
			mux.Handle("GET "+base+"/{execution}", tr(http.HandlerFunc(eh.get)))
			mux.Handle("POST "+base+"/{execution}/cancel",
				tr(http.HandlerFunc(eh.cancel)))
			mux.Handle("GET "+base+"/{execution}/tasks",
				tr(http.HandlerFunc(eh.tasks)))
			tb := base + "/{execution}/tasks/{task}"
			mux.Handle("GET "+tb+"/execution-spec",
				tr(http.HandlerFunc(eh.taskSpec)))
			mux.Handle("GET "+tb+"/validation",
				tr(http.HandlerFunc(eh.taskValidation)))
		}

		if deps.Users != nil {
			ph := &platformHandlers{svc: deps.Users}
			mux.Handle("GET /api/v1/platform/role-bindings", bearer(http.HandlerFunc(ph.listBindings)))
			mux.Handle("PUT /api/v1/platform/role-bindings/{user}/{role}", bearer(http.HandlerFunc(ph.putBinding)))
			mux.Handle("DELETE /api/v1/platform/role-bindings/{user}/{role}", bearer(http.HandlerFunc(ph.deleteBinding)))
		}
	}

	// ServeMux's built-in 404/405 responses are plain text; give unknown
	// paths the error envelope instead. (ServeMux method-mismatch 405s are
	// still plain text — documented limitation.)
	mux.Handle("/", notFound())
}

func openapiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if match := r.Header.Get("If-None-Match"); match == specETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("ETag", specETag)
		h.Set("Cache-Control", "no-store")
		_, _ = w.Write(specJSON)
	})
}

func decodeDTO(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpx.WriteError(r.Context(), w,
			apperr.New(apperr.Invalid, "MALFORMED", "malformed request body"))
		return false
	}
	return true
}

func pageOf(r *http.Request) tenants.Page {
	p := tenants.Page{Cursor: r.URL.Query().Get("cursor")}
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			p.Limit = n
		}
	}
	return p
}

type memberRef struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Slug     string    `json:"slug"`
	Name     string    `json:"name"`
	Roles    []string  `json:"roles"`
}

func meHandler(deps Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		p := authn.MustPrincipal(ctx)
		memberships := []memberRef{}
		if deps.TenantRepo != nil && p.UserID != uuid.Nil {
			ms, err := deps.TenantRepo.ListMembershipsForUser(ctx, p.UserID)
			if err != nil {
				httpx.WriteError(ctx, w, err)
				return
			}
			for _, m := range ms {
				memberships = append(memberships, memberRef{
					TenantID: m.TenantID,
					Slug:     m.TenantSlug,
					Name:     m.TenantName,
					Roles:    m.Roles,
				})
			}
		}
		roles := p.PlatformRoles
		if roles == nil {
			roles = []string{}
		}
		principal := map[string]any{
			"issuer":  p.Issuer,
			"subject": p.Subject,
			"kind":    p.Kind,
			"email":   p.Email,
			"name":    p.Name,
			"scopes":  p.Scopes,
			"groups":  p.Groups,
		}
		projectMemberships := []map[string]any{}
		if deps.ProjectMembers != nil && p.UserID != uuid.Nil {
			ms, err := deps.ProjectMembers.ListAllForUser(ctx, p.UserID)
			if err != nil {
				httpx.WriteError(ctx, w, err)
				return
			}
			for _, m := range ms {
				projectMemberships = append(projectMemberships, map[string]any{
					"project_id": m.ProjectID,
					"slug":       m.ProjectSlug,
					"tenant_id":  m.TenantID,
					"roles":      m.Roles,
				})
			}
		}
		body := map[string]any{
			"principal":           principal,
			"user_id":             p.UserID,
			"platform_roles":      roles,
			"memberships":         memberships,
			"project_memberships": projectMemberships,
		}
		httpx.WriteJSON(w, http.StatusOK, body)
	})
}

type tenantHandlers struct {
	svc *tenantsvc.Service
}

func tenantDTO(t tenants.Tenant) map[string]any {
	return map[string]any{
		"id":                t.ID,
		"slug":              t.Slug,
		"name":              t.Name,
		"state":             t.State,
		"settings":          t.Settings,
		"openbao_namespace": t.OpenBaoNamespace,
		"version":           t.Version,
		"created_at":        t.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":        t.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func memberDTO(m tenants.Membership) map[string]any {
	return map[string]any{
		"tenant_id":  m.TenantID,
		"user_id":    m.UserID,
		"roles":      m.Roles,
		"source":     m.Source,
		"created_at": m.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": m.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (h *tenantHandlers) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	ts, next, err := h.svc.List(ctx, p, pageOf(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(ts))
	for _, t := range ts {
		items = append(items, tenantDTO(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "next_cursor": next,
	})
}

func (h *tenantHandlers) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	var in tenantsvc.CreateTenant
	if !decodeDTO(w, r, &in) {
		return
	}
	t, err := h.svc.Create(ctx, p, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, tenantDTO(t))
}

func (h *tenantHandlers) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	t, err := h.svc.Get(ctx, p, tc)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tenantDTO(t))
}

func (h *tenantHandlers) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	var in tenantsvc.UpdateTenant
	if !decodeDTO(w, r, &in) {
		return
	}
	t, err := h.svc.Update(ctx, p, tc, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tenantDTO(t))
}

func (h *tenantHandlers) listMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	ms, next, err := h.svc.ListMembers(ctx, p, tc, pageOf(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(ms))
	for _, m := range ms {
		items = append(items, memberDTO(m))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "next_cursor": next,
	})
}

func (h *tenantHandlers) addMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	var in tenantsvc.AddMember
	if !decodeDTO(w, r, &in) {
		return
	}
	m, err := h.svc.AddMember(ctx, p, tc, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, memberDTO(m))
}

func (h *tenantHandlers) updateMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	uid, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		httpx.WriteError(ctx, w,
			apperr.New(apperr.Invalid, "MALFORMED", "invalid user id"))
		return
	}
	var in tenantsvc.UpdateMember
	if !decodeDTO(w, r, &in) {
		return
	}
	m, err := h.svc.UpdateMemberRoles(ctx, p, tc, uid, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, memberDTO(m))
}

func (h *tenantHandlers) removeMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	uid, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		httpx.WriteError(ctx, w,
			apperr.New(apperr.Invalid, "MALFORMED", "invalid user id"))
		return
	}
	if err := h.svc.RemoveMember(ctx, p, tc, uid); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- tenant deletion + user lookup ---

func (h *tenantHandlers) deleteTenant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	if err := h.svc.Delete(ctx, p, tc); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *tenantHandlers) lookupUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	us, err := h.svc.LookupUsers(ctx, p, tc, r.URL.Query().Get("email"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(us))
	for _, u := range us {
		items = append(items, map[string]any{
			"id": u.ID, "email": u.Email, "name": u.DisplayName, "kind": u.Kind,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- groups ---

func groupDTO(g tenants.Group) map[string]any {
	return map[string]any{
		"id": g.ID, "tenant_id": g.TenantID, "name": g.Name,
		"description": g.Description, "source": g.Source,
		"version":    g.Version,
		"created_at": g.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": g.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func groupMemberDTO(m tenants.GroupMembership) map[string]any {
	return map[string]any{
		"group_id": m.GroupID, "user_id": m.UserID, "source": m.Source,
		"created_at": m.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (h *tenantHandlers) listGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	gs, next, err := h.svc.ListGroups(ctx, p, tc, pageOf(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(gs))
	for _, g := range gs {
		items = append(items, groupDTO(g))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (h *tenantHandlers) createGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	var in tenantsvc.CreateGroup
	if !decodeDTO(w, r, &in) {
		return
	}
	g, err := h.svc.CreateGroup(ctx, p, tc, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, groupDTO(g))
}

func (h *tenantHandlers) getGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	g, err := h.svc.GetGroup(ctx, p, tc, r.PathValue("group"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, groupDTO(g))
}

func (h *tenantHandlers) updateGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	var in tenantsvc.UpdateGroup
	if !decodeDTO(w, r, &in) {
		return
	}
	g, err := h.svc.UpdateGroup(ctx, p, tc, r.PathValue("group"), in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, groupDTO(g))
}

func (h *tenantHandlers) deleteGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	if err := h.svc.DeleteGroup(ctx, p, tc, r.PathValue("group")); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *tenantHandlers) listGroupMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	ms, next, err := h.svc.ListGroupMembers(ctx, p, tc, r.PathValue("group"), pageOf(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(ms))
	for _, m := range ms {
		items = append(items, groupMemberDTO(m))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

type addGroupMemberDTO struct {
	UserID uuid.UUID `json:"user_id"`
}

func (h *tenantHandlers) addGroupMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	var in addGroupMemberDTO
	if !decodeDTO(w, r, &in) {
		return
	}
	if err := h.svc.AddGroupMember(ctx, p, tc, r.PathValue("group"), in.UserID); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *tenantHandlers) removeGroupMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	uid, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid, "MALFORMED", "invalid user id"))
		return
	}
	if err := h.svc.RemoveGroupMember(ctx, p, tc, r.PathValue("group"), uid); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- claim rules ---

func claimRuleDTO(c tenants.ClaimRule) map[string]any {
	return map[string]any{
		"id": c.ID, "tenant_id": c.TenantID, "claim": c.Claim,
		"match_value": c.MatchValue, "roles": c.Roles, "group_id": c.GroupID,
		"enabled": c.Enabled, "version": c.Version,
		"created_at": c.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": c.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (h *tenantHandlers) listClaimRules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	rs, next, err := h.svc.ListClaimRules(ctx, p, tc, pageOf(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(rs))
	for _, c := range rs {
		items = append(items, claimRuleDTO(c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (h *tenantHandlers) createClaimRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	var in tenantsvc.CreateClaimRule
	if !decodeDTO(w, r, &in) {
		return
	}
	c, err := h.svc.CreateClaimRule(ctx, p, tc, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, claimRuleDTO(c))
}

func (h *tenantHandlers) getClaimRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	rid, err := uuid.Parse(r.PathValue("rule"))
	if err != nil {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid, "MALFORMED", "invalid rule id"))
		return
	}
	c, err := h.svc.GetClaimRule(ctx, p, tc, rid)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, claimRuleDTO(c))
}

func (h *tenantHandlers) updateClaimRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	rid, err := uuid.Parse(r.PathValue("rule"))
	if err != nil {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid, "MALFORMED", "invalid rule id"))
		return
	}
	var in tenantsvc.UpdateClaimRule
	if !decodeDTO(w, r, &in) {
		return
	}
	c, err := h.svc.UpdateClaimRule(ctx, p, tc, rid, in)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, claimRuleDTO(c))
}

func (h *tenantHandlers) deleteClaimRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	rid, err := uuid.Parse(r.PathValue("rule"))
	if err != nil {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid, "MALFORMED", "invalid rule id"))
		return
	}
	if err := h.svc.DeleteClaimRule(ctx, p, tc, rid); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- platform role bindings ---

type platformHandlers struct {
	svc *users.Service
}

func bindingDTO(b users.PlatformRoleBinding) map[string]any {
	return map[string]any{
		"user_id": b.UserID, "issuer": b.Issuer, "subject": b.Subject,
		"email": b.Email, "role": b.Role,
		"created_at": b.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (h *platformHandlers) listBindings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	bs, err := h.svc.ListRoleBindings(ctx, p)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(bs))
	for _, b := range bs {
		items = append(items, bindingDTO(b))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *platformHandlers) putBinding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	uid, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid, "MALFORMED", "invalid user id"))
		return
	}
	if err := h.svc.GrantRole(ctx, p, uid, r.PathValue("role")); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *platformHandlers) deleteBinding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	uid, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid, "MALFORMED", "invalid user id"))
		return
	}
	if err := h.svc.RevokeRole(ctx, p, uid, r.PathValue("role")); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func notFound() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(r.Context(), w,
			apperr.New(apperr.NotFound, "NOT_FOUND", "not found"))
	})
}

// SpecJSON exposes the JSON-encoded OpenAPI document (tests/tooling).
func SpecJSON() []byte { return specJSON }
