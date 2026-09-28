import Link from "next/link";
import { redirect } from "next/navigation";
import { TenantDisplayName } from "@/components/tenant-shell";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { Badge, StateBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { createApiClient, toApiError, type Job, type ProjectList, type TenantClusterList } from "@/lib/api/client";
import { requireServerSession } from "@/lib/session/server";

const JOB_STATES = ["SUBMITTING", "QUEUED", "RUNNING", "COMPLETED", "FAILED", "CANCELED"] as const;
const STATE_MARKER_STYLES: Record<(typeof JOB_STATES)[number], string> = {
  SUBMITTING: "text-status-queued",
  QUEUED: "text-status-queued",
  RUNNING: "text-status-running",
  COMPLETED: "text-status-completed",
  FAILED: "text-status-failed",
  CANCELED: "text-status-canceled",
};

export default async function TenantDashboard({ params }: { params: Promise<{ tenant: string }> }) {
  const { tenant } = await params;
  const session = await requireServerSession(`/t/${encodeURIComponent(tenant)}`);
  const api = createApiClient(session.accessToken);
  const [projectResponse, clusterResponse, tenantJobResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/projects", { params: { path: { tenant }, query: { limit: 100 } } }),
    api.GET("/tenants/{tenant}/clusters", { params: { path: { tenant } } }),
    api.GET("/tenants/{tenant}/jobs", { params: { path: { tenant }, query: { limit: 100 } } }),
  ]);

  if (projectResponse.error) {
    if (projectResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(`/t/${tenant}`)}`);
    return <ApiErrorNotice error={toApiError(projectResponse.error, projectResponse.response.status)} />;
  }
  if (clusterResponse.error) {
    if (clusterResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(`/t/${tenant}`)}`);
    return <ApiErrorNotice error={toApiError(clusterResponse.error, clusterResponse.response.status)} />;
  }

  const projects: ProjectList["items"] = projectResponse.data.items;
  const clusters: TenantClusterList["items"] = clusterResponse.data.items;
  let jobs: Job[] = tenantJobResponse.error ? [] : tenantJobResponse.data.items;

  if (tenantJobResponse.error) {
    if (tenantJobResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(`/t/${tenant}`)}`);
    if (tenantJobResponse.response.status !== 403) {
      return <ApiErrorNotice error={toApiError(tenantJobResponse.error, tenantJobResponse.response.status)} />;
    }
    const responses = await Promise.all(projects.slice(0, 8).map((project) =>
      api.GET("/tenants/{tenant}/projects/{project}/jobs", {
        params: { path: { tenant, project: project.slug }, query: { limit: 100 } },
      }),
    ));
    for (const response of responses) {
      if (response.data) jobs.push(...response.data.items);
    }
    jobs = jobs.sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)).slice(0, 100);
  }

  const counts = new Map<string, number>(JOB_STATES.map((state) => [state, 0] as const));
  for (const job of jobs) counts.set(job.state, (counts.get(job.state) ?? 0) + 1);
  const projectById = new Map(projects.map((project) => [project.id, project]));
  const clusterById = new Map(clusters.map((cluster) => [cluster.id, cluster]));

  return (
    <div className="space-y-8">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <p className="font-mono text-[10px] uppercase tracking-[0.14em] text-muted-foreground">{"// Overview"}</p>
          <h1 className="mt-2 text-2xl font-semibold tracking-[0.12em]"><TenantDisplayName fallback={tenant} /></h1>
          <p className="mt-2 max-w-xl text-sm text-muted-foreground">Recent activity across the projects and clusters visible to you.</p>
        </div>
        <Button variant="outline" size="sm" nativeButton={false} render={<Link href={`/t/${encodeURIComponent(tenant)}/jobs`}>View jobs</Link>} />
      </header>

      <section aria-label="Job counts" className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {JOB_STATES.map((state, index) => (
          <Card key={state} className="panel-frame">
            <CardContent className="flex items-center justify-between py-4">
              <div>
                <p className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground"><span aria-hidden="true" className={STATE_MARKER_STYLES[state]}>■</span>{String(index + 1).padStart(2, "0")} — {state}</p>
                <p className="mt-2 font-mono text-3xl font-medium tabular-nums">{counts.get(state) ?? 0}</p>
              </div>
            </CardContent>
          </Card>
        ))}
      </section>
      <p className="-mt-5 font-mono text-[10px] text-muted-foreground">Last {jobs.length} jobs — counts by state</p>

      <section className="grid gap-0 border border-border bg-card xl:grid-cols-[minmax(0,1.4fr)_minmax(18rem,1fr)]">
        <section className="min-w-0 border-b border-border xl:border-b-0 xl:border-r" aria-labelledby="recent-jobs-heading">
          <header className="flex items-center justify-between gap-3 border-b border-border px-4 py-3">
            <h2 id="recent-jobs-heading" className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em]">Recent jobs</h2>
            <Badge variant="outline">{jobs.length} loaded</Badge>
          </header>
          <div className="space-y-2 p-3">
            {jobs.slice(0, 8).map((job) => {
              const project = projectById.get(job.project_id);
              const projectRef = project?.slug ?? job.project_id;
              const cluster = clusterById.get(job.cluster_id);
              return (
                <Link key={job.id} href={`/t/${encodeURIComponent(tenant)}/jobs/${job.id}?project=${encodeURIComponent(projectRef)}`} className="flex flex-wrap items-center justify-between gap-3 border border-border bg-card p-3 hover:bg-foreground/[0.04]">
                  <div className="min-w-0">
                    <p className="truncate font-medium">{job.name || job.id.slice(0, 8)}</p>
                    <p className="mt-1 font-mono text-[10px] tracking-[0.08em] text-muted-foreground">{project?.name ?? projectRef} · {cluster?.name ?? cluster?.display_name ?? job.cluster_id}</p>
                  </div>
                  <StateBadge state={job.state} />
                </Link>
              );
            })}
            {jobs.length === 0 ? <p className="py-6 text-sm text-muted-foreground">No jobs are visible yet.</p> : null}
          </div>
        </section>

        <section className="min-w-0" aria-labelledby="clusters-heading">
          <header className="border-b border-border px-4 py-3"><h2 id="clusters-heading" className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em]">Clusters</h2></header>
          <div className="space-y-2 p-3">
            {clusters.map((cluster) => (
              <div key={cluster.id} className="flex items-start justify-between gap-3 border border-border p-3">
                <div>
                  <p className="font-medium">{cluster.display_name}</p>
                  <p className="mt-1 font-mono text-[10px] text-muted-foreground">{cluster.name}{cluster.slurm_version ? ` · SLURM ${cluster.slurm_version}` : ""}</p>
                </div>
                <Badge variant="outline" className={cluster.state === "active" ? "border-status-active/35 bg-status-active/10 text-status-active" : "border-status-degraded/35 bg-status-degraded/10 text-status-degraded"}>
                  <span aria-hidden="true">■</span>{cluster.state}
                </Badge>
              </div>
            ))}
            {clusters.length === 0 ? <p className="py-4 text-sm text-muted-foreground">No clusters are assigned to this tenant.</p> : null}
          </div>
        </section>
      </section>
    </div>
  );
}
