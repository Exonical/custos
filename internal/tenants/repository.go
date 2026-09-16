package tenants

import (
	"context"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/platform/db"
)

// Page is the shared keyset-page request (db.Page).
type Page = db.Page

// Repository is the tenant/membership persistence port. Every method
// takes a Scope: implementations set the RLS scope on the transaction
// AND apply explicit WHERE predicates — RLS is defense in depth, not
// the filter.
type Repository interface {
	Create(ctx context.Context, t Tenant) error
	GetBySlugOrID(ctx context.Context, scope Scope, ref string) (Tenant, error)
	List(ctx context.Context, scope Scope, page Page) ([]Tenant, string, error)

	// Update applies optimistic concurrency: WHERE id AND version.
	// Version mismatch returns apperr Conflict.
	Update(ctx context.Context, scope Scope, t Tenant) error

	GetMembership(ctx context.Context, scope Scope, tenantID, userID uuid.UUID) (Membership, error)
	ListMemberships(ctx context.Context, scope Scope, tenantID uuid.UUID, page Page) ([]Membership, string, error)
	UpsertMembership(ctx context.Context, scope Scope, m Membership) error
	DeleteMembership(ctx context.Context, scope Scope, tenantID, userID uuid.UUID) error

	// ListMembershipsForUser reads across tenants (platform scope
	// internally); used by /me and the tenant middleware.
	ListMembershipsForUser(ctx context.Context, userID uuid.UUID) ([]Membership, error)

	// CountTenantAdmins counts members holding 'tenant-admin' (used by
	// last-admin protection).
	CountTenantAdmins(ctx context.Context, scope Scope, tenantID uuid.UUID) (int, error)
}
