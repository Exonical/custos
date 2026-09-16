// Package tenants holds the tenant domain model, the tenant-scope
// contract (Scope), request-scoped TenantContext, and the repository
// port. The PostgreSQL implementation lives in internal/tenants/postgres;
// the HTTP middleware (Require) resolves the {tenant} path segment into
// a TenantContext.
package tenants

import (
	"time"

	"github.com/google/uuid"
)

// State is the tenant lifecycle state.
type State string

// Tenant lifecycle states (docs/tenancy.md).
const (
	StateProvisioning State = "provisioning"
	StateActive       State = "active"
	StateSuspended    State = "suspended"
	StateDeleting     State = "deleting"
	StateDeleted      State = "deleted"
)

// Tenant is the isolation/billing/policy unit.
type Tenant struct {
	ID               uuid.UUID
	Slug             string
	Name             string
	State            State
	Settings         map[string]any
	OpenBaoNamespace string
	Version          int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// MembershipSource records how a membership was granted.
type MembershipSource string

// Membership sources.
const (
	SourceManual MembershipSource = "manual"
	SourceIDP    MembershipSource = "idp"
)

// Membership binds a user to a tenant with tenant-scoped role names.
// TenantSlug/TenantName are populated by ListMembershipsForUser (the /me
// projection) and are empty elsewhere.
type Membership struct {
	TenantID   uuid.UUID
	UserID     uuid.UUID
	Roles      []string
	Source     MembershipSource
	CreatedAt  time.Time
	UpdatedAt  time.Time
	TenantSlug string
	TenantName string
}

// TenantContext is the verified tenant scope for one request:
// the resolved tenant plus the principal's membership. Membership is
// nil for platform-role principals without a membership.
type TenantContext struct {
	Tenant     Tenant
	Membership *Membership
}
