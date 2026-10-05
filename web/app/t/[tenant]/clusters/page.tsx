import { ClustersTable } from "@/components/clusters-table";
import { createApiClient } from "@/lib/api/client";
import { resolveApiError } from "@/lib/api/resolve-api-error";
import { requireServerSession } from "@/lib/session/server";
import { first } from "@/lib/search-params";

export default async function ClustersPage({ params, searchParams }: {
  params: Promise<{ tenant: string }>;
  searchParams: Promise<{ q?: string | string[] }>;
}) {
  const { tenant } = await params;
  const search = first((await searchParams).q);
  const returnTo = `/t/${encodeURIComponent(tenant)}/clusters`;
  const session = await requireServerSession(returnTo);
  const response = await createApiClient(session.accessToken).GET("/tenants/{tenant}/clusters", { params: { path: { tenant } } });
  if (response.error) return resolveApiError(response, { returnTo, view: "Clusters" });
  return <ClustersTable tenant={tenant} clusters={response.data.items} search={search} />;
}
