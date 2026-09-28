import type { ReactNode } from "react";
import { notFound, redirect } from "next/navigation";
import { ApiError, getMe, type Me } from "@/lib/api/client";
import { createEndSessionUrl } from "@/lib/auth/oidc";
import { getConfig } from "@/lib/config";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { TenantShell, type TenantOption } from "@/components/tenant-shell";
import { requireServerSession } from "@/lib/session/server";

export default async function TenantLayout({
  children,
  params,
}: {
  children: ReactNode;
  params: Promise<{ tenant: string }>;
}) {
  const { tenant } = await params;
  const returnTo = `/t/${encodeURIComponent(tenant)}`;
  const session = await requireServerSession(returnTo);
  if (!session.csrfToken) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);

  let me: Me;
  try {
    me = await getMe(session.accessToken);
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    return <ApiErrorNotice error={error} />;
  }

  const tenantOptions: TenantOption[] = me.memberships.map(({ slug, name }) => ({ slug, name }));
  const current = tenantOptions.find((item) => item.slug === tenant);
  if (!current) notFound();
  const userLabel = me.principal.name || me.principal.email || "Account";
  const logoutUrl = await createEndSessionUrl()
    .then((url) => url.toString())
    .catch(() => new URL("/signed-out", getConfig().publicOrigin).toString());

  return (
    <TenantShell tenant={current} tenants={tenantOptions} userLabel={userLabel} csrfToken={session.csrfToken} logoutUrl={logoutUrl}>
      {children}
    </TenantShell>
  );
}
