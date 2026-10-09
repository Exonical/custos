import Link from "next/link";
import { notFound, redirect } from "next/navigation";
import { ArrowLeft } from "lucide-react";
import { ApiAccessDenied } from "@/components/api-access-denied";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { BreadcrumbEntity } from "@/components/tenant-shell";
import { NodeHooksEditor } from "@/components/clusters/node-hooks/node-hooks-editor";
import { createApiClient, toApiError } from "@/lib/api/client";
import { requireServerSession } from "@/lib/session/server";

export default async function NodeHooksPage({ params }: { params: Promise<{ tenant: string; cluster: string }> }) {
  const { tenant, cluster } = await params;
  const returnTo = `/t/${encodeURIComponent(tenant)}/clusters/${encodeURIComponent(cluster)}/node-hooks`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const unauthenticated = () => redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);

  const clusterResponse = await api.GET("/clusters/{cluster}", { params: { path: { cluster } } });
  if (clusterResponse.response.status === 401) unauthenticated();
  if (clusterResponse.response.status === 403) return <ApiAccessDenied view="Node hooks" />;
  if (clusterResponse.response.status === 404) notFound();
  if (clusterResponse.error) {
    return <ApiErrorNotice error={toApiError(clusterResponse.error, clusterResponse.response.status)} />;
  }
  const platformCluster = clusterResponse.data;
  const path = { cluster: platformCluster.id };
  const [configResponse, tokensResponse, statusResponse, assignmentsResponse, tenantsResponse] = await Promise.all([
    api.GET("/clusters/{cluster}/node-config", { params: { path } }),
    api.GET("/clusters/{cluster}/node-tokens", { params: { path } }),
    api.GET("/clusters/{cluster}/node-status", { params: { path } }),
    api.GET("/clusters/{cluster}/tenants", { params: { path, query: { limit: 200 } } }),
    api.GET("/tenants", { params: { query: { limit: 200 } } }),
  ]);
  for (const response of [configResponse, tokensResponse, statusResponse, assignmentsResponse, tenantsResponse]) {
    if (response.response.status === 401) unauthenticated();
    if (response.response.status === 403) return <ApiAccessDenied view="Node hooks" />;
  }
  if (configResponse.error) return <ApiErrorNotice error={toApiError(configResponse.error, configResponse.response.status)} />;
  if (tokensResponse.error) return <ApiErrorNotice error={toApiError(tokensResponse.error, tokensResponse.response.status)} />;
  if (statusResponse.error) return <ApiErrorNotice error={toApiError(statusResponse.error, statusResponse.response.status)} />;
  if (tenantsResponse.error) return <ApiErrorNotice error={toApiError(tenantsResponse.error, tenantsResponse.response.status)} />;

  const assigned = new Set(assignmentsResponse.data?.items.map((item) => item.tenant_id) ?? []);
  const tenantOptions = tenantsResponse.data.items
    .filter((item) => item.state !== "deleted" && item.state !== "deleting")
    .filter((item) => platformCluster.visibility === "all_tenants" || assigned.has(item.id))
    .map((item) => ({ id: item.id, slug: item.slug, name: item.name }));

  return (
    <div className="space-y-5">
      <BreadcrumbEntity label={platformCluster.display_name} />
      <header className="space-y-3 border-b border-border pb-4">
        <Link
          href={`/t/${encodeURIComponent(tenant)}/clusters/${encodeURIComponent(cluster)}`}
          className="inline-flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft aria-hidden="true" className="size-3.5" />
          {platformCluster.display_name}
        </Link>
        <div>
          <h1 className="text-xl font-semibold tracking-[0.12em]">Node hooks</h1>
          <p className="mt-1 font-mono text-[10px] tracking-[0.08em] text-muted-foreground">{platformCluster.name}</p>
        </div>
      </header>
      <div className="border border-border bg-card">
        <NodeHooksEditor
          clusterId={platformCluster.id}
          clusterName={platformCluster.name}
          initialView={configResponse.data}
          initialTokens={tokensResponse.data.items}
          initialStatus={statusResponse.data}
          tenants={tenantOptions}
          csrfToken={session.csrfToken}
        />
      </div>
    </div>
  );
}
