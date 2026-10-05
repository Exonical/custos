import { notFound, redirect } from "next/navigation";
import { ApiAccessDenied } from "@/components/api-access-denied";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { SecretsConnectorsTable } from "@/components/secrets-connectors-table";
import { SecretsReferencesTable } from "@/components/secrets-references-table";
import { SecretsTabs, type SecretTab } from "@/components/secrets-tabs";
import { ApiError, createApiClient } from "@/lib/api/client";
import { requireServerSession } from "@/lib/session/server";
import { first } from "@/lib/search-params";

export default async function SecretsPage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string }>;
  searchParams: Promise<{ tab?: string | string[] }>;
}) {
  const { tenant } = await params;
  const selectedValue = first((await searchParams).tab);
  const selectedTab: SecretTab = selectedValue === "references" ? "references" : "connectors";
  const returnTo = `/t/${encodeURIComponent(tenant)}/secrets`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const [connectorsResponse, referencesResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/secret-connectors", { params: { path: { tenant } } }),
    api.GET("/tenants/{tenant}/secret-references", { params: { path: { tenant } } }),
  ]);
  const connectorsStatus = connectorsResponse.response.status;
  const referencesStatus = referencesResponse.response.status;
  for (const status of [connectorsStatus, referencesStatus]) {
    if (status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (status === 404) notFound();
    if (status !== 200 && status !== 403) return <ApiErrorNotice error={new ApiError(status, "UPSTREAM_ERROR", null)} />;
  }

  const connectors = connectorsResponse.data?.items ?? [];
  const references = referencesResponse.data?.items ?? [];
  const connectorsContent = connectorsStatus === 403
    ? <ApiAccessDenied view="Secret connectors" />
    : <SecretsConnectorsTable connectors={connectors} />;
  const referencesContent = referencesStatus === 403
    ? <ApiAccessDenied view="Secret references" />
    : <SecretsReferencesTable references={references} connectors={connectors} />;

  return (
    <div className="space-y-5">
      <h1 className="sr-only">Secrets</h1>
      <SecretsTabs key={selectedTab} selectedTab={selectedTab} connectorsContent={connectorsContent} referencesContent={referencesContent} />
    </div>
  );
}
