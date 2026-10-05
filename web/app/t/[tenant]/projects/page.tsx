import { notFound, redirect } from "next/navigation";
import { ApiAccessDenied } from "@/components/api-access-denied";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { ProjectsTable } from "@/components/projects-table";
import { createApiClient, toApiError } from "@/lib/api/client";
import { requireServerSession } from "@/lib/session/server";
import { first } from "@/lib/search-params";

export default async function ProjectsPage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string }>;
  searchParams: Promise<{ cursor?: string | string[]; q?: string | string[] }>;
}) {
  const { tenant } = await params;
  const query = await searchParams;
  const cursor = first(query.cursor);
  const search = first(query.q);
  const returnTo = `/t/${encodeURIComponent(tenant)}/projects`;
  const session = await requireServerSession(returnTo);
  const response = await createApiClient(session.accessToken).GET("/tenants/{tenant}/projects", {
    params: { path: { tenant }, query: { cursor: cursor || undefined, limit: 50 } },
  });
  if (response.error) {
    if (response.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    if (response.response.status === 404) notFound();
    if (response.response.status === 403) return <ApiAccessDenied view="Projects" />;
    return <ApiErrorNotice error={toApiError(response.error, response.response.status)} />;
  }
  return <ProjectsTable tenant={tenant} projects={response.data.items} cursor={cursor} search={search} nextCursor={response.data.next_cursor ?? null} />;
}
