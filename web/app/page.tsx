import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import { ApiError, getMe, type Me } from "@/lib/api/client";
import { getConfig } from "@/lib/config";
import { isTenantSlug } from "@/lib/security/tenant-slug";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { getServerSession } from "@/lib/session/server";

export default async function HomePage() {
  const session = await getServerSession();
  if (!session || !session.csrfToken) {
    redirect(`/auth/login?returnTo=${encodeURIComponent("/")}`);
  }

  let me: Me;
  try {
    me = await getMe(session.accessToken);
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      redirect(`/auth/login?returnTo=${encodeURIComponent("/")}`);
    }
    return <ApiErrorNotice error={error} />;
  }

  if (me.memberships.length === 0) redirect("/select-tenant");
  const lastTenant = (await cookies()).get(getConfig().cookieNames.lastTenant)?.value;
  const previous = lastTenant && isTenantSlug(lastTenant)
    ? me.memberships.find((membership) => membership.slug === lastTenant)
    : undefined;
  if (previous) redirect(`/t/${encodeURIComponent(previous.slug)}`);
  if (me.memberships.length === 1) redirect(`/t/${encodeURIComponent(me.memberships[0].slug)}`);
  redirect("/select-tenant");
}
