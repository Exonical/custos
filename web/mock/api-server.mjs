// @ts-check
import { randomUUID } from "node:crypto";
import http from "node:http";
import { createMockData, userNameFromAccessToken } from "./data.mjs";

/** @typedef {import("../lib/api/schema").components["schemas"]["Me"]} Me */
/** @typedef {import("../lib/api/schema").components["schemas"]["ProjectList"]} ProjectList */
/** @typedef {import("../lib/api/schema").components["schemas"]["ClusterSummaryList"]} ClusterSummaryList */
/** @typedef {import("../lib/api/schema").components["schemas"]["Job"]} Job */
/** @typedef {import("../lib/api/schema").components["schemas"]["JobList"]} JobList */
/** @typedef {import("../lib/api/schema").components["schemas"]["HealthStatus"]} HealthStatus */
/** @typedef {import("../lib/api/schema").components["schemas"]["Error"]} ApiErrorBody */
/** @typedef {import("../lib/api/schema").operations["getJobExecutionSpec"]["responses"][200]["content"]["application/json"]} ExecutionSpec */
/** @typedef {"acme" | "globex"} TenantSlug */
/** @typedef {{ tenant: string, project: string, state: string, cluster: string, created_at: string, id: string }} JobCursor */

const port = Number(process.env.MOCK_API_PORT ?? 4301);
const issuer = process.env.MOCK_OIDC_ISSUER ?? "http://127.0.0.1:4300/realms/custos";
const latencyMs = Math.max(0, Math.min(30_000, Number(process.env.MOCK_API_LATENCY_MS ?? 0) || 0));
const data = createMockData(Date.now(), issuer, Number(process.env.MOCK_DATA_SEED ?? 20260927));

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

  const tenantProjects = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects$/);
  const tenantClusters = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/clusters$/);
  const tenantJobs = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/jobs$/);
  const projectJobs = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects\/([^/]+)\/jobs(?:\/([^/]+)(?:\/(execution-spec|cancel))?)?$/);
  const tenantPath = tenantProjects?.[1] ?? tenantClusters?.[1] ?? tenantJobs?.[1] ?? projectJobs?.[1];
  const tenant = tenantPath && isTenantSlug(tenantPath) ? tenantPath : undefined;
  if (tenantPath && (!tenant || !user.me.memberships.some((membership) => membership.slug === tenant))) {
    return sendError(response, 404, "NOT_FOUND", "Tenant not found", id);
  }

  if (tenantProjects && request.method === "GET" && tenant) {
    /** @type {ProjectList} */
    const page = { items: data.projects[tenant], next_cursor: null };
    return send(response, 200, page);
  }
  if (tenantClusters && request.method === "GET" && tenant) {
    /** @type {ClusterSummaryList} */
    const clusters = { items: data.clusters[tenant] };
    return send(response, 200, clusters);
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
