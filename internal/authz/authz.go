// Package authz is the authorization port and built-in RBAC
// implementation (docs/authorization.md). It is pure: no database, no
// HTTP. Membership comes from the TenantContext on ctx; platform roles
// come from the Principal.
package authz

import (
	"context"

	"github.com/Exonical/custos/internal/authn"
)

// Action is a permission name, e.g. "job.submit".
type Action string

// Resource describes what an action targets. "" TenantID marks a
// platform-level resource.
type Resource struct {
	Kind      string            // "tenant", "project", "cluster", "workflow", "job", ...
	ID        string            // resource id ("" for collection-level actions)
	TenantID  string            // "" for platform resources
	ProjectID string            // optional
	OwnerID   string            // optional; enables *.self permissions
	ClusterID string            // optional; enables cluster-scoped permissions
	Attrs     map[string]string // extension point for ABAC
}

// Decision is the outcome of Check.
type Decision struct {
	Allow  bool
	Reason string // stable, non-sensitive; logged and audited on deny
}

// Authorizer answers "may principal perform action on resource".
// error is reserved for infrastructure failures; infra failure denies.
type Authorizer interface {
	Check(ctx context.Context, p authn.Principal, a Action, r Resource) (Decision, error)
}
