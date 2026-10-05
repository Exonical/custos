import Link from "next/link";
import { NewWorkflowDialog } from "@/components/workflows/new-workflow-dialog";
import { WorkflowListFilters } from "@/components/workflows/workflow-list-filters";
import { buttonVariants } from "@/components/ui/button";
import { StateBadge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { createApiClient } from "@/lib/api/client";
import { resolveApiError } from "@/lib/api/resolve-api-error";
import { formatUtcDateTime } from "@/lib/format";
import { requireServerSession } from "@/lib/session/server";
import { first } from "@/lib/search-params";

export default async function WorkflowsPage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string }>;
  searchParams: Promise<{ project?: string | string[]; state?: string | string[] }>;
}) {
  const { tenant } = await params;
  const query = await searchParams;
  const project = first(query.project);
  const includeArchived = first(query.state) === "all";
  const returnTo = `/t/${encodeURIComponent(tenant)}/workflows`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const [workflowsResponse, projectsResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/workflows", { params: { path: { tenant }, query: { project: project || undefined } } }),
    api.GET("/tenants/{tenant}/projects", { params: { path: { tenant }, query: { limit: 100 } } }),
  ]);
  if (workflowsResponse.error) return resolveApiError(workflowsResponse, { returnTo, view: "Workflows" });
  if (projectsResponse.error) return resolveApiError(projectsResponse, { returnTo, view: "Projects" });

  const workflows = workflowsResponse.data.workflows ?? [];
  const projects = projectsResponse.data.items;
  const activeProjects = projects.filter((item) => item.state === "active");
  const versionResults = await Promise.all(workflows.filter((workflow) => workflow.latestPublishedVersionId).map(async (workflow) => ({
    workflow,
    response: await api.GET("/tenants/{tenant}/workflows/{workflow}/versions", { params: { path: { tenant, workflow: workflow.id } } }),
  })));
  const latestVersionNumbers = new Map<string, number>();
  for (const { workflow, response } of versionResults) {
    if (response.response.status !== 200) {
      const failure = resolveApiError({ response: response.response }, { returnTo, view: "Workflow versions", notFound: "skip" });
      if (failure) return failure;
      continue;
    }
    const published = response.data?.versions?.find((version) => version.id === workflow.latestPublishedVersionId);
    if (published) latestVersionNumbers.set(workflow.id, published.number);
  }

  const visibleWorkflows = workflows.filter((workflow) => includeArchived || workflow.state !== "archived");
  const projectById = new Map(projects.map((item) => [item.id, item.name]));
  return (
    <section className="space-y-5">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-border pb-4">
        <h1 className="text-xl font-semibold">Workflows</h1>
        <div className="flex items-center gap-2">
          <NewWorkflowDialog tenant={tenant} projects={activeProjects} csrfToken={session.csrfToken} />
          <Link className={buttonVariants({ variant: "outline" })} href={`/t/${encodeURIComponent(tenant)}/workflows/import`}>
            Import sbatch
          </Link>
        </div>
      </header>
      <WorkflowListFilters projects={projects} project={project} includeArchived={includeArchived} />
      <section aria-label="Workflows" className="overflow-hidden border border-border bg-card">
        <Table containerClassName="max-h-[72vh]">
          <caption className="sr-only">Tenant workflows</caption>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Name</TableHead>
              <TableHead>Project</TableHead>
              <TableHead>State</TableHead>
              <TableHead>Latest published</TableHead>
              <TableHead>Updated</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {visibleWorkflows.length === 0 ? (
              <TableRow><TableCell colSpan={5} className="py-12 text-center">
                <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No workflows</p>
                <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
              </TableCell></TableRow>
            ) : visibleWorkflows.map((workflow) => (
              <TableRow key={workflow.id}>
                <TableCell><Link href={`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflow.id)}`} className="font-medium text-foreground hover:text-primary">{workflow.name}</Link></TableCell>
                <TableCell>{projectById.get(workflow.projectId) ?? workflow.projectId}</TableCell>
                <TableCell><StateBadge state={workflow.state} /></TableCell>
                <TableCell className="font-mono text-xs">{latestVersionNumbers.get(workflow.id) ?? "—"}</TableCell>
                <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(workflow.updatedAt)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </section>
    </section>
  );
}
