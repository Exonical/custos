package authz

import "slices"

// Platform role names.
const (
	RolePlatformAdmin   = "platform-admin"
	RolePlatformAuditor = "platform-auditor"
)

// Tenant role names; the membership.roles check constraint and service
// validation use this set.
const (
	RoleTenantAdmin    = "tenant-admin"
	RoleTenantOperator = "tenant-operator"
	RoleWorkflowAuthor = "workflow-author"
	RoleResearcher     = "researcher"
	RoleViewer         = "viewer"
	RoleAuditor        = "auditor"
)

// auditorReadPerms is the platform-auditor's explicit read-only set —
// never a mutating action. Encoded as a list, not a substring match.
var auditorReadPerms = []Action{
	PlatformAuditRead,
	TenantRead,
	ProjectRead,
	ClusterRead,
	PolicyRead,
	WorkflowRead,
	ExecutionReadSelf, ExecutionReadTenant,
	JobReadSelf, JobReadTenant,
	SecretReferenceRead, SecretConnectorRead,
	AccountingReadSelf, AccountingReadProject, AccountingReadTenant,
	AuditReadTenant,
}

var researcherPerms = []Action{
	TenantRead, ProjectRead, ClusterRead,
	WorkflowRead, WorkflowExecute,
	JobSubmit, JobReadSelf, JobCancelSelf,
	ExecutionReadSelf, ExecutionCancelSelf,
	AccountingReadSelf,
	SecretReferenceRead, SecretReferenceCreate, SecretReferenceUse, SecretReferenceDelete,
}

// rolePermissions is the role -> permissions data table. Platform and
// tenant scopes are active; project/cluster roles are declared now and
// intentionally empty until M3.
var rolePermissions = map[string][]Action{
	RolePlatformAdmin:   AllActions,
	RolePlatformAuditor: auditorReadPerms,

	RoleTenantAdmin: {
		TenantRead, TenantManage, TenantMembersManage,
		ProjectRead, ProjectCreate, ProjectManage, ProjectMembersManage,
		ClusterRead,
		PolicyRead, PolicyManage,
		WorkflowRead, WorkflowCreate, WorkflowPublish, WorkflowExecute, WorkflowApprove,
		ExecutionReadSelf, ExecutionReadTenant, ExecutionCancelSelf, ExecutionCancelAny,
		JobSubmit, JobReadSelf, JobReadTenant, JobCancelSelf, JobCancelAny,
		SecretReferenceRead, SecretReferenceCreate, SecretReferenceUse, SecretReferenceDelete,
		SecretConnectorRead, SecretConnectorManage,
		AccountingReadSelf, AccountingReadProject, AccountingReadTenant,
		AuditReadTenant,
	},
	RoleTenantOperator: {
		TenantRead, ProjectRead, ClusterRead,
		JobReadTenant, JobCancelAny,
		ExecutionReadTenant, ExecutionCancelAny,
		AccountingReadTenant,
	},
	RoleWorkflowAuthor: append(slices.Clone(researcherPerms),
		WorkflowCreate, WorkflowPublish),
	RoleResearcher: researcherPerms,
	RoleViewer: {
		TenantRead, ProjectRead, ClusterRead, WorkflowRead,
		JobReadSelf, ExecutionReadSelf, AccountingReadSelf,
	},
	RoleAuditor: {
		TenantRead, ProjectRead, ClusterRead, WorkflowRead,
		AuditReadTenant, AccountingReadTenant,
		JobReadTenant, ExecutionReadTenant,
	},

	// Project roles (granted via project_memberships; evaluated only when
	// the request carries a matching project context).
	"project-admin": {
		ProjectRead, ProjectManage, ProjectMembersManage,
		WorkflowRead, WorkflowCreate, WorkflowPublish, WorkflowExecute,
		JobSubmit, JobReadSelf, JobReadProject, JobCancelSelf,
		ExecutionReadSelf, ExecutionReadProject, ExecutionCancelSelf,
		AccountingReadSelf, AccountingReadProject,
	},
	"project-member": {
		ProjectRead, WorkflowRead, WorkflowExecute,
		JobSubmit, JobReadSelf, JobCancelSelf,
		ExecutionReadSelf, ExecutionCancelSelf,
		AccountingReadSelf,
	},
	"project-viewer": {
		ProjectRead, WorkflowRead,
		JobReadSelf, ExecutionReadSelf,
	},

	// Declared for a later milestone; no permissions yet.
	"cluster-operator": {},
	"cluster-user":     {},
}

// ProjectRoles is the set valid in project_memberships.roles.
var ProjectRoles = []string{"project-admin", "project-member", "project-viewer"}

// ValidProjectRole reports whether r is a project-scope role name.
func ValidProjectRole(r string) bool { return slices.Contains(ProjectRoles, r) }

// Permissions returns the sorted permission list for role (nil for
// unknown roles).
func Permissions(role string) []Action {
	p := rolePermissions[role]
	out := slices.Clone(p)
	slices.Sort(out)
	return out
}

// Roles returns all declared role names (sorted) — golden-test input.
func Roles() []string {
	out := make([]string, 0, len(rolePermissions))
	for r := range rolePermissions {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// TenantRoles is the set valid in tenant_memberships.roles.
var TenantRoles = []string{
	RoleTenantAdmin, RoleTenantOperator, RoleWorkflowAuthor,
	RoleResearcher, RoleViewer, RoleAuditor,
}

// ValidTenantRole reports whether r is a tenant-scope role name.
func ValidTenantRole(r string) bool { return slices.Contains(TenantRoles, r) }

// ValidPlatformRole reports whether r is a platform-scope role name.
func ValidPlatformRole(r string) bool {
	return r == RolePlatformAdmin || r == RolePlatformAuditor
}
