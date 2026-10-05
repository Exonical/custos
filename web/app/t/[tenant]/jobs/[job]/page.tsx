import Link from "next/link";
import { notFound } from "next/navigation";
import { ArrowLeft } from "lucide-react";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { BreadcrumbEntity } from "@/components/tenant-shell";
import { CancelJobButton } from "@/components/jobs/cancel-job-button";
import { RefreshJobButton } from "@/components/jobs/refresh-job-button";
import { StateBadge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { createApiClient, toApiError, type Job } from "@/lib/api/client";
import { loginRedirectIfUnauthorized, resolveApiError } from "@/lib/api/resolve-api-error";
import { formatDurationBetween, formatUtcDateTime } from "@/lib/format";
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
  const returnTo = `/t/${encodeURIComponent(tenant)}/jobs/${encodeURIComponent(job)}?project=${encodeURIComponent(projectRef)}`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const [jobResponse, specResponse, clusterResponse, projectResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/projects/{project}/jobs/{job}", {
      params: { path: { tenant, project: projectRef, job } },
    }),
    api.GET("/tenants/{tenant}/projects/{project}/jobs/{job}/execution-spec", {
      params: { path: { tenant, project: projectRef, job } },
    }),
    api.GET("/tenants/{tenant}/clusters", { params: { path: { tenant } } }),
    api.GET("/tenants/{tenant}/projects", { params: { path: { tenant }, query: { limit: 100 } } }),
  ]);
  if (jobResponse.error) return resolveApiError(jobResponse, { returnTo });
  loginRedirectIfUnauthorized(specResponse.response.status, returnTo);
  loginRedirectIfUnauthorized(clusterResponse.response.status, returnTo);
  loginRedirectIfUnauthorized(projectResponse.response.status, returnTo);

  const current: Job = jobResponse.data;
  const cluster = clusterResponse.data?.items.find((item) => item.id === current.cluster_id);
  const project = projectResponse.data?.items.find((item) => item.id === current.project_id || item.slug === projectRef);
  const clusterLabel = cluster?.name || cluster?.display_name || current.cluster_id;
  const projectLabel = project?.name || projectRef;
  const terminal = ["COMPLETED", "FAILED", "CANCELED"].includes(current.state);
  const metadata = [
    { label: "STATUS", value: <StateBadge state={current.state} /> },
    { label: "PROJECT", value: projectLabel },
    { label: "CLUSTER", value: clusterLabel, title: current.cluster_id },
    { label: "SLURM JOB ID", value: current.slurm_job_id?.toString() ?? "—" },
    { label: "CREATED", value: formatUtcDateTime(current.created_at) },
    { label: "STARTED", value: formatUtcDateTime(current.started_at) },
    { label: "FINISHED", value: formatUtcDateTime(current.ended_at) },
    { label: "DURATION", value: formatDurationBetween(current.started_at, current.ended_at) },
  ];

  return (
    <div className="space-y-5">
      <BreadcrumbEntity label={current.name || current.id} />
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-border pb-4">
        <div className="min-w-0 space-y-3">
          <Link className="inline-flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground hover:text-foreground" href={`/t/${encodeURIComponent(tenant)}/jobs`}>
            <ArrowLeft aria-hidden="true" className="size-3.5" />
            Jobs
          </Link>
          <div>
            <h1 className="truncate text-xl font-semibold tracking-[0.12em]">{current.name || current.id}</h1>
            <p className="mt-1 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground">ID · {current.id.slice(0, 8)}</p>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <RefreshJobButton />
          {!terminal ? <CancelJobButton tenant={tenant} project={projectRef} job={job} csrfToken={session.csrfToken} /> : null}
        </div>
      </header>

      <section aria-label="Job metadata" className="grid grid-cols-2 gap-px border border-border bg-border sm:grid-cols-4 xl:grid-cols-8">
        {metadata.map(({ label, value, title }) => (
          <div key={label} className="min-w-0 bg-card px-3 py-3">
            <p className="font-mono text-[9px] uppercase tracking-[0.1em] text-muted-foreground">{label}</p>
            <div title={title} className="mt-1 truncate font-mono text-[10px] tabular-nums text-foreground">{value}</div>
          </div>
        ))}
      </section>

      <div className="grid gap-0 border border-border lg:grid-cols-[minmax(0,1.5fr)_minmax(18rem,0.9fr)]">
        <section className="min-w-0 border-b border-border lg:border-b-0 lg:border-r">
          <Card className="h-full border-0">
            <CardHeader className="border-b border-border pb-3"><h2 className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em]">Resources & execution</h2></CardHeader>
            <CardContent className="space-y-5 pt-4">
              <div>
                <p className="mb-2 font-mono text-[9px] uppercase tracking-[0.12em] text-muted-foreground">Resource request</p>
                <pre className="max-h-56 overflow-auto border border-border bg-background p-3 font-mono text-[10px] leading-5 text-foreground">{pretty(current.resource_request)}</pre>
              </div>
              {current.resource_usage ? (
                <div>
                  <p className="mb-2 font-mono text-[9px] uppercase tracking-[0.12em] text-muted-foreground">Resource usage</p>
                  <pre className="max-h-56 overflow-auto border border-border bg-background p-3 font-mono text-[10px] leading-5 text-foreground">{pretty(current.resource_usage)}</pre>
                </div>
              ) : null}
              <div>
                <p className="mb-2 font-mono text-[9px] uppercase tracking-[0.12em] text-muted-foreground">Execution specification</p>
                {specResponse.data ? <pre className="max-h-[28rem] overflow-auto border border-border bg-background p-3 font-mono text-[10px] leading-5 text-foreground">{pretty(specResponse.data)}</pre> : <p className="text-sm text-muted-foreground">ExecutionSpec is not available.</p>}
              </div>
            </CardContent>
          </Card>
        </section>

        <aside className="min-w-0 bg-card">
          <Tabs defaultValue="details">
            <TabsList variant="line" className="h-10 w-full justify-start border-b border-border px-3">
              <TabsTrigger value="details">Details</TabsTrigger>
            </TabsList>
            <TabsContent value="details" className="p-4">
              <dl className="grid gap-4">
                {[
                  ["STATE REASON", current.state_reason || "—"],
                  ["SLURM STATE", current.slurm_state || "—"],
                  ["SCRIPT LANGUAGE", current.script_language || "—"],
                  ["SCRIPT DIGEST", current.script_digest || "—"],
                  ["EXECUTION SPEC ID", current.execution_spec_id || "—"],
                  ["VALIDATION ID", current.validation_id || "—"],
                  ["CREATED BY", current.created_by || "—"],
                  ["EXIT CODE", current.exit_code?.toString() ?? "—"],
                  ["EXIT SIGNAL", current.exit_signal?.toString() ?? "—"],
                ].map(([label, value]) => (
                  <div key={label}>
                    <dt className="font-mono text-[9px] uppercase tracking-[0.1em] text-muted-foreground">{label}</dt>
                    <dd className="mt-1 break-all font-mono text-[10px] tabular-nums text-foreground">{value}</dd>
                  </div>
                ))}
              </dl>
            </TabsContent>
          </Tabs>
        </aside>
      </div>
      {specResponse.error && specResponse.response.status !== 401 ? <ApiErrorNotice error={toApiError(specResponse.error, specResponse.response.status)} /> : null}
    </div>
  );
}
