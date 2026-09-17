package authz

import "context"

type projectRolesKey struct{}

type projectRoles struct {
	projectID string
	roles     []string
}

// WithProjectRoles attaches the principal's project membership roles
// for projectID to ctx. Set by the project middleware; evaluated in
// RBAC.Check step 3 only when the resource names the same project.
func WithProjectRoles(ctx context.Context, projectID string, roles []string) context.Context {
	return context.WithValue(ctx, projectRolesKey{},
		projectRoles{projectID: projectID, roles: roles})
}

func projectRolesFrom(ctx context.Context, projectID string) ([]string, bool) {
	pr, ok := ctx.Value(projectRolesKey{}).(projectRoles)
	if !ok || pr.projectID != projectID {
		return nil, false
	}
	return pr.roles, true
}
