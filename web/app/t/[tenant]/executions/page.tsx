import { CursorPagination } from "@/components/cursor-pagination";
import { ExecutionListFilters } from "@/components/workflows/execution-list-filters";
import { WorkflowExecutionsTable } from "@/components/workflows/workflow-executions-table";
import { createApiClient, type WorkflowExecution, type WorkflowVersion } from "@/lib/api/client";
import { resolveApiError } from "@/lib/api/resolve-api-error";
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
  if (executionResponse.error) return resolveApiError(executionResponse, { returnTo, view: "Executions" });
  if (workflowResponse.error) return resolveApiError(workflowResponse, { returnTo, view: "Workflows" });

  const workflows = workflowResponse.data.workflows ?? [];
  const versionResponses = await Promise.all(workflows.map(async (workflow) => ({
    workflow,
    response: await api.GET("/tenants/{tenant}/workflows/{workflow}/versions", { params: { path: { tenant, workflow: workflow.id } } }),
  })));
  const versions: WorkflowVersion[] = [];
  for (const { response } of versionResponses) {
    if (response.response.status !== 200) {
      const failure = resolveApiError({ response: response.response }, { returnTo, view: "Workflow versions", notFound: "skip" });
      if (failure) return failure;
      continue;
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
