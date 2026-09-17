// Package policies holds resource-policy persistence ports. The stored
// policy shape is admission.ResourcePolicy — admission defines the
// canonical struct so the builder never imports policies.
package policies

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/tenants"
)

// Scope is the policy scope.
type Scope string

// Policy scopes.
const (
	ScopeTenant  Scope = "tenant"
	ScopeProject Scope = "project"
)

// Policy is a versioned resource_policy row.
type Policy struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Scope     Scope
	ProjectID *uuid.UUID
	Policy    admission.ResourcePolicy
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Repository is the resource-policy persistence port.
type Repository interface {
	// Upsert inserts or replaces the policy for its scope key.
	Upsert(ctx context.Context, scope tenants.Scope, p Policy) error
	GetTenant(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID) (Policy, error)
	GetProject(ctx context.Context, scope tenants.Scope, projectID uuid.UUID) (Policy, error)
}
