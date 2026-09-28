package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Exonical/custos/internal/platform/apperr"
)

// ErrNotFound is the NotFound error repositories return for a missing
// row.
var ErrNotFound = apperr.New(apperr.NotFound, "NOT_FOUND", "not found")

// Scope is the repository-visible tenant scope; tenants.Scope
// implements it.
type Scope interface {
	IsPlatform() bool
	TenantID() (uuid.UUID, bool)
}

// ApplyScope sets the RLS scope of s on tx: the platform-wide scope, or
// the scoped tenant.
func ApplyScope(ctx context.Context, tx pgx.Tx, s Scope) error {
	if s.IsPlatform() {
		return SetPlatformScope(ctx, tx)
	}
	id, _ := s.TenantID()
	return SetTenant(ctx, tx, id)
}

// ScopeTenant returns the tenant id a tenant scope constrains to;
// platform scope takes the explicit tenantID argument instead.
func ScopeTenant(s Scope, tenantID uuid.UUID) uuid.UUID {
	if id, ok := s.TenantID(); ok {
		return id
	}
	return tenantID
}
