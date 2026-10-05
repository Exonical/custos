import Link from "next/link";
import { redirect } from "next/navigation";
import { ArrowLeft } from "lucide-react";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { BreadcrumbEntity } from "@/components/tenant-shell";
import { WorkflowEditor } from "@/components/workflows/workflow-editor";
import { createApiClient, parseWorkflowVersion, toApiError } from "@/lib/api/client";
import { loginRedirectIfUnauthorized, resolveApiError } from "@/lib/api/resolve-api-error";
import { requireServerSession } from "@/lib/session/server";
import { stringify } from "yaml";

export default async function WorkflowVersionEditorPage({
  params,
}: {
  params: Promise<{ tenant: string; workflow: string; version: string }>;
}) {
  const { tenant, workflow: workflowId, version: versionId } = await params;
  const returnTo = `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(versionId)}/edit`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const path = { tenant, workflow: workflowId, version: versionId };
  const [workflowResponse, versionResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/workflows/{workflow}", { params: { path: { tenant, workflow: workflowId } } }),
    api.GET("/tenants/{tenant}/workflows/{workflow}/versions/{version}", { params: { path } }),
  ]);
  if (workflowResponse.error) return resolveApiError(workflowResponse, { returnTo, view: "Workflow" });
  if (versionResponse.response.status !== 200 || !versionResponse.data) return resolveApiError({ response: versionResponse.response }, { returnTo, view: "Workflow version" });
  const version = parseWorkflowVersion(versionResponse.data);
  if (!version) return <ApiErrorNotice error={toApiError(undefined, 502)} />;
  if (version.state !== "draft") {
    redirect(`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(versionId)}`);
  }

  const yamlResponse = await api.GET("/tenants/{tenant}/workflows/{workflow}/versions/{version}", {
    params: { path },
    headers: { Accept: "application/yaml" },
    parseAs: "text",
  });
  loginRedirectIfUnauthorized(yamlResponse.response.status, returnTo);
  const yaml = yamlResponse.response.status === 200 && typeof yamlResponse.data === "string"
    ? yamlResponse.data
    : stringify(version.spec, { lineWidth: 0 });
  const workflow = workflowResponse.data;

  return (
    <div className="space-y-5">
      <BreadcrumbEntity label={`${workflow.name} · v${String(version.number)} · edit`} />
      <header className="border-b border-border pb-4">
        <Link href={`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(versionId)}`} className="inline-flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground hover:text-foreground">
          <ArrowLeft aria-hidden="true" className="size-3.5" />
          Version {version.number}
        </Link>
        <h1 className="mt-3 text-xl font-semibold">Edit draft</h1>
        <p className="mt-1 text-sm text-muted-foreground">{workflow.name}</p>
      </header>
      <WorkflowEditor
        key={`${String(version.version)}-${version.specHash}`}
        tenant={tenant}
        workflowId={workflowId}
        workflowName={workflow.name}
        version={version}
        yaml={yaml}
        csrfToken={session.csrfToken}
      />
    </div>
  );
}
