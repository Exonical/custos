package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/nodehooks"
	nodehookssvc "github.com/Exonical/custos/internal/nodehooks/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
)

// nodeHookHandlers serves the platform-admin node-config routes and the
// token-authenticated node pull route (ADR-032).
type nodeHookHandlers struct {
	svc *nodehookssvc.Service
}

type nodeConfigUpdateDTO struct {
	Config  nodehooks.Config `json:"config"`
	Version int              `json:"version"`
}

type nodeTokenCreateDTO struct {
	Name string `json:"name"`
}

func nodeConfigDTO(v nodehookssvc.View) map[string]any {
	out := map[string]any{
		"cluster_id":     v.ClusterID,
		"config":         v.Config,
		"revision":       v.Revision,
		"content_sha256": v.ContentSHA256,
		"version":        v.Version,
		"warnings":       v.Warnings,
	}
	if v.UpdatedAt != nil {
		out["updated_at"] = v.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if v.UpdatedBy != nil {
		out["updated_by"] = *v.UpdatedBy
	}
	return out
}

func nodeTokenDTO(t nodehooks.Token) map[string]any {
	out := map[string]any{
		"id": t.ID, "cluster_id": t.ClusterID, "name": t.Name,
		"created_at": t.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if t.CreatedBy != nil {
		out["created_by"] = *t.CreatedBy
	}
	if t.LastUsedAt != nil {
		out["last_used_at"] = t.LastUsedAt.UTC().Format(time.RFC3339Nano)
	}
	if t.RevokedAt != nil {
		out["revoked_at"] = t.RevokedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func (h *nodeHookHandlers) getConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, err := h.svc.GetConfig(ctx, authn.MustPrincipal(ctx), r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, nodeConfigDTO(v))
}

func (h *nodeHookHandlers) putConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in nodeConfigUpdateDTO
	if !decodeDTO(w, r, &in) {
		return
	}
	v, err := h.svc.PutConfig(ctx, authn.MustPrincipal(ctx), r.PathValue("cluster"),
		in.Config, in.Version)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, nodeConfigDTO(v))
}

func writeBundle(w http.ResponseWriter, b nodehookssvc.Bundle) {
	h := w.Header()
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", `attachment; filename="`+b.Filename+`"`)
	h.Set("ETag", `"`+b.ETag+`"`)
	h.Set("X-Custos-Revision", strconv.FormatInt(b.Revision, 10))
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(b.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.Data)
}

func (h *nodeHookHandlers) bundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b, err := h.svc.Bundle(ctx, authn.MustPrincipal(ctx), r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	writeBundle(w, b)
}

func (h *nodeHookHandlers) createToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in nodeTokenCreateDTO
	if !decodeDTO(w, r, &in) {
		return
	}
	nt, err := h.svc.CreateToken(ctx, authn.MustPrincipal(ctx), r.PathValue("cluster"), in.Name)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	out := nodeTokenDTO(nt.Token)
	out["token"] = nt.Secret
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (h *nodeHookHandlers) listTokens(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ts, err := h.svc.ListTokens(ctx, authn.MustPrincipal(ctx), r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(ts))
	for _, t := range ts {
		items = append(items, nodeTokenDTO(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *nodeHookHandlers) revokeToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseUUID("token", r.PathValue("token"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	if err := h.svc.RevokeToken(ctx, authn.MustPrincipal(ctx), r.PathValue("cluster"), id); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *nodeHookHandlers) nodeStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := h.svc.NodeStatus(ctx, authn.MustPrincipal(ctx), r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	items := make([]any, 0, len(res.Items))
	for _, s := range res.Items {
		items = append(items, map[string]any{
			"node_name": s.NodeName, "revision": s.Revision, "bundle_sha256": s.BundleSHA256,
			"fetched_at": s.FetchedAt.UTC().Format(time.RFC3339Nano), "stale": s.Stale,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"current_revision": res.CurrentRevision, "current_bundle_sha256": res.CurrentBundleSHA256,
		"items": items})
}

// nodeBearer extracts a bearer secret without distinguishing missing from
// malformed credentials: both yield "" and take the normal lookup path.
func nodeBearer(r *http.Request) string {
	scheme, rest, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	tok := strings.TrimSpace(rest)
	if strings.ContainsAny(tok, " \t") {
		return ""
	}
	return tok
}

// pull serves GET /api/v1/node/bundle with a node token (no OIDC). The
// token is never logged.
func (h *nodeHookHandlers) pull(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := h.svc.Pull(ctx, nodeBearer(r), r.Header.Get("X-Custos-Node"),
		r.Header.Get("If-None-Match"))
	if err != nil {
		if apperr.Is(err, apperr.Unauthenticated) {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		}
		httpx.WriteError(ctx, w, err)
		return
	}
	if res.NotModified {
		h := w.Header()
		h.Set("ETag", `"`+res.Bundle.ETag+`"`)
		h.Set("X-Custos-Revision", strconv.FormatInt(res.Bundle.Revision, 10))
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeBundle(w, res.Bundle)
}

// mountNodeHookAdminRoutes registers the platform-admin routes behind the
// OIDC bearer middleware.
func mountNodeHookAdminRoutes(mux *http.ServeMux, h *nodeHookHandlers,
	bearer func(http.Handler) http.Handler) {
	base := "/api/v1/clusters/{cluster}"
	mux.Handle("GET "+base+"/node-config", bearer(http.HandlerFunc(h.getConfig)))
	mux.Handle("PUT "+base+"/node-config", bearer(http.HandlerFunc(h.putConfig)))
	mux.Handle("GET "+base+"/node-config/bundle", bearer(http.HandlerFunc(h.bundle)))
	mux.Handle("POST "+base+"/node-tokens", bearer(http.HandlerFunc(h.createToken)))
	mux.Handle("GET "+base+"/node-tokens", bearer(http.HandlerFunc(h.listTokens)))
	mux.Handle("DELETE "+base+"/node-tokens/{token}", bearer(http.HandlerFunc(h.revokeToken)))
	mux.Handle("GET "+base+"/node-status", bearer(http.HandlerFunc(h.nodeStatus)))
}

// mountNodePullRoute registers GET /api/v1/node/bundle, the only node-hook
// route outside the OIDC middleware: it authenticates with a node token
// and has its own per-client-address rate limit.
func mountNodePullRoute(mux *http.ServeMux, h *nodeHookHandlers, limiter *httpx.PrincipalRateLimiter) {
	var pull http.Handler = http.HandlerFunc(h.pull)
	if limiter != nil {
		pull = limiter.Middleware(func(r *http.Request) string {
			return "node:" + httpx.ClientIPFrom(r.Context())
		})(pull)
	}
	mux.Handle("GET /api/v1/node/bundle", pull)
}
