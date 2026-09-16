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
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/health"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/tenants"
	tenantsvc "github.com/Exonical/custos/internal/tenants/service"
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
	Health      *health.Registry
	ReadyBudget time.Duration
	Logger      *slog.Logger
	Verifier    authn.Verifier     // required for bearer routes
	Provisioner authn.Provisioner  // optional; enriches principal
	Audit       audit.Recorder     // may be nil (skips audit records)
	Tenants     *tenantsvc.Service // nil disables tenant routes
	TenantRepo  tenants.Repository // required when Tenants is set
}

// Mount registers the v1 routes on mux. The generated types in
// pkg/api/v1 describe the shapes; the health routes delegate to the
// health package handlers directly since they already emit the exact
// documented JSON.
func Mount(mux *http.ServeMux, deps Deps) {
	mux.Handle("GET /api/v1/openapi.json", openapiHandler())
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
		body := map[string]any{
			"principal":      principal,
			"user_id":        p.UserID,
			"platform_roles": roles,
			"memberships":    memberships,
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

func notFound() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(r.Context(), w,
			apperr.New(apperr.NotFound, "NOT_FOUND", "not found"))
	})
}

// SpecJSON exposes the JSON-encoded OpenAPI document (tests/tooling).
func SpecJSON() []byte { return specJSON }
