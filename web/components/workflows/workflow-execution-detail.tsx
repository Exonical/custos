"use client";

import Link from "next/link";
import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { RefreshCw } from "lucide-react";
import type { TaskExecution, Workflow, WorkflowExecution, WorkflowVersion } from "@/lib/api/client";
import { CancelWorkflowExecutionButton } from "@/components/workflows/cancel-workflow-execution-button";
import { WorkflowGraph } from "@/components/workflows/workflow-graph";
import { Button } from "@/components/ui/button";
import { StateBadge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDurationBetween, formatUtcDateTime } from "@/lib/format";
import { isTerminalWorkflowExecution } from "@/lib/workflow/graph";
import type { NormalizedWorkflowSpec } from "@/lib/workflow/normalize";

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

function renderValue(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (typeof value === "string") return value;
  return JSON.stringify(value);
}

type FrozenSpecState =
  | { status: "idle" | "loading" | "not-frozen" }
  | { status: "ready"; value: unknown }
  | { status: "error"; message: string };

export function WorkflowExecutionDetail({
  tenant,
  execution,
  workflow,
  version,
  spec,
  taskExecutions,
  csrfToken,
}: {
  tenant: string;
  execution: WorkflowExecution;
  workflow: Workflow;
  version: WorkflowVersion;
  spec: NormalizedWorkflowSpec;
  taskExecutions: TaskExecution[];
  csrfToken: string;
}) {
  const router = useRouter();
  const terminal = isTerminalWorkflowExecution(execution.state);
  const [selectedTask, setSelectedTask] = useState<string | null>(null);
  const [selectedTaskExecutionId, setSelectedTaskExecutionId] = useState<string | null>(null);
  const [frozenSpec, setFrozenSpec] = useState<FrozenSpecState>({ status: "idle" });
  const frozenRequest = useRef(0);
  const parameters = asRecord(execution.parameters) ?? {};
  const taskExecution = useMemo(
    () => taskExecutions.find((task) => task.id === selectedTaskExecutionId),
    [selectedTaskExecutionId, taskExecutions],
  );
  const visibleTaskExecutions = selectedTask
    ? taskExecutions.filter((task) => task.taskName === selectedTask)
    : taskExecutions;

  useEffect(() => {
    if (terminal) return undefined;
    const timer = window.setInterval(() => { router.refresh(); }, 5_000);
    return () => { window.clearInterval(timer); };
  }, [router, terminal]);

  function inspectTask(task: TaskExecution) {
    setSelectedTask(task.taskName);
    setSelectedTaskExecutionId(task.id);
    const request = ++frozenRequest.current;
    const settle = (next: FrozenSpecState) => { if (frozenRequest.current === request) setFrozenSpec(next); };
    setFrozenSpec({ status: "loading" });
    void fetch(`/api/bff/tenants/${encodeURIComponent(tenant)}/workflow-executions/${encodeURIComponent(execution.id)}/tasks/${encodeURIComponent(task.id)}/execution-spec`, {
      method: "GET",
      credentials: "same-origin",
      cache: "no-store",
      headers: { Accept: "application/json" },
    }).then(async (response) => {
      if (response.status === 404) {
        settle({ status: "not-frozen" });
        return;
      }
      if (!response.ok) {
        settle({ status: "error", message: `HTTP_${String(response.status)}` });
        return;
      }
      const value: unknown = await response.json().catch(() => null);
      settle({ status: "ready", value });
    }).catch(() => {
      settle({ status: "error", message: "UPSTREAM_UNAVAILABLE" });
    });
  }

  function clearSelection(taskName: string | null) {
    frozenRequest.current++;
    setSelectedTask(taskName);
    setSelectedTaskExecutionId(null);
    setFrozenSpec({ status: "idle" });
  }

  const versionHref = `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflow.id)}/versions/${encodeURIComponent(version.id)}`;
  const workflowHref = `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflow.id)}`;
  const metadata = [
    { label: "STATE", value: <StateBadge state={execution.state} /> },
    { label: "STATE REASON", value: execution.stateReason || "—" },
    { label: "WORKFLOW", value: <Link className="hover:text-primary" href={workflowHref}>{workflow.name}</Link> },
    { label: "VERSION", value: <Link className="font-mono hover:text-primary" href={versionHref}>v{version.number}</Link> },
    { label: "STRATEGY", value: execution.strategy ?? "—" },
    { label: "CREATED", value: formatUtcDateTime(execution.createdAt) },
    { label: "STARTED", value: formatUtcDateTime(execution.startedAt) },
    { label: "ENDED · DURATION", value: `${formatUtcDateTime(execution.endedAt)} · ${formatDurationBetween(execution.startedAt, execution.endedAt)}` },
  ];

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-border pb-4">
        <div className="min-w-0 space-y-2">
          <Link href={`/t/${encodeURIComponent(tenant)}/executions`} className="inline-flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground hover:text-foreground">
            Executions
          </Link>
          <div>
            <h1 className="truncate font-mono text-xl font-semibold">Execution {execution.id.slice(0, 8)}</h1>
            <p className="mt-1 font-mono text-[10px] text-muted-foreground" title={execution.id}>{execution.id}</p>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button type="button" variant="outline" size="icon-sm" aria-label="Refresh execution" onClick={() => { router.refresh(); }}>
            <RefreshCw aria-hidden="true" className="size-3.5" />
          </Button>
          {!terminal ? <CancelWorkflowExecutionButton tenant={tenant} execution={execution.id} csrfToken={csrfToken} /> : null}
        </div>
      </header>

      <section aria-label="Execution metadata" className="grid grid-cols-2 gap-px border border-border bg-border sm:grid-cols-4 xl:grid-cols-8">
        {metadata.map(({ label, value }) => (
          <div key={label} className="min-w-0 bg-card px-3 py-3">
            <p className="font-mono text-[9px] uppercase tracking-[0.1em] text-muted-foreground">{label}</p>
            <div className="mt-1 truncate font-mono text-[10px] tabular-nums">{value}</div>
          </div>
        ))}
      </section>

      <section aria-labelledby="execution-parameters-heading" className="border border-border bg-card">
        <header className="border-b border-border px-4 py-3">
          <h2 id="execution-parameters-heading" className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em]">Parameters used</h2>
        </header>
        <div className="grid gap-px bg-border sm:grid-cols-2 lg:grid-cols-4">
          {Object.entries(parameters).length === 0 ? <p className="bg-card px-4 py-3 font-mono text-xs text-muted-foreground">No parameters</p> : Object.entries(parameters).map(([key, value]) => (
            <div key={key} className="min-w-0 bg-card px-4 py-3">
              <p className="font-mono text-[9px] uppercase text-muted-foreground">{key}</p>
              <p className="mt-1 break-words font-mono text-xs">{renderValue(value)}</p>
            </div>
          ))}
        </div>
      </section>

      <section className="space-y-4">
        <h2 className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em]">Execution DAG</h2>
        <WorkflowGraph spec={spec} layout={version.layout} taskExecutions={taskExecutions} selectedTask={selectedTask} onSelectTask={(name) => { clearSelection(name); }} showProperties={false} />
      </section>

      <section className="space-y-3 border border-border bg-card">
        <header className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3">
          <h2 className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em]">Task executions{selectedTask ? ` · ${selectedTask}` : ""}</h2>
          {selectedTask ? <Button type="button" variant="outline" size="sm" onClick={() => { clearSelection(null); }}>Clear task filter</Button> : null}
        </header>
        <Table containerClassName="max-h-[60vh]">
          <caption className="sr-only">Task execution attempts</caption>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Task</TableHead>
              <TableHead>Index / count</TableHead>
              <TableHead>Attempt</TableHead>
              <TableHead>State</TableHead>
              <TableHead>Reason</TableHead>
              <TableHead>Job</TableHead>
              <TableHead>ExecutionSpec</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {visibleTaskExecutions.length === 0 ? (
              <TableRow><TableCell colSpan={7} className="py-10 text-center">
                <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No task executions</p>
                <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
              </TableCell></TableRow>
            ) : visibleTaskExecutions.map((task) => (
              <TableRow key={task.id} data-selected={selectedTaskExecutionId === task.id || undefined} className={selectedTaskExecutionId === task.id ? "bg-muted/50" : undefined}>
                <TableCell>
                  <button type="button" className="font-mono text-xs normal-case hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" onClick={() => { inspectTask(task); }}>{task.taskName}</button>
                </TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{task.index} / {task.count}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{task.attempt}</TableCell>
                <TableCell><StateBadge state={task.state} /></TableCell>
                <TableCell className="font-mono text-xs">{task.stateReason || "—"}</TableCell>
                <TableCell>{task.jobId ? <Link className="font-mono text-xs text-primary hover:underline" href={`/t/${encodeURIComponent(tenant)}/jobs/${encodeURIComponent(task.jobId)}?project=${encodeURIComponent(execution.projectId)}`}>{task.jobId.slice(0, 8)}</Link> : "—"}</TableCell>
                <TableCell><Button type="button" variant="outline" size="sm" onClick={() => { inspectTask(task); }}>Inspect</Button></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {selectedTaskExecutionId ? (
          <section aria-label="Frozen execution specification" className="border-t border-border p-4">
            <h3 className="mb-2 font-mono text-[10px] font-semibold uppercase tracking-[0.1em]">ExecutionSpec · {taskExecution?.taskName ?? "Task"}</h3>
            {frozenSpec.status === "loading" ? <p className="font-mono text-xs text-muted-foreground">Loading frozen specification…</p> : null}
            {frozenSpec.status === "not-frozen" ? <p className="font-mono text-xs text-muted-foreground">ExecutionSpec not frozen yet.</p> : null}
            {frozenSpec.status === "error" ? <p role="alert" className="font-mono text-xs text-destructive">Unable to load ExecutionSpec: {frozenSpec.message}</p> : null}
            {frozenSpec.status === "ready" ? <pre className="max-h-96 overflow-auto border border-border bg-background p-3 font-mono text-[10px]">{JSON.stringify(frozenSpec.value, null, 2)}</pre> : null}
          </section>
        ) : null}
      </section>
    </div>
  );
}
