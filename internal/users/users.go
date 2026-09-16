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
	// ClaimsHash/ClaimsSyncedAt drive IdP claim reconciliation
	// (docs/authentication.md JIT): sync runs when the hash differs or
	// the last sync is older than 5 minutes.
	ClaimsHash     []byte
	ClaimsSyncedAt *time.Time
}

// PlatformRoleBinding grants a platform-scope role to a user.
// Issuer/Subject/Email are populated by ListPlatformRoleBindings for the
// platform role-bindings API.
type PlatformRoleBinding struct {
	UserID    uuid.UUID
	Issuer    string
	Subject   string
	Email     string
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
	// CountPlatformRoleHolders counts users holding a platform role
	// (last-admin protection).
	CountPlatformRoleHolders(ctx context.Context, role string) (int, error)

	// SyncIDPClaims reconciles idp-sourced tenant/group memberships
	// with the claim-mapping rules matching claims, in one transaction
	// under platform scope. The users row is locked FOR UPDATE first
	// and the freshness check re-run inside, so a concurrent Provision
	// waits and then skips. Skipped reports the no-op fast path.
	SyncIDPClaims(ctx context.Context, userID uuid.UUID,
		claims map[string][]string, hash []byte, now time.Time) (SyncOutcome, error)
}

// SyncEvent is one audited membership change produced by SyncIDPClaims.
type SyncEvent struct {
	Action   string // membership.granted/updated/revoked/revoke_blocked, group.member.added/removed
	TenantID uuid.UUID
	UserID   uuid.UUID
	Roles    []string
	RuleIDs  []string
	Reason   string
	GroupID  uuid.UUID
}

// SyncOutcome reports what SyncIDPClaims did.
type SyncOutcome struct {
	Skipped bool
	Events  []SyncEvent
}
