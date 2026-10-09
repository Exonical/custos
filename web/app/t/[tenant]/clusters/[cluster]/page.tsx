import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { BreadcrumbEntity } from "@/components/tenant-shell";
import { ClusterPartitionsTab } from "@/components/cluster-partitions-tab";
import { ClusterSettingsForm } from "@/components/clusters/cluster-settings-form";
import { RefreshJobButton } from "@/components/jobs/refresh-job-button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { createApiClient, toApiError } from "@/lib/api/client";
import { loginRedirectIfUnauthorized, resolveApiError } from "@/lib/api/resolve-api-error";
import { requireServerSession } from "@/lib/session/server";

export default async function ClusterDetailPage({ params }: { params: Promise<{ tenant: string; cluster: string }> }) {
  const { tenant, cluster } = await params;
  const returnTo = `/t/${encodeURIComponent(tenant)}/clusters/${encodeURIComponent(cluster)}`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const [clusterResponse, partitionsResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/clusters/{cluster}", { params: { path: { tenant, cluster } } }),
    api.GET("/tenants/{tenant}/clusters/{cluster}/partitions", { params: { path: { tenant, cluster } } }),
  ]);
  if (clusterResponse.error) return resolveApiError(clusterResponse, { returnTo, view: "Cluster details" });
  if (partitionsResponse.error) return resolveApiError(partitionsResponse, { returnTo, view: "Cluster partitions" });

  const current = clusterResponse.data;
  const clusterSettingsResponse = await api.GET("/clusters/{cluster}", { params: { path: { cluster: current.id } } });
  loginRedirectIfUnauthorized(clusterSettingsResponse.response.status, returnTo);
  const meResponse = clusterSettingsResponse.response.status === 200
    ? await api.GET("/me", {})
    : undefined;
  if (meResponse) loginRedirectIfUnauthorized(meResponse.response.status, returnTo);
  const showSettingsTab = clusterSettingsResponse.response.status !== 403;
  const settingsError = clusterSettingsResponse.error
    ? toApiError(clusterSettingsResponse.error, clusterSettingsResponse.response.status)
    : meResponse?.error
      ? toApiError(meResponse.error, meResponse.response.status)
      : undefined;
  const platformCluster = clusterSettingsResponse.data;
  const platformMe = meResponse?.data;
  const canManageSettings = platformMe?.platform_roles.includes("platform-admin") ?? false;
  const statusStyle = current.state === "active" ? "text-status-active" : current.state === "degraded" || current.state === "unreachable" ? "text-status-degraded" : "text-status-canceled";
  return (
    <div className="space-y-5">
      <BreadcrumbEntity label={current.display_name} />
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-border pb-4">
        <div className="min-w-0 space-y-3">
          <Link href={`/t/${encodeURIComponent(tenant)}/clusters`} className="inline-flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground hover:text-foreground">
            <ArrowLeft aria-hidden="true" className="size-3.5" />
            Clusters
          </Link>
          <div>
            <h1 className="truncate text-xl font-semibold tracking-[0.12em]">{current.display_name}</h1>
            <p className="mt-1 font-mono text-[10px] tracking-[0.08em] text-muted-foreground">{current.name}</p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          {canManageSettings ? (
            <Link
              href={`/t/${encodeURIComponent(tenant)}/clusters/${encodeURIComponent(cluster)}/node-hooks`}
              className="inline-flex h-8 items-center border border-border px-3 font-mono text-[10px] uppercase tracking-[0.1em] hover:bg-muted"
            >
              Node hooks
            </Link>
          ) : null}
          <RefreshJobButton />
        </div>
      </header>

      <section aria-label="Cluster metadata" className="grid grid-cols-2 gap-px border border-border bg-border sm:grid-cols-5">
        {[
          ["SLUG", current.name],
          ["STATUS", current.state],
          ["SLURM VERSION", current.slurm_version ?? "—"],
          ["CONTAINER RUNTIME", current.container_runtime?.type ?? "none"],
          ["PARTITIONS", String(partitionsResponse.data.items.length)],
        ].map(([label, value]) => (
          <div key={label} className="min-w-0 bg-card px-3 py-3">
            <p className="font-mono text-[9px] uppercase tracking-[0.1em] text-muted-foreground">{label}</p>
            <p className={`mt-1 truncate font-mono text-[10px] tabular-nums ${label === "STATUS" ? statusStyle : "text-foreground"}`}>{value}</p>
          </div>
        ))}
      </section>

      <section className="border border-border bg-card">
        <Tabs defaultValue="partitions">
          <TabsList variant="line" className="h-8 w-full justify-start border-b border-border px-3">
            <TabsTrigger value="partitions">Partitions</TabsTrigger>
            {showSettingsTab ? <TabsTrigger value="settings">Settings</TabsTrigger> : null}
          </TabsList>
          <TabsContent value="partitions">
            <ClusterPartitionsTab partitions={partitionsResponse.data.items} />
          </TabsContent>
          {showSettingsTab ? (
            <TabsContent value="settings">
              {settingsError || clusterSettingsResponse.response.status !== 200 || !platformCluster || !platformMe ? (
                <ApiErrorNotice error={settingsError ?? toApiError(undefined, clusterSettingsResponse.response.status)} />
              ) : (
                <ClusterSettingsForm
                  clusterId={current.id}
                  version={platformCluster.version}
                  containerRuntime={platformCluster.container_runtime}
                  softwareModules={platformCluster.software_modules}
                  csrfToken={session.csrfToken}
                  editable={canManageSettings}
                />
              )}
            </TabsContent>
          ) : null}
        </Tabs>
      </section>
    </div>
  );
}
