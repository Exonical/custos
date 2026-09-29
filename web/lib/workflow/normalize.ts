import type {
  ArraySpec,
  CustosWorkflow,
  Defaults,
  Execution,
  FanOut,
  Output,
  Parameter,
  Placement,
  Retry,
  SecretUse,
  Task,
  TaskResources,
} from "./spec";

export type WorkflowParameter = Omit<Parameter, "type"> & { type: Parameter["type"] | "number" };
export type NormalizedWorkflowSpec = Omit<CustosWorkflow, "spec"> & {
  spec: Omit<CustosWorkflow["spec"], "parameters" | "tasks" | "secrets"> & {
    parameters: Record<string, WorkflowParameter>;
    tasks: Task[];
    secrets: Record<string, SecretUse>;
  };
};

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function text(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function finite(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function strings(value: unknown): string[] | undefined {
  return Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === "string") : undefined;
}

function stringMap(value: unknown): Record<string, string> | undefined {
  const source = record(value);
  if (!source) return undefined;
  return Object.fromEntries(Object.entries(source).filter((entry): entry is [string, string] => typeof entry[1] === "string"));
}

function normalizeParameter(value: unknown): WorkflowParameter {
  const source = record(value) ?? {};
  const kind = source.type;
  const type: WorkflowParameter["type"] = kind === "integer" || kind === "number" || kind === "boolean" ? kind : "string";
  const result: WorkflowParameter = {
    type,
    required: typeof source.required === "boolean" ? source.required : false,
  };
  if (Object.hasOwn(source, "default") && validDefault(type, source.default)) result.default = source.default;
  const pattern = text(source.pattern);
  if (pattern !== undefined) result.pattern = pattern;
  const minimum = finite(source.minimum);
  if (minimum !== undefined) result.minimum = minimum;
  const maximum = finite(source.maximum);
  if (maximum !== undefined) result.maximum = maximum;
  if (Array.isArray(source.enum)) {
    result.enum = source.enum.filter((item) => item === null || ["string", "number", "boolean"].includes(typeof item));
  }
  return result;
}

function validDefault(type: WorkflowParameter["type"], value: unknown): boolean {
  if (type === "string") return typeof value === "string";
  if (type === "boolean") return typeof value === "boolean";
  return typeof value === "number" && Number.isFinite(value) && (type !== "integer" || Number.isInteger(value));
}

function normalizePlacement(value: unknown): Placement | undefined {
  const source = record(value);
  if (!source) return undefined;
  const placement: Placement = {};
  const cluster = text(source.cluster);
  if (cluster !== undefined) placement.cluster = cluster;
  const requirements = record(source.requirements);
  if (requirements) placement.requirements = requirements;
  return placement;
}

function normalizeResources(value: unknown): TaskResources | undefined {
  const source = record(value);
  if (!source) return undefined;
  const resources: TaskResources = {};
  for (const key of ["cpu", "nodes", "tasks", "tasksPerNode", "cpusPerTask"] as const) {
    const number = finite(source[key]);
    if (number !== undefined) resources[key] = number;
  }
  for (const key of ["memory", "walltime", "memoryPerNode", "constraints"] as const) {
    const string = text(source[key]);
    if (string !== undefined) resources[key] = string;
  }
  const exclusive = source.exclusive;
  if (typeof exclusive === "boolean") resources.exclusive = exclusive;
  const licenses = strings(source.licenses);
  if (licenses) resources.licenses = licenses;
  const gpu = record(source.gpu);
  const gpuCount = finite(gpu?.count);
  if (gpu && gpuCount !== undefined) {
    const type = text(gpu.type);
    resources.gpu = type === undefined ? { count: gpuCount } : { count: gpuCount, type };
  }
  return resources;
}

// Only these task kinds still change behavior; batch/mpi/gpu/array are
// deprecated aliases folded into `launch` and resources.
const specialKinds = ["shell", "condition", "stageIn", "stageOut", "interactive"] as const;
export type TaskKind = (typeof specialKinds)[number];

function setTaskKind(task: Task, kind: TaskKind) {
  // eslint-disable-next-line @typescript-eslint/no-deprecated -- `type` remains the field for special kinds
  task.type = kind;
}

/** Returns the behavior-changing kind (shell, condition, ...) or undefined for ordinary Slurm tasks. */
export function taskKind(task: Task): TaskKind | undefined {
  // eslint-disable-next-line @typescript-eslint/no-deprecated -- read the special kind, never the legacy aliases
  const value = task.type;
  return specialKinds.find((candidate) => candidate === value);
}

function normalizeTask(value: unknown): Task | undefined {
  const source = record(value);
  const name = text(source?.name);
  if (!source || !name) return undefined;
  const rawType = text(source.type);
  const task: Task = {
    name,
    // Legacy `type: mpi` means srun; the other legacy aliases mean sbatch.
    launch: source.launch === "srun" || source.launch === "sbatch" ? source.launch : rawType === "mpi" ? "srun" : "sbatch",
    dependsOn: strings(source.dependsOn) ?? [],
  };
  const kind = specialKinds.find((candidate) => candidate === rawType);
  if (kind) setTaskKind(task, kind);
  const textFields = ["when", "partition", "qos", "workingDirectory", "stdout", "stderr"] as const;
  for (const key of textFields) {
    const string = text(source[key]);
    if (string !== undefined) task[key] = string;
  }
  const dependencyFailure = source.onDependencyFailure;
  if (dependencyFailure === "fail" || dependencyFailure === "run") task.onDependencyFailure = dependencyFailure;
  const command = strings(source.command);
  if (command) task.command = command;
  const args = strings(source.args);
  if (args) task.args = args;
  const env = stringMap(source.env);
  if (env) task.env = env;
  const resources = normalizeResources(source.resources);
  if (resources) task.resources = resources;
  const placement = normalizePlacement(source.placement);
  if (placement) task.placement = placement;
  const fanOut = record(source.fanOut);
  if (fanOut) {
    const count = typeof fanOut.count === "string" ? fanOut.count : finite(fanOut.count);
    const from = text(fanOut.from);
    const normalized: FanOut = {};
    if (count !== undefined) normalized.count = count;
    if (from !== undefined) normalized.from = from;
    task.fanOut = normalized;
  }
  const retry = record(source.retry);
  const attempts = finite(retry?.attempts);
  if (retry && attempts !== undefined) {
    const on = strings(retry.on);
    const normalized: Retry = { attempts };
    if (on) normalized.on = on.filter((state): state is NonNullable<Retry["on"]>[number] => state === "FAILED" || state === "NODE_FAIL" || state === "TIMEOUT");
    task.retry = normalized;
  }
  const array = record(source.array);
  const arrayStart = finite(array?.start);
  const arrayEnd = finite(array?.end);
  if (array && arrayStart !== undefined && arrayEnd !== undefined) {
    const normalized: ArraySpec = { start: arrayStart, end: arrayEnd };
    const step = finite(array.step);
    const maxConcurrent = finite(array.maxConcurrent);
    if (step !== undefined) normalized.step = step;
    if (maxConcurrent !== undefined) normalized.maxConcurrent = maxConcurrent;
    task.array = normalized;
  }
  const script = record(source.script);
  const scriptRef = text(script?.ref);
  const language = script?.language;
  if (script && scriptRef && (language === "bash" || language === "sh" || language === "python" || language === "yaml" || language === "json")) {
    task.script = { ref: scriptRef, language };
  }
  const software = Array.isArray(source.software) ? source.software.flatMap((item) => {
    const entry = record(item);
    const softwareName = text(entry?.name);
    const softwareVersion = text(entry?.version);
    return softwareName && softwareVersion ? [{ name: softwareName, version: softwareVersion }] : [];
  }) : undefined;
  if (software) task.software = software;
  const outputs = record(source.outputs);
  if (outputs) {
    const normalized: Record<string, Output> = {};
    for (const [key, item] of Object.entries(outputs)) {
      const output = record(item);
      const path = text(output?.path);
      if (output?.type === "file" && path) normalized[key] = { type: "file", path };
    }
    task.outputs = normalized;
  }
  return task;
}

function normalizeSecrets(value: unknown): Record<string, SecretUse> {
  const source = record(value) ?? {};
  const secrets: Record<string, SecretUse> = {};
  for (const [handle, item] of Object.entries(source)) {
    const secret = record(item);
    const ref = text(secret?.ref);
    if (!secret || !ref || (secret.use !== "env" && secret.use !== "wrapped_token")) continue;
    const normalized: SecretUse = { ref, use: secret.use };
    const envName = text(secret.envName);
    if (envName !== undefined) normalized.envName = envName;
    secrets[handle] = normalized;
  }
  return secrets;
}

function normalizeDefaults(value: unknown): Defaults | undefined {
  const source = record(value);
  if (!source) return undefined;
  const defaults: Defaults = {};
  if (typeof source.account === "string" || source.account === null) defaults.account = source.account;
  for (const key of ["partition", "qos", "workingDirectory"] as const) {
    const string = text(source[key]);
    if (string !== undefined) defaults[key] = string;
  }
  const env = stringMap(source.env);
  if (env) defaults.env = env;
  return defaults;
}

function normalizeExecution(value: unknown): Execution | undefined {
  const source = record(value);
  if (!source) return undefined;
  const execution: Execution = {};
  if (source.strategy === "auto" || source.strategy === "engine" || source.strategy === "native") execution.strategy = source.strategy;
  if (source.failurePolicy === "fail" || source.failurePolicy === "continue") execution.failurePolicy = source.failurePolicy;
  return execution;
}

export function normalizeWorkflowSpec(value: unknown): NormalizedWorkflowSpec {
  const source = record(value) ?? {};
  const metadata = record(source.metadata) ?? {};
  const rawSpec = record(source.spec) ?? {};
  const rawParameters = record(rawSpec.parameters) ?? {};
  const parameters = Object.fromEntries(Object.entries(rawParameters).map(([key, item]) => [key, normalizeParameter(item)]));
  const tasks = Array.isArray(rawSpec.tasks) ? rawSpec.tasks.flatMap((item) => {
    const task = normalizeTask(item);
    return task ? [task] : [];
  }) : [];
  const labels = stringMap(metadata.labels);
  const placement = normalizePlacement(rawSpec.placement);
  const defaults = normalizeDefaults(rawSpec.defaults);
  const execution = normalizeExecution(rawSpec.execution);
  const result: NormalizedWorkflowSpec = {
    apiVersion: "custos.io/v1alpha1",
    kind: "Workflow",
    metadata: { name: text(metadata.name) ?? "workflow", ...(labels ? { labels } : {}) },
    spec: { parameters, secrets: normalizeSecrets(rawSpec.secrets), tasks },
  };
  if (placement) result.spec.placement = placement;
  if (defaults) result.spec.defaults = defaults;
  if (execution) result.spec.execution = execution;
  return result;
}
