// Package service implements the resource-policy application service.
// Tenant policies are managed with policy.manage at tenant scope; a
// project policy requires policy.manage at the tenant level too —
// project admins cannot loosen policy, only the intersection makes the
// effective value stricter.
package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/policies"
	"github.com/Exonical/custos/internal/tenants"
)

// Service is the resource-policy application service.
type Service struct {
	repo policies.Repository
	az   authz.Authorizer
	rec  audit.Recorder
}

// NewService wires the policy service.
func NewService(repo policies.Repository, az authz.Authorizer, rec audit.Recorder) *Service {
	return &Service{repo: repo, az: az, rec: rec}
}

func actorOf(p authn.Principal) audit.Actor {
	t := audit.ActorUser
	if p.Kind == authn.KindService {
		t = audit.ActorService
	}
	return audit.Actor{Type: t, ID: p.UserID.String()}
}

func (s *Service) audit(ctx context.Context, p authn.Principal, tenantID uuid.UUID,
	action, targetID string) {
	if s.rec == nil {
		return
	}
	_ = s.rec.Record(ctx, audit.Event{
		Actor:    actorOf(p),
		Action:   action,
		Target:   audit.Target{Type: "resource_policy", ID: targetID},
		Result:   audit.ResultAllow,
		TenantID: &tenantID,
	})
}

func tenantRes(tc tenants.TenantContext) authz.Resource {
	return authz.Resource{Kind: "policy", TenantID: tc.Tenant.ID.String()}
}

// GetTenantPolicy returns the tenant-scope policy (policy.read); an
// unset policy returns the zero ResourcePolicy.
func (s *Service) GetTenantPolicy(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext) (admission.ResourcePolicy, int, error) {
	if err := authz.Require(ctx, s.az, p, authz.PolicyRead, tenantRes(tc), s.rec); err != nil {
		return admission.ResourcePolicy{}, 0, err
	}
	pol, err := s.repo.GetTenant(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return admission.ResourcePolicy{}, 0, nil
		}
		return admission.ResourcePolicy{}, 0, err
	}
	return pol.Policy, pol.Version, nil
}

// SetTenantPolicy replaces the tenant-scope policy (policy.manage).
func (s *Service) SetTenantPolicy(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pol admission.ResourcePolicy) (int, error) {
	if err := authz.Require(ctx, s.az, p, authz.PolicyManage, tenantRes(tc), s.rec); err != nil {
		return 0, err
	}
	row := policies.Policy{ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID,
		Scope: policies.ScopeTenant, Policy: pol}
	if err := s.repo.Upsert(ctx, tenants.ScopeFor(&tc), row); err != nil {
		return 0, err
	}
	s.audit(ctx, p, tc.Tenant.ID, "policy.resource.set", "tenant/"+tc.Tenant.ID.String())
	got, err := s.repo.GetTenant(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID)
	if err != nil {
		return 0, err
	}
	return got.Version, nil
}

// GetProjectPolicy returns the project-scope policy (policy.read).
func (s *Service) GetProjectPolicy(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, projectID uuid.UUID) (admission.ResourcePolicy, int, error) {
	if err := authz.Require(ctx, s.az, p, authz.PolicyRead, tenantRes(tc), s.rec); err != nil {
		return admission.ResourcePolicy{}, 0, err
	}
	pol, err := s.repo.GetProject(ctx, tenants.ScopeFor(&tc), projectID)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return admission.ResourcePolicy{}, 0, nil
		}
		return admission.ResourcePolicy{}, 0, err
	}
	return pol.Policy, pol.Version, nil
}

// SetProjectPolicy replaces a project-scope policy (policy.manage at
// tenant level — project admins cannot loosen policy).
func (s *Service) SetProjectPolicy(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, projectID uuid.UUID,
	pol admission.ResourcePolicy) (int, error) {
	if err := authz.Require(ctx, s.az, p, authz.PolicyManage, tenantRes(tc), s.rec); err != nil {
		return 0, err
	}
	row := policies.Policy{ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID,
		Scope: policies.ScopeProject, ProjectID: &projectID, Policy: pol}
	if err := s.repo.Upsert(ctx, tenants.ScopeFor(&tc), row); err != nil {
		return 0, err
	}
	s.audit(ctx, p, tc.Tenant.ID, "policy.resource.set",
		"project/"+projectID.String())
	got, err := s.repo.GetProject(ctx, tenants.ScopeFor(&tc), projectID)
	if err != nil {
		return 0, err
	}
	return got.Version, nil
}

// Effective returns the effective policy: tenant ∩ project via
// admission.IntersectPolicies (a missing policy is "no constraint").
func (s *Service) Effective(ctx context.Context, scope tenants.Scope,
	tenantID, projectID uuid.UUID) (admission.ResourcePolicy, error) {
	tp, err := s.repo.GetTenant(ctx, scope, tenantID)
	if err != nil && !apperr.Is(err, apperr.NotFound) {
		return admission.ResourcePolicy{}, err
	}
	if projectID == uuid.Nil {
		if err != nil {
			return admission.ResourcePolicy{}, nil
		}
		return tp.Policy, nil
	}
	pp, err := s.repo.GetProject(ctx, scope, projectID)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			if tp.ID == uuid.Nil {
				return admission.ResourcePolicy{}, nil
			}
			return tp.Policy, nil
		}
		return admission.ResourcePolicy{}, err
	}
	if tp.ID == uuid.Nil {
		return pp.Policy, nil
	}
	return admission.IntersectPolicies(tp.Policy, pp.Policy), nil
}
