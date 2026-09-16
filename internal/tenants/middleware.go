package tenants

import (
	"log/slog"
	"net/http"
	"regexp"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/platform/log"
)

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func validTenantRef(ref string) bool {
	if _, err := uuid.Parse(ref); err == nil {
		return true
	}
	return slugRE.MatchString(ref)
}

func tenantNotFound() error {
	return apperr.New(apperr.NotFound, "TENANT_NOT_FOUND", "tenant not found")
}

// Require resolves the {tenant} path value into a TenantContext on the
// request context. The tenant is loaded under platform scope (deleted
// tenants never resolve); the membership is loaded for the
// authenticated principal. A non-member without platform roles gets the
// identical 404 a nonexistent tenant produces — no audit event (noise)
// but an info log with the request id.
func Require(repo Repository, logger *slog.Logger, _ audit.Recorder) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			ref := r.PathValue("tenant")
			p := authn.MustPrincipal(ctx)

			var t Tenant
			ok := validTenantRef(ref)
			if ok {
				var err error
				t, err = repo.GetBySlugOrID(ctx, PlatformScope(), ref)
				if err != nil {
					if apperr.Is(err, apperr.NotFound) {
						ok = false
					} else {
						httpx.WriteError(ctx, w, err)
						return
					}
				}
			}
			// Deleting/deleted tenants are tombstones: invisible to
			// ordinary members, resolvable by platform roles for
			// audit review. Suspended tenants still resolve
			// (services decide what is blocked).
			if !ok || ((t.State == StateDeleting || t.State == StateDeleted) &&
				len(p.PlatformRoles) == 0) {
				logger.InfoContext(ctx, "tenant not found or not visible",
					"request_id", log.RequestIDFrom(ctx))
				httpx.WriteError(ctx, w, tenantNotFound())
				return
			}

			tc := TenantContext{Tenant: t}
			m, err := repo.GetMembership(ctx, ScopeFor(&tc), t.ID, p.UserID)
			switch {
			case err == nil:
				tc.Membership = &m
			case apperr.Is(err, apperr.NotFound):
				// No membership: allowed only via platform roles.
				if len(p.PlatformRoles) == 0 {
					logger.InfoContext(ctx, "tenant not found or not visible",
						"request_id", log.RequestIDFrom(ctx))
					httpx.WriteError(ctx, w, tenantNotFound())
					return
				}
			default:
				httpx.WriteError(ctx, w, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithTenantContext(ctx, tc)))
		})
	}
}
