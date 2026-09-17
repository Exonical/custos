package authz

// Permission catalog (docs/authorization.md). Shell tasks in workflows
// are gated by workflow.publish plus ValidationPolicy.allowShellTasks —
// there is intentionally no workflow.shell permission.
const (
	PlatformManage    Action = "platform.manage"
	PlatformAuditRead Action = "platform.audit.read"

	TenantRead          Action = "tenant.read"
	TenantManage        Action = "tenant.manage"
	TenantMembersManage Action = "tenant.members.manage"

	ProjectRead          Action = "project.read"
	ProjectCreate        Action = "project.create"
	ProjectManage        Action = "project.manage"
	ProjectMembersManage Action = "project.members.manage"

	ClusterRead   Action = "cluster.read"
	ClusterManage Action = "cluster.manage"
	ClusterAssign Action = "cluster.assign"

	PolicyRead   Action = "policy.read"
	PolicyManage Action = "policy.manage"

	WorkflowRead    Action = "workflow.read"
	WorkflowCreate  Action = "workflow.create"
	WorkflowPublish Action = "workflow.publish"
	WorkflowExecute Action = "workflow.execute"
	WorkflowApprove Action = "workflow.approve"

	ExecutionReadSelf    Action = "execution.read.self"
	ExecutionReadProject Action = "execution.read.project"
	ExecutionReadTenant  Action = "execution.read.tenant"
	ExecutionCancelSelf  Action = "execution.cancel.self"
	ExecutionCancelAny   Action = "execution.cancel.any"

	JobSubmit      Action = "job.submit"
	JobReadSelf    Action = "job.read.self"
	JobReadProject Action = "job.read.project"
	JobReadTenant  Action = "job.read.tenant"
	JobCancelSelf  Action = "job.cancel.self"
	JobCancelAny   Action = "job.cancel.any"

	SecretReferenceRead   Action = "secret.reference.read"
	SecretReferenceCreate Action = "secret.reference.create"
	SecretReferenceUse    Action = "secret.reference.use"

	AccountingReadSelf    Action = "accounting.read.self"
	AccountingReadProject Action = "accounting.read.project"
	AccountingReadTenant  Action = "accounting.read.tenant"

	AuditReadTenant Action = "audit.read.tenant"
)

// AllActions is the full catalog in declaration order; platform-admin
// is defined as all of them.
var AllActions = []Action{
	PlatformManage, PlatformAuditRead,
	TenantRead, TenantManage, TenantMembersManage,
	ProjectRead, ProjectCreate, ProjectManage, ProjectMembersManage,
	ClusterRead, ClusterManage, ClusterAssign,
	PolicyRead, PolicyManage,
	WorkflowRead, WorkflowCreate, WorkflowPublish, WorkflowExecute, WorkflowApprove,
	ExecutionReadSelf, ExecutionReadProject, ExecutionReadTenant, ExecutionCancelSelf, ExecutionCancelAny,
	JobSubmit, JobReadSelf, JobReadProject, JobReadTenant, JobCancelSelf, JobCancelAny,
	SecretReferenceRead, SecretReferenceCreate, SecretReferenceUse,
	AccountingReadSelf, AccountingReadProject, AccountingReadTenant,
	AuditReadTenant,
}
