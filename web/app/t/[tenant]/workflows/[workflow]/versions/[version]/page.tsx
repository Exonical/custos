import Link from "next/link";
import { notFound, redirect } from "next/navigation";
import { ArrowLeft } from "lucide-react";
import { ApiAccessDenied } from "@/components/api-access-denied";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { BreadcrumbEntity } from "@/components/tenant-shell";
import { DownloadWorkflowYamlButton } from "@/components/workflows/download-buttons";
import { WorkflowVersionLifecycleActions } from "@/components/workflows/workflow-version-actions";
import { WorkflowVersionTabs, type WorkflowVersionTab } from "@/components/workflows/workflow-version-tabs";
import { StateBadge } from "@/components/ui/badge";
import { createApiClient, parseWorkflowVersion, toApiError } from "@/lib/api/client";
import { formatUtcDateTime } from "@/lib/format";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";
import { requireServerSession } from "@/lib/session/server";

function first(value: string | string[] | undefined): string {
  return Array.isArray(value) ? value[0] ?? "" : value ?? "";
}

export default async function WorkflowVersionPage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string; workflow: string; version: string }>;
  searchParams: Promise<{ tab?: string | string[] }>;
}) {
  const { tenant, workflow: workflowId, version: versionId } = await params;
  const tabValue = first((await searchParams).tab);
  const selectedTab: WorkflowVersionTab = tabValue === "yaml" || tabValue === "parameters" ? tabValue : "graph";
  const returnTo = `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(versionId)}`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const [workflowResponse, versionResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/workflows/{workflow}", { params: { path: { tenant, workflow: workflowId } } }),
    api.GET("/tenants/{tenant}/workflows/{workflow}/versions/{version}", { params: { path: { tenant, workflow: workflowId, version: versionId } } }),
  ]);
  if (workflowResponse.error) {
    if (workflowResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (workflowResponse.response.status === 404) notFound();
    if (workflowResponse.response.status === 403) return <ApiAccessDenied view="Workflow" />;
    return <ApiErrorNotice error={toApiError(workflowResponse.error, workflowResponse.response.status)} />;
  }
  if (versionResponse.response.status !== 200) {
    if (versionResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (versionResponse.response.status === 404) notFound();
    if (versionResponse.response.status === 403) return <ApiAccessDenied view="Workflow version" />;
    return <ApiErrorNotice error={toApiError(undefined, versionResponse.response.status)} />;
  }

  const workflow = workflowResponse.data;
  const version = parseWorkflowVersion(versionResponse.data);
  if (!version) return <ApiErrorNotice error={toApiError(undefined, 502)} />;
  const spec = normalizeWorkflowSpec(version.spec);
  const shortHash = version.specHash.length > 19 ? `${version.specHash.slice(0, 19)}…` : version.specHash;
  return (
    <div className="space-y-5">
      <BreadcrumbEntity label={`${workflow.name} · v${String(version.number)}`} />
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-border pb-4">
        <div className="min-w-0 space-y-3">
          <Link href={`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflow.id)}`} className="inline-flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground hover:text-foreground">
            <ArrowLeft aria-hidden="true" className="size-3.5" /> {workflow.name}
          </Link>
          <div>
            <h1 className="truncate text-xl font-semibold">Version {version.number}</h1>
            <p className="mt-1 font-mono text-[10px] text-muted-foreground" title={version.specHash}>{shortHash}</p>
          </div>
        </div>
        <div className="flex flex-wrap items-start gap-3">
          <StateBadge state={version.state} />
          <WorkflowVersionLifecycleActions
            tenant={tenant}
            workflowId={workflow.id}
            versionId={version.id}
            state={version.state}
            csrfToken={session.csrfToken}
          />
          <DownloadWorkflowYamlButton
            tenant={tenant}
            workflow={workflow.id}
            version={version.id}
            workflowName={workflow.name}
            versionNumber={version.number}
          />
        </div>
      </header>

      <section aria-label="Workflow version metadata" className="grid grid-cols-2 gap-px border border-border bg-border sm:grid-cols-4">
        {[
          { label: "SCHEMA VERSION", value: version.schemaVersion },
          { label: "SPEC HASH", value: shortHash },
          { label: "CREATED", value: formatUtcDateTime(version.createdAt) },
          { label: "PUBLISHED", value: formatUtcDateTime(version.publishedAt) },
        ].map(({ label, value }) => (
          <div key={label} className="min-w-0 bg-card px-3 py-3">
            <p className="font-mono text-[9px] uppercase tracking-[0.1em] text-muted-foreground">{label}</p>
            <p className="mt-1 truncate font-mono text-[10px] tabular-nums" title={value}>{value}</p>
          </div>
        ))}
      </section>

      <section className="border border-border bg-card">
        <WorkflowVersionTabs
          key={selectedTab}
          spec={spec}
          layout={version.layout}
          selectedTab={selectedTab}
          downloadContext={{ tenant, workflow: workflow.id, version: version.id }}
        />
      </section>
    </div>
  );
}
