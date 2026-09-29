"use client";

import { useMemo, useState } from "react";
import { Background, BackgroundVariant, Controls, Handle, MarkerType, Position, ReactFlow, type NodeProps } from "@xyflow/react";
import type { TaskExecution } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { aggregateTaskExecutions, buildWorkflowGraph, type WorkflowNode } from "@/lib/workflow/graph";
import type { NormalizedWorkflowSpec } from "@/lib/workflow/normalize";

function stateClass(state: string): string {
  switch (state) {
    case "RUNNING": return "border-status-running/60";
    case "COMPLETED":
    case "SUCCEEDED":
    case "SKIPPED": return "border-status-completed/60";
    case "FAILED":
    case "PARTIAL_FAILURE": return "border-status-failed/60";
    case "CANCELED":
    case "CANCELING": return "border-status-canceled/60";
    case "QUEUED":
    case "SUBMITTING":
    case "READY":
    case "PENDING": return "border-status-queued/60";
    case "BLOCKED":
    case "ADMITTING": return "border-status-degraded/60";
    default: return "border-border";
  }
}

function TaskNodeView({ data }: NodeProps<WorkflowNode>) {
  const task = data.task;
  const tags = [
    task.fanOut?.count !== undefined ? `FAN-OUT ${String(task.fanOut.count)}` : undefined,
    task.array ? `ARRAY ${String(task.array.start)}–${String(task.array.end)}` : undefined,
    task.retry ? `RETRY ${String(task.retry.attempts)}` : undefined,
  ].filter((tag): tag is string => tag !== undefined);
  return (
    <article className={cn("relative w-[232px] border bg-card px-3 py-2.5", stateClass(data.aggregate.state), data.selected && "ring-1 ring-primary")}>
      <Handle type="target" position={Position.Left} className="!size-2 !border-0 !bg-primary" />
      <header className="flex items-start justify-between gap-2">
        <span className="min-w-0 truncate font-mono text-xs font-semibold">{task.name}</span>
        <span className="shrink-0 font-mono text-[9px] uppercase text-muted-foreground">{task.type}</span>
      </header>
      <p className="mt-2 font-mono text-[9px] uppercase tracking-[0.08em] text-muted-foreground">{data.aggregate.label}</p>
      {tags.length > 0 ? <div className="mt-2 flex flex-wrap gap-1">{tags.map((tag) => <span key={tag} className="border border-border px-1 py-0.5 font-mono text-[8px] uppercase text-muted-foreground">{tag}</span>)}</div> : null}
      <Handle type="source" position={Position.Right} className="!size-2 !border-0 !bg-primary" />
    </article>
  );
}

const nodeTypes = { workflowTask: TaskNodeView };

function display(value: unknown): string {
  if (value === null || value === undefined || value === "") return "—";
  if (typeof value === "string") return value;
  return JSON.stringify(value);
}

function taskProperties(spec: NormalizedWorkflowSpec, task: WorkflowNode["data"]["task"]): Array<[string, unknown]> {
  const placement = task.placement?.cluster ?? spec.spec.placement?.cluster;
  const partition = task.partition ?? spec.spec.defaults?.partition;
  const qos = task.qos ?? spec.spec.defaults?.qos;
  const secrets = Object.entries(spec.spec.secrets).map(([handle, secret]) => ({ handle, ref: secret.ref, use: secret.use }));
  return [
    ["TYPE", task.type],
    ["RESOURCES", task.resources],
    ["CLUSTER", placement],
    ["PARTITION", partition],
    ["QOS", qos],
    ["COMMAND", task.command],
    ["SCRIPT", task.script ? { ref: task.script.ref, language: task.script.language } : undefined],
    ["ARGS", task.args],
    ["DEPENDS ON", task.dependsOn],
    ["WHEN", task.when],
    ["FAN OUT", task.fanOut],
    ["RETRY", task.retry],
    ["ENV", task.env],
    ["SECRETS", secrets.length ? secrets : undefined],
  ];
}

export function WorkflowGraph({
  spec,
  layout,
  taskExecutions = [],
  selectedTask,
  onSelectTask,
  showProperties = true,
}: {
  spec: NormalizedWorkflowSpec;
  layout?: unknown;
  taskExecutions?: readonly TaskExecution[];
  selectedTask?: string | null;
  onSelectTask?: (taskName: string) => void;
  showProperties?: boolean;
}) {
  const [internalSelectedTask, setInternalSelectedTask] = useState<string | null>(null);
  const aggregates = useMemo(() => aggregateTaskExecutions(spec.spec.tasks, taskExecutions), [spec.spec.tasks, taskExecutions]);
  const currentSelected = selectedTask === undefined ? internalSelectedTask : selectedTask;
  const graph = useMemo(() => buildWorkflowGraph(spec, layout, aggregates, currentSelected), [spec, layout, aggregates, currentSelected]);
  const selected = spec.spec.tasks.find((task) => task.name === currentSelected);
  const selectTask = (taskName: string) => {
    if (onSelectTask) onSelectTask(taskName);
    else setInternalSelectedTask(taskName);
  };

  if (spec.spec.tasks.length === 0) {
    return <div className="grid h-80 place-items-center border border-border bg-card font-mono text-xs uppercase text-muted-foreground">No tasks in this version</div>;
  }

  return (
    <div className={cn("grid min-w-0 gap-0", showProperties && "lg:grid-cols-[minmax(0,1fr)_18rem]")}>
      <div className="workflow-canvas react-flow h-[34rem] min-w-0 border border-border bg-card">
        <ReactFlow
          nodes={graph.nodes}
          edges={graph.edges}
          nodeTypes={nodeTypes}
          onNodeClick={(_, node) => { selectTask(node.id); }}
          nodesDraggable={false}
          nodesConnectable={false}
          elementsSelectable
          deleteKeyCode={null}
          fitView
          fitViewOptions={{ padding: 0.16 }}
          defaultEdgeOptions={{ markerEnd: { type: MarkerType.ArrowClosed, color: "var(--muted-foreground)" } }}
        >
          <Background variant={BackgroundVariant.Dots} gap={22} size={1} color="var(--border)" />
          <Controls showInteractive={false} />
        </ReactFlow>
      </div>
      {showProperties ? (
        <aside aria-label="Task properties" className="min-w-0 border border-t-0 border-border bg-card p-4 lg:border-l-0 lg:border-t">
          <h3 className="mb-3 font-mono text-[11px] font-semibold uppercase tracking-[0.12em]">{selected?.name ?? "Task properties"}</h3>
          {selected ? <dl className="grid gap-3">
            {taskProperties(spec, selected).map(([label, value]) => value === undefined ? null : (
              <div key={label} className="min-w-0 border-b border-border/70 pb-2">
                <dt className="font-mono text-[9px] uppercase tracking-[0.1em] text-muted-foreground">{label}</dt>
                <dd className="mt-1 break-words font-mono text-[10px] text-foreground">{display(value)}</dd>
              </div>
            ))}
          </dl> : <p className="font-mono text-[10px] text-muted-foreground">Select a task to inspect its properties.</p>}
        </aside>
      ) : null}
    </div>
  );
}
