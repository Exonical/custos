// Package workflows holds the workflow domain: durable workflow
// records and their immutable versions (docs/workflows.md §Objects).
// Execution lands in M5-C; this slice covers authoring, validation
// and the publish gate.
package workflows

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/tenants"
)

// State is the workflow record lifecycle.
type State string

// Workflow states.
const (
	StateActive   State = "active"
	StateArchived State = "archived"
)

// VersionState is the per-version lifecycle (draft → published →
// deprecated, forward-only; enforced by trigger).
type VersionState string

// Version states.
const (
	VersionDraft      VersionState = "draft"
	VersionPublished  VersionState = "published"
	VersionDeprecated VersionState = "deprecated"
)

// Workflow is the durable workflow record.
type Workflow struct {
	ID                     uuid.UUID
	TenantID               uuid.UUID
	ProjectID              uuid.UUID
	Name                   string
	Description            string
	State                  State
	LatestPublishedVersion *uuid.UUID
	CreatedBy              uuid.UUID
	CreatedAt              time.Time
	UpdatedAt              time.Time
	Version                int64
}

// Version is one numbered, content-hashed spec revision.
type Version struct {
	ID            uuid.UUID
	WorkflowID    uuid.UUID
	TenantID      uuid.UUID
	Number        int
	State         VersionState
	SchemaVersion string
	Spec          []byte // canonical JSON
	SpecHash      [32]byte
	Layout        []byte // jsonb, opaque editor hints
	CreatedBy     uuid.UUID
	CreatedAt     time.Time
	PublishedAt   *time.Time
	Version       int64 // optimistic-lock counter
}

// Repository is the workflows persistence port.
type Repository interface {
	Create(ctx context.Context, scope tenants.Scope, w Workflow) error
	Get(ctx context.Context, scope tenants.Scope, tenantID, id uuid.UUID) (Workflow, error)
	List(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID,
		projectID *uuid.UUID) ([]Workflow, error)
	Update(ctx context.Context, scope tenants.Scope, w Workflow,
		expectVersion int64) error

	CreateVersion(ctx context.Context, scope tenants.Scope, v Version) error
	GetVersion(ctx context.Context, scope tenants.Scope, tenantID,
		workflowID, versionID uuid.UUID) (Version, error)
	ListVersions(ctx context.Context, scope tenants.Scope, tenantID,
		workflowID uuid.UUID) ([]Version, error)
	UpdateDraftSpec(ctx context.Context, scope tenants.Scope, v Version,
		expectVersion int64) error
	UpdateLayout(ctx context.Context, scope tenants.Scope, v Version,
		expectVersion int64) error
	// SetVersionState transitions state and (for publish) stamps
	// published_at and the workflow's latest_published_version_id in
	// the same transaction.
	SetVersionState(ctx context.Context, scope tenants.Scope, v Version,
		to VersionState, expectVersion int64) error
	NextVersionNumber(ctx context.Context, scope tenants.Scope,
		workflowID uuid.UUID) (int, error)
}
