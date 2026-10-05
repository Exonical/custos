import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { BreadcrumbEntity } from "@/components/tenant-shell";
import { ProjectDetailTabs } from "@/components/project-detail-tabs";
import { RefreshJobButton } from "@/components/jobs/refresh-job-button";
import { Button } from "@/components/ui/button";
import { createApiClient } from "@/lib/api/client";
import { resolveApiError } from "@/lib/api/resolve-api-error";
import { formatUtcDateTime } from "@/lib/format";
import { requireServerSession } from "@/lib/session/server";
import { first } from "@/lib/search-params";

export default async function ProjectDetailPage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string; project: string }>;
  searchParams: Promise<{ tab?: string | string[] }>;
}) {
  const { tenant, project } = await params;
  const tabValue = first((await searchParams).tab);
  const selectedTab = tabValue === "bindings" || tabValue === "allocations" ? tabValue : "members";
  const returnTo = `/t/${encodeURIComponent(tenant)}/projects/${encodeURIComponent(project)}`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const [projectResponse, membersResponse, bindingsResponse, clustersResponse, allocationsResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/projects/{project}", { params: { path: { tenant, project } } }),
    api.GET("/tenants/{tenant}/projects/{project}/members", { params: { path: { tenant, project }, query: { limit: 100 } } }),
    api.GET("/tenants/{tenant}/projects/{project}/cluster-bindings", { params: { path: { tenant, project } } }),
    api.GET("/tenants/{tenant}/clusters", { params: { path: { tenant } } }),
    api.GET("/tenants/{tenant}/projects/{project}/allocations", { params: { path: { tenant, project } } }),
  ]);

  if (projectResponse.error) return resolveApiError(projectResponse, { returnTo, view: "Project details" });
  if (membersResponse.error) return resolveApiError(membersResponse, { returnTo, view: "Project members" });
  if (bindingsResponse.error) return resolveApiError(bindingsResponse, { returnTo, view: "Project cluster bindings" });
  if (clustersResponse.error) return resolveApiError(clustersResponse, { returnTo, view: "Tenant clusters" });
  if (allocationsResponse.error) return resolveApiError(allocationsResponse, { returnTo, view: "Project allocations" });

  const currentProject = projectResponse.data;
  return (
    <div className="space-y-5">
      <BreadcrumbEntity label={currentProject.name} />
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-border pb-4">
        <div className="min-w-0 space-y-3">
          <Link href={`/t/${encodeURIComponent(tenant)}/projects`} className="inline-flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground hover:text-foreground">
            <ArrowLeft aria-hidden="true" className="size-3.5" />
            Projects
          </Link>
          <div>
            <h1 className="truncate text-xl font-semibold tracking-[0.12em]">{currentProject.name}</h1>
            <p className="mt-1 font-mono text-[10px] tracking-[0.08em] text-muted-foreground">{currentProject.slug}</p>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button variant="outline" size="sm" nativeButton={false} render={<Link href={`/t/${encodeURIComponent(tenant)}/jobs?project=${encodeURIComponent(currentProject.slug)}`}>Jobs</Link>} />
          <RefreshJobButton />
        </div>
      </header>

      <section aria-label="Project metadata" className="grid grid-cols-2 gap-px border border-border bg-border sm:grid-cols-5">
        {[
          ["SLUG", currentProject.slug],
          ["STATE", currentProject.state],
          ["CREATED", formatUtcDateTime(currentProject.created_at)],
          ["UPDATED", formatUtcDateTime(currentProject.updated_at)],
          ["VERSION", String(currentProject.version)],
        ].map(([label, value]) => (
          <div key={label} className="min-w-0 bg-card px-3 py-3">
            <p className="font-mono text-[9px] uppercase tracking-[0.1em] text-muted-foreground">{label}</p>
            <p className="mt-1 truncate font-mono text-[10px] tabular-nums">{value}</p>
          </div>
        ))}
      </section>

      <section className="border border-border bg-card">
        <ProjectDetailTabs
          key={selectedTab}
          members={membersResponse.data.items}
          bindings={bindingsResponse.data.items}
          clusters={clustersResponse.data.items}
          allocations={allocationsResponse.data.items}
          selectedTab={selectedTab}
        />
      </section>
    </div>
  );
}
