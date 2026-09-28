import Link from "next/link";
import { notFound, redirect } from "next/navigation";
import { createApiClient, toApiError, type Job } from "@/lib/api/client";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { CancelJobButton } from "@/components/jobs/cancel-job-button";
import { StateBadge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { requireServerSession } from "@/lib/session/server";

function pretty(value: unknown): string {
  return JSON.stringify(value, null, 2);
}

export default async function JobDetailPage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string; job: string }>;
  searchParams: Promise<{ project?: string | string[] }>;
}) {
  const { tenant, job } = await params;
  const query = await searchParams;
  const projectRef = Array.isArray(query.project) ? query.project[0] : query.project;
  if (!projectRef) notFound();
  const session = await requireServerSession(`/t/${encodeURIComponent(tenant)}/jobs/${encodeURIComponent(job)}?project=${encodeURIComponent(projectRef)}`);
  const api = createApiClient(session.accessToken);
  const [jobResponse, specResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/projects/{project}/jobs/{job}", {
      params: { path: { tenant, project: projectRef, job } },
    }),
    api.GET("/tenants/{tenant}/projects/{project}/jobs/{job}/execution-spec", {
      params: { path: { tenant, project: projectRef, job } },
    }),
  ]);
  if (jobResponse.error) {
    if (jobResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(`/t/${tenant}/jobs/${job}`)}`);
    if (jobResponse.response.status === 404) notFound();
    return <ApiErrorNotice error={toApiError(jobResponse.error, jobResponse.response.status)} />;
  }
  if (specResponse.error && specResponse.response.status === 401) {
    redirect(`/auth/login?returnTo=${encodeURIComponent(`/t/${tenant}/jobs/${job}`)}`);
  }

  const current: Job = jobResponse.data;
  const terminal = ["COMPLETED", "FAILED", "CANCELED"].includes(current.state);
  const timeline = [
    ["Created", current.created_at],
    ["Submitted", current.submitted_at],
    ["Started", current.started_at],
    ["Ended", current.ended_at],
    ["Last reconciled", current.last_reconciled_at],
  ] as const;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="space-y-2">
          <Link className="text-sm text-teal-800 hover:underline" href={`/t/${encodeURIComponent(tenant)}/jobs`}>← Jobs</Link>
          <h1 className="text-3xl font-bold tracking-tight">{current.name || current.id}</h1>
          <div className="flex flex-wrap items-center gap-3 text-sm text-slate-600"><StateBadge state={current.state} /><span>{current.id}</span></div>
        </div>
        {!terminal ? <CancelJobButton tenant={tenant} project={projectRef} job={job} csrfToken={session.csrfToken} /> : null}
      </div>

      <Card>
        <CardHeader><h2 className="font-semibold">Job timeline</h2></CardHeader>
        <CardContent>
          <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {timeline.map(([label, value]) => (
              <div key={label}><dt className="text-xs uppercase tracking-wide text-slate-500">{label}</dt><dd className="mt-1 text-sm">{value ? new Date(value).toLocaleString() : "—"}</dd></div>
            ))}
            <div><dt className="text-xs uppercase tracking-wide text-slate-500">Slurm job ID</dt><dd className="mt-1 font-mono text-sm">{current.slurm_job_id ?? "—"}</dd></div>
            <div><dt className="text-xs uppercase tracking-wide text-slate-500">State reason</dt><dd className="mt-1 text-sm">{current.state_reason || "—"}</dd></div>
          </dl>
        </CardContent>
      </Card>

      <div className="grid gap-6 xl:grid-cols-2">
        <Card>
          <CardHeader><h2 className="font-semibold">ExecutionSpec (read-only)</h2></CardHeader>
          <CardContent>
            {specResponse.data ? <pre className="max-h-[34rem] overflow-auto rounded-lg bg-slate-950 p-4 text-xs leading-5 text-slate-100">{pretty(specResponse.data)}</pre> : <p className="text-sm text-slate-500">ExecutionSpec is not available.</p>}
          </CardContent>
        </Card>
        <Card>
          <CardHeader><h2 className="font-semibold">Resource usage</h2></CardHeader>
          <CardContent>
            {current.resource_usage ? <pre className="max-h-[34rem] overflow-auto rounded-lg bg-slate-950 p-4 text-xs leading-5 text-slate-100">{pretty(current.resource_usage)}</pre> : <p className="text-sm text-slate-500">No accounting usage has been collected yet.</p>}
          </CardContent>
        </Card>
      </div>
      {specResponse.error && specResponse.response.status !== 401 ? <ApiErrorNotice error={toApiError(specResponse.error, specResponse.response.status)} /> : null}
    </div>
  );
}
