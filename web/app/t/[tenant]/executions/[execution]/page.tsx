import { ApiErrorNotice } from "@/components/api-error-notice";
import { BreadcrumbEntity } from "@/components/tenant-shell";
import { WorkflowExecutionDetail } from "@/components/workflows/workflow-execution-detail";
import { createApiClient, parseWorkflowVersion, toApiError } from "@/lib/api/client";
import { resolveApiError } from "@/lib/api/resolve-api-error";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";
import { requireServerSession } from "@/lib/session/server";

export default async function WorkflowExecutionPage({ params }: { params: Promise<{ tenant: string; execution: string }> }) {
  const { tenant, execution: executionId } = await params;
  const returnTo = `/t/${encodeURIComponent(tenant)}/executions/${encodeURIComponent(executionId)}`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const executionResponse = await api.GET("/tenants/{tenant}/workflow-executions/{execution}", { params: { path: { tenant, execution: executionId } } });
  if (executionResponse.error) return resolveApiError(executionResponse, { returnTo, view: "Workflow execution" });
  const execution = executionResponse.data;
  const [workflowResponse, versionResponse, taskResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/workflows/{workflow}", { params: { path: { tenant, workflow: execution.workflowId } } }),
    api.GET("/tenants/{tenant}/workflows/{workflow}/versions/{version}", { params: { path: { tenant, workflow: execution.workflowId, version: execution.workflowVersionId } } }),
    api.GET("/tenants/{tenant}/workflow-executions/{execution}/tasks", { params: { path: { tenant, execution: execution.id } } }),
  ]);
  if (workflowResponse.error) return resolveApiError(workflowResponse, { returnTo, view: "Workflow execution details" });
  if (taskResponse.error) return resolveApiError(taskResponse, { returnTo, view: "Workflow execution tasks" });
  if (versionResponse.response.status !== 200) return resolveApiError({ response: versionResponse.response }, { returnTo, view: "Pinned workflow version" });
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
