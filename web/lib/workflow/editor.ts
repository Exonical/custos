import { isNode, isScalar, isSeq, parseDocument, type Document } from "yaml";
import type { ImagePullSecret, SecretUse, ServiceTask } from "./spec";

export type WorkflowYamlDocument = Document.Parsed;

export type WorkflowYamlParseError = {
  message: string;
  line: number;
  column: number;
};

export type WorkflowYamlParseResult =
  | { document: WorkflowYamlDocument; error: null }
  | { document: null; error: WorkflowYamlParseError };

export type WorkflowYamlLocation = {
  startLineNumber: number;
  startColumn: number;
  endLineNumber: number;
  endColumn: number;
  taskName?: string;
};

export type WorkflowTaskArrayType = "default" | "multinode" | "array";
export type WorkflowMemoryMode = "perNode" | "perCpu";
export type WorkflowPosition = { x: number; y: number };
export type WorkflowPositionMap = Record<string, WorkflowPosition>;
export type WorkflowMemoryUnit = "MiB" | "GiB" | "MB" | "GB";

export function parseWorkflowYaml(source: string): WorkflowYamlParseResult {
  const document = parseDocument(source);
  if (document.errors.length > 0) {
    const error = document.errors[0];
    const position = error.linePos?.[0] ?? { line: 1, col: 1 };
    return {
      document: null,
      error: { message: error.message, line: position.line, column: position.col },
    };
  }
  return { document, error: null };
}

function taskSequence(document: WorkflowYamlDocument) {
  const node = document.getIn(["spec", "tasks"], true);
  return isSeq(node) ? node : null;
}

function taskNameAt(document: WorkflowYamlDocument, index: number): string | undefined {
  const name = document.getIn(["spec", "tasks", index, "name"]);
  return typeof name === "string" ? name : undefined;
}

function taskIndex(document: WorkflowYamlDocument, name: string): number {
  const tasks = taskSequence(document);
  if (!tasks) return -1;
  return tasks.items.findIndex((_, index) => taskNameAt(document, index) === name);
}

export function renameWorkflowTask(source: string, oldName: string, newName: string): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return source;
  const document = parsed.document;
  const index = taskIndex(document, oldName);
  const tasks = taskSequence(document);
  if (index < 0 || !tasks) return source;

  document.setIn(["spec", "tasks", index, "name"], newName);
  tasks.items.forEach((_, otherIndex) => {
    if (otherIndex === index) return;
    const dependsPath = ["spec", "tasks", otherIndex, "dependsOn"];
    const dependencies = document.getIn(dependsPath, true);
    if (!isSeq(dependencies)) return;
    for (const item of dependencies.items) {
      if (isScalar(item) && item.value === oldName) item.value = newName;
    }
  });
  return document.toString();
}

export function deleteWorkflowTask(source: string, taskName: string): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return source;
  const document = parsed.document;
  const index = taskIndex(document, taskName);
  const tasks = taskSequence(document);
  if (index < 0 || !tasks) return source;
  tasks.items.splice(index, 1);

  tasks.items.forEach((_, taskIndexValue) => {
    const dependsPath = ["spec", "tasks", taskIndexValue, "dependsOn"];
    const dependencies = document.getIn(dependsPath, true);
    if (!isSeq(dependencies)) return;
    dependencies.items = dependencies.items.filter((item) => !isScalar(item) || item.value !== taskName);
    if (dependencies.items.length === 0) document.deleteIn(dependsPath);
  });
  return document.toString();
}

export function addWorkflowTask(source: string): { yaml: string; taskName: string } {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return { yaml: source, taskName: "" };
  const document = parsed.document;
  let tasks = taskSequence(document);
  if (!tasks) {
    document.setIn(["spec", "tasks"], []);
    tasks = taskSequence(document);
  }
  if (!tasks) return { yaml: source, taskName: "" };

  const names = new Set(tasks.items.flatMap((_, index) => {
    const name = taskNameAt(document, index);
    return name ? [name] : [];
  }));
  let suffix = 1;
  while (names.has(`task-${String(suffix)}`)) suffix += 1;
  const taskName = `task-${String(suffix)}`;
  tasks.items.push(document.createNode({
    name: taskName,
    launch: "sbatch",
    resources: { cpu: 1, memory: "1Gi", walltime: "5m" },
    script: "#!/bin/bash\n",
  }));
  return { yaml: document.toString(), taskName };
}

export function setWorkflowTaskField(
  source: string,
  taskName: string,
  fieldPath: readonly (string | number)[],
  value: unknown,
): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return source;
  const document = parsed.document;
  const index = taskIndex(document, taskName);
  if (index < 0 || fieldPath.length === 0) return source;

  const path = ["spec", "tasks", index, ...fieldPath];
  const empty = value === undefined || value === null || value === "" ||
    (Array.isArray(value) && value.length === 0);
  if (fieldPath[0] === "image" && empty) {
    document.deleteIn(["spec", "tasks", index, "image"]);
  } else if (fieldPath[0] === "resources" && fieldPath[1] === "gpu" && empty) {
    document.deleteIn(["spec", "tasks", index, "resources", "gpu"]);
  } else if (empty) {
    document.deleteIn(path);
  } else {
    document.setIn(path, value);
  }
  return document.toString();
}

export function setWorkflowTaskType(source: string, taskName: string, type: WorkflowTaskArrayType): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return source;
  const document = parsed.document;
  const index = taskIndex(document, taskName);
  if (index < 0) return source;
  const base = ["spec", "tasks", index];

  if (type === "multinode") {
    document.deleteIn([...base, "array"]);
    if (document.getIn([...base, "type"]) === "array") document.deleteIn([...base, "type"]);
    for (const field of ["nodes", "tasks", "tasksPerNode", "cpusPerTask"]) {
      document.deleteIn([...base, "resources", field]);
    }
    const resources = document.getIn([...base, "resources"]);
    if (resources && typeof resources === "object" && Object.keys(resources).length === 0) {
      document.deleteIn([...base, "resources"]);
    }
    document.setIn([...base, "multinode"], { nodes: 2, implementation: "openmpi" });
    document.setIn([...base, "launch"], "srun");
  } else if (type === "array") {
    document.deleteIn([...base, "multinode"]);
    document.setIn([...base, "array"], { start: 0, end: 9 });
  } else {
    document.deleteIn([...base, "multinode"]);
    document.deleteIn([...base, "array"]);
    if (document.getIn([...base, "type"]) === "array") document.deleteIn([...base, "type"]);
  }
  return document.toString();
}

export function setTaskService(source: string, taskName: string, service: ServiceTask = { autoStop: true }): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return source;
  const document = parsed.document;
  const index = taskIndex(document, taskName);
  if (index < 0) return source;
  const base = ["spec", "tasks", index];
  for (const field of ["array", "fanOut", "retry", "when", "outputs"]) {
    document.deleteIn([...base, field]);
  }
  if (document.getIn([...base, "type"]) === "array") document.deleteIn([...base, "type"]);
  document.setIn([...base, "service"], service);
  return document.toString();
}

export function removeTaskService(source: string, taskName: string): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return source;
  const index = taskIndex(parsed.document, taskName);
  if (index < 0) return source;
  parsed.document.deleteIn(["spec", "tasks", index, "service"]);
  return parsed.document.toString();
}

export function setPullSecret(
  source: string,
  taskName: string,
  pullSecret: Partial<ImagePullSecret>,
): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return source;
  const document = parsed.document;
  const index = taskIndex(document, taskName);
  if (index < 0 || !document.getIn(["spec", "tasks", index, "image"], true)) return source;
  const value: Partial<ImagePullSecret> = {};
  for (const key of ["username", "usernameSecret", "passwordSecret"] as const) {
    if (typeof pullSecret[key] === "string") value[key] = pullSecret[key];
  }
  const path = ["spec", "tasks", index, "image", "pullSecret"];
  if (Object.keys(value).length === 0) document.deleteIn(path);
  else document.setIn(path, value);
  return document.toString();
}

export function clearPullSecret(source: string, taskName: string): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return source;
  const index = taskIndex(parsed.document, taskName);
  if (index < 0) return source;
  parsed.document.deleteIn(["spec", "tasks", index, "image", "pullSecret"]);
  return parsed.document.toString();
}

export function setMemoryMode(
  source: string,
  taskName: string,
  mode: WorkflowMemoryMode,
  value?: string,
): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return source;
  const document = parsed.document;
  const index = taskIndex(document, taskName);
  if (index < 0) return source;
  const resourcesPath = ["spec", "tasks", index, "resources"];
  const selectedField = mode === "perCpu" ? "memoryPerCpu" : "memory";
  const stringAt = (field: string): string | undefined => {
    const node = document.getIn([...resourcesPath, field], true);
    return isScalar(node) && typeof node.value === "string" ? node.value : undefined;
  };
  const currentValue = value ?? stringAt("memoryPerCpu") ?? stringAt("memory") ?? stringAt("memoryPerNode");
  for (const field of ["memory", "memoryPerCpu", "memoryPerNode"]) {
    document.deleteIn([...resourcesPath, field]);
  }
  if (typeof currentValue === "string" && currentValue !== "") {
    document.setIn([...resourcesPath, selectedField], currentValue);
  }
  return document.toString();
}

export function setSecretHandle(source: string, handle: string, secret: SecretUse): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document || !handle.trim()) return source;
  const value: SecretUse = { ref: secret.ref, use: secret.use };
  if (secret.use === "env" && secret.envName) value.envName = secret.envName;
  parsed.document.setIn(["spec", "secrets", handle], value);
  return parsed.document.toString();
}

export function removeSecretHandle(source: string, handle: string): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document || !handle) return source;
  parsed.document.deleteIn(["spec", "secrets", handle]);
  return parsed.document.toString();
}

export function normalizeServiceEnvName(taskName: string): string {
  let name = "";
  for (const character of taskName) {
    if (character >= "a" && character <= "z") name += String.fromCharCode(character.charCodeAt(0) - 32);
    else if ((character >= "A" && character <= "Z") || (character >= "0" && character <= "9")) name += character;
    else name += "_";
  }
  return name;
}

export function addWorkflowDependency(
  source: string,
  targetTaskName: string,
  dependencyName: string,
): { yaml: string; error?: "self" | "duplicate" | "cycle" | "missing-task" } {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return { yaml: source, error: "missing-task" };
  const document = parsed.document;
  const targetIndex = taskIndex(document, targetTaskName);
  const dependencyIndex = taskIndex(document, dependencyName);
  if (targetIndex < 0 || dependencyIndex < 0) return { yaml: source, error: "missing-task" };
  if (targetTaskName === dependencyName) return { yaml: source, error: "self" };

  const dependenciesFor = (name: string): string[] => {
    const index = taskIndex(document, name);
    const value = index < 0 ? undefined : document.getIn(["spec", "tasks", index, "dependsOn"], true);
    return isSeq(value)
      ? value.items.flatMap((entry) => isScalar(entry) && typeof entry.value === "string" ? [entry.value] : [])
      : [];
  };
  const existing = dependenciesFor(targetTaskName);
  if (existing.includes(dependencyName)) return { yaml: source, error: "duplicate" };

  const reachesTarget = (current: string, visited: Set<string>): boolean => {
    if (current === targetTaskName) return true;
    if (visited.has(current)) return false;
    visited.add(current);
    return dependenciesFor(current).some((dependency) => reachesTarget(dependency, visited));
  };
  if (reachesTarget(dependencyName, new Set())) return { yaml: source, error: "cycle" };

  document.setIn(["spec", "tasks", targetIndex, "dependsOn"], [...existing, dependencyName]);
  return { yaml: document.toString() };
}

export function removeWorkflowDependency(source: string, targetTaskName: string, dependencyName: string): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return source;
  const document = parsed.document;
  const targetIndex = taskIndex(document, targetTaskName);
  if (targetIndex < 0) return source;
  const path = ["spec", "tasks", targetIndex, "dependsOn"];
  const current = document.getIn(path, true);
  if (!isSeq(current)) return source;
  const dependencies = current.items.filter((entry) => !isScalar(entry) || entry.value !== dependencyName);
  if (dependencies.length === current.items.length) return source;
  if (dependencies.length === 0) document.deleteIn(["spec", "tasks", targetIndex, "dependsOn"]);
  else current.items = dependencies;
  return document.toString();
}

export function setWorkflowDocumentField(source: string, path: readonly (string | number)[], value: unknown): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document || path.length === 0) return source;
  const empty = value === undefined || value === null || value === "" ||
    (Array.isArray(value) && value.length === 0);
  if (empty) parsed.document.deleteIn(path);
  else parsed.document.setIn(path, value);
  return parsed.document.toString();
}

export function setWorkflowDefault(source: string, key: string, value: unknown): string {
  return setWorkflowDocumentField(source, ["spec", "defaults", key], value);
}

export function setWorkflowLabel(source: string, key: string, value: string): string {
  return setWorkflowDocumentField(source, ["metadata", "labels", key], value);
}

export function setWorkflowEnvironmentVariable(
  source: string,
  taskName: string | null,
  name: string,
  value: string | undefined,
): string {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document || !name) return source;
  const document = parsed.document;
  const index = taskName === null ? -1 : taskIndex(document, taskName);
  if (taskName !== null && index < 0) return source;
  const prefix: (string | number)[] = taskName === null
    ? ["spec", "defaults", "env"]
    : ["spec", "tasks", index, "env"];
  const environment = document.getIn(prefix, true);
  if (isSeq(environment)) {
    const sequence = environment;
    const matching = sequence.items.flatMap((item, index) => {
      if (!isScalar(item) || typeof item.value !== "string") return [];
      const separator = item.value.indexOf("=");
      return separator > 0 && item.value.slice(0, separator) === name ? [index] : [];
    });
    if (value === undefined) {
      for (const index of matching.reverse()) sequence.items.splice(index, 1);
    } else if (matching.length > 0) {
      const item = sequence.items[matching[0]];
      if (isScalar(item)) item.value = `${name}=${value}`;
      for (const index of matching.slice(1).reverse()) sequence.items.splice(index, 1);
    } else {
      sequence.items.push(document.createNode(`${name}=${value}`));
    }
    return document.toString();
  }
  const variablePath = [...prefix, name];
  if (value === undefined) document.deleteIn(variablePath);
  else document.setIn(variablePath, value);
  return document.toString();
}

export function layoutPositionsFromValue(layout: unknown): WorkflowPositionMap {
  if (!layout || typeof layout !== "object" || Array.isArray(layout)) return {};
  const nodes = (layout as { nodes?: unknown }).nodes;
  if (Array.isArray(nodes)) {
    return Object.fromEntries(nodes.flatMap((entry) => {
      if (!entry || typeof entry !== "object") return [];
      const value = entry as { taskName?: unknown; name?: unknown; id?: unknown; position?: unknown; x?: unknown; y?: unknown };
      const taskName = typeof value.taskName === "string" ? value.taskName
        : typeof value.name === "string" ? value.name
          : typeof value.id === "string" ? value.id
            : "";
      const position = value.position && typeof value.position === "object" ? value.position as { x?: unknown; y?: unknown } : value;
      return taskName && typeof position.x === "number" && Number.isFinite(position.x) &&
        typeof position.y === "number" && Number.isFinite(position.y)
        ? [[taskName, { x: position.x, y: position.y }]]
        : [];
    }));
  }
  if (!nodes || typeof nodes !== "object") return {};
  return Object.fromEntries(Object.entries(nodes).flatMap(([name, position]) => {
    if (!position || typeof position !== "object" || Array.isArray(position)) return [];
    const candidate = position as { x?: unknown; y?: unknown };
    return typeof candidate.x === "number" && Number.isFinite(candidate.x) &&
      typeof candidate.y === "number" && Number.isFinite(candidate.y)
      ? [[name, { x: candidate.x, y: candidate.y }]]
      : [];
  }));
}

export function renameWorkflowTaskLayout(
  positions: WorkflowPositionMap,
  oldName: string,
  newName: string,
): WorkflowPositionMap {
  const next = Object.fromEntries(Object.entries(positions).filter(([name]) => name !== oldName));
  if (Object.hasOwn(positions, oldName)) {
    next[newName] = positions[oldName];
  }
  return next;
}

export function removeWorkflowTaskLayout(positions: WorkflowPositionMap, taskName: string): WorkflowPositionMap {
  return Object.fromEntries(Object.entries(positions).filter(([name]) => name !== taskName));
}

export function parseWorkflowMemory(value: string): { amount: string; unit: WorkflowMemoryUnit } {
  const match = value.trim().match(/^(\d+(?:\.\d+)?)\s*(KiB|MiB|GiB|TiB|PiB|EiB|KB|MB|GB|TB|PB|EB|Ki|Mi|Gi|Ti|Pi|Ei|K|M|G|T|P|E|B)?$/i);
  if (!match) return { amount: value, unit: "MiB" };
  let suffix = match[2] ? match[2].toLowerCase() : "";
  if (suffix.endsWith("ib")) suffix = suffix.slice(0, -1);
  else if (suffix.endsWith("b")) suffix = suffix.slice(0, -1);
  const binaryScale: Record<string, number> = {
    ki: 2 ** 10,
    mi: 2 ** 20,
    gi: 2 ** 30,
    ti: 2 ** 40,
    pi: 2 ** 50,
    ei: 2 ** 60,
  };
  const decimalScale: Record<string, number> = {
    k: 1e3,
    m: 1e6,
    g: 1e9,
    t: 1e12,
    p: 1e15,
    e: 1e18,
  };
  const valueNumber = Number(match[1]);
  if (!suffix) return { amount: match[1], unit: "MiB" };
  const binary = Object.entries(binaryScale).find(([key]) => key === suffix)?.[1];
  if (binary !== undefined) {
    const unit: WorkflowMemoryUnit = binary >= 2 ** 30 ? "GiB" : "MiB";
    const scale = unit === "GiB" ? 2 ** 30 : 2 ** 20;
    return { amount: String(valueNumber * binary / scale), unit };
  }
  const decimal = Object.entries(decimalScale).find(([key]) => key === suffix)?.[1];
  if (decimal !== undefined) {
    const unit: WorkflowMemoryUnit = decimal >= 1e9 ? "GB" : "MB";
    const scale = unit === "GB" ? 1e9 : 1e6;
    return { amount: String(valueNumber * decimal / scale), unit };
  }
  if (suffix === "b") return { amount: String(valueNumber / (2 ** 20)), unit: "MiB" };
  return { amount: value, unit: "MiB" };
}

export function formatWorkflowMemory(amount: string, unit: WorkflowMemoryUnit): string {
  return amount.trim() ? `${amount.trim()}${unit}` : "";
}

function pathSegments(path: string): (string | number)[] {
  if (path.startsWith("/")) {
    return path.slice(1).split("/").filter(Boolean).map((segment) => {
      const decoded = segment.replace(/~1/g, "/").replace(/~0/g, "~");
      return /^\d+$/.test(decoded) ? Number(decoded) : decoded;
    });
  }
  return (path.match(/[^.[\]]+|\[\d+\]/g) ?? []).map((segment) => {
    if (segment.startsWith("[") && segment.endsWith("]")) return Number(segment.slice(1, -1));
    return /^\d+$/.test(segment) ? Number(segment) : segment;
  });
}

function positionAt(source: string, offset: number): { lineNumber: number; column: number } {
  const lines = source.slice(0, offset).split(/\r\n|\n|\r/);
  return { lineNumber: lines.length, column: (lines.at(-1)?.length ?? 0) + 1 };
}

export function validationPathToLocation(source: string, path: string): WorkflowYamlLocation | null {
  const parsed = parseWorkflowYaml(source);
  if (!parsed.document) return null;
  const document = parsed.document;
  const segments = pathSegments(path);
  if (segments.length === 0) return null;

  let lookup = [...segments];
  let node = document.getIn(lookup, true);
  while (!isNode(node) && lookup.length > 0) {
    lookup = lookup.slice(0, -1);
    node = document.getIn(lookup, true);
  }
  if (!isNode(node) || !node.range) return null;
  const start = positionAt(source, node.range[0]);
  const end = positionAt(source, Math.max(node.range[1], node.range[0] + 1));
  const taskIndexValue = segments[0] === "spec" && segments[1] === "tasks" && typeof segments[2] === "number"
    ? segments[2]
    : undefined;
  const taskName = taskIndexValue === undefined ? undefined : taskNameAt(document, taskIndexValue);
  return {
    startLineNumber: start.lineNumber,
    startColumn: start.column,
    endLineNumber: end.lineNumber,
    endColumn: end.column,
    ...(taskName ? { taskName } : {}),
  };
}
