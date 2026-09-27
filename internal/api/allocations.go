package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/allocations"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/tenants"
)

type allocationHandlers struct{ svc *allocations.Service }

func allocationDTO(a allocations.Allocation) map[string]any {
	return map[string]any{"id": a.ID, "tenant_id": a.TenantID, "project_id": a.ProjectID, "binding_id": a.BindingID, "name": a.Name, "unit": a.Unit, "limit_amount": a.LimitAmount, "period_start": a.PeriodStart.UTC().Format(time.RFC3339Nano), "period_end": a.PeriodEnd.UTC().Format(time.RFC3339Nano), "enforcement": a.Enforcement, "consumed_amount": a.ConsumedAmount, "consumed_as_of": a.ConsumedAsOf, "version": a.Version, "created_at": a.CreatedAt, "updated_at": a.UpdatedAt}
}
func (h *allocationHandlers) list(w http.ResponseWriter, r *http.Request) {
	tc, pc := projectTC(r)
	items, err := h.svc.List(r.Context(), authn.MustPrincipal(r.Context()), tc, pc)
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	out := make([]any, 0, len(items))
	for _, x := range items {
		out = append(out, allocationDTO(x))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}
func (h *allocationHandlers) create(w http.ResponseWriter, r *http.Request) {
	var in allocations.CreateInput
	if !decodeDTO(w, r, &in) {
		return
	}
	tc, pc := projectTC(r)
	a, err := h.svc.Create(r.Context(), authn.MustPrincipal(r.Context()), tc, pc, in)
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, allocationDTO(a))
}
func (h *allocationHandlers) get(w http.ResponseWriter, r *http.Request) {
	tc, pc := projectTC(r)
	id, err := uuid.Parse(r.PathValue("allocation"))
	if err != nil {
		httpx.WriteError(r.Context(), w, apperr.New(apperr.Invalid, "MALFORMED", "invalid allocation id"))
		return
	}
	a, err := h.svc.Get(r.Context(), authn.MustPrincipal(r.Context()), tc, pc, id)
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, allocationDTO(a))
}
func (h *allocationHandlers) update(w http.ResponseWriter, r *http.Request) {
	var in allocations.UpdateInput
	if !decodeDTO(w, r, &in) {
		return
	}
	tc, pc := projectTC(r)
	id, err := uuid.Parse(r.PathValue("allocation"))
	if err != nil {
		httpx.WriteError(r.Context(), w, apperr.New(apperr.Invalid, "MALFORMED", "invalid allocation id"))
		return
	}
	a, err := h.svc.Update(r.Context(), authn.MustPrincipal(r.Context()), tc, pc, id, in)
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, allocationDTO(a))
}
func (h *allocationHandlers) delete(w http.ResponseWriter, r *http.Request) {
	tc, pc := projectTC(r)
	id, err := uuid.Parse(r.PathValue("allocation"))
	if err != nil {
		httpx.WriteError(r.Context(), w, apperr.New(apperr.Invalid, "MALFORMED", "invalid allocation id"))
		return
	}
	if err = h.svc.Delete(r.Context(), authn.MustPrincipal(r.Context()), tc, pc, id); err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *allocationHandlers) tenantList(w http.ResponseWriter, r *http.Request) {
	tc := tenants.MustTenantContext(r.Context())
	items, err := h.svc.ListTenant(r.Context(), authn.MustPrincipal(r.Context()), tc)
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	out := make([]any, 0, len(items))
	now := time.Now()
	for _, a := range items {
		remaining := a.LimitAmount - a.ConsumedAmount
		percent := float64(0)
		if a.LimitAmount > 0 {
			percent = 100 * a.ConsumedAmount / a.LimitAmount
		}
		out = append(out, map[string]any{"allocation": allocationDTO(a), "consumed": a.ConsumedAmount, "remaining": remaining, "percent_used": percent, "as_of": a.ConsumedAsOf, "active": !now.Before(a.PeriodStart) && now.Before(a.PeriodEnd)})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}
func mountAllocationProjectRoutes(mux *http.ServeMux, h *allocationHandlers, pr func(http.Handler) http.Handler) {
	base := "/api/v1/tenants/{tenant}/projects/{project}/allocations"
	mux.Handle("GET "+base, pr(http.HandlerFunc(h.list)))
	mux.Handle("POST "+base, pr(http.HandlerFunc(h.create)))
	mux.Handle("GET "+base+"/{allocation}", pr(http.HandlerFunc(h.get)))
	mux.Handle("PATCH "+base+"/{allocation}", pr(http.HandlerFunc(h.update)))
	mux.Handle("DELETE "+base+"/{allocation}", pr(http.HandlerFunc(h.delete)))
}
