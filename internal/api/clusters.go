package api

import (
	"net/http"
	"time"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/clusters"
	clustersvc "github.com/Exonical/custos/internal/clusters/service"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/tenants"
)

// clusterHandlers serves the platform cluster-registry routes plus the
// tenant-facing cluster views.
type clusterHandlers struct {
	svc *clustersvc.Service
}

// clusterDTO is the platform view: token_ref is a reference
// (provider/path/key), never secret material.
func clusterDTO(c clusters.Cluster) map[string]any {
	tok := map[string]any{
		"provider":  c.TokenRef.Provider,
		"namespace": c.TokenRef.Namespace,
		"mount":     c.TokenRef.Mount,
		"path":      c.TokenRef.Path,
		"key":       c.TokenRef.Key,
		"version":   c.TokenRef.Version,
	}
	out := map[string]any{
		"id":                   c.ID,
		"name":                 c.Name,
		"display_name":         c.DisplayName,
		"base_url":             c.BaseURL,
		"api_version":          c.APIVersion,
		"identity_mode":        c.IdentityMode,
		"service_user":         c.ServiceUser,
		"token_ref":            tok,
		"visibility":           c.Visibility,
		"state":                c.State,
		"consecutive_failures": c.ConsecFailures,
		"last_error":           c.LastError,
		"version":              c.Version,
		"created_at":           c.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":           c.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if c.CABundlePEM != "" {
		out["ca_bundle_pem"] = c.CABundlePEM
	}
	if c.ClientCertRef != nil {
		out["client_cert_ref"] = map[string]any{
			"provider":  c.ClientCertRef.Provider,
			"namespace": c.ClientCertRef.Namespace,
			"mount":     c.ClientCertRef.Mount,
			"path":      c.ClientCertRef.Path,
			"key":       c.ClientCertRef.Key,
			"version":   c.ClientCertRef.Version,
		}
	}
	if c.LastSyncAt != nil {
		out["last_sync_at"] = c.LastSyncAt.UTC().Format(time.RFC3339Nano)
	}
	if c.CapabilitiesAt != nil {
		out["capabilities_at"] = c.CapabilitiesAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// summaryDTO is the tenant-facing view — never base_url, token_ref,
// ca_bundle_pem, or other platform configuration.
func summaryDTO(s clustersvc.ClusterSummary) map[string]any {
	parts := s.Partitions
	if parts == nil {
		parts = []string{}
	}
	return map[string]any{
		"id":            s.ID,
		"name":          s.Name,
		"display_name":  s.DisplayName,
		"state":         s.State,
		"slurm_version": s.SlurmVersion,
		"partitions":    parts,
		"gres_types":    s.GRESTypes,
		"node_summary":  s.NodeSummary,
		"defaults":      s.Defaults,
	}
}

type secretRefDTO struct {
	Provider  string `json:"provider"`
	Namespace string `json:"namespace,omitempty"`
	Mount     string `json:"mount,omitempty"`
	Path      string `json:"path"`
	Key       string `json:"key,omitempty"`
	Version   int    `json:"version,omitempty"`
}

func (r secretRefDTO) ref() secrets.Reference {
	return secrets.Reference{Provider: r.Provider, Namespace: r.Namespace,
		Mount: r.Mount, Path: r.Path, Key: r.Key, Version: r.Version}
}

type createClusterDTO struct {
	Name          string        `json:"name"`
	DisplayName   string        `json:"display_name"`
	BaseURL       string        `json:"base_url"`
	APIVersion    string        `json:"api_version"`
	CABundlePEM   string        `json:"ca_bundle_pem,omitempty"`
	IdentityMode  string        `json:"identity_mode"`
	ServiceUser   string        `json:"service_user"`
	TokenRef      secretRefDTO  `json:"token_ref"`
	ClientCertRef *secretRefDTO `json:"client_cert_ref,omitempty"`
	Visibility    string        `json:"visibility"`
}

type updateClusterDTO struct {
	DisplayName   *string        `json:"display_name,omitempty"`
	BaseURL       *string        `json:"base_url,omitempty"`
	APIVersion    *string        `json:"api_version,omitempty"`
	CABundlePEM   *string        `json:"ca_bundle_pem,omitempty"`
	IdentityMode  *string        `json:"identity_mode,omitempty"`
	ServiceUser   *string        `json:"service_user,omitempty"`
	TokenRef      *secretRefDTO  `json:"token_ref,omitempty"`
	ClientCertRef **secretRefDTO `json:"client_cert_ref,omitempty"`
	Visibility    *string        `json:"visibility,omitempty"`
	Version       int            `json:"version"`
}

func (h *clusterHandlers) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cs, next, err := h.svc.List(ctx, authn.MustPrincipal(ctx), pageOf(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(cs))
	for _, c := range cs {
		items = append(items, clusterDTO(c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "next_cursor": next,
	})
}

func (h *clusterHandlers) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in createClusterDTO
	if !decodeDTO(w, r, &in) {
		return
	}
	var cert *secrets.Reference
	if in.ClientCertRef != nil {
		cr := in.ClientCertRef.ref()
		cert = &cr
	}
	c, err := h.svc.Create(ctx, authn.MustPrincipal(ctx),
		clustersvc.CreateInput{
			Name: in.Name, DisplayName: in.DisplayName,
			BaseURL: in.BaseURL, APIVersion: in.APIVersion,
			CABundlePEM: in.CABundlePEM, IdentityMode: in.IdentityMode,
			ServiceUser: in.ServiceUser, TokenRef: in.TokenRef.ref(),
			ClientCertRef: cert, Visibility: in.Visibility,
		})
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, clusterDTO(c))
}

func (h *clusterHandlers) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := h.svc.Get(ctx, authn.MustPrincipal(ctx),
		r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, clusterDTO(c))
}

func (h *clusterHandlers) update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in updateClusterDTO
	if !decodeDTO(w, r, &in) {
		return
	}
	up := clustersvc.UpdateInput{
		DisplayName: in.DisplayName, BaseURL: in.BaseURL,
		APIVersion: in.APIVersion, CABundlePEM: in.CABundlePEM,
		IdentityMode: in.IdentityMode, ServiceUser: in.ServiceUser,
		Visibility: in.Visibility,
		Version:    in.Version,
	}
	if in.TokenRef != nil {
		tr := in.TokenRef.ref()
		up.TokenRef = &tr
	}
	if in.ClientCertRef != nil {
		var cr *secrets.Reference
		if *in.ClientCertRef != nil {
			r := (*in.ClientCertRef).ref()
			cr = &r
		}
		up.ClientCertRef = &cr
	}
	c, err := h.svc.Update(ctx, authn.MustPrincipal(ctx),
		r.PathValue("cluster"), up)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, clusterDTO(c))
}

// action dispatches POST /api/v1/clusters/{cluster}:<verb>. ServeMux
// cannot express a wildcard + literal suffix in one segment, so this
// subtree handler parses the tail.
func (h *clusterHandlers) disable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := h.svc.Disable(ctx, authn.MustPrincipal(ctx), r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, clusterDTO(c))
}

func (h *clusterHandlers) testConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := h.svc.TestConnection(ctx, authn.MustPrincipal(ctx), r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (h *clusterHandlers) listAssignments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	as, next, err := h.svc.ListAssignments(ctx, authn.MustPrincipal(ctx),
		r.PathValue("cluster"), pageOf(r))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(as))
	for _, a := range as {
		items = append(items, assignmentDTO(a))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "next_cursor": next,
	})
}

func assignmentDTO(a clusters.Assignment) map[string]any {
	return map[string]any{
		"cluster_id": a.ClusterID, "tenant_id": a.TenantID,
		"source": a.Source, "defaults": a.Defaults,
		"created_at": a.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": a.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

type assignDTO struct {
	Defaults clusters.AssignmentDefaults `json:"defaults"`
}

func (h *clusterHandlers) putAssignment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in assignDTO
	if !decodeDTO(w, r, &in) {
		return
	}
	err := h.svc.Assign(ctx, authn.MustPrincipal(ctx),
		r.PathValue("cluster"), r.PathValue("tenant"), in.Defaults)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *clusterHandlers) deleteAssignment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	err := h.svc.Unassign(ctx, authn.MustPrincipal(ctx),
		r.PathValue("cluster"), r.PathValue("tenant"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- tenant-facing routes ---

func (h *clusterHandlers) listVisible(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tc := tenants.MustTenantContext(ctx)
	list, err := h.svc.ListVisible(ctx, authn.MustPrincipal(ctx), tc)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(list))
	for _, s := range list {
		items = append(items, summaryDTO(s))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *clusterHandlers) getVisible(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tc := tenants.MustTenantContext(ctx)
	s, err := h.svc.GetVisible(ctx, authn.MustPrincipal(ctx), tc,
		r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, summaryDTO(s))
}

func (h *clusterHandlers) listPartitions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tc := tenants.MustTenantContext(ctx)
	recs, err := h.svc.ListPartitions(ctx, authn.MustPrincipal(ctx), tc,
		r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(recs))
	for _, p := range recs {
		items = append(items, map[string]any{
			"name": p.Name, "attributes": p.Attributes,
			"synced_at": p.SyncedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// mountClusterRoutes registers platform + tenant cluster routes. bearer
// and tr are the authn/tenant-context wrappers from Mount.
func mountClusterRoutes(mux *http.ServeMux, ch *clusterHandlers,
	bearer, tr func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/clusters", bearer(http.HandlerFunc(ch.list)))
	mux.Handle("POST /api/v1/clusters", bearer(http.HandlerFunc(ch.create)))
	mux.Handle("GET /api/v1/clusters/{cluster}", bearer(http.HandlerFunc(ch.get)))
	mux.Handle("PATCH /api/v1/clusters/{cluster}", bearer(http.HandlerFunc(ch.update)))
	mux.Handle("POST /api/v1/clusters/{cluster}/disable",
		bearer(http.HandlerFunc(ch.disable)))
	mux.Handle("POST /api/v1/clusters/{cluster}/test-connection",
		bearer(http.HandlerFunc(ch.testConnection)))
	mux.Handle("GET /api/v1/clusters/{cluster}/tenants",
		bearer(http.HandlerFunc(ch.listAssignments)))
	mux.Handle("PUT /api/v1/clusters/{cluster}/tenants/{tenant}",
		bearer(http.HandlerFunc(ch.putAssignment)))
	mux.Handle("DELETE /api/v1/clusters/{cluster}/tenants/{tenant}",
		bearer(http.HandlerFunc(ch.deleteAssignment)))

	mux.Handle("GET /api/v1/tenants/{tenant}/clusters",
		tr(http.HandlerFunc(ch.listVisible)))
	mux.Handle("GET /api/v1/tenants/{tenant}/clusters/{cluster}",
		tr(http.HandlerFunc(ch.getVisible)))
	mux.Handle("GET /api/v1/tenants/{tenant}/clusters/{cluster}/partitions",
		tr(http.HandlerFunc(ch.listPartitions)))
}
