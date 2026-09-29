import { describe, expect, it } from "vitest";
import type { TaskExecution } from "@/lib/api/client";
import { aggregateTaskExecutions, buildWorkflowGraph } from "@/lib/workflow/graph";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";

const spec = normalizeWorkflowSpec({
  spec: {
    tasks: [
      { name: "prepare" },
      { name: "simulate", dependsOn: ["prepare", "unknown"] },
      { name: "merge", dependsOn: ["simulate"] },
    ],
  },
});

describe("buildWorkflowGraph", () => {
  it("builds dependency edges and ignores unknown task names", () => {
    const graph = buildWorkflowGraph(spec);
    expect(graph.edges.map(({ source, target }) => [source, target])).toEqual([
      ["prepare", "simulate"],
      ["simulate", "merge"],
    ]);
  });

  it("uses saved positions and dagre left-to-right positions for missing entries", () => {
    const graph = buildWorkflowGraph(spec, { nodes: { prepare: { x: 17, y: 23 } } });
    const prepare = graph.nodes.find((node) => node.id === "prepare");
    const simulate = graph.nodes.find((node) => node.id === "simulate");
    const merge = graph.nodes.find((node) => node.id === "merge");
    expect(prepare?.position).toEqual({ x: 17, y: 23 });
    expect(simulate?.position.x).toBeGreaterThan(prepare?.position.x ?? 0);
    expect(merge?.position.x).toBeGreaterThan(simulate?.position.x ?? 0);
  });
});

describe("aggregateTaskExecutions", () => {
  it("uses the latest attempt per fan-out index", () => {
    const taskSpec = normalizeWorkflowSpec({ spec: { tasks: [{ name: "simulate", fanOut: { count: 4 } }] } });
    const tasks: TaskExecution[] = [
      { id: "1", executionId: "e", taskName: "simulate", index: 0, count: 4, attempt: 1, state: "FAILED", version: 1 },
      { id: "2", executionId: "e", taskName: "simulate", index: 0, count: 4, attempt: 2, state: "COMPLETED", version: 2 },
      { id: "3", executionId: "e", taskName: "simulate", index: 1, count: 4, attempt: 1, state: "COMPLETED", version: 1 },
      { id: "4", executionId: "e", taskName: "simulate", index: 2, count: 4, attempt: 1, state: "RUNNING", version: 1 },
      { id: "5", executionId: "e", taskName: "simulate", index: 3, count: 4, attempt: 1, state: "QUEUED", version: 1 },
    ];
    expect(aggregateTaskExecutions(taskSpec.spec.tasks, tasks).simulate).toEqual({
      state: "RUNNING",
      completed: 2,
      total: 4,
      label: "2/4 COMPLETED",
    });
  });
});
