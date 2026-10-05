import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { BreadcrumbEntity } from "@/components/tenant-shell";
import { WorkflowDetailTabs, type WorkflowDetailTab } from "@/components/workflows/workflow-detail-tabs";
import { CreateWorkflowDraftButton } from "@/components/workflows/create-workflow-draft-button";
import { WorkflowExecutionsTable } from "@/components/workflows/workflow-executions-table";
import { WorkflowRunDialog } from "@/components/workflows/workflow-run-dialog";
import { WorkflowVersionsTable } from "@/components/workflows/workflow-versions-table";
import { ExecutionListFilters } from "@/components/workflows/execution-list-filters";
import { CursorPagination } from "@/components/cursor-pagination";
import { StateBadge } from "@/components/ui/badge";
import { createApiClient, parseWorkflowVersion, toApiError, type WorkflowExecution, type WorkflowVersion } from "@/lib/api/client";
import { resolveApiError } from "@/lib/api/resolve-api-error";
import { formatUtcDateTime } from "@/lib/format";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";
import { requireServerSession } from "@/lib/session/server";
import { first } from "@/lib/search-params";

const states: readonly WorkflowExecution["state"][] = ["PENDING", "VALIDATING", "QUEUED", "RUNNING", "SUCCEEDED", "FAILED", "PARTIAL_FAILURE", "CANCELING", "CANCELED"];

export default async function WorkflowDetailPage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string; workflow: string }>;
  searchParams: Promise<{ tab?: string | string[]; state?: string | string[]; test?: string | string[]; cursor?: string | string[] }>;
}) {
  const { tenant, workflow: workflowId } = await params;
  const query = await searchParams;
  const selectedTab: WorkflowDetailTab = first(query.tab) === "executions" ? "executions" : "versions";
  const stateValue = first(query.state);
  const state = states.includes(stateValue as WorkflowExecution["state"]) ? stateValue : "";
  const testValue = first(query.test);
  const test = testValue === "true" ? true : testValue === "false" ? false : undefined;
  const testRuns = test === true ? "only" : test === false ? "exclude" : "all";
  const cursor = first(query.cursor);
  const returnTo = `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const [workflowResponse, versionsResponse, projectsResponse, executionsResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/workflows/{workflow}", { params: { path: { tenant, workflow: workflowId } } }),
    api.GET("/tenants/{tenant}/workflows/{workflow}/versions", { params: { path: { tenant, workflow: workflowId } } }),
    api.GET("/tenants/{tenant}/projects", { params: { path: { tenant }, query: { limit: 100 } } }),
    api.GET("/tenants/{tenant}/workflow-executions", {
      params: { path: { tenant }, query: { workflow: workflowId, state: state || undefined, test, cursor: cursor || undefined, limit: 50 } },
    }),
  ]);
  if (workflowResponse.error) return resolveApiError(workflowResponse, { returnTo, view: "Workflow details" });
  if (projectsResponse.error) return resolveApiError(projectsResponse, { returnTo, view: "Project details" });
  if (executionsResponse.error) return resolveApiError(executionsResponse, { returnTo, view: "Workflow executions" });
  if (versionsResponse.response.status !== 200) return resolveApiError({ response: versionsResponse.response }, { returnTo, view: "Workflow versions" });

  const workflow = workflowResponse.data;
  const versions: WorkflowVersion[] = versionsResponse.data?.versions ?? [];
  const latestVersionId = workflow.latestPublishedVersionId ?? "";
  let latestVersion: WorkflowVersion | undefined;
  if (latestVersionId) {
    const response = await api.GET("/tenants/{tenant}/workflows/{workflow}/versions/{version}", {
      params: { path: { tenant, workflow: workflow.id, version: latestVersionId } },
    });
    if (response.response.status !== 200) return resolveApiError({ response: response.response }, { returnTo, view: "Published workflow version" });
    latestVersion = parseWorkflowVersion(response.data) ?? undefined;
    if (!latestVersion) return <ApiErrorNotice error={toApiError(undefined, 502)} />;
  }
  const projects = projectsResponse.data.items;
  const projectName = projects.find((project) => project.id === workflow.projectId)?.name ?? workflow.projectId;
  const publishedSpec = latestVersion ? normalizeWorkflowSpec(latestVersion.spec) : null;
  const versionsContent = versions.length === 0 ? (
    <div className="grid min-h-64 place-items-center p-6 text-center">
      <CreateWorkflowDraftButton tenant={tenant} workflowId={workflow.id} workflowName={workflow.name} csrfToken={session.csrfToken} />
    </div>
  ) : <WorkflowVersionsTable tenant={tenant} workflow={workflow.id} versions={versions} />;
  const executions = executionsResponse.data.items;
  const nextCursor = executionsResponse.data.next_cursor ?? null;
  const paginationQuery: Record<string, string> = { tab: "executions" };
  if (state) paginationQuery.state = state;
  if (test !== undefined) paginationQuery.test = String(test);
  const executionsContent = (
    <div className="space-y-3">
      <ExecutionListFilters showWorkflow={false} workflows={[workflow]} workflow={workflow.id} state={state} testRuns={testRuns} />
      <WorkflowExecutionsTable tenant={tenant} executions={executions} workflows={[workflow]} versions={versions} />
      <CursorPagination
        pathname={`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflow.id)}`}
        query={paginationQuery}
        cursor={cursor}
        nextCursor={nextCursor}
        countLabel={`${String(executions.length)} executions`}
      />
    </div>
  );

  return (
    <div className="space-y-5">
      <BreadcrumbEntity label={workflow.name} />
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-border pb-4">
        <div className="min-w-0 space-y-3">
          <Link href={`/t/${encodeURIComponent(tenant)}/workflows`} className="inline-flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground hover:text-foreground">
            <ArrowLeft aria-hidden="true" className="size-3.5" /> Workflows
          </Link>
          <div>
            <h1 className="truncate text-xl font-semibold">{workflow.name}</h1>
            {workflow.description ? <p className="mt-1 max-w-3xl text-sm text-muted-foreground">{workflow.description}</p> : null}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {publishedSpec && latestVersion ? <WorkflowRunDialog tenant={tenant} workflow={workflow} version={latestVersion} parameters={publishedSpec.spec.parameters} csrfToken={session.csrfToken} /> : null}
        </div>
      </header>

      <section aria-label="Workflow metadata" className="grid grid-cols-2 gap-px border border-border bg-border sm:grid-cols-4">
        {[
          { label: "PROJECT", value: projectName },
          { label: "STATE", value: <StateBadge state={workflow.state} /> },
          { label: "LATEST PUBLISHED", value: latestVersion ? `v${String(latestVersion.number)}` : "—" },
          { label: "UPDATED", value: formatUtcDateTime(workflow.updatedAt) },
        ].map(({ label, value }) => (
          <div key={label} className="min-w-0 bg-card px-3 py-3">
            <p className="font-mono text-[9px] uppercase tracking-[0.1em] text-muted-foreground">{label}</p>
            <div className="mt-1 truncate font-mono text-xs tabular-nums">{value}</div>
          </div>
        ))}
      </section>

      <section className="border border-border bg-card">
        <WorkflowDetailTabs
          key={selectedTab}
          selectedTab={selectedTab}
          versionsContent={versionsContent}
          executionsContent={executionsContent}
        />
      </section>
    </div>
  );
}
