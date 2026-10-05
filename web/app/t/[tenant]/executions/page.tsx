import { notFound, redirect } from "next/navigation";
import { ApiAccessDenied } from "@/components/api-access-denied";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { CursorPagination } from "@/components/cursor-pagination";
import { ExecutionListFilters } from "@/components/workflows/execution-list-filters";
import { WorkflowExecutionsTable } from "@/components/workflows/workflow-executions-table";
import { createApiClient, toApiError, type WorkflowExecution, type WorkflowVersion } from "@/lib/api/client";
import { requireServerSession } from "@/lib/session/server";
import { first } from "@/lib/search-params";

const states: readonly WorkflowExecution["state"][] = ["PENDING", "VALIDATING", "QUEUED", "RUNNING", "SUCCEEDED", "FAILED", "PARTIAL_FAILURE", "CANCELING", "CANCELED"];

export default async function ExecutionsPage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string }>;
  searchParams: Promise<{ workflow?: string | string[]; state?: string | string[]; test?: string | string[]; cursor?: string | string[] }>;
}) {
  const { tenant } = await params;
  const query = await searchParams;
  const workflowFilter = first(query.workflow);
  const stateValue = first(query.state);
  const state = states.includes(stateValue as WorkflowExecution["state"]) ? stateValue : "";
  const testValue = first(query.test);
  const test = testValue === "true" ? true : testValue === "false" ? false : undefined;
  const testRuns = test === true ? "only" : test === false ? "exclude" : "all";
  const cursor = first(query.cursor);
  const returnTo = `/t/${encodeURIComponent(tenant)}/executions`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const [executionResponse, workflowResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/workflow-executions", {
      params: { path: { tenant }, query: { workflow: workflowFilter || undefined, state: state || undefined, test, cursor: cursor || undefined, limit: 50 } },
    }),
    api.GET("/tenants/{tenant}/workflows", { params: { path: { tenant } } }),
  ]);
  if (executionResponse.error) {
    if (executionResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (executionResponse.response.status === 404) notFound();
    if (executionResponse.response.status === 403) return <ApiAccessDenied view="Executions" />;
    return <ApiErrorNotice error={toApiError(executionResponse.error, executionResponse.response.status)} />;
  }
  if (workflowResponse.error) {
    if (workflowResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (workflowResponse.response.status === 404) notFound();
    if (workflowResponse.response.status === 403) return <ApiAccessDenied view="Workflows" />;
    return <ApiErrorNotice error={toApiError(workflowResponse.error, workflowResponse.response.status)} />;
  }

  const workflows = workflowResponse.data.workflows ?? [];
  const versionResponses = await Promise.all(workflows.map(async (workflow) => ({
    workflow,
    response: await api.GET("/tenants/{tenant}/workflows/{workflow}/versions", { params: { path: { tenant, workflow: workflow.id } } }),
  })));
  const versions: WorkflowVersion[] = [];
  for (const { response } of versionResponses) {
    if (response.response.status !== 200) {
      if (response.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
      if (response.response.status === 404) continue;
      if (response.response.status === 403) return <ApiAccessDenied view="Workflow versions" />;
      return <ApiErrorNotice error={toApiError(undefined, response.response.status)} />;
    }
    versions.push(...(response.data?.versions ?? []));
  }

  const items = executionResponse.data.items;
  const paginationQuery: Record<string, string> = {};
  if (workflowFilter) paginationQuery.workflow = workflowFilter;
  if (state) paginationQuery.state = state;
  if (test !== undefined) paginationQuery.test = String(test);
  return (
    <section className="space-y-5">
      <h1 className="sr-only">Executions</h1>
      <ExecutionListFilters workflows={workflows} workflow={workflowFilter} state={state} testRuns={testRuns} />
      <WorkflowExecutionsTable tenant={tenant} executions={items} workflows={workflows} versions={versions} />
      <CursorPagination pathname={`/t/${encodeURIComponent(tenant)}/executions`} query={paginationQuery} cursor={cursor} nextCursor={executionResponse.data.next_cursor ?? null} countLabel={`${String(items.length)} executions`} />
    </section>
  );
}
