import Link from "next/link";
import { redirect } from "next/navigation";
import { ApiError, getMe, type Me } from "@/lib/api/client";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { getServerSession } from "@/lib/session/server";

export default async function SelectTenantPage() {
  const session = await getServerSession();
  if (!session || !session.csrfToken) redirect(`/auth/login?returnTo=${encodeURIComponent("/select-tenant")}`);

  let me: Me;
  try {
    me = await getMe(session.accessToken);
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      redirect(`/auth/login?returnTo=${encodeURIComponent("/select-tenant")}`);
    }
    return <ApiErrorNotice error={error} />;
  }

  return (
    <main className="mx-auto flex min-h-screen max-w-2xl flex-col justify-center gap-6 px-6 py-12">
      <header className="space-y-2">
        <p className="text-sm font-semibold uppercase tracking-wide text-teal-700">Custos</p>
        <h1 className="text-3xl font-bold tracking-tight">Choose a tenant</h1>
        <p className="text-slate-600">Select a workspace to view its jobs and clusters.</p>
      </header>
      {me.memberships.length === 0 ? (
        <Card>
          <CardContent className="py-6 text-sm text-slate-600">
            You do not have a tenant membership yet. Ask a tenant administrator to grant access.
          </CardContent>
        </Card>
      ) : (
        <ul className="space-y-3">
          {me.memberships.map((membership) => (
            <li key={membership.tenant_id}>
              <Card>
                <CardHeader className="flex flex-row items-center justify-between gap-4">
                  <div>
                    <h2 className="font-semibold">{membership.name}</h2>
                    <p className="text-sm text-slate-500">{membership.slug}</p>
                  </div>
                  <Button asChild>
                    <Link href={`/t/${encodeURIComponent(membership.slug)}`}>Open tenant</Link>
                  </Button>
                </CardHeader>
                <CardContent className="pt-3 text-xs text-slate-500">
                  Roles: {membership.roles.join(", ")}
                </CardContent>
              </Card>
            </li>
          ))}
        </ul>
      )}
    </main>
  );
}
