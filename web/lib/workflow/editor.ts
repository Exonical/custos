import { isNode, isScalar, isSeq, parseDocument, type Document } from "yaml";

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
