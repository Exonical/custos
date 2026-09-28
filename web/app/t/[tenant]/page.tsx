import Link from "next/link";
import { createApiClient, toApiError, type Job, type ProjectList, type TenantClusterList } from "@/lib/api/client";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { Badge, StateBadge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { requireServerSession } from "@/lib/session/server";
import { redirect } from "next/navigation";

const JOB_STATES = ["SUBMITTING", "QUEUED", "RUNNING", "COMPLETED", "FAILED", "CANCELED"] as const;

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
  let jobsFromVisibleProjects = false;

  if (tenantJobResponse.error) {
    if (tenantJobResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(`/t/${tenant}`)}`);
    if (tenantJobResponse.response.status !== 403) {
      return <ApiErrorNotice error={toApiError(tenantJobResponse.error, tenantJobResponse.response.status)} />;
    }
    jobsFromVisibleProjects = true;
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
          <p className="text-sm font-semibold uppercase tracking-wide text-teal-700">Overview</p>
          <h1 className="mt-1 text-3xl font-bold tracking-tight">{tenant}</h1>
          <p className="mt-2 text-slate-600">Recent activity across the projects and clusters visible to you.</p>
        </div>
        <Link className="text-sm font-semibold text-teal-800 hover:underline" href={`/t/${encodeURIComponent(tenant)}/jobs`}>View jobs</Link>
      </header>

      <section aria-label="Job counts" className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {JOB_STATES.map((state) => (
          <Card key={state}>
            <CardContent className="flex items-center justify-between py-5">
              <div><p className="text-sm text-slate-500">{state.toLowerCase()}</p><p className="mt-1 text-3xl font-bold">{counts.get(state) ?? 0}</p></div>
              <StateBadge state={state} />
            </CardContent>
          </Card>
        ))}
      </section>
      <p className="-mt-6 text-xs text-slate-500">
        Counts reflect the loaded {jobsFromVisibleProjects ? "recent pages from visible projects" : "tenant jobs page"}; no total-count endpoint is exposed.
      </p>

      <section className="grid gap-6 xl:grid-cols-[minmax(0,1.4fr)_minmax(18rem,1fr)]">
        <Card>
          <CardHeader className="flex items-center justify-between">
            <h2 className="font-semibold">Recent jobs</h2>
            <Badge className="bg-slate-100 text-slate-600">{jobs.length} loaded</Badge>
          </CardHeader>
          <CardContent className="space-y-3">
            {jobs.slice(0, 8).map((job) => {
              const project = projectById.get(job.project_id);
              const projectRef = project?.slug ?? job.project_id;
              return (
                <Link key={job.id} href={`/t/${encodeURIComponent(tenant)}/jobs/${job.id}?project=${encodeURIComponent(projectRef)}`} className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-slate-100 p-3 hover:bg-slate-50">
                  <div className="min-w-0"><p className="truncate font-medium">{job.name || job.id.slice(0, 8)}</p><p className="mt-1 text-xs text-slate-500">{project?.name ?? projectRef} · {clusterById.get(job.cluster_id)?.name ?? job.cluster_id.slice(0, 8)}</p></div>
                  <StateBadge state={job.state} />
                </Link>
              );
            })}
            {jobs.length === 0 ? <p className="py-6 text-sm text-slate-500">No jobs are visible yet.</p> : null}
          </CardContent>
        </Card>

        <Card>
          <CardHeader><h2 className="font-semibold">Clusters</h2></CardHeader>
          <CardContent className="space-y-3">
            {clusters.map((cluster) => (
              <div key={cluster.id} className="flex items-start justify-between gap-3 rounded-lg border border-slate-100 p-3">
                <div><p className="font-medium">{cluster.display_name}</p><p className="mt-1 text-xs text-slate-500">{cluster.name}{cluster.slurm_version ? ` · Slurm ${cluster.slurm_version}` : ""}</p></div>
                <Badge className={cluster.state === "active" ? "bg-emerald-100 text-emerald-800" : "bg-amber-100 text-amber-800"}>{cluster.state}</Badge>
              </div>
            ))}
            {clusters.length === 0 ? <p className="py-4 text-sm text-slate-500">No clusters are assigned to this tenant.</p> : null}
          </CardContent>
        </Card>
      </section>
    </div>
  );
}
