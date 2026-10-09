package nodehooks

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Stored is a persisted node configuration.
type Stored struct {
	ClusterID     uuid.UUID
	Config        Config
	Revision      int64
	ContentSHA256 string
	Version       int
	UpdatedAt     time.Time
	UpdatedBy     *uuid.UUID
}

// Token is a node pull token record (the secret is never stored).
type Token struct {
	ID         uuid.UUID
	ClusterID  uuid.UUID
	Name       string
	CreatedAt  time.Time
	CreatedBy  *uuid.UUID
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// NodeStatus records which revision a node last fetched.
type NodeStatus struct {
	ClusterID uuid.UUID
	NodeName  string
	TokenID   *uuid.UUID
	Revision  int64
	// BundleSHA256 is the ETag of the bundle the node last fetched.
	BundleSHA256 string
	FetchedAt    time.Time
}

// Repository is the node-config persistence contract. All tables are
// platform-owned (no tenant RLS).
type Repository interface {
	// GetConfig returns apperr NotFound when no configuration was stored.
	GetConfig(ctx context.Context, clusterID uuid.UUID) (Stored, error)
	// PutConfig stores cfg. expectedVersion 0 creates the row (Conflict
	// when it exists); otherwise the row version must match. The revision
	// is bumped on every successful write.
	PutConfig(ctx context.Context, clusterID uuid.UUID, cfg Config,
		contentSHA256 string, expectedVersion int, by *uuid.UUID) (Stored, error)

	CreateToken(ctx context.Context, t Token, tokenSHA256 string) error
	ListTokens(ctx context.Context, clusterID uuid.UUID) ([]Token, error)
	// RevokeToken returns NotFound for unknown ids; revoking twice is a no-op.
	RevokeToken(ctx context.Context, clusterID, id uuid.UUID) error
	// LookupToken returns NotFound when the hash is unknown or revoked.
	LookupToken(ctx context.Context, tokenSHA256 string) (Token, error)
	// TouchToken sets last_used_at when older than minInterval.
	TouchToken(ctx context.Context, id uuid.UUID, minInterval time.Duration) error

	// UpsertStatus records a fetch, writing at most once per minInterval
	// for an unchanged revision.
	UpsertStatus(ctx context.Context, s NodeStatus, minInterval time.Duration) error
	ListStatus(ctx context.Context, clusterID uuid.UUID) ([]NodeStatus, error)
}
