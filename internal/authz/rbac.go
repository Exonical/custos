package authz

import (
	"context"
	"slices"
	"strings"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/tenants"
)

// RBAC is the built-in role-based authorizer. Evaluation order
// (docs/authorization.md):
//  1. platform role bindings (from the principal),
//  2. tenant membership (from the request TenantContext),
//  3. union of tenant/project/cluster role permissions,
//  4. action match with the *.self owner check.
type RBAC struct{}

var _ Authorizer = RBAC{}

// Check implements Authorizer.
func (RBAC) Check(ctx context.Context, p authn.Principal, a Action, r Resource) (Decision, error) {
	// 1. Platform bindings.
	for _, role := range p.PlatformRoles {
		if grants(role, a, p, r) {
			return Decision{Allow: true, Reason: "platform role " + role}, nil
		}
	}

	// 2. Tenant-scoped actions need the verified membership.
	if r.TenantID != "" {
		tc, ok := tenants.TenantContextFrom(ctx)
		if !ok || tc.Tenant.ID.String() != r.TenantID {
			return Decision{Reason: "no tenant membership"}, nil
		}
		if tc.Membership != nil {
			for _, role := range tc.Membership.Roles {
				if grants(role, a, p, r) {
					return Decision{Allow: true, Reason: "tenant role " + role}, nil
				}
			}
			// 3. Project roles apply only when the resource names the same
			// project the request context resolved — and only for tenant
			// members (a project membership requires tenant membership).
			if r.ProjectID != "" {
				if roles, ok := projectRolesFrom(ctx, r.ProjectID); ok {
					for _, role := range roles {
						if grants(role, a, p, r) {
							return Decision{Allow: true, Reason: "project role " + role}, nil
						}
					}
				}
			}
		}
		return Decision{Reason: "tenant/project roles do not permit " + string(a)}, nil
	}

	return Decision{Reason: "no binding grants " + string(a)}, nil
}

// grants reports whether role's permission list satisfies action a on
// resource r, applying the *.self owner check.
func grants(role string, a Action, p authn.Principal, r Resource) bool {
	perms := rolePermissions[role]
	if !slices.Contains(perms, a) {
		return false
	}
	if strings.HasSuffix(string(a), ".self") &&
		r.OwnerID != p.UserID.String() {
		return false
	}
	return true
}
