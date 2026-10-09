// @ts-check
import { createHash, randomUUID } from "node:crypto";
import http from "node:http";
import { parseDocument, stringify } from "yaml";
import { createMockData, userNameFromAccessToken } from "./data.mjs";
import { handleNodeHooks } from "./node-hooks.mjs";
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

/** @param {http.ServerResponse} response @param {number} status @param {string} body @param {string} contentType @param {Record<string,string>} [extraHeaders] */
function sendText(response, status, body, contentType, extraHeaders = {}) {
  response.writeHead(status, {
    "content-type": contentType,
    "cache-control": "no-store",
    ...extraHeaders,
  });
  response.end(body);
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

/** @param {http.IncomingMessage} request @param {number} [maxBytes] @returns {Promise<{raw: Buffer, value: unknown}>} */
async function readJsonBody(request, maxBytes = 1_048_576) {
  const raw = await readRawBody(request, maxBytes);
  return { raw, value: raw.length ? JSON.parse(raw.toString("utf8")) : null };
}

/** @param {http.IncomingMessage} request @param {number} [maxBytes] @returns {Promise<Buffer>} */
async function readRawBody(request, maxBytes = 2 * 1024 * 1024) {
  const chunks = [];
  let length = 0;
  for await (const chunk of request) {
    const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    length += bytes.byteLength;
    if (length > maxBytes) throw new RangeError("body too large");
    chunks.push(bytes);
  }
  return Buffer.concat(chunks);
}

/** @param {string} raw @param {string} contentType */
function parseWorkflowBody(raw, contentType) {
  const mediaType = contentType.split(";")[0]?.trim().toLowerCase();
  if (mediaType === "application/yaml" || mediaType === "text/yaml") {
    const document = parseDocument(raw);
    if (document.errors.length > 0) {
      return {
        value: null,
        errors: document.errors.map((error) => {
          const pos = error.linePos?.[0];
          return {
            path: "",
            code: "YAML_PARSE",
            message: `${error.message}${pos ? ` (line ${String(pos.line)}, column ${String(pos.col)})` : ""}`,
          };
        }),
      };
    }
    return { value: document.toJS(), errors: [] };
  }
  if (mediaType === "application/json") {
    return { value: raw ? JSON.parse(raw) : null, errors: [] };
  }
  throw new TypeError("workflow spec Content-Type must be application/yaml or application/json");
}

/** @param {unknown} value @param {TenantSlug} [tenant] @returns {{path:string,code:string,message:string}[]} */
function validateMockWorkflow(value, tenant) {
  /** @type {{path:string,code:string,message:string}[]} */
  const errors = [];
  const document = objectRecord(value);
  const spec = objectRecord(document?.spec);
  const tasks = spec?.tasks;
  if (document?.apiVersion !== "custos.io/v1alpha1") {
    errors.push({ path: "apiVersion", code: "API_VERSION", message: "apiVersion must be custos.io/v1alpha1" });
  }
  if (document?.kind !== "Workflow") {
    errors.push({ path: "kind", code: "KIND", message: "kind must be Workflow" });
  }
  if (!Array.isArray(tasks) || tasks.length === 0) {
    errors.push({ path: "spec.tasks", code: "TASKS_REQUIRED", message: "at least one task is required" });
    return errors;
  }

  /** @type {Map<string,number>} */
  const names = new Map();
  /** @type {Map<string,string[]>} */
  const dependencies = new Map();
  const secrets = objectRecord(spec?.secrets) ?? {};
  /** @type {Set<string>} */
  const usedImagePullHandles = new Set();
  /** @type {Map<string,string>} */
  const serviceNames = new Map();
  tasks.forEach((entry, index) => {
    const task = objectRecord(entry);
    const path = `spec.tasks[${String(index)}]`;
    if (!task || typeof task.name !== "string" || !task.name) {
      errors.push({ path: `${path}.name`, code: "NAME_REQUIRED", message: "task name is required" });
      return;
    }
    if (names.has(task.name)) {
      errors.push({ path: `${path}.name`, code: "NAME_DUPLICATE", message: `task name ${task.name} is duplicated` });
    } else {
      names.set(task.name, index);
    }
    if (task.service !== undefined && task.service !== null) {
      const suffix = task.name.toUpperCase().replace(/[^A-Z0-9]/g, "_");
      const previous = serviceNames.get(suffix);
      if (previous) {
        errors.push({ path: `${path}.name`, code: "SERVICE_NAME_COLLISION", message: `service name collides with ${previous} after normalization` });
      } else {
        serviceNames.set(suffix, task.name);
      }
      const resources = objectRecord(task.resources) ?? {};
      if (typeof resources.walltime !== "string" || !resources.walltime.trim()) {
        errors.push({ path: `${path}.resources.walltime`, code: "SERVICE_WALLTIME_REQUIRED", message: "service tasks require resources.walltime" });
      }
      if (["condition", "approval", "array"].includes(String(task.type))) {
        errors.push({ path: `${path}.service`, code: "SERVICE_FIELD_UNSUPPORTED", message: "service tasks require a supported Slurm task kind" });
      }
      for (const field of ["array", "fanOut", "retry", "when", "outputs"]) {
        if (task[field] !== undefined && task[field] !== null) {
          errors.push({ path: `${path}.${field}`, code: "SERVICE_FIELD_UNSUPPORTED", message: `${field} is not supported on service tasks` });
        }
      }
    }
    const resources = objectRecord(task.resources) ?? {};
    if (resources.memoryPerCpu !== undefined
      && (resources.memory !== undefined || resources.memoryPerNode !== undefined)) {
      errors.push({
        path: `${path}.resources.memoryPerCpu`,
        code: "MEMORY_CONFLICT",
        message: "memoryPerCpu is mutually exclusive with memory and memoryPerNode",
      });
    }
    const image = objectRecord(task.image);
    const pullSecret = objectRecord(image?.pullSecret);
    if (image?.pullSecret !== undefined && image.pullSecret !== null) {
      const uri = typeof image.uri === "string" ? image.uri : "";
      if (!uri) {
        errors.push({ path: `${path}.image.pullSecret`, code: "PULL_SECRET_REQUIRES_IMAGE", message: "pullSecret requires image.uri" });
      } else if (!uri.startsWith("docker://") && !uri.startsWith("oras://")) {
        errors.push({ path: `${path}.image.pullSecret`, code: "PULL_SECRET_INVALID", message: "pullSecret requires a docker:// or oras:// image URI" });
      }
      if (!pullSecret) {
        errors.push({ path: `${path}.image.pullSecret`, code: "PULL_SECRET_INVALID", message: "pullSecret must be an object" });
      } else {
        const literalUsername = typeof pullSecret.username === "string" && pullSecret.username
          ? pullSecret.username
          : undefined;
        const usernameSecret = typeof pullSecret.usernameSecret === "string" && pullSecret.usernameSecret
          ? pullSecret.usernameSecret
          : undefined;
        const passwordSecret = typeof pullSecret.passwordSecret === "string" && pullSecret.passwordSecret
          ? pullSecret.passwordSecret
          : undefined;
        const hasLiteral = literalUsername !== undefined;
        const hasUsernameSecret = usernameSecret !== undefined;
        if (hasLiteral === hasUsernameSecret
          || !passwordSecret) {
          errors.push({ path: `${path}.image.pullSecret`, code: "PULL_SECRET_INVALID", message: "specify exactly one username or usernameSecret and a passwordSecret" });
        }
        if (literalUsername !== undefined && (literalUsername.length > 256 || /[\s\u0000-\u001f\u007f]/.test(literalUsername))) {
          errors.push({ path: `${path}.image.pullSecret.username`, code: "PULL_SECRET_INVALID", message: "literal username must be at most 256 characters without whitespace or control characters" });
        }
        /** @type {string[]} */
        const handles = [];
        if (usernameSecret) handles.push(usernameSecret);
        if (passwordSecret) handles.push(passwordSecret);
      for (const handle of handles) {
          const definition = objectRecord(secrets[handle]);
          if (!definition || definition.use !== "image_pull") {
            errors.push({ path: `${path}.image.pullSecret`, code: "PULL_SECRET_INVALID", message: `secret handle ${handle} must use image_pull` });
          } else {
            usedImagePullHandles.add(handle);
            const reference = tenant
              ? data.references[tenant]?.find((item) => item.name === definition.ref)
              : undefined;
            if (tenant && (!reference || reference.kind !== "generic"
              || !Array.isArray(reference.allowed_uses) || !reference.allowed_uses.includes("image_pull"))) {
              errors.push({ path: `spec.secrets.${handle}.ref`, code: "SECRET_USE_NOT_ALLOWED", message: `reference for ${handle} must allow image_pull` });
            }
          }
        }
        if (task.multinode || (typeof resources.nodes === "number" && resources.nodes > 1)) {
          errors.push({ path: `${path}.image.pullSecret`, code: "PULL_SECRET_MULTINODE_UNSUPPORTED", message: "pull secrets are only supported on single-node tasks" });
        }
      }
    }
    if (task.launch !== undefined && task.launch !== "sbatch" && task.launch !== "srun") {
      errors.push({ path: `${path}.launch`, code: "LAUNCH_INVALID", message: "launch must be sbatch or srun" });
    }
    if (task.dependsOn !== undefined && (!Array.isArray(task.dependsOn) || task.dependsOn.some((name) => typeof name !== "string"))) {
      errors.push({ path: `${path}.dependsOn`, code: "DEPENDS_ON_INVALID", message: "dependsOn must be a list of task names" });
      dependencies.set(task.name, []);
      return;
    }
    /** @type {string[]} */
    const namesForTask = Array.isArray(task.dependsOn)
      ? task.dependsOn.filter((name) => typeof name === "string")
      : [];
    dependencies.set(task.name, namesForTask);
    namesForTask.forEach((dependency) => {
      if (!tasks.some((candidate) => objectRecord(candidate)?.name === dependency)) {
        errors.push({ path: `${path}.dependsOn`, code: "DEPENDS_ON_UNKNOWN", message: `unknown dependency ${String(dependency)}` });
      }
    });
  });

  const workflowPlacement = objectRecord(spec?.placement);
  /** @param {Record<string, unknown>} task */
  const explicitCluster = (task) => {
    const placement = objectRecord(task.placement);
    return typeof placement?.cluster === "string" && placement.cluster
      ? placement.cluster
      : typeof workflowPlacement?.cluster === "string" ? workflowPlacement.cluster : "";
  };
  tasks.forEach((entry, taskIndex) => {
    const dependent = objectRecord(entry);
    if (!dependent) return;
    const dependsOn = Array.isArray(dependent?.dependsOn) ? dependent.dependsOn : [];
    dependsOn.forEach((dependencyName, dependencyIndex) => {
      if (typeof dependencyName !== "string") return;
      const serviceIndex = names.get(dependencyName);
      const service = serviceIndex === undefined ? undefined : objectRecord(tasks[serviceIndex]);
      if (!service?.service) return;
      const serviceCluster = explicitCluster(service);
      const dependentCluster = explicitCluster(dependent);
      if (serviceCluster && dependentCluster && serviceCluster !== dependentCluster) {
        errors.push({
          path: `spec.tasks[${String(taskIndex)}].dependsOn[${String(dependencyIndex)}]`,
          code: "SERVICE_CLUSTER_MISMATCH",
          message: "service and dependent tasks must use the same Slurm cluster",
        });
      }
    });
  });

  const active = new Set();
  const visited = new Set();
  /** @type {Set<string>} */
  const cycles = new Set();
  /** @param {string} name */
  function visit(name) {
    if (active.has(name)) {
      const index = names.get(name);
      cycles.add(`spec.tasks[${String(index)}].dependsOn`);
      return;
    }
    if (visited.has(name)) return;
    active.add(name);
    for (const dependency of dependencies.get(name) ?? []) {
      if (names.has(dependency)) visit(dependency);
    }
    active.delete(name);
    visited.add(name);
  }
  for (const name of names.keys()) visit(name);
  for (const path of cycles) errors.push({ path, code: "DEPENDENCY_CYCLE", message: "task dependencies contain a cycle" });
  for (const [handle, definitionValue] of Object.entries(secrets)) {
    const definition = objectRecord(definitionValue);
    if (!definition) continue;
    if (definition.use === "image_pull") {
      if (definition.envName !== undefined) {
        errors.push({ path: `spec.secrets.${handle}.envName`, code: "PULL_SECRET_INVALID", message: "image_pull handles cannot set envName" });
      }
      if (!usedImagePullHandles.has(handle)) {
        errors.push({ path: `spec.secrets.${handle}`, code: "SECRET_USE_UNUSED", message: "image_pull handles must be referenced by a task pullSecret" });
      }
    }
  }
  return errors;
}

/** @param {unknown} value @returns {Record<string, unknown> | undefined} */
function objectRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? /** @type {Record<string, unknown>} */ (value) : undefined;
}

/** @param {string} value @param {string} fallback */
function workflowSlug(value, fallback) {
  const slug = value.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 63).replace(/-+$/g, "");
  return slug || fallback;
}

/** @param {string} value */
function integerDirective(value) {
  const number = Number(value);
  return Number.isInteger(number) && number > 0 ? number : undefined;
}

/** @param {string} content @param {string} filename @param {number} index */
function importSbatchScript(content, filename, index) {
  /** @type {Record<string, unknown>} */
  const resources = {};
  /** @type {string[]} */
  const imported = [];
  let jobName = "";
  let partition;
  const originalLines = content.split(/\r\n|\n|\r/);
  const rewrittenLines = originalLines.map((line) => {
    const directive = line.match(/^\s*#SBATCH\s+--([a-z0-9-]+)(?:=(.*)|\s+(.*))?\s*$/i);
    if (!directive) return line;
    const option = directive[1].toLowerCase();
    const value = (directive[2] ?? directive[3] ?? "").trim().replace(/^(['"])(.*)\1$/, "$2");
    switch (option) {
      case "job-name":
        jobName = value;
        imported.push("job-name");
        break;
      case "nodes": {
        const number = integerDirective(value);
        if (number) resources.nodes = number;
        imported.push("nodes");
        break;
      }
      case "ntasks": {
        const number = integerDirective(value);
        if (number) resources.tasks = number;
        imported.push("tasks");
        break;
      }
      case "ntasks-per-node": {
        const number = integerDirective(value);
        if (number) resources.tasksPerNode = number;
        imported.push("tasksPerNode");
        break;
      }
      case "cpus-per-task": {
        const number = integerDirective(value);
        if (number) resources.cpusPerTask = number;
        imported.push("cpusPerTask");
        break;
      }
      case "time":
        if (value) resources.walltime = value;
        imported.push("walltime");
        break;
      case "mem":
        if (value) resources.memory = value;
        imported.push("memory");
        break;
      case "partition":
        partition = value;
        imported.push("partition");
        break;
      case "gres": {
        const gpu = value.match(/(?:^|,)gpu(?::[a-z0-9_.-]+)?:(\d+)/i) ?? value.match(/(?:^|,)gpu:(\d+)/i);
        const count = integerDirective(gpu?.[1] ?? "");
        if (count) resources.gpu = { count };
        imported.push("gres");
        break;
      }
      default:
        return line;
    }
    return `# [custos] imported: ${line.trim()}`;
  });
  const taskName = workflowSlug(jobName || filename.replace(/\.[^.]*$/, ""), `task-${String(index + 1)}`);
  const rewritten = rewrittenLines.join("\n");
  const digestValue = `sha256:${createHash("sha256").update(rewritten).digest("hex")}`;
  const language = /^#!.*(?:\/|^)(?:sh|dash)(?:\s|$)/i.test(rewritten.split(/\r?\n/, 1)[0] ?? "")
    ? "sh"
    : "bash";
  const task = {
    name: taskName,
    launch: "sbatch",
    ...(Object.keys(resources).length ? { resources } : {}),
    script: { ref: digestValue, language },
    ...(partition ? { partition } : {}),
  };
  return {
    task,
    filename,
    imported: [...new Set(imported)],
    digest: digestValue,
  };
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

  if (await handleNodeHooks({
    request, response, url, id, data, send, sendError, sendEmpty, readJsonBody,
    isAdmin: user.me.platform_roles.includes("platform-admin"),
  })) return;

  const platformCluster = url.pathname.match(/^\/api\/v1\/clusters\/([^/]+)$/);
  if (platformCluster && (request.method === "GET" || request.method === "PATCH")) {
    if (!user.me.platform_roles.includes("platform-admin")) {
      return sendError(response, 403, "FORBIDDEN", "Requires cluster.read or cluster.manage at platform scope", id);
    }
    const cluster = Object.values(data.platformClusters).find((item) => item.id === platformCluster[1] || item.name === platformCluster[1]);
    if (!cluster) return sendError(response, 404, "NOT_FOUND", "Cluster not found", id);
    if (request.method === "GET") return send(response, 200, cluster);

    let input;
    try {
      input = objectRecord((await readJsonBody(request)).value);
    } catch {
      return sendError(response, 400, "MALFORMED", "Invalid cluster update body", id);
    }
    if (!input || typeof input.version !== "number" || !Number.isInteger(input.version)) {
      return sendError(response, 422, "VALIDATION", "version is required", id);
    }
    if (input.version !== cluster.version) {
      return sendError(response, 409, "VERSION_CONFLICT", "Cluster changed since it was loaded", id);
    }

    /** @type {{field:string,reason:string}[]} */
    const details = [];
    let nextRuntime = cluster.container_runtime;
    if (Object.hasOwn(input, "container_runtime")) {
      if (input.container_runtime === null) {
        nextRuntime = null;
      } else {
        const runtime = objectRecord(input.container_runtime);
        if (!runtime || (runtime.type !== "apptainer" && runtime.type !== "pyxis")) {
          details.push({ field: "container_runtime.type", reason: "type must be apptainer or pyxis" });
        } else {
          if (runtime.binary !== undefined && typeof runtime.binary !== "string") {
            details.push({ field: "container_runtime.binary", reason: "binary must be a string" });
          }
          const binary = typeof runtime.binary === "string" ? runtime.binary : "";
          const prefixes = runtime.allowed_image_prefixes === undefined ? [] : runtime.allowed_image_prefixes;
          const mpiPlugin = runtime.mpi_plugin === undefined || runtime.mpi_plugin === "" ? "pmix" : runtime.mpi_plugin;
          if (runtime.type === "apptainer" && binary &&
            (binary.length > 128 || !/^[A-Za-z0-9_.:@/=-]+$/.test(binary))) {
            details.push({ field: "container_runtime.binary", reason: "binary is invalid" });
          }
          if (runtime.type === "pyxis" && binary) {
            details.push({ field: "container_runtime.binary", reason: "binary is only valid for apptainer" });
          }
          if (!Array.isArray(prefixes) || prefixes.some((prefix) =>
            typeof prefix !== "string" || prefix.length > 512 ||
            !(/^(docker|oras):\/\/[A-Za-z0-9._/:@+-]+$|^\/[A-Za-z0-9._/+-]+$/).test(prefix) ||
            prefix.split("/").includes(".."))) {
            details.push({ field: "container_runtime.allowed_image_prefixes", reason: "one or more image prefixes are invalid" });
          }
          if (typeof mpiPlugin !== "string" || !/^[a-z0-9_]+$/.test(mpiPlugin)) {
            details.push({ field: "container_runtime.mpi_plugin", reason: "mpi_plugin is invalid" });
          }
          for (const key of ["require_digest", "slurm_in_container"]) {
            if (runtime[key] !== undefined && typeof runtime[key] !== "boolean") {
              details.push({ field: `container_runtime.${key}`, reason: `${key} must be a boolean` });
            }
          }
          if (Array.isArray(prefixes) && typeof mpiPlugin === "string") {
            const normalizedRuntime = {
              type: runtime.type,
              ...(runtime.type === "apptainer" ? { binary: binary || "apptainer" } : {}),
              allowed_image_prefixes: prefixes,
              require_digest: runtime.require_digest === true,
              slurm_in_container: runtime.slurm_in_container === true,
              mpi_plugin: mpiPlugin,
            };
            nextRuntime = /** @type {typeof nextRuntime} */ (/** @type {unknown} */ (normalizedRuntime));
          }
        }
      }
    }

    let nextSoftwareModules = cluster.software_modules ?? [];
    if (Object.hasOwn(input, "software_modules")) {
      const modules = input.software_modules;
      if (!Array.isArray(modules)) {
        details.push({ field: "software_modules", reason: "software_modules must be a list" });
      } else {
        const seen = new Set();
        /** @type {import("../lib/api/schema").components["schemas"]["SoftwareModule"][]} */
        const normalized = [];
        modules.forEach((entry, index) => {
          const moduleEntry = objectRecord(entry);
          if (!moduleEntry) {
            details.push({ field: `software_modules[${String(index)}]`, reason: "entry must be an object" });
            return;
          }
          const name = typeof moduleEntry.name === "string" ? moduleEntry.name : "";
          const version = typeof moduleEntry.version === "string" ? moduleEntry.version : "";
          const moduleNames = moduleEntry.modules;
          const prefix = `software_modules[${String(index)}]`;
          if (!/^[A-Za-z0-9][A-Za-z0-9_.+-]{0,63}$/.test(name)) {
            details.push({ field: `${prefix}.name`, reason: "name is invalid" });
          }
          if (version && !/^[A-Za-z0-9][A-Za-z0-9_.+-]{0,63}$/.test(version)) {
            details.push({ field: `${prefix}.version`, reason: "version is invalid" });
          }
          if (moduleEntry.version !== undefined && typeof moduleEntry.version !== "string") {
            details.push({ field: `${prefix}.version`, reason: "version must be a string" });
          }
          if (!Array.isArray(moduleNames) || moduleNames.length < 1 || moduleNames.length > 16 ||
            moduleNames.some((value) => typeof value !== "string" || !/^[A-Za-z0-9][A-Za-z0-9_.+/@-]{0,127}$/.test(value))) {
            details.push({ field: `${prefix}.modules`, reason: "modules must contain 1–16 valid names" });
          }
          const pair = `${name}\u0000${version}`;
          if (seen.has(pair)) details.push({ field: `${prefix}.name`, reason: "name/version pair is duplicated" });
          seen.add(pair);
          if (Array.isArray(moduleNames) && moduleNames.every((value) => typeof value === "string")) {
            normalized.push({ name, ...(version ? { version } : {}), modules: moduleNames });
          }
        });
        nextSoftwareModules = normalized;
      }
    }

    if (details.length > 0) {
      return send(response, 422, {
        error: { code: "CLUSTER_SETTINGS_INVALID", message: "Cluster settings are invalid", request_id: id, details },
      });
    }
    const updated = {
      ...cluster,
      container_runtime: nextRuntime,
      software_modules: nextSoftwareModules,
      version: cluster.version + 1,
      updated_at: new Date().toISOString(),
    };
    data.platformClusters[cluster.id] = updated;
    for (const tenantClusters of Object.values(data.clusters)) {
      const summary = tenantClusters.find((item) => item.id === cluster.id);
      if (summary) summary.container_runtime = nextRuntime ? { type: nextRuntime.type } : null;
    }
    return send(response, 200, updated);
  }

  const tenantProjects = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects$/);
  const projectDetail = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects\/([^/]+)$/);
  const projectMembers = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects\/([^/]+)\/members$/);
  const projectBindings = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects\/([^/]+)\/cluster-bindings$/);
  const projectAllocations = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/projects\/([^/]+)\/allocations$/);
  const tenantSbatchImport = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflow-imports\/sbatch$/);
  const tenantWorkflows = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows$/);
  const workflowVersion = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows\/([^/]+)\/versions\/([^/]+)$/);
  const workflowVersionLayout = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows\/([^/]+)\/versions\/([^/]+)\/layout$/);
  const workflowVersionValidate = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows\/([^/]+)\/versions\/validate$/);
  const workflowVersionPublish = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows\/([^/]+)\/versions\/([^/]+)\/publish$/);
  const workflowVersionDeprecate = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows\/([^/]+)\/versions\/([^/]+)\/deprecate$/);
  const workflowTaskSbatch = url.pathname.match(/^\/api\/v1\/tenants\/([^/]+)\/workflows\/([^/]+)\/versions\/([^/]+)\/tasks\/([^/]+)\/sbatch$/);
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

  if (tenantSbatchImport && request.method === "POST" && tenant) {
    let input;
    try {
      input = objectRecord((await readJsonBody(request, 2 * 1024 * 1024)).value);
    } catch (error) {
      const tooLarge = error instanceof RangeError;
      return sendError(response, tooLarge ? 413 : 400, tooLarge ? "BODY_TOO_LARGE" : "MALFORMED", "Invalid sbatch import request", id);
    }
    if (!input || !Array.isArray(input.scripts) || input.scripts.length < 1 || input.scripts.length > 20) {
      return sendError(response, 400, "MALFORMED", "scripts must contain 1–20 files", id);
    }
    if (input.name !== undefined && (typeof input.name !== "string" || input.name.length > 256)) {
      return sendError(response, 400, "MALFORMED", "name must be at most 256 characters", id);
    }
    /** @type {ReturnType<typeof importSbatchScript>[]} */
    const importedTasks = [];
    for (let index = 0; index < input.scripts.length; index += 1) {
      const entry = objectRecord(input.scripts[index]);
      if (typeof entry?.filename !== "string" || typeof entry.content !== "string") {
        return sendError(response, 400, "MALFORMED", `scripts[${String(index)}] requires filename and content`, id);
      }
      if (Buffer.byteLength(entry.content, "utf8") > 262_144) {
        return sendError(response, 413, "SCRIPT_TOO_LARGE", `${entry.filename} exceeds 256 KiB`, id);
      }
      importedTasks.push(importSbatchScript(entry.content, entry.filename, index));
    }
    const usedNames = new Set();
    for (const imported of importedTasks) {
      const base = imported.task.name;
      let candidate = base;
      let suffix = 2;
      while (usedNames.has(candidate)) {
        candidate = `${base.slice(0, 58)}-${String(suffix++)}`;
      }
      imported.task.name = candidate;
      usedNames.add(candidate);
    }
    const firstTask = importedTasks[0]?.task.name ?? "imported-workflow";
    const workflowName = workflowSlug(typeof input.name === "string" && input.name.trim() ? input.name.trim() : firstTask, firstTask);
    const spec = {
      apiVersion: "custos.io/v1alpha1",
      kind: "Workflow",
      metadata: { name: workflowName },
      spec: { tasks: importedTasks.map(({ task }) => task) },
    };
    const result = {
      spec,
      yaml: stringify(spec),
      tasks: importedTasks.map(({ filename, task, digest, imported }) => ({
        filename,
        task: task.name,
        scriptDigest: digest,
        imported,
        diagnostics: [],
      })),
    };
    return send(response, 200, result);
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
    if (!project || !name) {
      return sendError(response, 422, "VALIDATION", "project and name are required", id);
    }
    const membership = user.me.memberships.find((item) => item.slug === tenant);
    const projectRoles = (user.me.project_memberships ?? [])
      .filter((item) => item.project_id === project.id)
      .flatMap((item) => item.roles);
    const canCreate = user.me.platform_roles.includes("platform-admin")
      || membership?.roles.some((role) => ["tenant-admin", "workflow-author"].includes(role))
      || projectRoles.some((role) => ["project-admin", "project-member"].includes(role));
    if (!canCreate) return sendError(response, 403, "FORBIDDEN", "Requires workflow.create", id);
    if (name.length > 63 || !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name)) {
      return sendError(response, 422, "WORKFLOW_NAME_INVALID", "name must be a lowercase DNS label of at most 63 characters", id);
    }
    if (data.workflows[tenant].some((item) => item.projectId === project.id && item.name === name)) {
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
    const contentType = String(request.headers["content-type"] ?? "");
    let raw;
    let decoded;
    try {
      raw = (await readRawBody(request)).toString("utf8");
      decoded = parseWorkflowBody(raw, contentType);
    } catch (error) {
      const status = error instanceof RangeError ? 413 : error instanceof TypeError ? 415 : 400;
      const code = status === 413 ? "BODY_TOO_LARGE" : status === 415 ? "UNSUPPORTED_MEDIA_TYPE" : "MALFORMED";
      return sendError(response, status, code, "Invalid workflow spec body", id);
    }
    const document = objectRecord(decoded.value);
    const errors = [...decoded.errors, ...validateMockWorkflow(decoded.value, tenant)];
    const details = errors.map((error) => ({ field: error.path, reason: `${error.code}: ${error.message}` }));
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
  if (workflowVersionValidate && request.method === "POST" && tenant) {
    const workflowId = workflowVersionValidate[2];
    if (!data.workflows[tenant].some((item) => item.id === workflowId)) return sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
    let decoded;
    try {
      const raw = (await readRawBody(request)).toString("utf8");
      decoded = parseWorkflowBody(raw, String(request.headers["content-type"] ?? ""));
    } catch (error) {
      if (error instanceof RangeError) return sendError(response, 413, "BODY_TOO_LARGE", "Workflow spec body is too large", id);
      if (error instanceof TypeError) return sendError(response, 415, "UNSUPPORTED_MEDIA_TYPE", "Expected application/yaml or application/json", id);
      return sendError(response, 400, "MALFORMED", "Invalid workflow spec body", id);
    }
    const errors = [...decoded.errors, ...validateMockWorkflow(decoded.value, tenant)];
    return send(response, 200, { valid: errors.length === 0, errors });
  }
  if (workflowVersionLayout && request.method === "PUT" && tenant) {
    const workflowId = workflowVersionLayout[2];
    const versionId = workflowVersionLayout[3];
    const version = (data.workflowVersions[workflowId] ?? []).find((item) => item.id === versionId);
    if (!version) return sendError(response, 404, "VERSION_UNKNOWN", "Workflow version not found", id);
    if (version.state === "deprecated") return sendError(response, 409, "VERSION_IMMUTABLE", "Deprecated versions are immutable", id);
    let input;
    try {
      input = objectRecord((await readJsonBody(request)).value);
    } catch {
      return sendError(response, 400, "MALFORMED", "Invalid layout JSON", id);
    }
    const expectedHeader = request.headers["x-expected-version"];
    const expectedVersion = typeof expectedHeader === "string" ? Number(expectedHeader) : 0;
    if (expectedVersion > 0 && expectedVersion !== version.version) {
      return sendError(response, 409, "VERSION_CONFLICT", "Version changed since it was loaded", id);
    }
    if (!input || !objectRecord(input.nodes)) {
      return sendError(response, 400, "MALFORMED", "layout must contain a nodes map", id);
    }
    version.layout = input;
    version.version += 1;
    return sendEmpty(response, 204);
  }
  if (workflowTaskSbatch && request.method === "GET" && tenant) {
    const workflowId = workflowTaskSbatch[2];
    const versionId = workflowTaskSbatch[3];
    const taskName = decodeURIComponent(workflowTaskSbatch[4]);
    if (!data.workflows[tenant].some((item) => item.id === workflowId)) return sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
    const version = (data.workflowVersions[workflowId] ?? []).find((item) => item.id === versionId);
    if (!version) return sendError(response, 404, "VERSION_UNKNOWN", "Workflow version not found", id);
    const task = version.spec.spec.tasks.find((item) => item.name === taskName);
    if (!task) return sendError(response, 404, "TASK_UNKNOWN", "Workflow task not found", id);
    if (task.image || task.multinode) {
      return sendError(response, 422, "EXPORT_UNSUPPORTED", "This task uses a container image or multinode and cannot be exported as a standalone script", id);
    }
    const filename = `${task.name.replace(/[^A-Za-z0-9_.-]/g, "-")}.sbatch`;
    const script = [
      "#!/bin/bash",
      `#SBATCH --job-name=${task.name}`,
      `#SBATCH --nodes=${task.resources?.nodes ?? 1}`,
      `#SBATCH --ntasks=${task.resources?.tasks ?? 1}`,
      `# Mock sbatch export for ${task.name}`,
      ...(task.script ? [`${typeof task.script === "string" ? task.script : ""}`] : [`exec ${task.command?.map((word) => `'${word.replaceAll("'", "'\\''")}'`).join(" ") ?? "true"}`]),
      "",
    ].join("\n");
    return sendText(response, 200, script, "text/x-shellscript; charset=utf-8", {
      "content-disposition": `attachment; filename="${filename}"`,
    });
  }
  if (workflowVersion && request.method === "GET" && tenant) {
    const workflowId = workflowVersion[2];
    const versionId = workflowVersion[3];
    if (!data.workflows[tenant].some((item) => item.id === workflowId)) return sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
    const version = (data.workflowVersions[workflowId] ?? []).find((item) => item.id === versionId);
    if (version && request.headers.accept === "application/yaml") {
      return sendText(response, 200, stringify(version.spec), "application/yaml; charset=utf-8");
    }
    return version ? send(response, 200, version) : sendError(response, 404, "VERSION_UNKNOWN", "Workflow version not found", id);
  }
  if (workflowVersion && request.method === "PUT" && tenant) {
    const workflowId = workflowVersion[2];
    const versionId = workflowVersion[3];
    if (!data.workflows[tenant].some((item) => item.id === workflowId)) return sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
    const version = (data.workflowVersions[workflowId] ?? []).find((item) => item.id === versionId);
    if (!version) return sendError(response, 404, "VERSION_UNKNOWN", "Workflow version not found", id);
    if (version.state !== "draft") return sendError(response, 409, "VERSION_IMMUTABLE", "Only draft versions can be updated", id);
    const activeTest = data.workflowExecutions[tenant].find((execution) =>
      execution.workflowVersionId === version.id
      && execution.test === true
      && ["QUEUED", "RUNNING"].includes(execution.state));
    if (activeTest) {
      return sendError(response, 409, "DRAFT_LOCKED",
        `draft is locked by active test execution ${activeTest.id}`, id);
    }
    const expectedHeader = request.headers["x-expected-version"];
    const expectedVersion = typeof expectedHeader === "string" ? Number(expectedHeader) : 0;
    if (expectedVersion > 0 && expectedVersion !== version.version) {
      return sendError(response, 409, "VERSION_CONFLICT", "Draft version changed since it was loaded", id);
    }
    let decoded;
    try {
      const raw = (await readRawBody(request)).toString("utf8");
      decoded = parseWorkflowBody(raw, String(request.headers["content-type"] ?? ""));
    } catch (error) {
      if (error instanceof RangeError) return sendError(response, 413, "BODY_TOO_LARGE", "Workflow spec body is too large", id);
      if (error instanceof TypeError) return sendError(response, 415, "UNSUPPORTED_MEDIA_TYPE", "Expected application/yaml or application/json", id);
      return sendError(response, 400, "MALFORMED", "Invalid workflow spec body", id);
    }
    const validationErrors = [...decoded.errors, ...validateMockWorkflow(decoded.value, tenant)];
    if (validationErrors.length > 0) {
      return send(response, 422, {
        error: {
          code: "SPEC_INVALID",
          message: "Workflow spec is invalid",
          request_id: id,
          details: validationErrors.map((error) => ({ field: error.path, reason: `${error.code}: ${error.message}` })),
        },
      });
    }
    const document = objectRecord(decoded.value);
    if (!document) return sendError(response, 422, "SPEC_INVALID", "Workflow document must be an object", id);
    version.spec = /** @type {import("../lib/workflow/spec").CustosWorkflow} */ (/** @type {unknown} */ (document));
    version.specHash = `sha256:${createHash("sha256").update(JSON.stringify(document)).digest("hex")}`;
    version.version += 1;
    return send(response, 200, version);
  }
  if (workflowVersionPublish && request.method === "POST" && tenant) {
    const workflowId = workflowVersionPublish[2];
    const versionId = workflowVersionPublish[3];
    const workflow = data.workflows[tenant].find((item) => item.id === workflowId);
    if (!workflow) return sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
    const version = (data.workflowVersions[workflowId] ?? []).find((item) => item.id === versionId);
    if (!version) return sendError(response, 404, "VERSION_UNKNOWN", "Workflow version not found", id);
    if (version.state !== "draft") return sendError(response, 409, "VERSION_IMMUTABLE", "Only draft versions can be published", id);
    const errors = validateMockWorkflow(version.spec, tenant);
    if (errors.length > 0) {
      return send(response, 422, {
        error: {
          code: "SPEC_INVALID",
          message: "Workflow spec is invalid",
          request_id: id,
          details: errors.map((error) => ({ field: error.path, reason: `${error.code}: ${error.message}` })),
        },
      });
    }
    version.state = "published";
    version.publishedAt = new Date().toISOString();
    version.version += 1;
    workflow.latestPublishedVersionId = version.id;
    return send(response, 200, version);
  }
  if (workflowVersionDeprecate && request.method === "POST" && tenant) {
    const workflowId = workflowVersionDeprecate[2];
    const versionId = workflowVersionDeprecate[3];
    if (!data.workflows[tenant].some((item) => item.id === workflowId)) return sendError(response, 404, "WORKFLOW_UNKNOWN", "Workflow not found", id);
    const version = (data.workflowVersions[workflowId] ?? []).find((item) => item.id === versionId);
    if (!version) return sendError(response, 404, "VERSION_UNKNOWN", "Workflow version not found", id);
    if (version.state !== "published") return sendError(response, 409, "VERSION_STATE", "Only published versions can be deprecated", id);
    version.state = "deprecated";
    version.version += 1;
    return sendEmpty(response, 204);
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
    if (!requestBody || Object.keys(requestBody).some((key) => !["workflow", "version", "parameters", "test"].includes(key))) {
      return sendError(response, 400, "MALFORMED", "Malformed workflow execution request", id);
    }
    if (requestBody.test !== undefined && typeof requestBody.test !== "boolean") {
      return sendError(response, 400, "MALFORMED", "test must be a boolean", id);
    }
    const isTest = requestBody.test === true;
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
    const canCreate = user.me.platform_roles.includes("platform-admin")
      || tenantRoles.some((role) => ["tenant-admin", "workflow-author"].includes(role))
      || projectRoles.some((role) => ["project-admin", "project-member"].includes(role));
    if (isTest ? !canCreate || !canExecute : !canExecute) {
      return sendError(response, 403, "FORBIDDEN", isTest
        ? "Requires workflow.create and workflow.execute"
        : "Requires workflow.execute", id);
    }
    if (isTest && typeof requestBody.version !== "string") {
      return sendError(response, 422, "TEST_RUN_REQUIRES_DRAFT", "Test runs require an explicit draft version", id);
    }
    const requestedVersion = typeof requestBody.version === "string" ? requestBody.version : workflow.latestPublishedVersionId;
    if (!requestedVersion) return sendError(response, 409, "NO_PUBLISHED_VERSION", "Workflow has no published version", id);
    const version = (data.workflowVersions[workflow.id] ?? []).find((item) => item.id === requestedVersion);
    if (!version) return sendError(response, 404, "VERSION_UNKNOWN", "Workflow version not found", id);
    if (isTest && version.state !== "draft") {
      return sendError(response, 422, "TEST_RUN_REQUIRES_DRAFT", "Test runs require a draft version", id);
    }
    if (!isTest && version.state === "draft") {
      return sendError(response, 422, "DRAFT_REQUIRES_TEST_RUN", "Draft versions require test=true", id);
    }
    if (!isTest && version.state !== "published") return sendError(response, 409, "VERSION_STATE", "Only published versions can be executed", id);
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
      test: isTest,
      strategy: version.spec.spec.execution?.strategy ?? "auto",
      state: isTest ? "RUNNING" : "PENDING",
      stateReason: "",
      requestedBy: user.me.user_id,
      createdAt: now,
      startedAt: isTest ? now : null,
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
          state: isTest ? "RUNNING" : "PENDING", stateReason: "", jobId: null, validationId: null,
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
    const testValue = url.searchParams.get("test");
    if (testValue !== null && testValue !== "true" && testValue !== "false") {
      return sendError(response, 400, "TEST_FILTER_INVALID", "test must be true or false", id);
    }
    const testFilter = testValue === null ? undefined : testValue === "true";
    const source = data.workflowExecutions[tenant].filter((execution) =>
      (canReadTenant || execution.requestedBy === user.me.user_id)
      && (!workflowFilter || execution.workflowId === workflowFilter)
      && (!stateFilter || execution.state === stateFilter)
      && (testFilter === undefined || Boolean(execution.test) === testFilter));
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
