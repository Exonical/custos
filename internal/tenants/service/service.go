// Package service implements the tenant application service:
// authorization, lifecycle state gating, membership management, and
// audit for the /tenants API.
package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/users"
)

// Service is the tenant application service: every method authorizes
// through authz, applies state gating, and records audit events.
type Service struct {
	repo   tenants.Repository
	groups tenants.GroupRepository
	rules  tenants.ClaimRuleRepository
	users  users.Repository
	az     authz.Authorizer
	rec    audit.Recorder
}

// NewService wires the tenant service. groups/rules are separate ports;
// the postgres Repository satisfies all three.
func NewService(repo tenants.Repository, groups tenants.GroupRepository,
	rules tenants.ClaimRuleRepository, u users.Repository,
	az authz.Authorizer, rec audit.Recorder) *Service {
	return &Service{repo: repo, groups: groups, rules: rules, users: u, az: az, rec: rec}
}

// CreateTenant is the POST /tenants body.
type CreateTenant struct {
	Slug     string         `json:"slug"`
	Name     string         `json:"name"`
	Settings map[string]any `json:"settings,omitempty"`
}

// UpdateTenant is the PATCH /tenants/{tenant} body. Version is
// required (optimistic concurrency); State here may only move
// active <-> suspended.
type UpdateTenant struct {
	Name     *string         `json:"name,omitempty"`
	Settings *map[string]any `json:"settings,omitempty"`
	State    *tenants.State  `json:"state,omitempty"`
	Version  int             `json:"version"`
}

// AddMember is the POST .../members body.
type AddMember struct {
	UserID uuid.UUID `json:"user_id"`
	Roles  []string  `json:"roles"`
}

// UpdateMember is the PATCH .../members/{user} body.
type UpdateMember struct {
	Roles []string `json:"roles"`
}

func actorOf(p authn.Principal) audit.Actor {
	t := audit.ActorUser
	if p.Kind == authn.KindService {
		t = audit.ActorService
	}
	return audit.Actor{Type: t, ID: p.UserID.String()}
}

func (s *Service) audit(ctx context.Context, p authn.Principal, tenantID *uuid.UUID,
	action, targetType, targetID string, details map[string]any) {
	if s.rec == nil {
		return
	}
	_ = s.rec.Record(ctx, audit.Event{
		Actor:    actorOf(p),
		Action:   action,
		Target:   audit.Target{Type: targetType, ID: targetID},
		Result:   audit.ResultAllow,
		TenantID: tenantID,
		Details:  details,
	})
}

func platformRes() authz.Resource { return authz.Resource{Kind: "platform"} }

func tenantRes(tc tenants.TenantContext) authz.Resource {
	return authz.Resource{
		Kind:     "tenant",
		ID:       tc.Tenant.ID.String(),
		TenantID: tc.Tenant.ID.String(),
	}
}

// Create makes a tenant (platform.manage). tenants.State starts active.
func (s *Service) Create(ctx context.Context, p authn.Principal, in CreateTenant) (tenants.Tenant, error) {
	if err := authz.Require(ctx, s.az, p, authz.PlatformManage, platformRes(), s.rec); err != nil {
		return tenants.Tenant{}, err
	}
	t := tenants.Tenant{
		ID:       uuid.Must(uuid.NewV7()),
		Slug:     in.Slug,
		Name:     in.Name,
		State:    tenants.StateActive,
		Settings: in.Settings,
	}
	if t.Settings == nil {
		t.Settings = map[string]any{}
	}
	if err := s.repo.Create(ctx, t); err != nil {
		return tenants.Tenant{}, err
	}
	s.audit(ctx, p, &t.ID, "tenant.created", "tenant", t.ID.String(),
		map[string]any{"slug": t.Slug})
	return t, nil
}

// List pages tenants (platform.manage or platform.audit.read).
func (s *Service) List(ctx context.Context, p authn.Principal, page tenants.Page) ([]tenants.Tenant, string, error) {
	if err := authz.Require(ctx, s.az, p, authz.PlatformManage, platformRes(), s.rec); err != nil {
		if err2 := authz.Require(ctx, s.az, p, authz.PlatformAuditRead, platformRes(), s.rec); err2 != nil {
			return nil, "", err
		}
	}
	return s.repo.List(ctx, tenants.PlatformScope(), page)
}

// Get returns the tenant in tc (tenant.read).
func (s *Service) Get(ctx context.Context, p authn.Principal, tc tenants.TenantContext) (tenants.Tenant, error) {
	if err := authz.Require(ctx, s.az, p, authz.TenantRead, tenantRes(tc), s.rec); err != nil {
		return tenants.Tenant{}, err
	}
	return tc.Tenant, nil
}

func (s *Service) mutable(ctx context.Context, p authn.Principal, tc tenants.TenantContext, a authz.Action) error {
	if tc.Tenant.State == tenants.StateDeleting || tc.Tenant.State == tenants.StateDeleted {
		return apperr.New(apperr.Conflict, "TENANT_STATE", "tenant is being deleted")
	}
	return authz.Require(ctx, s.az, p, a, tenantRes(tc), s.rec)
}

// Update patches name/settings/state (tenant.manage). Only
// active<->suspended transitions are allowed here.
func (s *Service) Update(ctx context.Context, p authn.Principal, tc tenants.TenantContext, in UpdateTenant) (tenants.Tenant, error) {
	if err := s.mutable(ctx, p, tc, authz.TenantManage); err != nil {
		return tenants.Tenant{}, err
	}
	t := tc.Tenant
	var changed []string
	if in.Name != nil {
		t.Name = *in.Name
		changed = append(changed, "name")
	}
	if in.Settings != nil {
		t.Settings = *in.Settings
		changed = append(changed, "settings")
	}
	if in.State != nil {
		ns := *in.State
		ok := (t.State == tenants.StateActive && ns == tenants.StateSuspended) ||
			(t.State == tenants.StateSuspended && ns == tenants.StateActive)
		if !ok && ns != t.State {
			return tenants.Tenant{}, apperr.New(apperr.Conflict, "TENANT_STATE",
				"only active<->suspended transitions are allowed here")
		}
		if ns != t.State {
			t.State = ns
			changed = append(changed, "state")
		}
	}
	t.Version = in.Version
	// The tenants row itself is platform-managed: the RLS WITH CHECK
	// only admits '*' scope, so the write runs under PlatformScope.
	// The explicit WHERE id+version still pins the row.
	if err := s.repo.Update(ctx, tenants.PlatformScope(), t); err != nil {
		return tenants.Tenant{}, err
	}
	if len(changed) > 0 {
		details := map[string]any{"fields": changed}
		s.audit(ctx, p, &t.ID, "tenant.updated", "tenant", t.ID.String(), details)
	}
	return t, nil
}

// ListMembers pages memberships (tenant.read).
func (s *Service) ListMembers(ctx context.Context, p authn.Principal, tc tenants.TenantContext, page tenants.Page) ([]tenants.Membership, string, error) {
	if err := authz.Require(ctx, s.az, p, authz.TenantRead, tenantRes(tc), s.rec); err != nil {
		return nil, "", err
	}
	return s.repo.ListMemberships(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, page)
}

func validRoles(roles []string) bool {
	if len(roles) == 0 {
		return false
	}
	for _, r := range roles {
		if !authz.ValidTenantRole(r) {
			return false
		}
	}
	return true
}

// AddMember grants tenant roles to an existing user
// (tenant.members.manage). Users exist only after first login.
func (s *Service) AddMember(ctx context.Context, p authn.Principal, tc tenants.TenantContext, in AddMember) (tenants.Membership, error) {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return tenants.Membership{}, err
	}
	if !validRoles(in.Roles) {
		return tenants.Membership{}, apperr.New(apperr.Validation, "ROLES_INVALID", "unknown tenant role")
	}
	if _, err := s.users.GetByID(ctx, in.UserID); err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return tenants.Membership{}, apperr.New(apperr.Validation, "USER_UNKNOWN",
				"user has not logged in yet")
		}
		return tenants.Membership{}, err
	}
	m := tenants.Membership{
		TenantID: tc.Tenant.ID,
		UserID:   in.UserID,
		Roles:    in.Roles,
		Source:   tenants.SourceManual,
	}
	if err := s.repo.UpsertMembership(ctx, tenants.ScopeFor(&tc), m); err != nil {
		return tenants.Membership{}, err
	}
	s.audit(ctx, p, &tc.Tenant.ID, "membership.granted", "user", in.UserID.String(),
		map[string]any{"roles": in.Roles})
	return m, nil
}

// UpdateMemberRoles replaces a member's role set (tenant.members.manage);
// refuses to downgrade the last tenant-admin.
func (s *Service) UpdateMemberRoles(ctx context.Context, p authn.Principal, tc tenants.TenantContext, userID uuid.UUID, in UpdateMember) (tenants.Membership, error) {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return tenants.Membership{}, err
	}
	if !validRoles(in.Roles) {
		return tenants.Membership{}, apperr.New(apperr.Validation, "ROLES_INVALID", "unknown tenant role")
	}
	m, err := s.repo.GetMembership(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, userID)
	if err != nil {
		return tenants.Membership{}, err
	}
	if losesAdmin(m.Roles, in.Roles) {
		if err := s.checkLastAdmin(ctx, tc); err != nil {
			return tenants.Membership{}, err
		}
	}
	m.Roles = in.Roles
	if err := s.repo.UpsertMembership(ctx, tenants.ScopeFor(&tc), m); err != nil {
		return tenants.Membership{}, err
	}
	s.audit(ctx, p, &tc.Tenant.ID, "membership.granted", "user", userID.String(),
		map[string]any{"roles": in.Roles})
	return m, nil
}

// RemoveMember deletes a membership (tenant.members.manage); refuses to
// remove the last tenant-admin.
func (s *Service) RemoveMember(ctx context.Context, p authn.Principal, tc tenants.TenantContext, userID uuid.UUID) error {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return err
	}
	m, err := s.repo.GetMembership(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, userID)
	if err != nil {
		return err
	}
	if hasAdmin(m.Roles) {
		if err := s.checkLastAdmin(ctx, tc); err != nil {
			return err
		}
	}
	if err := s.repo.DeleteMembership(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, userID); err != nil {
		return err
	}
	s.audit(ctx, p, &tc.Tenant.ID, "membership.revoked", "user", userID.String(), nil)
	return nil
}

func hasAdmin(roles []string) bool {
	for _, r := range roles {
		if r == authz.RoleTenantAdmin {
			return true
		}
	}
	return false
}

func losesAdmin(old, next []string) bool {
	return hasAdmin(old) && !hasAdmin(next)
}

func (s *Service) checkLastAdmin(ctx context.Context, tc tenants.TenantContext) error {
	n, err := s.repo.CountTenantAdmins(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return apperr.New(apperr.Conflict, "LAST_ADMIN",
			"cannot remove or downgrade the last tenant-admin")
	}
	return nil
}
