import dagre from "@dagrejs/dagre";
import type { Edge, Node, XYPosition } from "@xyflow/react";
import type { TaskExecution } from "@/lib/api/client";
import type { Task } from "./spec";
import type { NormalizedWorkflowSpec } from "./normalize";

const nodeWidth = 232;
const nodeHeight = 118;

export type TaskAggregate = Readonly<{
  state: TaskExecution["state"] | "PENDING";
  completed: number;
  total: number;
  label: string;
}>;

export type WorkflowNodeData = Readonly<{
  task: Task;
  aggregate: TaskAggregate;
  selected: boolean;
}>;

export type WorkflowNode = Node<WorkflowNodeData, "workflowTask">;

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function positionFrom(value: unknown): XYPosition | undefined {
  const source = asRecord(value);
  if (!source) return undefined;
  const position = asRecord(source.position) ?? source;
  return typeof position.x === "number" && Number.isFinite(position.x)
    && typeof position.y === "number" && Number.isFinite(position.y)
    ? { x: position.x, y: position.y }
    : undefined;
}

function savedPosition(layout: unknown, taskName: string): XYPosition | undefined {
  const root = asRecord(layout);
  if (!root) return undefined;
  const nodes = root.nodes;
  if (Array.isArray(nodes)) {
    const entry: unknown = nodes.find((candidate) => {
      const node = asRecord(candidate);
      return node?.taskName === taskName || node?.name === taskName || node?.id === taskName;
    });
    const position = positionFrom(entry);
    if (position) return position;
  }
  for (const candidate of [asRecord(root.nodes)?.[taskName], asRecord(root.tasks)?.[taskName], root[taskName]]) {
    const position = positionFrom(candidate);
    if (position) return position;
  }
  return undefined;
}

function expectedInstances(task: Task): number {
  const fanOutCount = task.fanOut?.count;
  if (typeof fanOutCount === "number" && Number.isInteger(fanOutCount) && fanOutCount > 0) return fanOutCount;
  if (typeof fanOutCount === "string" && /^\d+$/.test(fanOutCount)) return Math.max(1, Number(fanOutCount));
  if (task.array) {
    const step = task.array.step && task.array.step > 0 ? task.array.step : 1;
    return Math.max(1, Math.floor((task.array.end - task.array.start) / step) + 1);
  }
  return 1;
}

export function aggregateTaskExecutions(
  tasks: readonly Task[],
  executions: readonly TaskExecution[],
): Record<string, TaskAggregate> {
  const result: Record<string, TaskAggregate> = {};
  for (const task of tasks) {
    const latestByIndex = new Map<number, TaskExecution>();
    for (const execution of executions) {
      if (execution.taskName !== task.name) continue;
      const current = latestByIndex.get(execution.index);
      if (!current || execution.attempt > current.attempt
        || (execution.attempt === current.attempt && (execution.updatedAt ?? "") > (current.updatedAt ?? ""))) {
        latestByIndex.set(execution.index, execution);
      }
    }
    const latest = [...latestByIndex.values()];
    const states = latest.map((execution) => execution.state);
    const completed = states.filter((state) => state === "COMPLETED").length;
    const total = Math.max(expectedInstances(task), latest.length, ...latest.map((execution) => execution.count));
    let state: TaskAggregate["state"] = "PENDING";
    if (states.length > 0) {
      if (states.includes("RUNNING")) state = "RUNNING";
      else if (states.includes("QUEUED")) state = "QUEUED";
      else if (states.includes("SUBMITTING")) state = "SUBMITTING";
      else if (states.includes("ADMITTING")) state = "ADMITTING";
      else if (states.includes("READY")) state = "READY";
      else if (states.includes("BLOCKED")) state = "BLOCKED";
      else if (states.includes("PENDING")) state = "PENDING";
      else if (states.includes("FAILED")) state = "FAILED";
      else if (states.includes("CANCELED")) state = "CANCELED";
      else if (states.every((item) => item === "SKIPPED")) state = "SKIPPED";
      else state = "COMPLETED";
    }
    result[task.name] = { state, completed, total, label: total > 1 ? `${String(completed)}/${String(total)} COMPLETED` : state };
  }
  return result;
}

export function buildWorkflowGraph(
  spec: NormalizedWorkflowSpec,
  layout?: unknown,
  aggregates: Readonly<Record<string, TaskAggregate>> = {},
  selectedTask: string | null = null,
): { nodes: WorkflowNode[]; edges: Edge[] } {
  const tasks = spec.spec.tasks;
  const taskNames = new Set(tasks.map((task) => task.name));
  const graph = new dagre.graphlib.Graph().setDefaultEdgeLabel(() => ({}));
  graph.setGraph({ rankdir: "LR", nodesep: 52, ranksep: 86, marginx: 24, marginy: 24 });
  for (const task of tasks) graph.setNode(task.name, { width: nodeWidth, height: nodeHeight });
  const edges: Edge[] = [];
  for (const task of tasks) {
    for (const dependency of task.dependsOn ?? []) {
      if (!taskNames.has(dependency)) continue;
      graph.setEdge(dependency, task.name);
      edges.push({ id: `${dependency}->${task.name}`, source: dependency, target: task.name, type: "smoothstep" });
    }
  }
  dagre.layout(graph);
  const nodes = tasks.map((task): WorkflowNode => {
    const positioned = graph.node(task.name) as { x: number; y: number } | undefined;
    const fallback = positioned
      ? { x: positioned.x - nodeWidth / 2, y: positioned.y - nodeHeight / 2 }
      : { x: 0, y: 0 };
    return {
      id: task.name,
      type: "workflowTask",
      position: savedPosition(layout, task.name) ?? fallback,
      width: nodeWidth,
      height: nodeHeight,
      data: {
        task,
        aggregate: aggregates[task.name] ?? { state: "PENDING", completed: 0, total: expectedInstances(task), label: expectedInstances(task) > 1 ? `0/${String(expectedInstances(task))} COMPLETED` : "PENDING" },
        selected: selectedTask === task.name,
      },
    };
  });
  return { nodes, edges };
}

export function isTerminalWorkflowExecution(state: string): boolean {
  return state === "SUCCEEDED" || state === "FAILED" || state === "PARTIAL_FAILURE" || state === "CANCELED";
}
