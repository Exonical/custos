import { ProjectsTable } from "@/components/projects-table";
import { createApiClient } from "@/lib/api/client";
import { resolveApiError } from "@/lib/api/resolve-api-error";
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
  if (response.error) return resolveApiError(response, { returnTo, view: "Projects" });
  return <ProjectsTable tenant={tenant} projects={response.data.items} cursor={cursor} search={search} nextCursor={response.data.next_cursor ?? null} />;
}
