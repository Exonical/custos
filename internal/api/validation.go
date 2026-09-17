package api

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	clustersvc "github.com/Exonical/custos/internal/clusters/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/scripts"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/pipeline"
	vpolicy "github.com/Exonical/custos/internal/validation/policy"
	"github.com/Exonical/custos/internal/validation/sbatchimport"
	"github.com/Exonical/custos/internal/workflowspec"
)

// validateBodyLimit is script limit + envelope headroom
// (docs/script-validation.md §API).
const validateBodyLimit = 256*1024 + 64*1024

// scriptHandlers serves the ad-hoc validate/import endpoints and the
// validation-policy routes.
type scriptHandlers struct {
	pipe       *pipeline.Pipeline
	vpol       *vpolicy.Service
	vstore     ValidationStore
	scripts    scripts.Store
	clusterSvc *clustersvc.Service
	metrics    *pipeline.Metrics
	az         authz.Authorizer
	audit      audit.Recorder
}

// ValidationStore persists ScriptValidation rows.
type ValidationStore interface {
	Put(ctx context.Context, scope tenants.Scope,
		sv validation.ScriptValidation) error
}

func tenantResOf(tc tenants.TenantContext) authz.Resource {
	return authz.Resource{Kind: "tenant", ID: tc.Tenant.ID.String(),
		TenantID: tc.Tenant.ID.String()}
}

// scriptSnapshot projects capabilities for validators.
func scriptSnapshot(c *clusters.Cluster) *validation.ClusterSnapshot {
	if c == nil || c.Capabilities == nil {
		return nil
	}
	snap := &validation.ClusterSnapshot{
		GRESTypes:   c.Capabilities.GRESTypes,
		QoS:         c.Capabilities.QoSNames,
		MaxWalltime: map[string]time.Duration{},
	}
	for _, pt := range c.Capabilities.Partitions {
		snap.Partitions = append(snap.Partitions, pt.Name)
		if pt.MaxTime != nil {
			snap.MaxWalltime[pt.Name] = *pt.MaxTime
		}
	}
	return snap
}

type validateRequest struct {
	Language    workflowspec.Language              `json:"language"`
	Script      string                             `json:"script"`
	Resources   workflowspec.Resources             `json:"resources"`
	Environment map[string]string                  `json:"environment"`
	Software    []workflowspec.SoftwareRequirement `json:"software"`
	Cluster     string                             `json:"cluster,omitempty"`
}

func (h *scriptHandlers) validate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	if err := authz.Require(ctx, h.az, p, authz.WorkflowCreate,
		tenantResOf(tc), h.audit); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var in validateRequest
	if !decodeDTO(w, r, &in) {
		return
	}
	body := []byte(in.Script)
	if len(body) == 0 {
		httpx.WriteError(ctx, w, apperr.New(apperr.Invalid,
			"SCRIPT_REQUIRED", "script is required"))
		return
	}
	scope := tenants.ScopeFor(&tc)

	// Store the payload so the returned digest is referenceable.
	digest, err := h.scripts.Put(ctx, scope, tc.Tenant.ID, in.Language,
		body, p.UserID)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}

	var cluster *clusters.Cluster
	if in.Cluster != "" && h.clusterSvc != nil {
		c, err := h.clusterSvc.Get(ctx, p, in.Cluster)
		if err != nil {
			httpx.WriteError(ctx, w, err)
			return
		}
		cluster = &c
	}
	var cid *uuid.UUID
	if cluster != nil {
		cid = &cluster.ID
	}
	pol, fp, err := h.vpol.Effective(ctx, scope, tc.Tenant.ID, cid)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	sv := h.pipe.Run(ctx, tc.Tenant.ID, pipeline.Request{
		In: validation.Input{
			Language:    in.Language,
			Script:      body,
			Digest:      digest,
			Resources:   in.Resources,
			Environment: in.Environment,
			Software:    in.Software,
			Cluster:     scriptSnapshot(cluster),
			Policy:      pol.Effective(),
		},
		PolicyVersion: fp,
	})
	if h.metrics != nil {
		h.metrics.Observe(ctx, sv)
	}
	if err := h.vstore.Put(ctx, scope, sv); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	blocking := make([]string, 0)
	for _, d := range sv.Diagnostics {
		if d.Severity.AtLeast(pol.BlockAt) ||
			d.Severity == validation.SeverityPolicy ||
			d.Severity == validation.SeveritySecurity {
			blocking = append(blocking, d.Code+":"+d.Field)
		}
	}
	auditScript(ctx, h.audit, p, tc.Tenant.ID, "workflow.script.validate",
		map[string]any{
			"result":        sv.Valid,
			"script_digest": digest.String(),
			"blocking":      blocking,
			"policy_fp":     fp,
		})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"valid":        sv.Valid,
		"digest":       digest.String(),
		"blockAt":      pol.BlockAt,
		"diagnostics":  sv.Diagnostics,
		"toolVersions": sv.ToolVersions,
		"effectivePolicy": map[string]any{
			"blockAt":                 pol.BlockAt,
			"allowShellTasks":         pol.AllowShellTasks,
			"allowLegacySbatchImport": pol.AllowLegacySbatchImport,
			"filteredEnvAllowed":      pol.FilteredEnvAllow,
		},
	})
}

type importRequest struct {
	Language workflowspec.Language `json:"language"`
	Script   string                `json:"script"`
}

func (h *scriptHandlers) importSbatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authn.MustPrincipal(ctx)
	tc := tenants.MustTenantContext(ctx)
	if err := authz.Require(ctx, h.az, p, authz.WorkflowCreate,
		tenantResOf(tc), h.audit); err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	var in importRequest
	if !decodeDTO(w, r, &in) {
		return
	}
	scope := tenants.ScopeFor(&tc)
	pol, _, err := h.vpol.Effective(ctx, scope, tc.Tenant.ID, nil)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	if !pol.AllowLegacySbatchImport {
		httpx.WriteError(ctx, w, apperr.New(apperr.Forbidden,
			"IMPORT_DISABLED",
			"legacy #SBATCH import is disabled by validation policy"))
		return
	}
	prop, err := sbatchimport.Import([]byte(in.Script), in.Language)
	if err != nil {
		httpx.WriteError(ctx, w, apperr.New(apperr.Validation,
			"IMPORT_FAILED", err.Error()))
		return
	}
	auditScript(ctx, h.audit, p, tc.Tenant.ID, "workflow.script.import",
		map[string]any{
			"digest_before": validation.DigestOf([]byte(in.Script)).String(),
			"digest_after":  validation.DigestOf(prop.Rewritten).String(),
			"imported":      prop.Imported,
		})
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

// auditScript records a validation-domain audit event; never script bytes.
func auditScript(ctx context.Context, rec audit.Recorder, p authn.Principal,
	tenantID uuid.UUID, action string, details map[string]any) {
	if rec == nil {
		return
	}
	actor := audit.ActorUser
	if p.Kind == authn.KindService {
		actor = audit.ActorService
	}
	_ = rec.Record(ctx, audit.Event{
		Actor:    audit.Actor{Type: actor, ID: p.UserID.String()},
		Action:   action,
		Target:   audit.Target{Type: "script", ID: ""},
		Result:   audit.ResultAllow,
		TenantID: &tenantID,
		Details:  details,
	})
}

func vpolDTO(s vpolicy.Scoped) map[string]any {
	return map[string]any{"policy": s.Body, "version": s.Version}
}

func (h *scriptHandlers) getTenantPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tc := tenants.MustTenantContext(ctx)
	s, err := h.vpol.GetTenant(ctx, authn.MustPrincipal(ctx), tc)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, vpolDTO(s))
}

func (h *scriptHandlers) putTenantPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Policy  vpolicy.Policy `json:"policy"`
		Version int64          `json:"version"`
	}
	if !decodeDTO(w, r, &in) {
		return
	}
	tc := tenants.MustTenantContext(ctx)
	s, err := h.vpol.PutTenant(ctx, authn.MustPrincipal(ctx), tc,
		in.Policy, in.Version)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, vpolDTO(s))
}

func (h *scriptHandlers) getClusterPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := h.clusterSvc.Get(ctx, authn.MustPrincipal(ctx),
		r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	s, err := h.vpol.GetCluster(ctx, authn.MustPrincipal(ctx), c.ID)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, vpolDTO(s))
}

func (h *scriptHandlers) putClusterPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Policy  vpolicy.Policy `json:"policy"`
		Version int64          `json:"version"`
	}
	if !decodeDTO(w, r, &in) {
		return
	}
	c, err := h.clusterSvc.Get(ctx, authn.MustPrincipal(ctx),
		r.PathValue("cluster"))
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	s, err := h.vpol.PutCluster(ctx, authn.MustPrincipal(ctx), c.ID,
		in.Policy, in.Version)
	if err != nil {
		httpx.WriteError(ctx, w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, vpolDTO(s))
}
