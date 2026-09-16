package tenants

import "github.com/google/uuid"

// Scope is the repository-visible tenant scope. Its fields are
// unexported: callers can only construct it via PlatformScope() or
// ScopeFor(TenantContext), which makes cross-tenant access grep-able
// (docs/tenancy.md).
type Scope struct {
	tenantID uuid.UUID
	platform bool
}

// PlatformScope returns the platform-wide scope; only platform services
// and bootstrap paths should use it.
func PlatformScope() Scope { return Scope{platform: true} }

// ScopeFor returns the tenant scope of an established TenantContext.
func ScopeFor(tc *TenantContext) Scope {
	return Scope{tenantID: tc.Tenant.ID}
}

// TenantID returns the scoped tenant id, or false for platform scope.
func (s Scope) TenantID() (uuid.UUID, bool) {
	if s.platform {
		return uuid.Nil, false
	}
	return s.tenantID, true
}

// IsPlatform reports whether this is the platform-wide scope.
func (s Scope) IsPlatform() bool { return s.platform }
