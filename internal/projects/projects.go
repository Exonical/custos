// Package projects holds the project domain types and persistence
// ports (docs/tenancy.md). No pgx, no net/http.
package projects

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/tenants"
)

// State is the project lifecycle state.
type State string

// Project states.
const (
	StateActive   State = "active"
	StateArchived State = "archived"
)

// Project is a work and accounting unit inside a tenant.
type Project struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Slug        string
	Name        string
	Description string
	State       State
	Settings    map[string]any
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Membership is a user's role set inside a project.
type Membership struct {
	TenantID  uuid.UUID
	ProjectID uuid.UUID
	UserID    uuid.UUID
	Roles     []string
	Source    string // "manual" | "idp"
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ClusterBinding binds a project to a tenant-visible cluster with the
// Slurm account/QoS/partition constraints used at admission.
type ClusterBinding struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	ProjectID         uuid.UUID
	ClusterID         uuid.UUID
	SlurmAccount      string
	DefaultPartition  string
	AllowedPartitions []string
	DefaultQoS        string
	AllowedQoS        []string
	Enabled           bool
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ProjectContext is the resolved project plus the principal's
// membership (nil for tenant-level readers without membership).
type ProjectContext struct {
	Project    Project
	Membership *Membership
}

// Repository is the project persistence port; all methods take a
// tenants.Scope.
type Repository interface {
	Create(ctx context.Context, scope tenants.Scope, p Project) error
	GetBySlugOrID(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID, ref string) (Project, error)
	List(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID, page tenants.Page) ([]Project, string, error)
	// ListForUser lists projects the user is a member of (tenant scope).
	ListForUser(ctx context.Context, scope tenants.Scope, tenantID, userID uuid.UUID) ([]Project, error)
	// Update applies optimistic concurrency (WHERE id AND version).
	Update(ctx context.Context, scope tenants.Scope, p Project) error
}

// MembershipRepository is the project-membership port.
type MembershipRepository interface {
	GetMembership(ctx context.Context, scope tenants.Scope, projectID, userID uuid.UUID) (Membership, error)
	ListMemberships(ctx context.Context, scope tenants.Scope, projectID uuid.UUID, page tenants.Page) ([]Membership, string, error)
	UpsertMembership(ctx context.Context, scope tenants.Scope, m Membership) error
	DeleteMembership(ctx context.Context, scope tenants.Scope, projectID, userID uuid.UUID) error
	// CountProjectAdmins counts members holding 'project-admin'
	// (last-admin protection).
	CountProjectAdmins(ctx context.Context, scope tenants.Scope, projectID uuid.UUID) (int, error)
	// ListMembershipsForUser reads across projects in a tenant.
	ListMembershipsForUser(ctx context.Context, scope tenants.Scope, tenantID, userID uuid.UUID) ([]Membership, error)
	// ListAllForUser reads across tenants (platform scope internally);
	// used by /me.
	ListAllForUser(ctx context.Context, userID uuid.UUID) ([]MembershipRef, error)
}

// MembershipRef is a membership with its project slug for /me.
type MembershipRef struct {
	ProjectID   uuid.UUID
	ProjectSlug string
	TenantID    uuid.UUID
	Roles       []string
}

// BindingRepository is the project-cluster-binding port.
type BindingRepository interface {
	CreateBinding(ctx context.Context, scope tenants.Scope, b ClusterBinding) error
	GetBinding(ctx context.Context, scope tenants.Scope, projectID, bindingID uuid.UUID) (ClusterBinding, error)
	GetBindingByCluster(ctx context.Context, scope tenants.Scope, projectID, clusterID uuid.UUID) (ClusterBinding, error)
	ListBindings(ctx context.Context, scope tenants.Scope, projectID uuid.UUID) ([]ClusterBinding, error)
	// UpdateBinding applies optimistic concurrency.
	UpdateBinding(ctx context.Context, scope tenants.Scope, b ClusterBinding) error
	DeleteBinding(ctx context.Context, scope tenants.Scope, projectID, bindingID uuid.UUID) error
}
