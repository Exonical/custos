import { notFound, redirect } from "next/navigation";
import { ApiAccessDenied } from "@/components/api-access-denied";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { ClustersTable } from "@/components/clusters-table";
import { createApiClient, toApiError } from "@/lib/api/client";
import { requireServerSession } from "@/lib/session/server";

function first(value: string | string[] | undefined): string {
  return Array.isArray(value) ? value[0] ?? "" : value ?? "";
}

export default async function ClustersPage({ params, searchParams }: {
  params: Promise<{ tenant: string }>;
  searchParams: Promise<{ q?: string | string[] }>;
}) {
  const { tenant } = await params;
  const search = first((await searchParams).q);
  const returnTo = `/t/${encodeURIComponent(tenant)}/clusters`;
  const session = await requireServerSession(returnTo);
  const response = await createApiClient(session.accessToken).GET("/tenants/{tenant}/clusters", { params: { path: { tenant } } });
  if (response.error) {
    if (response.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (response.response.status === 404) notFound();
    if (response.response.status === 403) return <ApiAccessDenied view="Clusters" />;
    return <ApiErrorNotice error={toApiError(response.error, response.response.status)} />;
  }
  return <ClustersTable tenant={tenant} clusters={response.data.items} search={search} />;
}
