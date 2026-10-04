import Link from "next/link";
import { notFound, redirect } from "next/navigation";
import { ApiAccessDenied } from "@/components/api-access-denied";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { SbatchImport } from "@/components/workflows/sbatch-import";
import { buttonVariants } from "@/components/ui/button";
import { createApiClient, toApiError } from "@/lib/api/client";
import { requireServerSession } from "@/lib/session/server";

export default async function WorkflowImportPage({ params }: {
  params: Promise<{ tenant: string }>;
}) {
  const { tenant } = await params;
  const returnTo = `/t/${encodeURIComponent(tenant)}/workflows/import`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const response = await api.GET("/tenants/{tenant}/projects", {
    params: { path: { tenant }, query: { limit: 100 } },
  });
  if (response.error) {
    if (response.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (response.response.status === 404) notFound();
    if (response.response.status === 403) return <ApiAccessDenied view="Projects" />;
    return <ApiErrorNotice error={toApiError(response.error, response.response.status)} />;
  }
  const projects = response.data.items.filter((project) => project.state === "active");
  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-border pb-4">
        <div className="space-y-3">
          <Link href={`/t/${encodeURIComponent(tenant)}/workflows`} className="font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground hover:text-foreground">
            Workflows
          </Link>
          <div>
            <h1 className="text-xl font-semibold">Import sbatch</h1>
            <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
              Import creates only a draft proposal. No workflow is created until you review it and choose Create workflow.
            </p>
          </div>
        </div>
        <Link className={buttonVariants({ variant: "outline" })} href={`/t/${encodeURIComponent(tenant)}/workflows`}>
          Back to workflows
        </Link>
      </header>
      <SbatchImport tenant={tenant} projects={projects} csrfToken={session.csrfToken} />
    </div>
  );
}
