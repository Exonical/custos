import { notFound, redirect } from "next/navigation";
import { ApiAccessDenied } from "@/components/api-access-denied";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { BreadcrumbEntity } from "@/components/tenant-shell";
import { WorkflowExecutionDetail } from "@/components/workflows/workflow-execution-detail";
import { createApiClient, parseWorkflowVersion, toApiError } from "@/lib/api/client";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";
import { requireServerSession } from "@/lib/session/server";

export default async function WorkflowExecutionPage({ params }: { params: Promise<{ tenant: string; execution: string }> }) {
  const { tenant, execution: executionId } = await params;
  const returnTo = `/t/${encodeURIComponent(tenant)}/executions/${encodeURIComponent(executionId)}`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const executionResponse = await api.GET("/tenants/{tenant}/workflow-executions/{execution}", { params: { path: { tenant, execution: executionId } } });
  if (executionResponse.error) {
    if (executionResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (executionResponse.response.status === 404) notFound();
    if (executionResponse.response.status === 403) return <ApiAccessDenied view="Workflow execution" />;
    return <ApiErrorNotice error={toApiError(executionResponse.error, executionResponse.response.status)} />;
  }
  const execution = executionResponse.data;
  const [workflowResponse, versionResponse, taskResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/workflows/{workflow}", { params: { path: { tenant, workflow: execution.workflowId } } }),
    api.GET("/tenants/{tenant}/workflows/{workflow}/versions/{version}", { params: { path: { tenant, workflow: execution.workflowId, version: execution.workflowVersionId } } }),
    api.GET("/tenants/{tenant}/workflow-executions/{execution}/tasks", { params: { path: { tenant, execution: execution.id } } }),
  ]);
  if (workflowResponse.error) {
    if (workflowResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (workflowResponse.response.status === 404) notFound();
    if (workflowResponse.response.status === 403) return <ApiAccessDenied view="Workflow execution details" />;
    return <ApiErrorNotice error={toApiError(workflowResponse.error, workflowResponse.response.status)} />;
  }
  if (taskResponse.error) {
    if (taskResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (taskResponse.response.status === 404) notFound();
    if (taskResponse.response.status === 403) return <ApiAccessDenied view="Workflow execution tasks" />;
    return <ApiErrorNotice error={toApiError(taskResponse.error, taskResponse.response.status)} />;
  }
  if (versionResponse.response.status !== 200) {
    if (versionResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (versionResponse.response.status === 404) notFound();
    if (versionResponse.response.status === 403) return <ApiAccessDenied view="Pinned workflow version" />;
    return <ApiErrorNotice error={toApiError(undefined, versionResponse.response.status)} />;
  }
  const workflow = workflowResponse.data;
  const version = parseWorkflowVersion(versionResponse.data);
  if (!version) return <ApiErrorNotice error={toApiError(undefined, 502)} />;
  const spec = normalizeWorkflowSpec(version.spec);
  return (
    <div className="space-y-5">
      <BreadcrumbEntity label={`Execution ${execution.id.slice(0, 8)}`} />
      <WorkflowExecutionDetail
        tenant={tenant}
        execution={execution}
        workflow={workflow}
        version={version}
        spec={spec}
        taskExecutions={taskResponse.data.tasks}
        csrfToken={session.csrfToken}
      />
    </div>
  );
}
