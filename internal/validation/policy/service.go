package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/tenants"
)

// Store is the validation_policies persistence port.
type Store interface {
	// Get returns the stored policy for a scope or NotFound when unset.
	Get(ctx context.Context, scope tenants.Scope, kind string,
		id uuid.UUID) (Scoped, error)
	// Put is optimistic: expectVersion 0 creates, else must equal the
	// current version (Conflict on mismatch).
	Put(ctx context.Context, scope tenants.Scope, s Scoped,
		expectVersion int64) (Scoped, error)
}

// ClusterLister is the seam used to find the clusters assigned to a
// tenant for the stricter-than-cluster write rule.
type ClusterLister interface {
	ListVisibleForTenant(ctx context.Context, scope tenants.Scope,
		tenantID uuid.UUID) ([]clusters.Cluster, map[uuid.UUID]clusters.Assignment, error)
}

// Deps wires the service.
type Deps struct {
	Store    Store
	Clusters ClusterLister
	AZ       authz.Authorizer
	Audit    audit.Recorder
}

// Service is the validation-policy application service.
type Service struct {
	d Deps
}

// NewService wires the service.
func NewService(d Deps) *Service { return &Service{d: d} }

func actorOf(p authn.Principal) audit.Actor {
	t := audit.ActorUser
	if p.Kind == authn.KindService {
		t = audit.ActorService
	}
	return audit.Actor{Type: t, ID: p.UserID.String()}
}

// auditUpdate records a policy write: scope + version + body digest,
// never the body itself.
func (s *Service) auditUpdate(ctx context.Context, p authn.Principal,
	tenantID *uuid.UUID, s2 Scoped) {
	if s.d.Audit == nil {
		return
	}
	b, _ := json.Marshal(s2.Body)
	sum := sha256.Sum256(b)
	_ = s.d.Audit.Record(ctx, audit.Event{
		Actor:    actorOf(p),
		Action:   "validation.policy.updated",
		Target:   audit.Target{Type: "validation_policy", ID: s2.ScopeKind + "/" + s2.ScopeID.String()},
		Result:   audit.ResultAllow,
		TenantID: tenantID,
		Details: map[string]any{
			"scope_kind":  s2.ScopeKind,
			"version":     s2.Version,
			"body_sha256": hex.EncodeToString(sum[:]),
		},
	})
}

func tenantRes(tc tenants.TenantContext) authz.Resource {
	return authz.Resource{Kind: "policy", TenantID: tc.Tenant.ID.String()}
}

// Effective returns the resolved policy (cluster ∩ tenant; unset scopes
// contribute Default()) and its fingerprint — what the pipeline stores
// as ScriptValidation.PolicyVersion.
func (s *Service) Effective(ctx context.Context, scope tenants.Scope,
	tenantID uuid.UUID, clusterID *uuid.UUID) (Policy, int64, error) {
	tenantPol, err := s.get(ctx, scope, ScopeTenant, tenantID)
	if err != nil {
		return Policy{}, 0, err
	}
	pol := tenantPol
	if clusterID != nil {
		clusterPol, err := s.get(ctx, scope, ScopeCluster, *clusterID)
		if err != nil {
			return Policy{}, 0, err
		}
		pol = Merge(clusterPol, tenantPol)
	}
	return pol, Fingerprint(pol), nil
}

// GetCluster returns a cluster-scope policy for the API.
func (s *Service) GetCluster(ctx context.Context, p authn.Principal,
	clusterID uuid.UUID) (Scoped, error) {
	if err := authz.Require(ctx, s.d.AZ, p, authz.PolicyRead,
		authz.Resource{Kind: "platform"}, s.d.Audit); err != nil {
		return Scoped{}, err
	}
	return s.getScoped(ctx, tenants.PlatformScope(), ScopeCluster, clusterID)
}

// GetTenant returns the tenant-scope policy (policy.read).
func (s *Service) GetTenant(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext) (Scoped, error) {
	if err := authz.Require(ctx, s.d.AZ, p, authz.PolicyRead,
		tenantRes(tc), s.d.Audit); err != nil {
		return Scoped{}, err
	}
	return s.getScoped(ctx, tenants.ScopeFor(&tc), ScopeTenant, tc.Tenant.ID)
}

// PutCluster writes a cluster-scope policy (platform only).
func (s *Service) PutCluster(ctx context.Context, p authn.Principal,
	clusterID uuid.UUID, body Policy, expectVersion int64) (Scoped, error) {
	if err := authz.Require(ctx, s.d.AZ, p, authz.PolicyManage,
		authz.Resource{Kind: "platform"}, s.d.Audit); err != nil {
		return Scoped{}, err
	}
	body = body.Normalize()
	if err := body.Validate(); err != nil {
		return Scoped{}, apperr.New(apperr.Validation, "POLICY_INVALID",
			err.Error())
	}
	out, err := s.d.Store.Put(ctx, tenants.PlatformScope(), Scoped{
		ScopeKind: ScopeCluster, ScopeID: clusterID, Body: body,
		UpdatedBy: p.UserID, UpdatedAt: time.Now().UTC(),
	}, expectVersion)
	if err != nil {
		return Scoped{}, err
	}
	s.auditUpdate(ctx, p, nil, out)
	return out, nil
}

// PutTenant writes a tenant-scope policy (policy.manage). Tenant
// admins can only make policy stricter than every cluster assigned to
// the tenant — enforced at write time per docs/script-validation.md.
func (s *Service) PutTenant(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, body Policy, expectVersion int64) (Scoped, error) {
	if err := authz.Require(ctx, s.d.AZ, p, authz.PolicyManage,
		tenantRes(tc), s.d.Audit); err != nil {
		return Scoped{}, err
	}
	body = body.Normalize()
	if err := body.Validate(); err != nil {
		return Scoped{}, apperr.New(apperr.Validation, "POLICY_INVALID",
			err.Error())
	}
	scope := tenants.ScopeFor(&tc)
	if s.d.Clusters != nil {
		cls, _, err := s.d.Clusters.ListVisibleForTenant(ctx, scope,
			tc.Tenant.ID)
		if err != nil {
			return Scoped{}, err
		}
		for _, c := range cls {
			cp, err := s.get(ctx, scope, ScopeCluster, c.ID)
			if err != nil {
				return Scoped{}, err
			}
			if dim := notStricter(cp, body); dim != "" {
				return Scoped{}, ErrNotStricter(c.Name, dim)
			}
		}
	}
	out, err := s.d.Store.Put(ctx, scope, Scoped{
		ScopeKind: ScopeTenant, ScopeID: tc.Tenant.ID, Body: body,
		UpdatedBy: p.UserID, UpdatedAt: time.Now().UTC(),
	}, expectVersion)
	if err != nil {
		return Scoped{}, err
	}
	tid := tc.Tenant.ID
	s.auditUpdate(ctx, p, &tid, out)
	return out, nil
}

// getScoped returns the stored row; unset scopes yield the Default
// body with version 0 (mirroring the resource-policy endpoints).
func (s *Service) getScoped(ctx context.Context, scope tenants.Scope,
	kind string, id uuid.UUID) (Scoped, error) {
	sv, err := s.d.Store.Get(ctx, scope, kind, id)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return Scoped{ScopeKind: kind, ScopeID: id, Body: Default()}, nil
		}
		return Scoped{}, fmt.Errorf("validation policy %s/%s: %w", kind, id, err)
	}
	return sv, nil
}

func (s *Service) get(ctx context.Context, scope tenants.Scope,
	kind string, id uuid.UUID) (Policy, error) {
	sv, err := s.d.Store.Get(ctx, scope, kind, id)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return Default(), nil
		}
		return Policy{}, fmt.Errorf("validation policy %s/%s: %w", kind, id, err)
	}
	return sv.Body, nil
}
