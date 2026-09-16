// Package clusters defines the cluster-registry domain types and
// repository contract (docs/architecture.md, docs/tenancy.md).
// Platform tables: no RLS on clusters/cluster_partitions; assignments are
// tenant-scoped through tenants.Scope.
package clusters

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/tenants"
)

// State is the cluster health state driven by cluster.sync.
type State string

// Cluster health states driven by cluster.sync hysteresis.
const (
	StateActive      State = "active"
	StateDegraded    State = "degraded"
	StateUnreachable State = "unreachable"
	StateDisabled    State = "disabled"
)

// Visibility controls which tenants may see a cluster.
type Visibility string

// Cluster visibility modes (docs/tenancy.md "Global vs assigned clusters").
const (
	VisibilityAssigned   Visibility = "assigned"
	VisibilityAllTenants Visibility = "all_tenants"
)

// IdentityMode selects the slurmrestd credential mode.
type IdentityMode string

// slurmrestd credential modes (docs/slurm.md).
const (
	IdentityService     IdentityMode = "service"
	IdentityImpersonate IdentityMode = "impersonate"
)

// Cluster is a platform-owned Slurm endpoint. token_ref/client_cert_ref
// are secrets.Reference values — never secret material.
type Cluster struct {
	ID              uuid.UUID
	Name            string // slug
	DisplayName     string
	BaseURL         string
	APIVersion      string
	CABundlePEM     string
	IdentityMode    IdentityMode
	ServiceUser     string
	TokenRef        secrets.Reference
	ClientCertRef   *secrets.Reference
	Visibility      Visibility
	State           State
	ConsecFailures  int
	ConsecSuccesses int
	LastSyncAt      *time.Time
	LastError       string
	Capabilities    *slurm.Capabilities
	CapabilitiesAt  *time.Time
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SlurmConfig maps a registry row onto the adapter-facing config.
func (c Cluster) SlurmConfig() slurm.ClusterConfig {
	return slurm.ClusterConfig{
		ID:            c.ID,
		Name:          c.Name,
		BaseURL:       c.BaseURL,
		APIVersion:    c.APIVersion,
		CABundle:      []byte(c.CABundlePEM),
		IdentityMode:  string(c.IdentityMode),
		ServiceUser:   c.ServiceUser,
		TokenRef:      c.TokenRef,
		ClientCertRef: c.ClientCertRef,
	}
}

// AssignmentDefaults are per-tenant overrides on an assignment.
type AssignmentDefaults struct {
	DefaultAccountPrefix string   `json:"default_account_prefix,omitempty"`
	AllowedPartitions    []string `json:"allowed_partitions,omitempty"`
}

// Assignment row provenance.
const (
	SourceManual = "manual"
	SourceAuto   = "auto"
)

// Assignment grants a tenant visibility of a cluster.
type Assignment struct {
	ClusterID uuid.UUID
	TenantID  uuid.UUID
	Source    string // "manual" | "auto"
	Defaults  AssignmentDefaults
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PartitionRecord is one synced partition row.
type PartitionRecord struct {
	ClusterID  uuid.UUID
	Name       string
	Attributes map[string]any
	SyncedAt   time.Time
}

// SyncResult is what the sync worker records per run.
type SyncResult struct {
	OK           bool
	Err          string
	Capabilities *slurm.Capabilities
	Partitions   []slurm.Partition
	At           time.Time
}

// Page is a keyset page request (shared shape with tenants).
type Page = tenants.Page

// Repository is the clusters persistence contract.
type Repository interface {
	Create(ctx context.Context, c Cluster) error
	GetByNameOrID(ctx context.Context, ref string) (Cluster, error)
	List(ctx context.Context, page Page) ([]Cluster, string, error)
	Update(ctx context.Context, c Cluster) error // optimistic: WHERE id AND version
	// SetState sets state directly (platform ops, e.g. disable).
	SetState(ctx context.Context, id uuid.UUID, s State) error

	// RecordSyncResult applies the hysteresis and stores the capability
	// snapshot + partition rows atomically; returns the new state.
	RecordSyncResult(ctx context.Context, id uuid.UUID, r SyncResult) (State, error)

	UpsertAssignment(ctx context.Context, scope tenants.Scope, a Assignment) error
	DeleteAssignment(ctx context.Context, scope tenants.Scope, clusterID, tenantID uuid.UUID) error
	ListAssignments(ctx context.Context, scope tenants.Scope, clusterID uuid.UUID, page Page) ([]Assignment, string, error)
	// ListVisibleForTenant returns the clusters a tenant may see
	// (assignment join), with the assignment defaults.
	ListVisibleForTenant(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID) ([]Cluster, map[uuid.UUID]Assignment, error)
	// ListAutoTenants lists tenant ids needing auto-assignment rows
	// (active/suspended) — used by the sync worker.
	AutoAssignTenants(ctx context.Context, clusterID uuid.UUID) (assign []uuid.UUID, drop []uuid.UUID, err error)
	ListPartitions(ctx context.Context, clusterID uuid.UUID) ([]PartitionRecord, error)
	ListAll(ctx context.Context) ([]Cluster, error) // for sync bootstrap
}
