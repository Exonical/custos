import "server-only";
import createClient from "openapi-fetch";
import type { components, paths } from "@/lib/api/schema";
import { getConfig } from "@/lib/config";
import { createSafeFetch } from "@/lib/http";

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly requestId: string | null,
  ) {
    super(code);
    this.name = "ApiError";
  }
}

export type Me = components["schemas"]["Me"];
export type TenantClusterList = components["schemas"]["ClusterSummaryList"];
export type TenantCluster = components["schemas"]["ClusterSummary"];
export type PartitionRecord = components["schemas"]["PartitionRecord"];
export type PartitionList = components["schemas"]["PartitionList"];
export type Project = components["schemas"]["Project"];
export type ProjectList = components["schemas"]["ProjectList"];
export type ProjectMembership = components["schemas"]["ProjectMembership"];
export type ProjectMembershipList = components["schemas"]["ProjectMembershipList"];
export type ClusterBinding = components["schemas"]["ClusterBinding"];
export type ClusterBindingList = components["schemas"]["ClusterBindingList"];
export type Allocation = components["schemas"]["Allocation"];
export type AllocationList = components["schemas"]["AllocationList"];
export type SecretConnector = components["schemas"]["SecretConnector"];
export type SecretConnectorList = components["schemas"]["SecretConnectorList"];
export type SecretReference = components["schemas"]["SecretReference"];
export type SecretReferenceList = components["schemas"]["SecretReferenceList"];
export type AccountingUsageList = components["schemas"]["AccountingUsageList"];
export type AccountingUsageRow = components["schemas"]["AccountingUsageRow"];
export type AccountingTopRow = components["schemas"]["AccountingTopRow"];
export type AccountingTopList = components["schemas"]["AccountingTopList"];
export type AccountingAllocationItem = components["schemas"]["AccountingAllocationItem"];
export type AccountingAllocationList = components["schemas"]["AccountingAllocationList"];
export type Job = components["schemas"]["Job"];
export type JobList = components["schemas"]["JobList"];
export type Workflow = components["schemas"]["Workflow"];
export type WorkflowVersion = components["schemas"]["WorkflowVersion"];
export type WorkflowExecution = components["schemas"]["WorkflowExecution"];
export type WorkflowExecutionList = components["schemas"]["WorkflowExecutionList"];
export type TaskExecution = components["schemas"]["TaskExecution"];
export type TaskExecutionList = components["schemas"]["TaskExecutionList"];
export type WorkflowExecuteRequest = components["schemas"]["WorkflowExecuteRequest"];
export type ExecutionSpec = Record<string, unknown>;

export function parseWorkflowVersion(value: unknown): WorkflowVersion | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const candidate = value as Record<string, unknown>;
  return typeof candidate.id === "string"
    && typeof candidate.workflowId === "string"
    && typeof candidate.number === "number"
    && ["draft", "published", "deprecated"].includes(String(candidate.state))
    && typeof candidate.schemaVersion === "string"
    && typeof candidate.specHash === "string"
    && typeof candidate.version === "number"
    ? value as WorkflowVersion
    : null;
}

export function createApiClient(accessToken: string, options: { mockFail?: string } = {}) {
  const config = getConfig();
  const headers = new Headers({ Authorization: `Bearer ${accessToken}` });
  if (
    config.devMode &&
    process.env.NODE_ENV !== "production" &&
    options.mockFail &&
    /^\d{3}$/.test(options.mockFail)
  ) headers.set("X-Mock-Fail", options.mockFail);
  return createClient<paths>({
    baseUrl: new URL("/api/v1", config.apiUrl).toString().replace(/\/$/, ""),
    headers,
    fetch: createSafeFetch(config.apiCaFile, 30_000),
  });
}

export function toApiError(error: unknown, status: number): ApiError {
  const record = error && typeof error === "object" ? (error as Record<string, unknown>) : {};
  const detail = record.error && typeof record.error === "object"
    ? (record.error as Record<string, unknown>)
    : {};
  const code = typeof detail.code === "string" ? detail.code : "UPSTREAM_ERROR";
  const requestId = typeof detail.request_id === "string" ? detail.request_id : null;
  return new ApiError(status, code, requestId);
}

export async function getMe(accessToken: string): Promise<Me> {
  const { data, error, response } = await createApiClient(accessToken).GET("/me", {});
  if (error) throw toApiError(error, response.status);
  return data;
}
