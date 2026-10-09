import type { components } from "@/lib/api/schema";
import { extractErrorEnvelope } from "@/lib/api/error-details";

export type NodeConfig = components["schemas"]["ClusterNodeConfig"];
export type NodeConfigView = components["schemas"]["NodeConfigView"];
export type NodeConfigWarning = components["schemas"]["NodeConfigWarning"];
export type NodeToken = components["schemas"]["NodeToken"];
export type NodeStatusList = components["schemas"]["NodeStatusList"];

export type IsolationMode = NodeConfig["isolation_mode"];
export type Mechanism = NodeConfig["tenant_exclusive_mechanism"];
export type Fstype = "nfs" | "nfs4";

export type MountRow = {
  id: string;
  name: string;
  fstype: Fstype;
  source: string;
  target: string;
  readOnly: boolean;
  extraOptions: string[];
};
export type TenantMountRow = MountRow & { tenant: string };
export type HookRow = {
  id: string;
  name: string;
  phase: "prolog" | "epilog";
  order: string;
  script: string;
};
export type NodeConfigDraft = {
  isolationMode: IsolationMode;
  mechanism: Mechanism;
  timeout: string;
  shared: MountRow[];
  tenantMounts: TenantMountRow[];
  tenantGroups: string[];
  hooks: HookRow[];
};

export type FieldIssue = { field: string; reason: string };

export const HOOK_TEMPLATE = "#!/bin/bash\nset -euo pipefail\n\n# Runs as root on every compute node. Do not call Slurm commands.\n";
export const MAX_HOOK_BYTES = 64 * 1024;
export const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,62}$/;
const sourcePattern = /^[A-Za-z0-9.-]+:\/[A-Za-z0-9._/-]*$/;
const targetPattern = /^[A-Za-z0-9._/-]+$/;
const reservedTargets = ["/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var", "/tmp", "/dev/shm"];
const plainOptions = new Set(["hard", "soft", "noatime", "nodiratime", "relatime", "nosuid", "nodev", "noexec", "nolock", "_netdev"]);
const intOptions: Record<string, [number, number] | undefined> = {
  timeo: [1, 6000], retrans: [0, 100], rsize: [1024, 1048576], wsize: [1024, 1048576],
  actimeo: [0, 3600], port: [1, 65535], nconnect: [1, 16],
};
const enumOptions: Record<string, string[] | undefined> = {
  nfsvers: ["3", "4", "4.0", "4.1", "4.2"],
  vers: ["3", "4", "4.0", "4.1", "4.2"],
  proto: ["tcp", "rdma"],
  sec: ["sys", "krb5", "krb5i", "krb5p"],
  lookupcache: ["all", "none", "pos", "positive"],
};
const accessOptions = new Set(["ro", "rw"]);
const hardenedOptions = new Set(["nosuid", "nodev"]);

let rowCounter = 0;
export function newRowId(prefix: string): string {
  rowCounter += 1;
  return `${prefix}-${String(rowCounter)}`;
}

export function emptyMount(readOnly: boolean): MountRow {
  return { id: newRowId("mount"), name: "", fstype: "nfs4", source: "", target: "", readOnly, extraOptions: [] };
}

export function emptyHook(): HookRow {
  return { id: newRowId("hook"), name: "", phase: "prolog", order: "50", script: HOOK_TEMPLATE };
}

function rowFromOptions(options: readonly string[] | undefined, defaultReadOnly: boolean, hardened: boolean) {
  const list = options ?? [];
  const readOnly = list.includes("ro") ? true : list.includes("rw") ? false : defaultReadOnly;
  const extraOptions = list.filter((option) =>
    !accessOptions.has(option) && !(hardened && hardenedOptions.has(option)));
  return { readOnly, extraOptions };
}

export function draftFromConfig(config: NodeConfig): NodeConfigDraft {
  const tenantMounts = config.tenant_mounts.map((mount): TenantMountRow => ({
    id: newRowId("tenant-mount"),
    tenant: mount.tenant,
    name: mount.name,
    fstype: mount.fstype,
    source: mount.source,
    target: mount.target,
    ...rowFromOptions(mount.options, false, true),
  }));
  return {
    isolationMode: config.isolation_mode,
    mechanism: config.tenant_exclusive_mechanism,
    timeout: String(config.mount_timeout_seconds),
    shared: config.shared_mounts.map((mount): MountRow => ({
      id: newRowId("mount"),
      name: mount.name,
      fstype: mount.fstype,
      source: mount.source,
      target: mount.target,
      ...rowFromOptions(mount.options, true, false),
    })),
    tenantMounts,
    tenantGroups: [...new Set(tenantMounts.map((mount) => mount.tenant))],
    hooks: config.hooks.map((hook): HookRow => ({
      id: newRowId("hook"),
      name: hook.name,
      phase: hook.phase,
      order: String(hook.order),
      script: hook.script,
    })),
  };
}

function optionList(row: MountRow): string[] {
  return [row.readOnly ? "ro" : "rw", ...row.extraOptions];
}

// Submission order is the draft order, so server field paths such as
// tenant_mounts[2].target index into the same arrays as the draft.
export function configFromDraft(draft: NodeConfigDraft): NodeConfig {
  return {
    isolation_mode: draft.isolationMode,
    tenant_exclusive_mechanism: draft.mechanism,
    mount_timeout_seconds: Number(draft.timeout),
    shared_mounts: draft.shared.map((row) => ({
      name: row.name.trim(), fstype: row.fstype, source: row.source.trim(), target: row.target.trim(), options: optionList(row),
    })),
    tenant_mounts: draft.tenantMounts.map((row) => ({
      tenant: row.tenant, name: row.name.trim(), fstype: row.fstype, source: row.source.trim(), target: row.target.trim(), options: optionList(row),
    })),
    hooks: draft.hooks.map((hook) => ({
      name: hook.name.trim(), phase: hook.phase, order: Number(hook.order), script: hook.script,
    })),
  };
}

export function isDirty(current: NodeConfigDraft, saved: NodeConfigDraft): boolean {
  return JSON.stringify(configFromDraft(current)) !== JSON.stringify(configFromDraft(saved))
    || JSON.stringify(current.tenantGroups) !== JSON.stringify(saved.tenantGroups);
}

export function nested(target: string, other: string): boolean {
  return target === other || target.startsWith(`${other}/`) || other.startsWith(`${target}/`);
}

export function validateExtraOption(option: string): string | null {
  if (accessOptions.has(option)) return "Use the read-only toggle instead of ro or rw.";
  if (plainOptions.has(option)) return null;
  const parts = option.split("=");
  if (parts.length !== 2) return `Option ${option} is not allowed.`;
  const [key, value] = parts as [string, string];
  const choices = enumOptions[key];
  if (choices) return choices.includes(value) ? null : `${key} must be one of ${choices.join(", ")}.`;
  const range = intOptions[key];
  if (range) {
    if (!/^[0-9]{1,9}$/.test(value)) return `${key} must be a positive integer.`;
    const number = Number(value);
    return number >= range[0] && number <= range[1] ? null : `${key} must be ${String(range[0])}..${String(range[1])}.`;
  }
  return `Option ${option} is not allowed.`;
}

function mountIssues(path: string, row: MountRow): FieldIssue[] {
  const issues: FieldIssue[] = [];
  const add = (field: string, reason: string) => issues.push({ field: `${path}.${field}`, reason });
  const name = row.name.trim();
  const source = row.source.trim();
  const target = row.target.trim();
  if (!NAME_PATTERN.test(name)) add("name", "Use lowercase letters, digits and dashes (up to 63 characters).");
  if (!sourcePattern.test(source) || source.length > 512 || source.split("/").includes("..")) {
    add("source", "Use host:/export/path with no .. segments.");
  }
  if (!target.startsWith("/") || !targetPattern.test(target) || target.length > 256 || target.split("/").includes("..")) {
    add("target", "Use an absolute path of letters, digits, . _ - and / with no .. segments.");
  } else if (target === "/" || target.endsWith("/")) {
    add("target", "Remove the trailing slash.");
  } else if (reservedTargets.some((root) => target === root || target.startsWith(`${root}/`))) {
    add("target", "This path is reserved for the operating system.");
  }
  for (const option of row.extraOptions) {
    const problem = validateExtraOption(option);
    if (problem) {
      add("options", problem);
      break;
    }
  }
  if (row.extraOptions.includes("hard") && row.extraOptions.includes("soft")) add("options", "hard and soft are mutually exclusive.");
  return issues;
}

// Mirrors internal/nodehooks.Validate for fast feedback; the server stays authoritative.
export function validateDraft(draft: NodeConfigDraft): FieldIssue[] {
  const issues: FieldIssue[] = [];
  const timeout = Number(draft.timeout);
  if (!Number.isInteger(timeout) || timeout < 5 || timeout > 300) {
    issues.push({ field: "mount_timeout_seconds", reason: "Use a whole number from 5 to 300." });
  }
  const sharedNames = new Set<string>();
  const sharedTargets: string[] = [];
  draft.shared.forEach((row, index) => {
    const path = `shared_mounts[${String(index)}]`;
    issues.push(...mountIssues(path, row));
    const name = row.name.trim();
    const target = row.target.trim();
    if (name && sharedNames.has(name)) issues.push({ field: `${path}.name`, reason: "Duplicate name." });
    sharedNames.add(name);
    if (target && sharedTargets.includes(target)) issues.push({ field: `${path}.target`, reason: "Duplicate target." });
    sharedTargets.push(target);
  });
  const tenantNames = new Set<string>();
  const targetsByTenant = new Map<string, string[]>();
  draft.tenantMounts.forEach((row, index) => {
    const path = `tenant_mounts[${String(index)}]`;
    issues.push(...mountIssues(path, row));
    const name = row.name.trim();
    const target = row.target.trim();
    const key = `${row.tenant}\u0000${name}`;
    if (name && tenantNames.has(key)) issues.push({ field: `${path}.name`, reason: "Duplicate name for this tenant." });
    tenantNames.add(key);
    const clash = sharedTargets.find((shared) => shared && target && nested(target, shared));
    if (clash) issues.push({ field: `${path}.target`, reason: `Must not equal or nest with shared mount ${clash}.` });
    const other = (targetsByTenant.get(row.tenant) ?? []).find((existing) => existing && target && nested(target, existing));
    if (other) issues.push({ field: `${path}.target`, reason: `Must not equal or nest with another target of this tenant: ${other}.` });
    targetsByTenant.set(row.tenant, [...(targetsByTenant.get(row.tenant) ?? []), target]);
  });
  const hookNames = new Set<string>();
  const hookOrders = new Set<string>();
  draft.hooks.forEach((hook, index) => {
    const path = `hooks[${String(index)}]`;
    const name = hook.name.trim();
    const order = Number(hook.order);
    if (!NAME_PATTERN.test(name)) issues.push({ field: `${path}.name`, reason: "Use lowercase letters, digits and dashes (up to 63 characters)." });
    if (hook.order.trim() === "" || !Number.isInteger(order) || order < 0 || order > 99) {
      issues.push({ field: `${path}.order`, reason: "Use a whole number from 0 to 99." });
    }
    if (!hook.script.startsWith("#!/bin/bash") && !hook.script.startsWith("#!/bin/sh")) {
      issues.push({ field: `${path}.script`, reason: "The script must start with #!/bin/bash or #!/bin/sh." });
    } else if (hook.script.includes("\u0000")) {
      issues.push({ field: `${path}.script`, reason: "The script must not contain NUL bytes." });
    }
    if (new TextEncoder().encode(hook.script).length > MAX_HOOK_BYTES) {
      issues.push({ field: `${path}.script`, reason: "The script must be at most 64 KiB." });
    }
    if (name && hookNames.has(name)) issues.push({ field: `${path}.name`, reason: "Duplicate name." });
    hookNames.add(name);
    const orderKey = `${hook.phase}/${hook.order.trim()}`;
    if (hookOrders.has(orderKey)) issues.push({ field: `${path}.order`, reason: "Order must be unique within a phase." });
    hookOrders.add(orderKey);
  });
  return issues;
}

export type GroupedIssues = {
  // rowId -> field -> message
  rows: Record<string, Record<string, string>>;
  // top-level fields such as isolation_mode or mount_timeout_seconds
  general: Record<string, string>;
  // messages that could not be attached to a row or field
  unmapped: string[];
};

const rowPath = /^(shared_mounts|tenant_mounts|hooks)\[(\d+)\](?:\.([A-Za-z_]+))?$/;

// Maps server (or client) field paths onto draft row ids.
export function groupIssues(issues: readonly FieldIssue[], draft: NodeConfigDraft): GroupedIssues {
  const grouped: GroupedIssues = { rows: {}, general: {}, unmapped: [] };
  for (const issue of issues) {
    const match = rowPath.exec(issue.field);
    if (match) {
      const list = match[1] === "shared_mounts" ? draft.shared : match[1] === "tenant_mounts" ? draft.tenantMounts : draft.hooks;
      const row = list[Number(match[2])] as { id: string } | undefined;
      if (row) {
        const groups: (string | undefined)[] = match;
        const field = groups[3] ?? "row";
        const existing = grouped.rows[row.id] ?? {};
        existing[field] ??= issue.reason;
        grouped.rows[row.id] = existing;
        continue;
      }
    }
    if (issue.field === "isolation_mode" || issue.field === "tenant_exclusive_mechanism" || issue.field === "mount_timeout_seconds") {
      grouped.general[issue.field] ??= issue.reason;
      continue;
    }
    grouped.unmapped.push(issue.field ? `${issue.field}: ${issue.reason}` : issue.reason);
  }
  return grouped;
}

export function issuesFromDetails(details: unknown): FieldIssue[] {
  if (!Array.isArray(details)) return [];
  return details.flatMap((detail): FieldIssue[] => {
    if (!detail || typeof detail !== "object") return [];
    const { field, reason } = detail as { field?: unknown; reason?: unknown };
    if (typeof reason !== "string" || !reason) return [];
    return [{ field: typeof field === "string" ? field : "", reason }];
  });
}

export type SaveOutcome =
  | { kind: "saved"; view: NodeConfigView }
  | { kind: "conflict" }
  | { kind: "invalid"; issues: FieldIssue[]; message: string }
  | { kind: "forbidden" }
  | { kind: "error"; message: string };

function isView(value: unknown): value is NodeConfigView {
  if (!value || typeof value !== "object") return false;
  const view = value as Partial<NodeConfigView>;
  return typeof view.version === "number" && typeof view.revision === "number"
    && !!view.config && typeof view.config === "object" && Array.isArray(view.warnings);
}

export function interpretSave(status: number, payload: unknown): SaveOutcome {
  if (status === 200 && isView(payload)) return { kind: "saved", view: payload };
  if (status === 409) return { kind: "conflict" };
  if (status === 403) return { kind: "forbidden" };
  const error = extractErrorEnvelope(payload);
  if (status === 422) {
    const issues = issuesFromDetails(error.details);
    return { kind: "invalid", issues, message: error.message ?? error.code ?? "The configuration was rejected." };
  }
  return { kind: "error", message: error.message ?? error.code ?? `Request failed (HTTP ${String(status)}).` };
}

export function shortSha(value: string): string {
  return value.slice(0, 12);
}

export function relativeTime(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return "never";
  const seconds = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
  if (Number.isNaN(seconds)) return "unknown";
  if (seconds < 60) return `${String(seconds)}s ago`;
  if (seconds < 3600) return `${String(Math.round(seconds / 60))}m ago`;
  if (seconds < 86_400) return `${String(Math.round(seconds / 3600))}h ago`;
  return `${String(Math.round(seconds / 86_400))}d ago`;
}

export function currentNodeCount(status: NodeStatusList): { current: number; total: number } {
  return { current: status.items.filter((node) => !node.stale).length, total: status.items.length };
}
