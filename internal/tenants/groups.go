package tenants

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Group is a tenant-owned named set of users for bulk membership
// management (docs/tenancy.md).
type Group struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Description string
	Source      MembershipSource
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// GroupMembership binds a user to a group. A group member must also be a
// tenant member (service check + DB trigger).
type GroupMembership struct {
	TenantID  uuid.UUID
	GroupID   uuid.UUID
	UserID    uuid.UUID
	Source    MembershipSource
	CreatedAt time.Time
}

// ClaimRule maps an exact IdP claim value to tenant roles (and optionally
// a group) granted during JIT provisioning (docs/authentication.md).
type ClaimRule struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	Claim      string
	MatchValue string
	Roles      []string
	GroupID    *uuid.UUID
	Enabled    bool
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// GroupRepository is the group persistence port.
type GroupRepository interface {
	CreateGroup(ctx context.Context, scope Scope, g Group) error
	// GetGroup resolves a group by id or name within the tenant.
	GetGroup(ctx context.Context, scope Scope, tenantID uuid.UUID, ref string) (Group, error)
	ListGroups(ctx context.Context, scope Scope, tenantID uuid.UUID, page Page) ([]Group, string, error)
	// UpdateGroup applies optimistic concurrency: WHERE id AND version.
	UpdateGroup(ctx context.Context, scope Scope, g Group) error
	DeleteGroup(ctx context.Context, scope Scope, tenantID, groupID uuid.UUID) error

	ListGroupMembers(ctx context.Context, scope Scope, tenantID, groupID uuid.UUID, page Page) ([]GroupMembership, string, error)
	// UpsertGroupMember inserts or refreshes the membership source.
	UpsertGroupMember(ctx context.Context, scope Scope, m GroupMembership) error
	DeleteGroupMember(ctx context.Context, scope Scope, tenantID, groupID, userID uuid.UUID) error

	// ListIDPGroupMembershipsForUser reads all of a user's idp-sourced
	// group memberships across tenants (platform scope internally);
	// used by claim reconciliation.
	ListIDPGroupMembershipsForUser(ctx context.Context, userID uuid.UUID) ([]GroupMembership, error)
}

// ClaimRuleRepository is the claim-mapping-rule persistence port.
type ClaimRuleRepository interface {
	CreateClaimRule(ctx context.Context, scope Scope, r ClaimRule) error
	// GetClaimRule resolves a rule by id within the tenant.
	GetClaimRule(ctx context.Context, scope Scope, tenantID, ruleID uuid.UUID) (ClaimRule, error)
	ListClaimRules(ctx context.Context, scope Scope, tenantID uuid.UUID, page Page) ([]ClaimRule, string, error)
	// UpdateClaimRule applies optimistic concurrency: WHERE id AND version.
	UpdateClaimRule(ctx context.Context, scope Scope, r ClaimRule) error
	DeleteClaimRule(ctx context.Context, scope Scope, tenantID, ruleID uuid.UUID) error

	// MatchRules returns enabled rules whose (claim, match_value) pairs
	// intersect the principal's claim values. Platform scope internally;
	// input is bounded to 200 values.
	MatchRules(ctx context.Context, claims map[string][]string) ([]ClaimRule, error)
}
