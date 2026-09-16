// Package authn verifies bearer tokens (OIDC resource-server side) and
// carries the resulting Principal on the request context. User
// provisioning and platform roles land in M2-B; here Principal.UserID is
// zero and PlatformRoles is empty.
package authn

import (
	"context"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/google/uuid"
)

// Kind distinguishes interactive users from service accounts.
type Kind string

const (
	// KindUser is an interactive human principal.
	KindUser Kind = "user"
	// KindService is a machine/service principal (client-credentials).
	KindService Kind = "service"
)

// Principal is the authenticated caller. Claims that are absent in the
// token map to zero values; TokenID carries the jti for audit
// correlation — the raw token is never stored or logged.
type Principal struct {
	Issuer        string
	Subject       string
	UserID        uuid.UUID
	Kind          Kind
	Email         string
	Name          string
	Scopes        []string
	Groups        []string
	PlatformRoles []string
	TokenID       string
}

// Verifier authenticates a raw bearer token.
type Verifier interface {
	Verify(ctx context.Context, rawToken string) (Principal, error)
}

// Provisioner maps a verified principal to a provisioned Custos user:
// it fills UserID and PlatformRoles (implemented by internal/users).
type Provisioner interface {
	Provision(ctx context.Context, p Principal) (Principal, error)
}

// DenyAll rejects every token; used when auth is unconfigured so
// protected routes fail closed (401) instead of silently allowing.
type DenyAll struct{}

// Verify implements Verifier.
func (DenyAll) Verify(context.Context, string) (Principal, error) {
	return Principal{}, errUnauthenticated("TOKEN_MISSING", "authentication is not configured")
}

type ctxKey struct{}

// WithPrincipal attaches p to ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom returns the principal on ctx, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// MustPrincipal returns the principal on ctx or panics — a programming
// error meaning a handler ran without RequireBearer upstream.
func MustPrincipal(ctx context.Context) Principal {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		panic("authn: no principal on context")
	}
	return p
}

// errUnauthenticated builds the shared error type; msg must never carry
// token contents or claim values.
func errUnauthenticated(code, msg string) error {
	return apperr.New(apperr.Unauthenticated, code, msg)
}
