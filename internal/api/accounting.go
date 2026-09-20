package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/accounting"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	clustersvc "github.com/Exonical/custos/internal/clusters/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/tenants"
)

type accountingHandlers struct {
	svc      *accounting.Service
	clusters *clustersvc.Service
	exec     workqueue.Execer
	az       authz.Authorizer
}

func parseRange(r *http.Request) (time.Time, time.Time, error) {
	from, err := accounting.ParseTime(r.URL.Query().Get("from"))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := accounting.ParseTime(r.URL.Query().Get("to"))
	return from, to, err
}
func uuidQuery(r *http.Request, name string) (*uuid.UUID, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, apperr.New(apperr.Invalid, "MALFORMED", "invalid "+name)
	}
	return &id, nil
}
func (h *accountingHandlers) usage(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseRange(r)
	if err != nil {
		httpx.WriteError(r.Context(), w, apperr.New(apperr.Invalid, "ACCOUNTING_RANGE_INVALID", err.Error()))
		return
	}
	project, err := uuidQuery(r, "project")
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	cluster, err := uuidQuery(r, "cluster")
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := h.svc.Usage(r.Context(), authn.MustPrincipal(r.Context()), tenants.MustTenantContext(r.Context()), accounting.UsageRequest{From: from, To: to, GroupBy: r.URL.Query().Get("group_by"), ProjectID: project, ClusterID: cluster, Limit: limit, Cursor: r.URL.Query().Get("cursor")})
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	items := make([]any, 0, len(res.Items))
	for _, x := range res.Items {
		items = append(items, map[string]any{"key": x.Key, "jobs": x.Jobs, "failed": x.Failed, "cpu_hours": x.CPUHours, "gpu_hours": x.GPUHours, "node_hours": x.NodeHours, "mem_gb_hours": x.MemGBHours, "wait_hours": x.WaitHours, "run_hours": x.RunHours, "energy_joules": x.EnergyJoules, "wait_p50": x.WaitP50, "wait_p90": x.WaitP90, "wait_p99": x.WaitP99, "run_p50": x.RunP50, "run_p90": x.RunP90, "run_p99": x.RunP99})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": res.NextCursor})
}
func (h *accountingHandlers) top(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseRange(r)
	if err != nil {
		httpx.WriteError(r.Context(), w, apperr.New(apperr.Invalid, "ACCOUNTING_RANGE_INVALID", err.Error()))
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.svc.Top(r.Context(), authn.MustPrincipal(r.Context()), tenants.MustTenantContext(r.Context()), accounting.TopRequest{From: from, To: to, Metric: r.URL.Query().Get("metric"), By: r.URL.Query().Get("by"), Limit: limit})
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": rows})
}
func (h *accountingHandlers) cluster(w http.ResponseWriter, r *http.Request) {
	p := authn.MustPrincipal(r.Context())
	c, err := h.clusters.Get(r.Context(), p, r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	wm, n, err := h.svc.ClusterStatus(r.Context(), p, c.ID)
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"cluster_id": c.ID, "watermark": wm.Watermark, "last_collected_at": wm.LastCollectedAt, "last_error": wm.LastError, "unattributed": n})
}
func (h *accountingHandlers) trigger(w http.ResponseWriter, r *http.Request, aggregate bool) {
	p := authn.MustPrincipal(r.Context())
	c, err := h.clusters.Get(r.Context(), p, r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	if err = authz.Require(r.Context(), h.az, p, authz.ClusterManage, authz.Resource{Kind: "cluster", ID: c.ID.String()}, nil); err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	req := workqueue.EnqueueRequest{Kind: accounting.KindCollect, Key: "cluster:" + c.ID.String(), Payload: map[string]string{"cluster_id": c.ID.String()}}
	if aggregate {
		req = workqueue.EnqueueRequest{Kind: accounting.KindAggregate, Key: "singleton"}
	}
	if _, err = workqueue.Enqueue(r.Context(), h.exec, req); err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
func mountAccountingRoutes(mux *http.ServeMux, h *accountingHandlers, bearer func(http.Handler) http.Handler, tr func(http.Handler) http.Handler) {
	base := "/api/v1/tenants/{tenant}/accounting"
	mux.Handle("GET "+base+"/usage", tr(http.HandlerFunc(h.usage)))
	mux.Handle("GET "+base+"/top", tr(http.HandlerFunc(h.top)))
	cb := "/api/v1/clusters/{cluster}/accounting"
	mux.Handle("GET "+cb, bearer(http.HandlerFunc(h.cluster)))
	mux.Handle("POST "+cb+"/collect", bearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.trigger(w, r, false) })))
	mux.Handle("POST "+cb+"/aggregate", bearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.trigger(w, r, true) })))
}
