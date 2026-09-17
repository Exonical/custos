package projects

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/platform/log"
	"github.com/Exonical/custos/internal/tenants"
)

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func validRef(ref string) bool {
	if _, err := uuid.Parse(ref); err == nil {
		return true
	}
	return slugRE.MatchString(ref)
}

func projectNotFound() error {
	return apperr.New(apperr.NotFound, "PROJECT_NOT_FOUND", "project not found")
}

// Getter is the repository surface the middleware needs.
type Getter interface {
	GetBySlugOrID(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID, ref string) (Project, error)
}

// Require resolves the {project} path value (slug or uuid within the
// tenant from tenants.MustTenantContext) into a ProjectContext. Archived
// projects resolve — reads stay allowed, mutations are gated by the
// service. There is no membership requirement here: tenant roles read
// projects without project membership; the authorizer decides. When the
// principal is a member, the roles are attached for RBAC step 3.
func Require(repo Getter, memberships MembershipRepository, logger *slog.Logger) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			ref := r.PathValue("project")
			tc := tenants.MustTenantContext(ctx)
			p := authn.MustPrincipal(ctx)

			if !validRef(ref) {
				httpx.WriteError(ctx, w, projectNotFound())
				return
			}
			proj, err := repo.GetBySlugOrID(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
			if err != nil {
				if apperr.Is(err, apperr.NotFound) {
					logger.InfoContext(ctx, "project not found",
						"request_id", log.RequestIDFrom(ctx))
					httpx.WriteError(ctx, w, projectNotFound())
					return
				}
				httpx.WriteError(ctx, w, err)
				return
			}

			pc := ProjectContext{Project: proj}
			m, err := memberships.GetMembership(ctx, tenants.ScopeFor(&tc), proj.ID, p.UserID)
			if err == nil {
				pc.Membership = &m
				ctx = authz.WithProjectRoles(ctx, proj.ID.String(), m.Roles)
			} else if !apperr.Is(err, apperr.NotFound) {
				httpx.WriteError(ctx, w, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithProjectContext(ctx, pc)))
		})
	}
}
