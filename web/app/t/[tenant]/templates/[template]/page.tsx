import Link from "next/link";
import { notFound, redirect } from "next/navigation";
import { ArrowLeft } from "lucide-react";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { BreadcrumbEntity } from "@/components/tenant-shell";
import { TemplateTags } from "@/components/workflows/template-tags";
import { UseTemplateDialog } from "@/components/workflows/use-template-dialog";
import { WorkflowVersionTabs, type WorkflowVersionTab } from "@/components/workflows/workflow-version-tabs";
import { createApiClient, toApiError } from "@/lib/api/client";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";
import { requireServerSession } from "@/lib/session/server";

function first(value: string | string[] | undefined): string {
  return Array.isArray(value) ? value[0] ?? "" : value ?? "";
}

export default async function TemplatePage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string; template: string }>;
  searchParams: Promise<{ tab?: string | string[] }>;
}) {
  const { tenant, template: templateId } = await params;
  const tabValue = first((await searchParams).tab);
  const selectedTab: WorkflowVersionTab = tabValue === "yaml" || tabValue === "parameters" ? tabValue : "graph";
  const returnTo = `/t/${encodeURIComponent(tenant)}/templates/${encodeURIComponent(templateId)}`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const [templateResponse, projectsResponse] = await Promise.all([
    api.GET("/workflow-templates/{template}", { params: { path: { template: templateId } } }),
    api.GET("/tenants/{tenant}/projects", { params: { path: { tenant }, query: { limit: 100 } } }),
  ]);
  if (templateResponse.error) {
    if (templateResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (templateResponse.response.status === 404) notFound();
    return <ApiErrorNotice error={toApiError(templateResponse.error, templateResponse.response.status)} />;
  }
  if (projectsResponse.error) {
    if (projectsResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (projectsResponse.response.status === 404) notFound();
    return <ApiErrorNotice error={toApiError(projectsResponse.error, projectsResponse.response.status)} />;
  }
  const template = templateResponse.data;
  const spec = normalizeWorkflowSpec(template.spec);
  const projects = projectsResponse.data.items.filter((project) => project.state === "active");
  return (
    <div className="space-y-5">
      <BreadcrumbEntity label={template.title} />
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-border pb-4">
        <div className="min-w-0 space-y-3">
          <Link href={`/t/${encodeURIComponent(tenant)}/templates`} className="inline-flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground hover:text-foreground">
            <ArrowLeft aria-hidden="true" className="size-3.5" /> Templates
          </Link>
          <div>
            <h1 className="truncate text-xl font-semibold">{template.title}</h1>
            <p className="mt-1 max-w-3xl text-sm text-muted-foreground">{template.summary}</p>
          </div>
        </div>
        <UseTemplateDialog tenant={tenant} template={template} projects={projects} csrfToken={session.csrfToken} />
      </header>
      <section aria-label="Template notes" className="space-y-3 border border-border bg-card px-4 py-3">
        <p className="max-w-4xl whitespace-pre-line text-sm">{template.description}</p>
        <TemplateTags tags={template.tags} />
      </section>
      <section className="border border-border bg-card">
        <WorkflowVersionTabs key={selectedTab} spec={spec} selectedTab={selectedTab} />
      </section>
    </div>
  );
}
