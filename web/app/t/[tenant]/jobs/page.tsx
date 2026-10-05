import { ApiError, createApiClient, toApiError, type ProjectList, type TenantClusterList } from "@/lib/api/client";
import { loginRedirectIfUnauthorized, resolveApiError } from "@/lib/api/resolve-api-error";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { JobsTable } from "@/components/jobs/jobs-table";
import { requireServerSession } from "@/lib/session/server";
import { first } from "@/lib/search-params";

export default async function JobsPage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string }>;
  searchParams: Promise<{ project?: string | string[]; state?: string | string[]; cursor?: string | string[]; q?: string | string[]; mock_fail?: string | string[] }>;
}) {
  const { tenant } = await params;
  const query = await searchParams;
  const projectFilter = first(query.project);
  const state = first(query.state);
  const cursor = first(query.cursor);
  const search = first(query.q);
  const mockFail = first(query.mock_fail);
  const session = await requireServerSession(`/t/${encodeURIComponent(tenant)}/jobs`);
  const api = createApiClient(session.accessToken, { mockFail });
  const [projectResponse, clusterResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/projects", { params: { path: { tenant }, query: { limit: 100 } } }),
    api.GET("/tenants/{tenant}/clusters", { params: { path: { tenant } } }),
  ]);
  if (projectResponse.error) return resolveApiError(projectResponse, { returnTo: `/t/${tenant}/jobs`, notFound: "notice" });
  loginRedirectIfUnauthorized(clusterResponse.response.status, `/t/${tenant}/jobs`);
  const projects: ProjectList["items"] = projectResponse.data.items;
  const clusters: TenantClusterList["items"] = clusterResponse.data?.items ?? [];

  if (projectFilter) {
    const selected = projects.find((project) => project.slug === projectFilter || project.id === projectFilter);
    if (!selected) return <ApiErrorNotice error={new ApiError(404, "PROJECT_NOT_FOUND", null)} />;
    const response = await api.GET("/tenants/{tenant}/projects/{project}/jobs", {
      params: {
        path: { tenant, project: selected.slug },
        query: { cursor: cursor || undefined, limit: 50, state: state || undefined },
      },
    });
    if (response.error) return resolveApiError(response, { returnTo: `/t/${tenant}/jobs`, notFound: "notice" });
    return <JobsTable tenant={tenant} jobs={response.data.items} projects={projects} clusters={clusters} selectedProject={selected.slug} selectedState={state} cursor={cursor} search={search} nextCursor={response.data.next_cursor ?? null} />;
  }

  const tenantResponse = await api.GET("/tenants/{tenant}/jobs", {
    params: { path: { tenant }, query: { cursor: cursor || undefined, limit: 50, state: state || undefined } },
  });
  if (!tenantResponse.error) {
    return <JobsTable tenant={tenant} jobs={tenantResponse.data.items} projects={projects} clusters={clusters} selectedProject="" selectedState={state} cursor={cursor} search={search} nextCursor={tenantResponse.data.next_cursor ?? null} />;
  }
  if (tenantResponse.response.status !== 403) {
    return resolveApiError(tenantResponse, { returnTo: `/t/${tenant}/jobs`, notFound: "notice" });
  }

  if (projects.length === 0) return <ApiErrorNotice error={toApiError(tenantResponse.error, tenantResponse.response.status)} />;
  const firstProject = projects[0];
  const fallback = await api.GET("/tenants/{tenant}/projects/{project}/jobs", {
    params: { path: { tenant, project: firstProject.slug }, query: { cursor: cursor || undefined, limit: 50, state: state || undefined } },
  });
  if (fallback.error) return resolveApiError(fallback, { returnTo: `/t/${tenant}/jobs`, notFound: "notice" });
  return <JobsTable tenant={tenant} jobs={fallback.data.items} projects={projects} clusters={clusters} selectedProject={firstProject.slug} selectedState={state} cursor={cursor} search={search} nextCursor={fallback.data.next_cursor ?? null} />;
}
