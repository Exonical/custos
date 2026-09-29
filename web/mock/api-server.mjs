// @ts-check
import { createHash, randomUUID } from "node:crypto";
import http from "node:http";
import { createMockData, userNameFromAccessToken } from "./data.mjs";
import { loadWorkflowTemplates, templateSummary } from "./templates.mjs";

/** @typedef {import("../lib/api/schema").components["schemas"]["Me"]} Me */
/** @typedef {import("../lib/api/schema").components["schemas"]["ProjectList"]} ProjectList */
/** @typedef {import("../lib/api/schema").components["schemas"]["AllocationList"]} AllocationList */
/** @typedef {import("../lib/api/schema").components["schemas"]["SecretConnectorList"]} SecretConnectorList */
/** @typedef {import("../lib/api/schema").components["schemas"]["SecretReferenceList"]} SecretReferenceList */
/** @typedef {import("../lib/api/schema").components["schemas"]["AccountingTopList"]} AccountingTopList */
/** @typedef {import("../lib/api/schema").components["schemas"]["AccountingAllocationList"]} AccountingAllocationList */
/** @typedef {import("../lib/api/schema").components["schemas"]["ProjectMembershipList"]} ProjectMembershipList */
/** @typedef {import("../lib/api/schema").components["schemas"]["ClusterBindingList"]} ClusterBindingList */
/** @typedef {import("../lib/api/schema").components["schemas"]["ClusterSummaryList"]} ClusterSummaryList */
/** @typedef {import("../lib/api/schema").components["schemas"]["PartitionList"]} PartitionList */
/** @typedef {import("../lib/api/schema").components["schemas"]["AccountingUsageList"]} AccountingUsageList */
/** @typedef {import("../lib/api/schema").components["schemas"]["AccountingUsageRow"]} AccountingUsageRow */
/** @typedef {import("../lib/api/schema").components["schemas"]["Job"]} Job */
/** @typedef {import("../lib/api/schema").components["schemas"]["JobList"]} JobList */
/** @typedef {import("../lib/api/schema").components["schemas"]["Workflow"]} Workflow */
/** @typedef {import("../lib/api/schema").components["schemas"]["WorkflowVersion"]} WorkflowVersion */
/** @typedef {import("../lib/api/schema").components["schemas"]["WorkflowExecution"]} WorkflowExecution */
/** @typedef {Omit<WorkflowExecution, "parameters"> & {parameters: Record<string, unknown>}} WorkflowExecutionFixture */
/** @typedef {import("../lib/api/schema").components["schemas"]["WorkflowExecuteRequest"]} WorkflowExecuteRequest */
/** @typedef {import("../lib/api/schema").components["schemas"]["TaskExecution"]} TaskExecution */
/** @typedef {import("../lib/api/schema").components["schemas"]["TaskExecutionList"]} TaskExecutionList */
/** @typedef {import("../lib/api/schema").components["schemas"]["HealthStatus"]} HealthStatus */
/** @typedef {import("../lib/api/schema").components["schemas"]["Error"]} ApiErrorBody */
/** @typedef {import("../lib/api/schema").operations["getJobExecutionSpec"]["responses"][200]["content"]["application/json"]} ExecutionSpec */
/** @typedef {import("../lib/api/schema").components["schemas"]["WorkflowTemplateList"]} WorkflowTemplateList */
/** @typedef {"acme" | "globex"} TenantSlug */
/** @typedef {{ tenant: string, project: string, state: string, cluster: string, created_at: string, id: string }} JobCursor */

const port = Number(process.env.MOCK_API_PORT ?? 4301);
const issuer = process.env.MOCK_OIDC_ISSUER ?? "http://127.0.0.1:4300/realms/custos";
const latencyMs = Math.max(0, Math.min(30_000, Number(process.env.MOCK_API_LATENCY_MS ?? 0) || 0));
const data = createMockData(Date.now(), issuer, Number(process.env.MOCK_DATA_SEED ?? 20260927));
const workflowTemplates = loadWorkflowTemplates();

/** @param {http.ServerResponse} response @param {number} status @param {unknown} body */
function send(response, status, body) {
  response.writeHead(status, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" });
  response.end(JSON.stringify(body));
}

/** @param {http.ServerResponse} response @param {number} status @param {string} code @param {string} message @param {string} requestId */
function sendError(response, status, code, message, requestId) {
  /** @type {ApiErrorBody} */
  const body = { error: { code, message, request_id: requestId } };
  send(response, status, body);
}

/** @param {http.ServerResponse} response @param {number} status */
function sendEmpty(response, status) {
  response.writeHead(status, { "cache-control": "no-store" });
  response.end();
}

/** @param {http.IncomingMessage} request @returns {Promise<{raw: Buffer, value: unknown}>} */
async function readJsonBody(request) {
  const chunks = [];
  let length = 0;
  for await (const chunk of request) {
    const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    length += bytes.byteLength;
    if (length > 1_048_576) throw new RangeError("body too large");
    chunks.push(bytes);
  }
  const raw = Buffer.concat(chunks);
  return { raw, value: raw.length ? JSON.parse(raw.toString("utf8")) : null };
}

/** @param {unknown} value @returns {Record<string, unknown> | undefined} */
function objectRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? /** @type {Record<string, unknown>} */ (value) : undefined;
}

/** @param {http.IncomingMessage} request */
function requestId(request) {
  const value = request.headers["x-request-id"];
  return typeof value === "string" && /^[A-Za-z0-9._:-]{1,128}$/.test(value) ? value : randomUUID();
}

/** @param {string | null} value */
function parseFailureStatus(value) {
  if (!value || !/^\d{3}$/.test(value)) return undefined;
  const status = Number(value);
  return status >= 400 && status <= 599 ? status : undefined;
}

/** @param {string} value @returns {value is TenantSlug} */
function isTenantSlug(value) {
  return value === "acme" || value === "globex";
}

/** @param {string | null} cursor */
function parseCursor(cursor) {
  if (!cursor) return undefined;
  try {
    const value = JSON.parse(Buffer.from(cursor, "base64url").toString("utf8"));
    if (
      value && typeof value === "object" &&
      typeof value.tenant === "string" &&
      typeof value.project === "string" &&
      typeof value.state === "string" &&
      typeof value.cluster === "string" &&
      typeof value.created_at === "string" &&
      typeof value.id === "string"
    ) return /** @type {JobCursor} */ (value);
  } catch {
    return undefined;
  }
  return undefined;
}

/** @param {JobCursor} value */
function encodeCursor(value) {
  return Buffer.from(JSON.stringify(value)).toString("base64url");
}

/** @param {URLSearchParams} query */
function queryFilters(query) {
  const states = (query.get("state") ?? "").split(",").map((state) => state.trim()).filter(Boolean);
  return { states, cluster: query.get("cluster") ?? "" };
}

/** @param {Job[]} source @param {TenantSlug} tenant @param {string} project @param {URLSearchParams} query @param {string} requestIdValue @param {http.ServerResponse} response */
function sendJobPage(source, tenant, project, query, requestIdValue, response) {
  const { states, cluster } = queryFilters(query);
  const cursorText = query.get("cursor");
  const cursor = parseCursor(cursorText);
  if (cursorText && (!cursor || cursor.tenant !== tenant || cursor.project !== project || cursor.state !== states.join(",") || cursor.cluster !== cluster)) {
    sendError(response, 400, "INVALID_CURSOR", "The job cursor is invalid", requestIdValue);
    return;
  }

  const limitValue = Number(query.get("limit") ?? 50);
  const limit = Number.isInteger(limitValue) && limitValue > 0 ? Math.min(limitValue, 100) : 50;
  let jobs = source.filter((job) => (!states.length || states.includes(job.state)) && (!cluster || job.cluster_id === cluster));
  if (cursor) {
    jobs = jobs.filter((job) => job.created_at < cursor.created_at || (job.created_at === cursor.created_at && job.id < cursor.id));
  }
  const items = jobs.slice(0, limit);
  const last = items.at(-1);
  const nextCursor = last && jobs.length > items.length
    ? encodeCursor({ tenant, project, state: states.join(","), cluster, created_at: last.created_at, id: last.id })
    : null;
  /** @type {JobList} */
  const page = { items, next_cursor: nextCursor };
  send(response, 200, page);
}

/** @template T @param {T[]} source @param {URLSearchParams} query @param {string} requestIdValue @param {http.ServerResponse} response @returns {{items:T[],next_cursor:string|null}|undefined} */
function cursorPage(source, query, requestIdValue, response) {
  const cursorText = query.get("cursor");
  let offset = 0;
  if (cursorText) {
    try {
      const decoded = Buffer.from(cursorText, "base64url").toString("utf8");
      if (!/^\\d+$/.test(decoded)) throw new Error("cursor");
      offset = Number(decoded);
    } catch {
      sendError(response, 400, "INVALID_CURSOR", "The list cursor is invalid", requestIdValue);
      return undefined;
    }
  }
  const requestedLimit = Number(query.get("limit") ?? 50);
  const limit = Number.isInteger(requestedLimit) && requestedLimit > 0 ? Math.min(requestedLimit, 200) : 50;
  const items = source.slice(offset, offset + limit);
  const nextOffset = offset + items.length;
  return { items, next_cursor: nextOffset < source.length ? Buffer.from(String(nextOffset)).toString("base64url") : null };
}

/** @param {number[]} values @param {number} quantile */
function percentile(values, quantile) {
  if (values.length === 0) return null;
  const sorted = [...values].sort((left, right) => left - right);
  const index = (sorted.length - 1) * quantile;
  const low = Math.floor(index);
  const high = Math.ceil(index);
  return sorted[low] + (sorted[high] - sorted[low]) * (index - low);
}

/** @param {ReturnType<typeof createMockData>["usageRecords"][TenantSlug]} source @param {string} groupBy @returns {AccountingUsageRow[]} */
function groupUsageRows(source, groupBy) {
  /** @type {Map<string, { jobs:number, failed:number, cpu_seconds:number, gpu_seconds:number, node_seconds:number, mem_gb_seconds:number, wait_seconds:number[], run_seconds:number[] }>} */
  const groups = new Map();
  for (const record of source) {
    const key = groupBy === "user" ? record.user_id
      : groupBy === "project" ? record.project_id
        : groupBy === "cluster" ? record.cluster_id
          : groupBy === "account" ? record.account
            : groupBy === "partition" ? record.partition
              : record.day;
    let group = groups.get(key);
    if (!group) {
      group = { jobs: 0, failed: 0, cpu_seconds: 0, gpu_seconds: 0, node_seconds: 0, mem_gb_seconds: 0, wait_seconds: [], run_seconds: [] };
      groups.set(key, group);
    }
    group.jobs += record.jobs;
    group.failed += record.failed;
    group.cpu_seconds += record.cpu_seconds;
    group.gpu_seconds += record.gpu_seconds;
    group.node_seconds += record.node_seconds;
    group.mem_gb_seconds += record.mem_gb_seconds;
    group.wait_seconds.push(...record.wait_seconds);
    group.run_seconds.push(...record.run_seconds);
  }
  return [...groups.entries()].sort(([left], [right]) => left.localeCompare(right)).map(([key, group]) => ({
    key,
    jobs: group.jobs,
    failed: group.failed,
    cpu_hours: group.cpu_seconds / 3600,
    gpu_hours: group.gpu_seconds / 3600,
    node_hours: group.node_seconds / 3600,
    mem_gb_hours: group.mem_gb_seconds / 3600,
    wait_hours: group.wait_seconds.reduce((sum, value) => sum + value, 0) / 3600,
    run_hours: group.run_seconds.reduce((sum, value) => sum + value, 0) / 3600,
    wait_p50: percentile(group.wait_seconds, 0.5),
    wait_p90: percentile(group.wait_seconds, 0.9),
    wait_p99: percentile(group.wait_seconds, 0.99),
    run_p50: percentile(group.run_seconds, 0.5),
    run_p90: percentile(group.run_seconds, 0.9),
    run_p99: percentile(group.run_seconds, 0.99),
  }));
}

const server = http.createServer(async (request, response) => {
  if (latencyMs) await new Promise((resolve) => setTimeout(resolve, latencyMs));
  const url = new URL(request.url ?? "/", `http://127.0.0.1:${port}`);
  const id = requestId(request);
  if (url.pathname === "/health") {
    /** @type {HealthStatus} */
    const health = { status: "ok" };
    return send(response, 200, health);
  }

  const authorization = request.headers.authorization;
  const bearer = typeof authorization === "string" ? authorization.match(/^Bearer (.+)$/)?.[1] : undefined;
  const userName = userNameFromAccessToken(bearer);
  const user = userName ? data.users[userName] : undefined;
  if (!user) return sendError(response, 401, "UNAUTHENTICATED", "Authentication required", id);

  const failHeader = request.headers["x-mock-fail"];
  const failHeaderValue = typeof failHeader === "string" ? failHeader : null;
  const forcedStatus = parseFailureStatus(url.searchParams.get("mock_fail") ?? failHeaderValue);
  if (forcedStatus) return sendError(response, forcedStatus, "MOCK_FAILURE", "Mock failure requested", id);

  if (url.pathname === "/api/v1/me" && request.method === "GET") {
    /** @type {Me} */
    const me = user.me;
    return send(response, 200, me);
  }
  if (url.pathname === "/api/v1/workflow-templates" && request.method === "GET") {
    /** @type {WorkflowTemplateList} */
    const templateList = { items: workflowTemplates.map(templateSummary) };
    return send(response, 200, templateList);
  }
  const templateDetail = url.pathname.match(/^\/api\/v1\/workflow-templates\/([^/]+)$/);
  if (templateDetail && request.method === "GET") {
    const template = workflowTemplates.find((item) => item.id === templateDetail[1]);
    return template ? send(response, 200, template) : sendError(response, 404, "NOT_FOUND", "workflow template not found", id);
  }

  const tenantProjects = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects$/);
  const projectDetail = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects\/([^/]+)$/);
  const projectMembers = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects\/([^/]+)\/members$/);
  const projectBindings = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects\/([^/]+)\/cluster-bindings$/);
  const projectAllocations = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects\/([^/]+)\/allocations$/);
  const tenantWorkflows = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows$/);
  const workflowVersion = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows\/([^/]+)\/versions\/([^/]+)$/);
  const workflowVersions = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows\/([^/]+)\/versions$/);
  const workflowDetail = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows\/([^/]+)$/);
  const tenantWorkflowExecutions = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflow-executions$/);
  const workflowExecutionTaskSpec = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflow-executions\/([^/]+)\/tasks\/([^/]+)\/execution-spec$/);
  const workflowExecutionTasks = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflow-executions\/([^/]+)\/tasks$/);
  const workflowExecutionCancel = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflow-executions\/([^/]+)\/cancel$/);
  const workflowExecutionDetail = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflow-executions\/([^/]+)$/);
  const tenantClusters = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/clusters$/);
  const clusterDetail = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/clusters\/([^/]+)$/);
  const clusterPartitions = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/clusters\/([^/]+)\/partitions$/);
  const tenantJobs = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/jobs$/);
  const projectJobs = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects\/([^/]+)\/jobs(?:\/([^/]+)(?:\/(execution-spec|cancel))?)?$/);
  const secretConnectors = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/secret-connectors$/);
  const secretReferences = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/secret-references$/);
  const accountingUsage = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/accounting\/usage$/);
  const accountingTop = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/accounting\/top$/);
  const accountingAllocations = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/accounting\/allocations$/);
  const tenantPath = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)(?:\/|$)/)?.[1];
  const tenant = tenantPath && isTenantSlug(tenantPath) ? tenantPath : undefined;
  if (tenantPath && (!tenant || !user.me.memberships.some((membership) => membership.slug === tenant))) {
    return sendError(response, 404, "NOT_FOUND", "Tenant not found", id);
  }

  if (tenantWorkflows && request.method === "POST" && tenant) {
    let input;
    try {
      input = objectRecord((await readJsonBody(request)).value);
    } catch {
      return sendError(response, 400, "MALFORMED", "invalid JSON body", id);
    }
    const project = data.projects[tenant].find((item) => item.id === input?.project);
    const name = typeof input?.name === "string" ? input.name.trim() : "";
    if (!project || !name || name.length > 128) {
      return sendError(response, 422, "VALIDATION", "project and name are required", id);
    }
    if (data.workflows[tenant].some((item) => item.projectId === project.id && item.name === name && item.state === "active")) {
      return sendError(response, 409, "WORKFLOW_NAME_TAKEN", "A workflow with this name already exists in the project", id);
    }
    const tenantId = user.me.memberships.find((membership) => membership.slug === tenant)?.tenant_id ?? "";
    const now = new Date().toISOString();
    /** @type {Workflow} */
    const workflow = {
      id: randomUUID(), tenantId, projectId: project.id, name,
      description: typeof input?.description === "string" ? input.description : "",
      state: "active", latestPublishedVersionId: null, version: 1, createdAt: now, updatedAt: now,
    };
    data.workflows[tenant].unshift(workflow);
    data.workflowVersions[workflow.id] = [];
    return send(response, 201, workflow);
  }
  if (workflowVersions && request.method === "POST" && tenant) {
    const workflowId = workflowVersions[2];
    if (!data.workflows[tenant].some((item) => item.id === workflowId)) return sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
    if (!String(request.headers["content-type"] ?? "").startsWith("application/json")) {
      return sendError(response, 415, "UNSUPPORTED_MEDIA_TYPE", "mock accepts application/json documents", id);
    }
    let raw;
    let spec;
    try {
      ({ raw, value: spec } = await readJsonBody(request));
    } catch {
      return sendError(response, 400, "MALFORMED", "invalid JSON body", id);
    }
    const document = objectRecord(spec);
    const tasks = objectRecord(document?.spec)?.tasks;
    const details = [];
    if (document?.apiVersion !== "custos.io/v1alpha1") details.push({ field: "apiVersion", reason: "apiVersion must be custos.io/v1alpha1" });
    if (document?.kind !== "Workflow") details.push({ field: "kind", reason: "kind must be Workflow" });
    if (!Array.isArray(tasks) || tasks.length === 0) details.push({ field: "spec.tasks", reason: "at least one task is required" });
    if (details.length) {
      /** @type {ApiErrorBody} */
      const body = { error: { code: "SPEC_INVALID", message: "Workflow spec is invalid", request_id: id, details } };
      return send(response, 422, body);
    }
    const versions = data.workflowVersions[workflowId] ?? [];
    const now = new Date().toISOString();
    const created = {
      id: randomUUID(), workflowId, number: versions.reduce((max, item) => Math.max(max, item.number), 0) + 1,
      state: /** @type {const} */ ("draft"), schemaVersion: "custos.io/v1alpha1",
      specHash: `sha256:${createHash("sha256").update(raw).digest("hex")}`,
      layout: {}, spec: /** @type {import("../lib/workflow/spec").CustosWorkflow} */ (/** @type {unknown} */ (document)),
      version: 1, createdAt: now, publishedAt: null,
    };
    data.workflowVersions[workflowId] = [created, ...versions];
    return send(response, 201, created);
  }
  if (tenantWorkflows && request.method === "GET" && tenant) {
    const project = url.searchParams.get("project");
    if (project && !data.projects[tenant].some((item) => item.id === project)) {
      return sendError(response, 400, "MALFORMED", "invalid project", id);
    }
    const workflows = data.workflows[tenant].filter((item) => !project || item.projectId === project);
    return send(response, 200, { workflows });
  }
  if (workflowDetail && request.method === "GET" && tenant) {
    const workflow = data.workflows[tenant].find((item) => item.id === workflowDetail[2]);
    return workflow ? send(response, 200, workflow) : sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
  }
  if (workflowVersions && request.method === "GET" && tenant) {
    const workflowId = workflowVersions[2];
    if (!data.workflows[tenant].some((item) => item.id === workflowId)) return sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
    const versions = (data.workflowVersions[workflowId] ?? []).map((version) => ({
      id: version.id,
      workflowId: version.workflowId,
      number: version.number,
      state: version.state,
      schemaVersion: version.schemaVersion,
      specHash: version.specHash,
      layout: version.layout,
      version: version.version,
      createdAt: version.createdAt,
      publishedAt: version.publishedAt,
    }));
    return send(response, 200, { versions });
  }
  if (workflowVersion && request.method === "GET" && tenant) {
    const workflowId = workflowVersion[2];
    const versionId = workflowVersion[3];
    if (!data.workflows[tenant].some((item) => item.id === workflowId)) return sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
    const version = (data.workflowVersions[workflowId] ?? []).find((item) => item.id === versionId);
    return version ? send(response, 200, version) : sendError(response, 404, "VERSION_UNKNOWN", "Workflow version not found", id);
  }

  if (tenantProjects && request.method === "GET" && tenant) {
    const page = cursorPage(data.projects[tenant], url.searchParams, id, response);
    if (!page) return;
    /** @type {ProjectList} */
    const projectPage = page;
    return send(response, 200, projectPage);
  }
  if (projectDetail && request.method === "GET" && tenant) {
    const projectRef = projectDetail[2];
    const project = data.projects[tenant].find((item) => item.slug === projectRef || item.id === projectRef);
    return project ? send(response, 200, project) : sendError(response, 404, "NOT_FOUND", "Project not found", id);
  }
  if (projectMembers && request.method === "GET" && tenant) {
    const projectRef = projectMembers[2];
    const project = data.projects[tenant].find((item) => item.slug === projectRef || item.id === projectRef);
    if (!project) return sendError(response, 404, "NOT_FOUND", "Project not found", id);
    const page = cursorPage(data.projectMembers[project.id] ?? [], url.searchParams, id, response);
    if (!page) return;
    /** @type {ProjectMembershipList} */
    const memberPage = page;
    return send(response, 200, memberPage);
  }
  if (projectBindings && request.method === "GET" && tenant) {
    const projectRef = projectBindings[2];
    const project = data.projects[tenant].find((item) => item.slug === projectRef || item.id === projectRef);
    if (!project) return sendError(response, 404, "NOT_FOUND", "Project not found", id);
    /** @type {ClusterBindingList} */
    const bindingList = { items: data.clusterBindings[tenant].filter((binding) => binding.project_id === project.id) };
    return send(response, 200, bindingList);
  }
  if (projectAllocations && request.method === "GET" && tenant) {
    const projectRef = projectAllocations[2];
    const project = data.projects[tenant].find((item) => item.slug === projectRef || item.id === projectRef);
    if (!project) return sendError(response, 404, "NOT_FOUND", "Project not found", id);
    /** @type {AllocationList} */
    const allocationList = { items: data.projectAllocations[project.id] ?? [] };
    return send(response, 200, allocationList);
  }
  if (tenantClusters && request.method === "GET" && tenant) {
    /** @type {ClusterSummaryList} */
    const clusters = { items: data.clusters[tenant] };
    return send(response, 200, clusters);
  }
  if (clusterDetail && request.method === "GET" && tenant) {
    const clusterRef = clusterDetail[2];
    const cluster = data.clusters[tenant].find((item) => item.name === clusterRef || item.id === clusterRef);
    return cluster ? send(response, 200, cluster) : sendError(response, 404, "NOT_FOUND", "Cluster not found", id);
  }
  if (clusterPartitions && request.method === "GET" && tenant) {
    const clusterRef = clusterPartitions[2];
    const cluster = data.clusters[tenant].find((item) => item.name === clusterRef || item.id === clusterRef);
    if (!cluster) return sendError(response, 404, "NOT_FOUND", "Cluster not found", id);
    /** @type {PartitionList} */
    const partitionList = { items: data.partitions[cluster.id] ?? [] };
    return send(response, 200, partitionList);
  }
  if (secretConnectors && request.method === "GET" && tenant) {
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const canReadConnectors = user.me.platform_roles.includes("platform-admin") || membership?.roles.includes("tenant-admin");
    if (!canReadConnectors) return sendError(response, 403, "FORBIDDEN", "Requires secret.connector.read", id);
    /** @type {SecretConnectorList} */
    const connectorList = { items: data.connectors[tenant] };
    return send(response, 200, connectorList);
  }
  if (secretReferences && request.method === "GET" && tenant) {
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const isManager = user.me.platform_roles.includes("platform-admin") || membership?.roles.includes("tenant-admin");
    const references = isManager ? data.references[tenant] : data.references[tenant].filter((item) => item.owner_id === user.me.user_id);
    /** @type {SecretReferenceList} */
    const referenceList = { items: references };
    return send(response, 200, referenceList);
  }
  if (accountingAllocations && request.method === "GET" && tenant) {
    /** @type {AccountingAllocationList} */
    const allocationList = { items: data.tenantAllocations[tenant] };
    return send(response, 200, allocationList);
  }
  if (accountingTop && request.method === "GET" && tenant) {
    const metric = url.searchParams.get("metric");
    const by = url.searchParams.get("by");
    if (!metric || !["cpu_seconds", "gpu_seconds", "jobs"].includes(metric) || !by || !["user", "project"].includes(by)) {
      return sendError(response, 400, "ACCOUNTING_TOP_INVALID", "Invalid top query", id);
    }
    const fromText = url.searchParams.get("from");
    const toText = url.searchParams.get("to");
    const fromTime = fromText ? Date.parse(fromText) : Number.NaN;
    const toTime = toText ? Date.parse(toText) : Number.NaN;
    if (!Number.isFinite(fromTime) || !Number.isFinite(toTime) || toTime <= fromTime) {
      return sendError(response, 400, "ACCOUNTING_RANGE_INVALID", "from and to are required", id);
    }
    if (toTime - fromTime > 400 * 86_400_000) {
      return sendError(response, 400, "ACCOUNTING_RANGE_TOO_LARGE", "Accounting range must not exceed 400 days", id);
    }
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const tenantWide = user.me.platform_roles.includes("platform-admin") || membership?.roles.includes("tenant-admin") === true;
    const records = data.usageRecords[tenant].filter((record) => {
      const day = Date.parse(`${record.day}T00:00:00.000Z`);
      return day >= fromTime && day < toTime && (tenantWide || record.user_id === user.me.user_id);
    });
    const rows = groupUsageRows(records, by).map((row) => ({
      key: row.key,
      value: metric === "jobs" ? row.jobs : Math.trunc((metric === "cpu_seconds" ? row.cpu_hours : row.gpu_hours) * 3_600),
    }));
    rows.sort((left, right) => right.value - left.value || left.key.localeCompare(right.key));
    const requestedLimit = Number(url.searchParams.get("limit") ?? 50);
    const limit = Number.isInteger(requestedLimit) && requestedLimit > 0 ? Math.min(requestedLimit, 50) : 50;
    /** @type {AccountingTopList} */
    const topList = { items: rows.slice(0, limit) };
    return send(response, 200, topList);
  }
  if (tenantWorkflowExecutions && tenant && request.method === "POST") {
    const keyHeader = request.headers["idempotency-key"];
    const idempotencyKey = typeof keyHeader === "string" ? keyHeader : "";
    if (!idempotencyKey || idempotencyKey.length > 128) return sendError(response, 400, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required", id);
    let rawBody;
    let input;
    try {
      ({ raw: rawBody, value: input } = await readJsonBody(request));
    } catch (error) {
      return sendError(response, error instanceof RangeError ? 413 : 400, error instanceof RangeError ? "BODY_TOO_LARGE" : "MALFORMED", "Malformed workflow execution request", id);
    }
    const requestBody = objectRecord(input);
    if (!requestBody || Object.keys(requestBody).some((key) => !["workflow", "version", "parameters"].includes(key))) {
      return sendError(response, 400, "MALFORMED", "Malformed workflow execution request", id);
    }
    if (typeof requestBody.workflow !== "string" || !/^[0-9a-f-]{36}$/i.test(requestBody.workflow)
      || (requestBody.version !== undefined && (typeof requestBody.version !== "string" || !/^[0-9a-f-]{36}$/i.test(requestBody.version)))) {
      return sendError(response, 400, "MALFORMED", "Workflow and version must be UUIDs", id);
    }
    const parameters = requestBody.parameters === undefined ? {} : objectRecord(requestBody.parameters);
    if (!parameters) return sendError(response, 400, "MALFORMED", "parameters must be an object", id);
    const workflow = data.workflows[tenant].find((item) => item.id === requestBody.workflow);
    if (!workflow) return sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const tenantRoles = membership?.roles ?? [];
    const projectRoles = (user.me.project_memberships ?? []).filter((item) => item.project_id === workflow.projectId).flatMap((item) => item.roles);
    const canExecute = user.me.platform_roles.includes("platform-admin")
      || tenantRoles.some((role) => ["tenant-admin", "workflow-author", "researcher"].includes(role))
      || projectRoles.some((role) => ["project-admin", "project-member"].includes(role));
    if (!canExecute) return sendError(response, 403, "FORBIDDEN", "Requires workflow.execute", id);
    const requestedVersion = typeof requestBody.version === "string" ? requestBody.version : workflow.latestPublishedVersionId;
    if (!requestedVersion) return sendError(response, 409, "NO_PUBLISHED_VERSION", "Workflow has no published version", id);
    const version = (data.workflowVersions[workflow.id] ?? []).find((item) => item.id === requestedVersion);
    if (!version) return sendError(response, 404, "VERSION_UNKNOWN", "Workflow version not found", id);
    if (version.state !== "published") return sendError(response, 409, "VERSION_STATE", "Only published versions can be executed", id);
    const key = `${tenant}:${idempotencyKey}`;
    const bodyHash = createHash("sha256").update(rawBody).digest("hex");
    const previous = data.idempotency.get(key);
    if (previous) {
      if (previous.bodyHash !== bodyHash) return sendError(response, 409, "IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was reused with a different request", id);
      return send(response, 202, previous.execution);
    }
    const now = new Date().toISOString();
    /** @type {WorkflowExecutionFixture} */
    const execution = {
      id: randomUUID(),
      tenantId: workflow.tenantId,
      projectId: workflow.projectId,
      workflowId: workflow.id,
      workflowVersionId: version.id,
      specHash: version.specHash,
      parameters,
      strategy: version.spec.spec.execution?.strategy ?? "auto",
      state: "PENDING",
      stateReason: "",
      requestedBy: user.me.user_id,
      createdAt: now,
      startedAt: null,
      endedAt: null,
      updatedAt: now,
      version: 1,
    };
    /** @type {TaskExecution[]} */
    const rows = [];
    for (const task of version.spec.spec.tasks) {
      let count = 1;
      if (task.fanOut?.count !== undefined) {
        const rawCount = typeof task.fanOut.count === "number" ? task.fanOut.count : Number(task.fanOut.count.match(/^\d+$/)?.[0] ?? parameters.shards ?? 1);
        count = Number.isFinite(rawCount) && rawCount > 0 ? Math.min(100, Math.floor(rawCount)) : 1;
      } else if (task.array) {
        const step = task.array.step && task.array.step > 0 ? task.array.step : 1;
        count = Math.min(100, Math.max(1, Math.floor((task.array.end - task.array.start) / step) + 1));
      }
      for (let index = 0; index < count; index += 1) {
        rows.push({
          id: randomUUID(), executionId: execution.id, taskName: task.name, index, count, attempt: 1,
          state: "PENDING", stateReason: "", jobId: null, validationId: null,
          createdAt: now, updatedAt: now, version: 1,
        });
      }
    }
    data.workflowExecutions[tenant].unshift(execution);
    data.taskExecutions[execution.id] = rows;
    data.idempotency.set(key, { bodyHash, execution });
    return send(response, 202, execution);
  }

  if (tenantWorkflowExecutions && tenant && request.method === "GET") {
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const roles = membership?.roles ?? [];
    const canReadTenant = user.me.platform_roles.includes("platform-admin") || roles.some((role) => ["tenant-admin", "tenant-operator", "auditor"].includes(role));
    const canReadSelf = canReadTenant || roles.some((role) => ["workflow-author", "researcher", "viewer"].includes(role));
    if (!canReadSelf) return sendError(response, 403, "FORBIDDEN", "Requires execution.read.self", id);
    const workflowFilter = url.searchParams.get("workflow") ?? "";
    if (workflowFilter && !/^[0-9a-f-]{36}$/i.test(workflowFilter)) return sendError(response, 400, "MALFORMED", "workflow must be a uuid", id);
    const stateFilter = url.searchParams.get("state") ?? "";
    const source = data.workflowExecutions[tenant].filter((execution) =>
      (canReadTenant || execution.requestedBy === user.me.user_id)
      && (!workflowFilter || execution.workflowId === workflowFilter)
      && (!stateFilter || execution.state === stateFilter));
    const page = cursorPage(source, url.searchParams, id, response);
    if (!page) return;
    /** @type {{items: WorkflowExecutionFixture[], next_cursor: string | null}} */
    const executionPage = page;
    return send(response, 200, executionPage);
  }

  if (workflowExecutionCancel && tenant && request.method === "POST") {
    const execution = data.workflowExecutions[tenant].find((item) => item.id === workflowExecutionCancel[2]);
    if (!execution) return sendError(response, 404, "EXECUTION_UNKNOWN", "Execution not found", id);
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const canCancel = user.me.platform_roles.includes("platform-admin")
      || execution.requestedBy === user.me.user_id
      || membership?.roles.some((role) => ["tenant-admin", "tenant-operator"].includes(role));
    if (!canCancel) return sendError(response, 403, "FORBIDDEN", "Requires execution.cancel.self or execution.cancel.any", id);
    if (["SUCCEEDED", "FAILED", "PARTIAL_FAILURE", "CANCELED"].includes(execution.state)) {
      return sendError(response, 409, "EXECUTION_TERMINAL", "Execution is already terminal", id);
    }
    const now = new Date().toISOString();
    execution.state = "CANCELED";
    execution.stateReason = "Canceled by requester.";
    execution.endedAt = now;
    execution.updatedAt = now;
    execution.version += 1;
    for (const task of data.taskExecutions[execution.id] ?? []) {
      if (!["COMPLETED", "FAILED", "SKIPPED", "CANCELED"].includes(task.state)) {
        task.state = "CANCELED";
        task.stateReason = "Canceled with workflow execution.";
        task.updatedAt = now;
        task.version += 1;
      }
    }
    return sendEmpty(response, 202);
  }

  if (workflowExecutionTaskSpec && tenant && request.method === "GET") {
    const execution = data.workflowExecutions[tenant].find((item) => item.id === workflowExecutionTaskSpec[2]);
    if (!execution) return sendError(response, 404, "EXECUTION_UNKNOWN", "Execution not found", id);
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const canRead = user.me.platform_roles.includes("platform-admin") || execution.requestedBy === user.me.user_id
      || membership?.roles.some((role) => ["tenant-admin", "tenant-operator", "auditor"].includes(role));
    if (!canRead) return sendError(response, 403, "FORBIDDEN", "Requires execution.read.*", id);
    const taskId = workflowExecutionTaskSpec[3];
    const task = (data.taskExecutions[execution.id] ?? []).find((item) => item.id === taskId);
    if (!task) return sendError(response, 404, "TASK_EXECUTION_UNKNOWN", "Task execution not found", id);
    const spec = data.frozenTaskSpecs.get(taskId);
    return spec ? send(response, 200, spec) : sendError(response, 404, "EXECUTION_SPEC_NOT_FROZEN", "ExecutionSpec is not frozen yet", id);
  }

  if (workflowExecutionTasks && tenant && request.method === "GET") {
    const execution = data.workflowExecutions[tenant].find((item) => item.id === workflowExecutionTasks[2]);
    if (!execution) return sendError(response, 404, "EXECUTION_UNKNOWN", "Execution not found", id);
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const canRead = user.me.platform_roles.includes("platform-admin") || execution.requestedBy === user.me.user_id
      || membership?.roles.some((role) => ["tenant-admin", "tenant-operator", "auditor"].includes(role));
    if (!canRead) return sendError(response, 403, "FORBIDDEN", "Requires execution.read.*", id);
    /** @type {TaskExecutionList} */
    const taskList = { tasks: data.taskExecutions[execution.id] ?? [] };
    return send(response, 200, taskList);
  }

  if (workflowExecutionDetail && tenant && request.method === "GET") {
    const execution = data.workflowExecutions[tenant].find((item) => item.id === workflowExecutionDetail[2]);
    if (!execution) return sendError(response, 404, "EXECUTION_UNKNOWN", "Execution not found", id);
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const canRead = user.me.platform_roles.includes("platform-admin") || execution.requestedBy === user.me.user_id
      || membership?.roles.some((role) => ["tenant-admin", "tenant-operator", "auditor"].includes(role));
    if (!canRead) return sendError(response, 403, "FORBIDDEN", "Requires execution.read.*", id);
    return send(response, 200, execution);
  }

  if (tenantJobs && request.method === "GET" && tenant) {
    return sendJobPage(data.jobs[tenant], tenant, "", url.searchParams, id, response);
  }
  if (projectJobs && tenant) {
    const projectRef = projectJobs[2];
    const project = data.projects[tenant].find((candidate) => candidate.slug === projectRef || candidate.id === projectRef);
    if (!project) return sendError(response, 404, "NOT_FOUND", "Project not found", id);

    const jobId = projectJobs[3];
    const operation = projectJobs[4];
    const jobsForProject = data.jobs[tenant].filter((job) => job.project_id === project.id);
    if (!jobId && !operation && request.method === "GET") {
      return sendJobPage(jobsForProject, tenant, project.slug, url.searchParams, id, response);
    }
    const job = jobsForProject.find((candidate) => candidate.id === jobId);
    if (!job) return sendError(response, 404, "NOT_FOUND", "Job not found", id);
    if (operation === "execution-spec" && request.method === "GET") {
      /** @type {ExecutionSpec} */
      const spec = data.executionSpecs.get(job.id) ?? {};
      return send(response, 200, spec);
    }
    if (operation === "cancel" && request.method === "POST") {
      if (["COMPLETED", "FAILED", "CANCELED"].includes(job.state)) {
        return sendError(response, 409, "JOB_TERMINAL", "Job is already terminal", id);
      }
      const endedAt = new Date().toISOString();
      /** @type {Job} */
      const canceled = { ...job, state: "CANCELED", state_reason: "Canceled by user", ended_at: endedAt, updated_at: endedAt, version: job.version + 1 };
      const jobs = data.jobs[tenant];
      jobs[jobs.findIndex((candidate) => candidate.id === job.id)] = canceled;
      return send(response, 202, canceled);
    }
    if (!operation && request.method === "GET") return send(response, 200, job);
  }

  if (accountingUsage && request.method === "GET" && tenant) {
    const groupBy = url.searchParams.get("group_by");
    if (!groupBy || !["user", "project", "cluster", "account", "partition", "day"].includes(groupBy)) {
      return sendError(response, 400, "ACCOUNTING_GROUP_INVALID", "Invalid group_by", id);
    }
    const fromText = url.searchParams.get("from");
    const toText = url.searchParams.get("to");
    const fromTime = fromText ? Date.parse(fromText) : Number.NaN;
    const toTime = toText ? Date.parse(toText) : Number.NaN;
    if (!Number.isFinite(fromTime) || !Number.isFinite(toTime) || toTime <= fromTime) {
      return sendError(response, 400, "ACCOUNTING_RANGE_INVALID", "from and to are required", id);
    }
    if (toTime - fromTime > 400 * 86_400_000) {
      return sendError(response, 400, "ACCOUNTING_RANGE_TOO_LARGE", "Accounting range must not exceed 400 days", id);
    }
    let afterKey = "";
    const cursorText = url.searchParams.get("cursor");
    if (cursorText) {
      try {
        afterKey = Buffer.from(cursorText, "base64url").toString("utf8");
        if (!/^[A-Za-z0-9._:-]+$/.test(afterKey)) throw new Error("cursor");
      } catch {
        return sendError(response, 400, "INVALID_CURSOR", "The usage cursor is invalid", id);
      }
    }
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const tenantWide = user.me.platform_roles.includes("platform-admin") || membership?.roles.includes("tenant-admin") === true;
    let usageRecords = data.usageRecords[tenant].filter((record) => {
      const day = Date.parse(`${record.day}T00:00:00.000Z`);
      return day >= fromTime && day < toTime && (tenantWide || record.user_id === user.me.user_id);
    });
    const projectFilter = url.searchParams.get("project");
    const clusterFilter = url.searchParams.get("cluster");
    if (projectFilter) usageRecords = usageRecords.filter((record) => record.project_id === projectFilter);
    if (clusterFilter) usageRecords = usageRecords.filter((record) => record.cluster_id === clusterFilter);
    const grouped = groupUsageRows(usageRecords, groupBy).filter((row) => row.key > afterKey);
    const requestedLimit = Number(url.searchParams.get("limit") ?? 100);
    const limit = Number.isInteger(requestedLimit) && requestedLimit > 0 ? Math.min(requestedLimit, 200) : 100;
    const items = grouped.slice(0, limit);
    const last = items.at(-1);
    /** @type {AccountingUsageList} */
    const usagePage = {
      items,
      next_cursor: last && grouped.length > items.length ? Buffer.from(last.key).toString("base64url") : "",
    };
    return send(response, 200, usagePage);
  }

  return sendError(response, 404, "NOT_FOUND", "Resource not found", id);
});

server.once("error", (error) => {
  /** @type {NodeJS.ErrnoException} */
  const serverError = error;
  const release = process.platform === "win32"
    ? `netstat -ano | findstr :${port}, then taskkill /PID <PID> /F`
    : `lsof -ti tcp:${port} | xargs kill`;
  if (serverError.code === "EADDRINUSE") {
    process.stderr.write(`Mock port ${port} is already in use; free it with ${release}.\n`);
    process.exit(73);
  }
  process.stderr.write(`Mock API server failed on port ${port}: ${serverError.message}\n`);
  process.exit(1);
});
server.listen(port, "127.0.0.1", () => process.stdout.write(`mock API ready on 127.0.0.1:${port}\n`));
