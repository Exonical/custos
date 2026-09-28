import { redirect } from "next/navigation";
import { ApiError, createApiClient, toApiError, type ProjectList } from "@/lib/api/client";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { JobsTable } from "@/components/jobs/jobs-table";
import { requireServerSession } from "@/lib/session/server";

function first(value: string | string[] | undefined): string {
  return Array.isArray(value) ? value[0] ?? "" : value ?? "";
}

export default async function JobsPage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string }>;
  searchParams: Promise<{ project?: string | string[]; state?: string | string[]; cursor?: string | string[] }>;
}) {
  const { tenant } = await params;
  const query = await searchParams;
  const projectFilter = first(query.project);
  const state = first(query.state);
  const cursor = first(query.cursor);
  const session = await requireServerSession(`/t/${encodeURIComponent(tenant)}/jobs`);
  const api = createApiClient(session.accessToken);
  const projectResponse = await api.GET("/tenants/{tenant}/projects", {
    params: { path: { tenant }, query: { limit: 100 } },
  });
  if (projectResponse.error) {
    if (projectResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(`/t/${tenant}/jobs`)}`);
    return <ApiErrorNotice error={toApiError(projectResponse.error, projectResponse.response.status)} />;
  }
  const projects: ProjectList["items"] = projectResponse.data.items;

  if (projectFilter) {
    const selected = projects.find((project) => project.slug === projectFilter || project.id === projectFilter);
    if (!selected) return <ApiErrorNotice error={new ApiError(404, "PROJECT_NOT_FOUND", null)} />;
    const response = await api.GET("/tenants/{tenant}/projects/{project}/jobs", {
      params: {
        path: { tenant, project: selected.slug },
        query: { cursor: cursor || undefined, limit: 50, state: state || undefined },
      },
    });
    if (response.error) {
      if (response.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(`/t/${tenant}/jobs`)}`);
      return <ApiErrorNotice error={toApiError(response.error, response.response.status)} />;
    }
    return <JobsTable tenant={tenant} jobs={response.data.items} projects={projects} selectedProject={selected.slug} selectedState={state} nextCursor={response.data.next_cursor ?? null} />;
  }

  const tenantResponse = await api.GET("/tenants/{tenant}/jobs", {
    params: { path: { tenant }, query: { cursor: cursor || undefined, limit: 50, state: state || undefined } },
  });
  if (!tenantResponse.error) {
    return <JobsTable tenant={tenant} jobs={tenantResponse.data.items} projects={projects} selectedProject="" selectedState={state} nextCursor={tenantResponse.data.next_cursor ?? null} />;
  }
  if (tenantResponse.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(`/t/${tenant}/jobs`)}`);
  if (tenantResponse.response.status !== 403) {
    return <ApiErrorNotice error={toApiError(tenantResponse.error, tenantResponse.response.status)} />;
  }

  if (projects.length === 0) return <ApiErrorNotice error={toApiError(tenantResponse.error, tenantResponse.response.status)} />;
  const firstProject = projects[0];
  const fallback = await api.GET("/tenants/{tenant}/projects/{project}/jobs", {
    params: { path: { tenant, project: firstProject.slug }, query: { cursor: cursor || undefined, limit: 50, state: state || undefined } },
  });
  if (fallback.error) {
    if (fallback.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(`/t/${tenant}/jobs`)}`);
    return <ApiErrorNotice error={toApiError(fallback.error, fallback.response.status)} />;
  }
  return <JobsTable tenant={tenant} jobs={fallback.data.items} projects={projects} selectedProject={firstProject.slug} selectedState={state} nextCursor={fallback.data.next_cursor ?? null} />;
}
