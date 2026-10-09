// @ts-check
import { createHash, randomBytes, randomUUID } from "node:crypto";
import { gzipSync } from "node:zlib";

/** @typedef {import("../lib/api/schema").components["schemas"]["ClusterNodeConfig"]} NodeConfig */
/** @typedef {import("../lib/api/schema").components["schemas"]["NodeToken"]} NodeToken */
/** @typedef {{ field: string, reason: string }} Detail */
/**
 * @typedef {{
 *   config: NodeConfig, revision: number, version: number, updatedAt?: string,
 *   tokens: NodeToken[], nodes: { name: string, revision: number, sha: string, fetchedAt: string }[],
 * }} NodeState
 */

const nameRE = /^[a-z0-9][a-z0-9-]{0,62}$/;
const sourceRE = /^[A-Za-z0-9.-]+:\/[A-Za-z0-9._/-]*$/;
const targetRE = /^[A-Za-z0-9._/-]+$/;
const reserved = ["/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var", "/tmp", "/dev/shm"];
const plainOptions = new Set(["ro", "rw", "hard", "soft", "noatime", "nodiratime", "relatime", "nosuid", "nodev", "noexec", "nolock", "_netdev"]);
const modes = new Set(["namespace", "tenant_exclusive", "node_exclusive"]);

/** @type {Map<string, NodeState>} */
const states = new Map();

/** @returns {NodeConfig} */
function defaultConfig() {
  return {
    isolation_mode: "namespace",
    tenant_exclusive_mechanism: "mcs_label",
    mount_timeout_seconds: 30,
    shared_mounts: [],
    tenant_mounts: [],
    hooks: [],
  };
}

/** @param {NodeConfig} config @param {number} revision */
function bundleSha(config, revision) {
  return createHash("sha256").update(JSON.stringify(config)).update(String(revision)).digest("hex");
}

/** @param {string} clusterId @returns {NodeState} */
function stateFor(clusterId) {
  let state = states.get(clusterId);
  if (!state) {
    const config = defaultConfig();
    const now = Date.now();
    const current = bundleSha(config, 0);
    state = {
      config, revision: 0, version: 0, tokens: [],
      nodes: [
        { name: "c1", revision: 0, sha: current, fetchedAt: new Date(now - 90_000).toISOString() },
        { name: "c2", revision: 0, sha: current, fetchedAt: new Date(now - 3 * 60_000).toISOString() },
        { name: "c3", revision: 0, sha: "a".repeat(64), fetchedAt: new Date(now - 6 * 3_600_000).toISOString() },
      ],
    };
    states.set(clusterId, state);
  }
  return state;
}

/** @param {string} version @param {number} major @param {number} minor */
function slurmAtLeast(version, major, minor) {
  const match = /(\d+)\.(\d+)/.exec(version);
  if (!match) return false;
  const mj = Number(match[1]);
  const mn = Number(match[2]);
  return mj > major || (mj === major && mn >= minor);
}

/** @param {string} target @param {string} other */
function nested(target, other) {
  return target === other || target.startsWith(`${other}/`) || other.startsWith(`${target}/`);
}

/** @param {string} path @param {unknown} options @returns {Detail[]} */
function optionDetails(path, options) {
  /** @type {Detail[]} */
  const details = [];
  if (options === undefined) return details;
  if (!Array.isArray(options) || options.some((option) => typeof option !== "string")) {
    return [{ field: `${path}.options`, reason: "options must be a list of strings" }];
  }
  for (const option of /** @type {string[]} */ (options)) {
    const [key, value] = option.split("=", 2);
    const ok = plainOptions.has(option)
      || (["nfsvers", "vers"].includes(key ?? "") && ["3", "4", "4.0", "4.1", "4.2"].includes(value ?? ""))
      || (key === "proto" && ["tcp", "rdma"].includes(value ?? ""))
      || (key === "sec" && ["sys", "krb5", "krb5i", "krb5p"].includes(value ?? ""))
      || (key === "lookupcache" && ["all", "none", "pos", "positive"].includes(value ?? ""))
      || (["timeo", "retrans", "rsize", "wsize", "actimeo", "port", "nconnect"].includes(key ?? "") && /^[0-9]{1,9}$/.test(value ?? ""));
    if (!ok) details.push({ field: `${path}.options`, reason: `option ${option} is not allowed` });
  }
  return details;
}

/** @param {string} path @param {Record<string, unknown>} mount @returns {Detail[]} */
function mountDetails(path, mount) {
  /** @type {Detail[]} */
  const details = [];
  const name = typeof mount.name === "string" ? mount.name : "";
  const source = typeof mount.source === "string" ? mount.source : "";
  const target = typeof mount.target === "string" ? mount.target : "";
  if (!nameRE.test(name)) details.push({ field: `${path}.name`, reason: "name must match ^[a-z0-9][a-z0-9-]{0,62}$" });
  if (mount.fstype !== "nfs" && mount.fstype !== "nfs4") details.push({ field: `${path}.fstype`, reason: "fstype must be nfs or nfs4" });
  if (!sourceRE.test(source) || source.length > 512 || source.split("/").includes("..")) {
    details.push({ field: `${path}.source`, reason: "source must look like host:/path" });
  }
  if (!target.startsWith("/") || !targetRE.test(target) || target.length > 256 || target.split("/").includes("..")
    || (target.length > 1 && target.endsWith("/")) || target === "/") {
    details.push({ field: `${path}.target`, reason: "target must be an absolute path without .. or trailing slash" });
  } else if (reserved.some((root) => target === root || target.startsWith(`${root}/`))) {
    details.push({ field: `${path}.target`, reason: "target is under a reserved system path" });
  }
  details.push(...optionDetails(path, mount.options));
  return details;
}

/** @param {unknown} value @returns {Record<string, unknown> | null} */
function record(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? /** @type {Record<string, unknown>} */ (value) : null;
}

/**
 * @param {unknown} raw
 * @param {Set<string>} knownTenants
 * @returns {{ details: Detail[], code: string, config?: NodeConfig }}
 */
function validate(raw, knownTenants) {
  const input = record(raw);
  if (!input) return { details: [{ field: "config", reason: "config must be an object" }], code: "NODE_CONFIG_INVALID" };
  /** @type {Detail[]} */
  const configDetails = [];
  /** @type {Detail[]} */
  const mountIssues = [];
  /** @type {Detail[]} */
  const hookIssues = [];

  const mode = typeof input.isolation_mode === "string" && input.isolation_mode ? input.isolation_mode : "namespace";
  if (!modes.has(mode)) configDetails.push({ field: "isolation_mode", reason: "must be namespace, tenant_exclusive or node_exclusive" });
  const mechanism = typeof input.tenant_exclusive_mechanism === "string" && input.tenant_exclusive_mechanism ? input.tenant_exclusive_mechanism : "mcs_label";
  if (mechanism !== "mcs_label" && mechanism !== "user") configDetails.push({ field: "tenant_exclusive_mechanism", reason: "must be mcs_label or user" });
  const timeout = typeof input.mount_timeout_seconds === "number" && input.mount_timeout_seconds !== 0 ? input.mount_timeout_seconds : 30;
  if (!Number.isInteger(timeout) || timeout < 5 || timeout > 300) configDetails.push({ field: "mount_timeout_seconds", reason: "must be 5..300" });

  const sharedInput = Array.isArray(input.shared_mounts) ? input.shared_mounts : [];
  const tenantInput = Array.isArray(input.tenant_mounts) ? input.tenant_mounts : [];
  const hooksInput = Array.isArray(input.hooks) ? input.hooks : [];

  /** @type {NodeConfig["shared_mounts"]} */
  const shared = [];
  const sharedNames = new Set();
  /** @type {string[]} */
  const sharedTargets = [];
  sharedInput.forEach((entry, index) => {
    const path = `shared_mounts[${String(index)}]`;
    const mount = record(entry) ?? {};
    mountIssues.push(...mountDetails(path, mount));
    const name = String(mount.name ?? "");
    if (sharedNames.has(name)) mountIssues.push({ field: `${path}.name`, reason: "duplicate name" });
    sharedNames.add(name);
    const target = String(mount.target ?? "");
    if (sharedTargets.includes(target)) mountIssues.push({ field: `${path}.target`, reason: "duplicate target" });
    sharedTargets.push(target);
    shared.push({
      name, fstype: mount.fstype === "nfs" ? "nfs" : "nfs4", source: String(mount.source ?? ""), target,
      options: canonicalOptions(mount.options, "ro", false),
    });
  });

  /** @type {NodeConfig["tenant_mounts"]} */
  const tenantMounts = [];
  const tenantNames = new Set();
  /** @type {Map<string, string[]>} */
  const byTenant = new Map();
  tenantInput.forEach((entry, index) => {
    const path = `tenant_mounts[${String(index)}]`;
    const mount = record(entry) ?? {};
    const tenant = String(mount.tenant ?? "").toLowerCase();
    if (!knownTenants.has(tenant)) mountIssues.push({ field: `${path}.tenant`, reason: "tenant not found or not assigned to this cluster" });
    mountIssues.push(...mountDetails(path, mount));
    const name = String(mount.name ?? "");
    const key = `${tenant}\u0000${name}`;
    if (tenantNames.has(key)) mountIssues.push({ field: `${path}.name`, reason: "duplicate name for this tenant" });
    tenantNames.add(key);
    const target = String(mount.target ?? "");
    for (const other of sharedTargets) {
      if (other && nested(target, other)) mountIssues.push({ field: `${path}.target`, reason: `must not equal or nest with shared mount target ${other}` });
    }
    for (const other of byTenant.get(tenant) ?? []) {
      if (nested(target, other)) mountIssues.push({ field: `${path}.target`, reason: `must not equal or nest with another target of the same tenant: ${other}` });
    }
    byTenant.set(tenant, [...(byTenant.get(tenant) ?? []), target]);
    tenantMounts.push({
      tenant, name, fstype: mount.fstype === "nfs" ? "nfs" : "nfs4", source: String(mount.source ?? ""), target,
      options: canonicalOptions(mount.options, "rw", true),
    });
  });

  /** @type {NodeConfig["hooks"]} */
  const hooks = [];
  const hookNames = new Set();
  const hookOrders = new Set();
  hooksInput.forEach((entry, index) => {
    const path = `hooks[${String(index)}]`;
    const hook = record(entry) ?? {};
    const name = String(hook.name ?? "");
    const script = typeof hook.script === "string" ? hook.script : "";
    const order = typeof hook.order === "number" ? hook.order : -1;
    if (!nameRE.test(name)) hookIssues.push({ field: `${path}.name`, reason: "name must match ^[a-z0-9][a-z0-9-]{0,62}$" });
    if (hook.phase !== "prolog" && hook.phase !== "epilog") hookIssues.push({ field: `${path}.phase`, reason: "phase must be prolog or epilog" });
    if (!Number.isInteger(order) || order < 0 || order > 99) hookIssues.push({ field: `${path}.order`, reason: "order must be 0..99" });
    if (!script.startsWith("#!/bin/bash") && !script.startsWith("#!/bin/sh")) {
      hookIssues.push({ field: `${path}.script`, reason: "script must start with #!/bin/bash or #!/bin/sh" });
    }
    if (script.length > 65_536) hookIssues.push({ field: `${path}.script`, reason: "script exceeds 64 KiB" });
    if (hookNames.has(name)) hookIssues.push({ field: `${path}.name`, reason: "duplicate name" });
    hookNames.add(name);
    const orderKey = `${String(hook.phase)}/${String(order)}`;
    if (hookOrders.has(orderKey)) hookIssues.push({ field: `${path}.order`, reason: "order must be unique within a phase" });
    hookOrders.add(orderKey);
    hooks.push({ name, phase: hook.phase === "epilog" ? "epilog" : "prolog", order, script });
  });

  const details = [...configDetails, ...mountIssues, ...hookIssues];
  if (details.length > 0) {
    const categories = [configDetails.length > 0, mountIssues.length > 0, hookIssues.length > 0].filter(Boolean).length;
    const code = categories > 1 || configDetails.length > 0 ? "NODE_CONFIG_INVALID"
      : mountIssues.length > 0 ? "NODE_MOUNT_INVALID" : "NODE_HOOK_INVALID";
    return { details, code };
  }
  return {
    details, code: "",
    config: {
      isolation_mode: /** @type {NodeConfig["isolation_mode"]} */ (mode),
      tenant_exclusive_mechanism: mechanism === "user" ? "user" : "mcs_label",
      mount_timeout_seconds: timeout, shared_mounts: shared, tenant_mounts: tenantMounts, hooks,
    },
  };
}

/** @param {unknown} options @param {"ro" | "rw"} defaultAccess @param {boolean} harden */
function canonicalOptions(options, defaultAccess, harden) {
  const set = new Set(Array.isArray(options) ? options.filter((option) => typeof option === "string" && option) : []);
  if (!set.has("ro") && !set.has("rw")) set.add(defaultAccess);
  if (harden) {
    set.add("nosuid");
    set.add("nodev");
  }
  return [...set].sort();
}

/** @param {NodeConfig} config @param {string} slurmVersion */
function warnings(config, slurmVersion) {
  /** @type {{ code: "SHARED_SERVICE_USER" | "NAMESPACE_REQUIRES_SLURM_25_11" | "TENANT_EXCLUSIVE_SHARED_USER", message: string }[]} */
  const out = [];
  if (config.isolation_mode === "namespace" && !slurmAtLeast(slurmVersion, 25, 11)) {
    out.push({ code: "NAMESPACE_REQUIRES_SLURM_25_11", message: `namespace mode needs Slurm 25.11 or newer with namespace/linux; cluster reports ${slurmVersion || "unknown"}` });
  }
  const tenants = new Set(config.tenant_mounts.map((mount) => mount.tenant));
  if (tenants.size >= 2) {
    out.push({
      code: "SHARED_SERVICE_USER",
      message: `bindings of ${String(tenants.size)} tenants share the Slurm service user "custos"; jobs of different tenants run as the same OS user and tenant NFS isolation cannot rely on file ownership`,
    });
  }
  if (config.isolation_mode === "tenant_exclusive" && config.tenant_exclusive_mechanism === "user" && tenants.size >= 2) {
    out.push({
      code: "TENANT_EXCLUSIVE_SHARED_USER",
      message: "tenant_exclusive with mechanism user needs one Slurm user per tenant, but tenants share the service user; jobs of different tenants can co-locate, the prolog will refuse them and drain the node",
    });
  }
  return out;
}

/** @param {{name: string, data: string}[]} files */
function tarGz(files) {
  /** @type {Buffer[]} */
  const parts = [];
  for (const file of files) {
    const body = Buffer.from(file.data, "utf8");
    const header = Buffer.alloc(512);
    header.write(file.name, 0, 100, "utf8");
    header.write("0000644\0", 100, 8, "ascii");
    header.write("0000000\0", 108, 8, "ascii");
    header.write("0000000\0", 116, 8, "ascii");
    header.write(`${body.length.toString(8).padStart(11, "0")}\0`, 124, 12, "ascii");
    header.write("00000000000\0", 136, 12, "ascii");
    header.write("        ", 148, 8, "ascii");
    header.write("0", 156, 1, "ascii");
    header.write("ustar\0", 257, 6, "ascii");
    header.write("00", 263, 2, "ascii");
    let sum = 0;
    for (const byte of header) sum += byte;
    header.write(`${sum.toString(8).padStart(6, "0")}\0 `, 148, 8, "ascii");
    parts.push(header, body, Buffer.alloc((512 - (body.length % 512)) % 512));
  }
  parts.push(Buffer.alloc(1024));
  return gzipSync(Buffer.concat(parts));
}

/**
 * Handles the platform-admin node hooks routes of the mock API.
 * Returns true when the request was handled.
 * @param {{
 *   request: import("node:http").IncomingMessage,
 *   response: import("node:http").ServerResponse,
 *   url: URL,
 *   id: string,
 *   data: ReturnType<typeof import("./data.mjs").createMockData>,
 *   isAdmin: boolean,
 *   send: (response: import("node:http").ServerResponse, status: number, body: unknown) => void,
 *   sendError: (response: import("node:http").ServerResponse, status: number, code: string, message: string, requestId: string) => void,
 *   sendEmpty: (response: import("node:http").ServerResponse, status: number) => void,
 *   readJsonBody: (request: import("node:http").IncomingMessage) => Promise<{ value: unknown }>,
 * }} context
 */
export async function handleNodeHooks(context) {
  const { request, response, url, id, data, isAdmin, send, sendError, sendEmpty, readJsonBody } = context;
  const tenantList = url.pathname === "/api/v1/tenants";
  const assignments = url.pathname.match(/^\/api\/v1\/clusters\/([^/]+)\/tenants$/);
  const route = url.pathname.match(/^\/api\/v1\/clusters\/([^/]+)\/(node-config(?:\/bundle)?|node-tokens(?:\/([^/]+))?|node-status)$/);
  if (!tenantList && !assignments && !route) return false;
  if (request.method === "OPTIONS") return false;
  if (!isAdmin) {
    sendError(response, 403, "FORBIDDEN", "Requires platform-admin", id);
    return true;
  }

  /** @type {Map<string, { id: string, slug: string, name: string }>} */
  const tenants = new Map();
  for (const user of Object.values(data.users)) {
    for (const membership of user.me.memberships) {
      tenants.set(membership.tenant_id, { id: membership.tenant_id, slug: membership.slug, name: membership.name });
    }
  }
  const stamp = new Date().toISOString();

  if (tenantList && request.method === "GET") {
    send(response, 200, {
      items: [...tenants.values()].map((tenant) => ({
        ...tenant, state: "active", settings: {}, version: 1, created_at: stamp, updated_at: stamp,
      })),
    });
    return true;
  }
  const clusterRef = decodeURIComponent((assignments ?? route)?.[1] ?? "");
  const cluster = Object.values(data.platformClusters).find((item) => item.id === clusterRef || item.name === clusterRef);
  if (!cluster) {
    sendError(response, 404, "NOT_FOUND", "Cluster not found", id);
    return true;
  }
  const summary = Object.values(data.clusters).flat().find((item) => item.id === cluster.id);
  const slurmVersion = summary?.slurm_version ?? "";

  if (assignments && request.method === "GET") {
    const assigned = Object.entries(data.clusters)
      .filter(([, list]) => list.some((item) => item.id === cluster.id))
      .map(([slug]) => [...tenants.values()].find((tenant) => tenant.slug === slug))
      .filter((tenant) => tenant !== undefined);
    send(response, 200, {
      items: assigned.map((tenant) => ({
        cluster_id: cluster.id, tenant_id: tenant.id, source: "manual", defaults: {}, created_at: stamp, updated_at: stamp,
      })),
    });
    return true;
  }
  if (!route) return false;

  const state = stateFor(cluster.id);
  const assignedTenantIds = new Set(
    Object.entries(data.clusters)
      .filter(([, list]) => list.some((item) => item.id === cluster.id))
      .flatMap(([slug]) => [...tenants.values()].filter((tenant) => tenant.slug === slug).map((tenant) => tenant.id)),
  );
  const view = () => ({
    cluster_id: cluster.id,
    config: state.config,
    revision: state.revision,
    content_sha256: createHash("sha256").update(JSON.stringify(state.config)).digest("hex"),
    version: state.version,
    ...(state.updatedAt ? { updated_at: state.updatedAt, updated_by: data.users.admin.me.user_id } : {}),
    warnings: warnings(state.config, slurmVersion),
  });
  const segment = route[2] ?? "";

  if (segment === "node-config" && request.method === "GET") {
    send(response, 200, view());
    return true;
  }
  if (segment === "node-config" && request.method === "PUT") {
    /** @type {Record<string, unknown> | null} */
    let body;
    try {
      body = record((await readJsonBody(request)).value);
    } catch {
      sendError(response, 400, "MALFORMED", "Invalid node config body", id);
      return true;
    }
    if (!body || typeof body.version !== "number" || !Number.isInteger(body.version)) {
      sendError(response, 422, "VALIDATION", "version is required", id);
      return true;
    }
    if (body.version !== state.version) {
      sendError(response, 409, "VERSION_CONFLICT", "node configuration changed since it was loaded", id);
      return true;
    }
    const result = validate(body.config, assignedTenantIds);
    if (!result.config) {
      send(response, 422, { error: { code: result.code, message: "node configuration is invalid", request_id: id, details: result.details } });
      return true;
    }
    state.config = result.config;
    state.revision += 1;
    state.version += 1;
    state.updatedAt = new Date().toISOString();
    send(response, 200, view());
    return true;
  }
  if (segment === "node-config/bundle" && request.method === "GET") {
    const current = view();
    const tsv = [
      `meta\trevision\t${String(state.revision)}`,
      `meta\tmode\t${state.config.isolation_mode}`,
      `meta\ttimeout\t${String(state.config.mount_timeout_seconds)}`,
      ...state.config.shared_mounts.map((mount) => `shared\t${mount.name}\t${mount.fstype}\t${mount.source}\t${mount.target}\t${(mount.options ?? []).join(",")}`),
      "",
    ].join("\n");
    const archive = tarGz([
      { name: "README.md", data: `# Mock Custos node bundle: cluster ${cluster.name}, revision ${String(state.revision)}\n` },
      { name: "etc/custos/node/mounts.tsv", data: tsv },
    ]);
    response.writeHead(200, {
      "content-type": "application/gzip",
      "content-disposition": `attachment; filename="custos-node-${cluster.name}-r${String(state.revision)}.tar.gz"`,
      etag: `"${current.content_sha256}"`,
      "x-custos-revision": String(state.revision),
      "cache-control": "no-store",
    });
    response.end(archive);
    return true;
  }
  if (segment === "node-tokens" && request.method === "GET") {
    send(response, 200, { items: state.tokens });
    return true;
  }
  if (segment === "node-tokens" && request.method === "POST") {
    /** @type {Record<string, unknown> | null} */
    let body;
    try {
      body = record((await readJsonBody(request)).value);
    } catch {
      sendError(response, 400, "MALFORMED", "Invalid token body", id);
      return true;
    }
    const name = typeof body?.name === "string" ? body.name : "";
    if (!nameRE.test(name)) {
      send(response, 422, {
        error: {
          code: "NODE_TOKEN_INVALID", message: "token name is invalid", request_id: id,
          details: [{ field: "name", reason: "name must match ^[a-z0-9][a-z0-9-]{0,62}$" }],
        },
      });
      return true;
    }
    /** @type {NodeToken} */
    const token = {
      id: randomUUID(), cluster_id: cluster.id, name, created_at: new Date().toISOString(),
      created_by: data.users.admin.me.user_id,
    };
    state.tokens.push(token);
    send(response, 201, { ...token, token: `cnt_${randomBytes(32).toString("base64url")}` });
    return true;
  }
  if (segment.startsWith("node-tokens/") && request.method === "DELETE") {
    const token = state.tokens.find((item) => item.id === route[3]);
    if (!token) {
      sendError(response, 404, "NOT_FOUND", "token not found", id);
      return true;
    }
    token.revoked_at ??= new Date().toISOString();
    sendEmpty(response, 204);
    return true;
  }
  if (segment === "node-status" && request.method === "GET") {
    const sha = bundleSha(state.config, state.revision);
    send(response, 200, {
      current_revision: state.revision,
      current_bundle_sha256: sha,
      items: state.nodes.map((node) => ({
        node_name: node.name, revision: node.revision, bundle_sha256: node.sha, fetched_at: node.fetchedAt,
        stale: node.sha !== sha,
      })),
    });
    return true;
  }
  sendError(response, 405, "METHOD_NOT_ALLOWED", "Method not allowed", id);
  return true;
}
