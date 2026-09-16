// Package users holds the platform-level user domain model, the
// repository port, and the provisioning service that turns an
// authenticated principal into a Custos user on first login.
package users

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/authn"
)

// User is a platform-level identity mapped from (issuer, subject).
type User struct {
	ID          uuid.UUID
	Issuer      string
	Subject     string
	Kind        authn.Kind
	Email       string
	DisplayName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastSeenAt  *time.Time
}

// PlatformRoleBinding grants a platform-scope role to a user.
type PlatformRoleBinding struct {
	UserID    uuid.UUID
	Role      string
	CreatedAt time.Time
	CreatedBy *uuid.UUID
}

// Repository is the user persistence port. Platform tables have no RLS
// scope; methods run under platform scope internally.
type Repository interface {
	// UpsertByIdentity inserts or refreshes the user keyed by
	// (issuer, subject); created reports a new row. last_seen_at is
	// bumped at most once per 5 minutes to avoid write amplification.
	UpsertByIdentity(ctx context.Context, u User) (User, bool, error)
	GetByID(ctx context.Context, id uuid.UUID) (User, error)
	FindByEmail(ctx context.Context, email string) ([]User, error)
	PlatformRoles(ctx context.Context, userID uuid.UUID) ([]string, error)
	GrantPlatformRole(ctx context.Context, userID uuid.UUID, role string, by *uuid.UUID) error
	RevokePlatformRole(ctx context.Context, userID uuid.UUID, role string) error
	ListPlatformRoleBindings(ctx context.Context) ([]PlatformRoleBinding, error)
}
