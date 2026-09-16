package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/users"
)

// CreateGroup is the POST .../groups body.
type CreateGroup struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// UpdateGroup is the PATCH .../groups/{group} body.
type UpdateGroup struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Version     int     `json:"version"`
}

// CreateClaimRule is the POST .../claim-rules body.
type CreateClaimRule struct {
	Claim      string     `json:"claim"`
	MatchValue string     `json:"match_value"`
	Roles      []string   `json:"roles"`
	GroupID    *uuid.UUID `json:"group_id,omitempty"`
	Enabled    *bool      `json:"enabled,omitempty"`
}

// UpdateClaimRule is the PATCH .../claim-rules/{rule} body.
type UpdateClaimRule struct {
	Roles   []string   `json:"roles,omitempty"`
	GroupID *uuid.UUID `json:"group_id,omitempty"`
	Enabled *bool      `json:"enabled,omitempty"`
	Version int        `json:"version"`
}

func groupRes(tc tenants.TenantContext, id uuid.UUID) authz.Resource {
	return authz.Resource{
		Kind: "group", ID: id.String(), TenantID: tc.Tenant.ID.String(),
	}
}

// CreateGroup creates a tenant group (tenant.members.manage).
func (s *Service) CreateGroup(ctx context.Context, p authn.Principal, tc tenants.TenantContext, in CreateGroup) (tenants.Group, error) {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return tenants.Group{}, err
	}
	g := tenants.Group{
		ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID,
		Name: in.Name, Description: in.Description,
		Source: tenants.SourceManual,
	}
	if err := s.groups.CreateGroup(ctx, tenants.ScopeFor(&tc), g); err != nil {
		return tenants.Group{}, err
	}
	s.audit(ctx, p, &tc.Tenant.ID, "group.created", "group", g.ID.String(),
		map[string]any{"name": g.Name})
	return g, nil
}

// ListGroups pages tenant groups (tenant.read).
func (s *Service) ListGroups(ctx context.Context, p authn.Principal, tc tenants.TenantContext, page tenants.Page) ([]tenants.Group, string, error) {
	if err := authz.Require(ctx, s.az, p, authz.TenantRead, tenantRes(tc), s.rec); err != nil {
		return nil, "", err
	}
	return s.groups.ListGroups(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, page)
}

// GetGroup returns a group by id or name (tenant.read).
func (s *Service) GetGroup(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string) (tenants.Group, error) {
	if err := authz.Require(ctx, s.az, p, authz.TenantRead, tenantRes(tc), s.rec); err != nil {
		return tenants.Group{}, err
	}
	return s.groups.GetGroup(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
}

// UpdateGroup patches name/description (tenant.members.manage).
func (s *Service) UpdateGroup(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string, in UpdateGroup) (tenants.Group, error) {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return tenants.Group{}, err
	}
	g, err := s.groups.GetGroup(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
	if err != nil {
		return tenants.Group{}, err
	}
	var changed []string
	if in.Name != nil {
		g.Name = *in.Name
		changed = append(changed, "name")
	}
	if in.Description != nil {
		g.Description = *in.Description
		changed = append(changed, "description")
	}
	g.Version = in.Version
	if err := s.groups.UpdateGroup(ctx, tenants.ScopeFor(&tc), g); err != nil {
		return tenants.Group{}, err
	}
	if len(changed) > 0 {
		s.audit(ctx, p, &tc.Tenant.ID, "group.updated", "group", g.ID.String(),
			map[string]any{"fields": changed})
	}
	return g, nil
}

// DeleteGroup deletes a group (tenant.members.manage).
func (s *Service) DeleteGroup(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string) error {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return err
	}
	g, err := s.groups.GetGroup(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
	if err != nil {
		return err
	}
	if err := s.groups.DeleteGroup(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, g.ID); err != nil {
		return err
	}
	s.audit(ctx, p, &tc.Tenant.ID, "group.deleted", "group", g.ID.String(),
		map[string]any{"name": g.Name})
	return nil
}

// ListGroupMembers pages a group's members (tenant.read).
func (s *Service) ListGroupMembers(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string, page tenants.Page) ([]tenants.GroupMembership, string, error) {
	if err := authz.Require(ctx, s.az, p, authz.TenantRead, tenantRes(tc), s.rec); err != nil {
		return nil, "", err
	}
	g, err := s.groups.GetGroup(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
	if err != nil {
		return nil, "", err
	}
	return s.groups.ListGroupMembers(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, g.ID, page)
}

// AddGroupMember adds a tenant member to a group (tenant.members.manage).
// The user must already be a tenant member.
func (s *Service) AddGroupMember(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string, userID uuid.UUID) error {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return err
	}
	g, err := s.groups.GetGroup(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
	if err != nil {
		return err
	}
	if _, err := s.repo.GetMembership(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, userID); err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return apperr.New(apperr.Validation, "NOT_TENANT_MEMBER",
				"group members must be tenant members")
		}
		return err
	}
	if err := s.groups.UpsertGroupMember(ctx, tenants.ScopeFor(&tc), tenants.GroupMembership{
		TenantID: tc.Tenant.ID, GroupID: g.ID, UserID: userID,
		Source: tenants.SourceManual,
	}); err != nil {
		return err
	}
	s.audit(ctx, p, &tc.Tenant.ID, "group.member.added", "group", g.ID.String(),
		map[string]any{"user_id": userID.String()})
	return nil
}

// RemoveGroupMember removes a user from a group (tenant.members.manage).
func (s *Service) RemoveGroupMember(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string, userID uuid.UUID) error {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return err
	}
	g, err := s.groups.GetGroup(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
	if err != nil {
		return err
	}
	if err := s.groups.DeleteGroupMember(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, g.ID, userID); err != nil {
		return err
	}
	s.audit(ctx, p, &tc.Tenant.ID, "group.member.removed", "group", g.ID.String(),
		map[string]any{"user_id": userID.String()})
	return nil
}

func validRuleRoles(roles []string) bool {
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

// checkRuleGroup verifies an optional group_id belongs to the tenant.
func (s *Service) checkRuleGroup(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID, gid *uuid.UUID) error {
	if gid == nil {
		return nil
	}
	g, err := s.groups.GetGroup(ctx, scope, tenantID, gid.String())
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return apperr.New(apperr.Validation, "GROUP_UNKNOWN",
				"group_id does not belong to this tenant")
		}
		return err
	}
	if g.TenantID != tenantID {
		return apperr.New(apperr.Validation, "GROUP_UNKNOWN",
			"group_id does not belong to this tenant")
	}
	return nil
}

// CreateClaimRule creates a claim-mapping rule (tenant.members.manage).
func (s *Service) CreateClaimRule(ctx context.Context, p authn.Principal, tc tenants.TenantContext, in CreateClaimRule) (tenants.ClaimRule, error) {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return tenants.ClaimRule{}, err
	}
	if !validRuleRoles(in.Roles) {
		return tenants.ClaimRule{}, apperr.New(apperr.Validation, "ROLES_INVALID", "unknown tenant role")
	}
	scope := tenants.ScopeFor(&tc)
	if err := s.checkRuleGroup(ctx, scope, tc.Tenant.ID, in.GroupID); err != nil {
		return tenants.ClaimRule{}, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	cr := tenants.ClaimRule{
		ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID,
		Claim: in.Claim, MatchValue: in.MatchValue, Roles: in.Roles,
		GroupID: in.GroupID, Enabled: enabled,
	}
	if err := s.rules.CreateClaimRule(ctx, scope, cr); err != nil {
		return tenants.ClaimRule{}, err
	}
	s.audit(ctx, p, &tc.Tenant.ID, "claim_rule.created", "claim_rule", cr.ID.String(),
		map[string]any{"claim": cr.Claim, "match_value": cr.MatchValue})
	return cr, nil
}

// ListClaimRules pages claim rules (tenant.read).
func (s *Service) ListClaimRules(ctx context.Context, p authn.Principal, tc tenants.TenantContext, page tenants.Page) ([]tenants.ClaimRule, string, error) {
	if err := authz.Require(ctx, s.az, p, authz.TenantRead, tenantRes(tc), s.rec); err != nil {
		return nil, "", err
	}
	return s.rules.ListClaimRules(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, page)
}

// GetClaimRule returns a rule (tenant.read).
func (s *Service) GetClaimRule(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ruleID uuid.UUID) (tenants.ClaimRule, error) {
	if err := authz.Require(ctx, s.az, p, authz.TenantRead, tenantRes(tc), s.rec); err != nil {
		return tenants.ClaimRule{}, err
	}
	return s.rules.GetClaimRule(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ruleID)
}

// UpdateClaimRule patches roles/group/enabled (tenant.members.manage).
func (s *Service) UpdateClaimRule(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ruleID uuid.UUID, in UpdateClaimRule) (tenants.ClaimRule, error) {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return tenants.ClaimRule{}, err
	}
	scope := tenants.ScopeFor(&tc)
	cr, err := s.rules.GetClaimRule(ctx, scope, tc.Tenant.ID, ruleID)
	if err != nil {
		return tenants.ClaimRule{}, err
	}
	var changed []string
	if in.Roles != nil {
		if !validRuleRoles(in.Roles) {
			return tenants.ClaimRule{}, apperr.New(apperr.Validation, "ROLES_INVALID", "unknown tenant role")
		}
		cr.Roles = in.Roles
		changed = append(changed, "roles")
	}
	if in.GroupID != nil {
		if err := s.checkRuleGroup(ctx, scope, tc.Tenant.ID, in.GroupID); err != nil {
			return tenants.ClaimRule{}, err
		}
		cr.GroupID = in.GroupID
		changed = append(changed, "group_id")
	}
	if in.Enabled != nil {
		cr.Enabled = *in.Enabled
		changed = append(changed, "enabled")
	}
	cr.Version = in.Version
	if err := s.rules.UpdateClaimRule(ctx, scope, cr); err != nil {
		return tenants.ClaimRule{}, err
	}
	if len(changed) > 0 {
		s.audit(ctx, p, &tc.Tenant.ID, "claim_rule.updated", "claim_rule", cr.ID.String(),
			map[string]any{"fields": changed})
	}
	return cr, nil
}

// DeleteClaimRule deletes a rule (tenant.members.manage).
func (s *Service) DeleteClaimRule(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ruleID uuid.UUID) error {
	if err := s.mutable(ctx, p, tc, authz.TenantMembersManage); err != nil {
		return err
	}
	if err := s.rules.DeleteClaimRule(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ruleID); err != nil {
		return err
	}
	s.audit(ctx, p, &tc.Tenant.ID, "claim_rule.deleted", "claim_rule", ruleID.String(), nil)
	return nil
}

// Delete requests tenant deletion (platform.manage): state → deleting
// and a tenant.delete work item, committed atomically by the repo.
func (s *Service) Delete(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
	if err := authz.Require(ctx, s.az, p, authz.PlatformManage, platformRes(), s.rec); err != nil {
		return err
	}
	if err := s.repo.RequestDeletion(ctx, tenants.PlatformScope(), tc.Tenant.ID); err != nil {
		return err
	}
	s.audit(ctx, p, &tc.Tenant.ID, "tenant.delete_requested", "tenant", tc.Tenant.ID.String(), nil)
	return nil
}

// LookupUsers finds users by exact (case-insensitive) email
// (tenant.members.manage) — used to invite members by email.
func (s *Service) LookupUsers(ctx context.Context, p authn.Principal, tc tenants.TenantContext, email string) ([]users.User, error) {
	if err := authz.Require(ctx, s.az, p, authz.TenantMembersManage, tenantRes(tc), s.rec); err != nil {
		return nil, err
	}
	if email == "" {
		return nil, nil
	}
	return s.users.FindByEmail(ctx, email)
}

var _ = groupRes // reserved for resource-scoped checks
