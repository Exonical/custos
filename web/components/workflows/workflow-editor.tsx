"use client";

import dynamic from "next/dynamic";
import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PublishVersionDialogButton } from "@/components/workflows/workflow-version-actions";
import { WorkflowGraph } from "@/components/workflows/workflow-graph";
import { StateBadge } from "@/components/ui/badge";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { sendText } from "@/lib/api/bff-fetch";
import type { components } from "@/lib/api/schema";
import {
  addWorkflowTask,
  deleteWorkflowTask,
  parseWorkflowYaml,
  renameWorkflowTask,
  setWorkflowTaskField,
  validationPathToLocation,
} from "@/lib/workflow/editor";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";
import type { Task } from "@/lib/workflow/spec";
import type { WorkflowYamlMarker } from "@/components/workflows/yaml-editor";

const WorkflowYamlEditor = dynamic(() => import("@/components/workflows/yaml-editor").then((module) => module.WorkflowYamlEditor), {
  ssr: false,
  loading: () => <div className="grid h-[34rem] place-items-center border border-border bg-card font-mono text-xs text-muted-foreground">Loading editor…</div>,
});

type ValidationIssue = { path: string; code: string; message: string };
type ValidationStatus = "idle" | "validating" | "valid" | "errors" | "rate-limited" | "failed";
type SaveConflict = "stale" | "immutable" | null;
type ErrorEnvelope = { error?: { code?: unknown; message?: unknown; details?: unknown } };
type WorkflowVersion = components["schemas"]["WorkflowVersion"];

function issuesFrom(value: unknown): ValidationIssue[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((entry) => {
    if (!entry || typeof entry !== "object") return [];
    const issue = entry as Record<string, unknown>;
    return [{
      path: typeof issue.path === "string" ? issue.path : typeof issue.field === "string" ? issue.field : "",
      code: typeof issue.code === "string" ? issue.code : "VALIDATION",
      message: typeof issue.message === "string" ? issue.message : typeof issue.reason === "string" ? issue.reason : "Validation failed",
    }];
  });
}

function errorDetails(payload: unknown): { code: string; message: string; details: ValidationIssue[] } {
  const error = payload && typeof payload === "object" ? (payload as ErrorEnvelope).error : undefined;
  const code = typeof error?.code === "string" ? error.code : "REQUEST_REJECTED";
  const message = typeof error?.message === "string" ? error.message : code;
  const details = issuesFrom(error?.details);
  return { code, message, details };
}

function commandText(task: Task): string {
  return task.command?.join("\n") ?? "";
}

function inlineScriptText(task: Task): string {
  if (typeof task.script === "string") return task.script;
  return task.script?.inline ?? "";
}

function parseErrorMarker(error: { line: number; column: number; message: string }): WorkflowYamlMarker {
  return {
    startLineNumber: error.line,
    startColumn: error.column,
    endLineNumber: error.line,
    endColumn: error.column + 1,
    message: error.message,
    severity: "error",
  };
}

function isCurrentValidation(sequence: number, currentSequence: number, signal: AbortSignal): boolean {
  return !signal.aborted && sequence === currentSequence;
}

export function WorkflowEditor({
  tenant,
  workflowId,
  workflowName,
  version,
  layout,
  yaml: initialYaml,
  csrfToken,
}: {
  tenant: string;
  workflowId: string;
  workflowName: string;
  version: WorkflowVersion;
  layout?: unknown;
  yaml: string;
  csrfToken: string;
}) {
  const router = useRouter();
  const [yaml, setYaml] = useState(initialYaml);
  const [savedYaml, setSavedYaml] = useState(initialYaml);
  const [currentVersion, setCurrentVersion] = useState(version.version);
  const [activeTab, setActiveTab] = useState<"graph" | "yaml">("graph");
  const [selectedTaskName, setSelectedTaskName] = useState<string | null>(null);
  const [validationStatus, setValidationStatus] = useState<ValidationStatus>("idle");
  const [validationIssues, setValidationIssues] = useState<ValidationIssue[]>([]);
  const [validationMessage, setValidationMessage] = useState("");
  const [saveMessage, setSaveMessage] = useState("");
  const [saveError, setSaveError] = useState("");
  const [conflict, setConflict] = useState<SaveConflict>(null);
  const [copied, setCopied] = useState(false);
  const [saving, setSaving] = useState(false);
  const [revealLine, setRevealLine] = useState<number | undefined>();
  const validationSequence = useRef(0);
  const validationAbort = useRef<AbortController | null>(null);
  const tenantPath = `/api/bff/tenants/${encodeURIComponent(tenant)}`;
  const versionPath = `${tenantPath}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(version.id)}`;
  const versionPagePath = `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(version.id)}`;
  const dirty = yaml !== savedYaml;
  const parsed = useMemo(() => parseWorkflowYaml(yaml), [yaml]);
  const spec = useMemo(
    () => parsed.document ? normalizeWorkflowSpec(parsed.document.toJS()) : null,
    [parsed],
  );
  const tasks = spec?.spec.tasks ?? [];
  const activeTaskName = selectedTaskName && tasks.some((task) => task.name === selectedTaskName)
    ? selectedTaskName
    : tasks[0]?.name ?? null;
  const task = tasks.find((item) => item.name === activeTaskName);
  const markers = useMemo<WorkflowYamlMarker[]>(() => {
    if (parsed.error) return [parseErrorMarker(parsed.error)];
    return validationIssues.flatMap((issue) => {
      if (!issue.path) return [];
      const location = validationPathToLocation(yaml, issue.path);
      if (!location) return [];
      return [{
        startLineNumber: location.startLineNumber,
        startColumn: location.startColumn,
        endLineNumber: location.endLineNumber,
        endColumn: location.endColumn,
        message: `${issue.code}: ${issue.message}`,
        severity: "error" as const,
      }];
    });
  }, [parsed.error, validationIssues, yaml]);

  const setYamlChanged = useCallback((nextYaml: string) => {
    validationAbort.current?.abort();
    validationSequence.current += 1;
    setYaml(nextYaml);
    setValidationStatus("idle");
    setValidationIssues([]);
    setValidationMessage("");
    setSaveMessage("");
    setSaveError("");
    setConflict(null);
    setCopied(false);
  }, []);

  const runValidation = useCallback(async (source: string, sequence: number, controller: AbortController) => {
    const url = `${tenantPath}/workflows/${encodeURIComponent(workflowId)}/versions/validate`;
    async function request(): Promise<Response | null> {
      try {
        return await fetch(url, {
          method: "POST",
          credentials: "same-origin",
          cache: "no-store",
          headers: {
            Accept: "application/json",
            "Content-Type": "application/yaml",
            "X-CSRF-Token": csrfToken,
          },
          body: source,
          signal: controller.signal,
        });
      } catch {
        return null;
      }
    }
    try {
      setValidationStatus("validating");
      let response = await request();
      if (!response) {
        if (isCurrentValidation(sequence, validationSequence.current, controller.signal)) {
          setValidationMessage("Validation could not be completed. Try again.");
          setValidationStatus("failed");
        }
        return;
      }
      if (!isCurrentValidation(sequence, validationSequence.current, controller.signal)) return;
      if (response.status === 429) {
        setValidationStatus("rate-limited");
        setValidationMessage("Validation is rate limited; will retry.");
        await new Promise<void>((resolve) => {
          const timeout = window.setTimeout(resolve, 5_000);
          controller.signal.addEventListener("abort", () => {
            window.clearTimeout(timeout);
            resolve();
          }, { once: true });
        });
        if (!isCurrentValidation(sequence, validationSequence.current, controller.signal)) return;
        setValidationStatus("validating");
        response = await request();
        if (!response) {
          if (isCurrentValidation(sequence, validationSequence.current, controller.signal)) {
            setValidationMessage("Validation could not be completed. Try again.");
            setValidationStatus("failed");
          }
          return;
        }
        if (!isCurrentValidation(sequence, validationSequence.current, controller.signal)) return;
      }
      const payload: unknown = await response.json().catch(() => null);
      if (!isCurrentValidation(sequence, validationSequence.current, controller.signal)) return;
      if (!response.ok) {
        const details = errorDetails(payload);
        setValidationIssues(details.details.length > 0 ? details.details : [{
          path: "",
          code: details.code,
          message: details.message,
        }]);
        setValidationMessage(response.status === 429 ? "Validation is rate limited; will retry." : "");
        setValidationStatus(response.status === 429 ? "rate-limited" : "failed");
        return;
      }
      const body = payload && typeof payload === "object" ? payload as { valid?: unknown; errors?: unknown } : {};
      const issues = issuesFrom(body.errors);
      setValidationIssues(issues);
      setValidationMessage("");
      setValidationStatus(body.valid === true && issues.length === 0 ? "valid" : "errors");
    } catch {
      if (!isCurrentValidation(sequence, validationSequence.current, controller.signal)) return;
      setValidationMessage("Validation could not be completed. Try again.");
      setValidationStatus("failed");
    }
  }, [csrfToken, tenantPath, workflowId]);

  useEffect(() => {
    if (!parsed.document) return;
    const sequence = ++validationSequence.current;
    const controller = new AbortController();
    validationAbort.current?.abort();
    validationAbort.current = controller;
    const timeout = window.setTimeout(() => {
      void runValidation(yaml, sequence, controller);
    }, 800);
    return () => {
      window.clearTimeout(timeout);
      controller.abort();
    };
  }, [parsed.document, runValidation, yaml]);

  useEffect(() => {
    if (!dirty) return;
    const preventUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
    };
    window.addEventListener("beforeunload", preventUnload);
    return () => { window.removeEventListener("beforeunload", preventUnload); };
  }, [dirty]);

  const saveDraft = useCallback(async () => {
    if (!dirty || saving) return;
    setSaving(true);
    setSaveError("");
    setSaveMessage("");
    setConflict(null);
    try {
      const response = await sendText("PUT", versionPath, yaml, "application/yaml", csrfToken, {
        "X-Expected-Version": String(currentVersion),
      });
      if (response.status === 200) {
        const body = response.payload && typeof response.payload === "object"
          ? response.payload as { version?: unknown }
          : {};
        const nextVersion = typeof body.version === "number" ? body.version : currentVersion + 1;
        setCurrentVersion(nextVersion);
        setSavedYaml(yaml);
        setSaveMessage("Draft saved.");
        return;
      }
      const details = errorDetails(response.payload);
      if (response.status === 409 && details.code === "VERSION_CONFLICT") {
        setConflict("stale");
        setSaveError("Someone else saved this draft.");
        return;
      }
      if (response.status === 409 && details.code === "VERSION_IMMUTABLE") {
        setConflict("immutable");
        setSaveError("This version is no longer a draft.");
        return;
      }
      if (response.status === 422) {
        setValidationIssues(details.details);
        setValidationStatus("errors");
        setSaveError(details.details.length === 0 ? details.message : "");
        return;
      }
      setSaveError(response.status === 403 ? "You do not have permission to edit this workflow draft." : details.message);
    } catch {
      setSaveError("The request could not be sent. Check your connection and try again.");
    } finally {
      setSaving(false);
    }
  }, [csrfToken, currentVersion, dirty, saving, versionPath, yaml]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
        event.preventDefault();
        void saveDraft();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => { window.removeEventListener("keydown", onKeyDown); };
  }, [saveDraft]);

  async function copyMyYaml() {
    try {
      await navigator.clipboard.writeText(yaml);
      setCopied(true);
    } catch {
      setSaveError("Clipboard access is unavailable; copy the YAML from the editor.");
    }
  }

  function updateTaskField(taskName: string, path: readonly (string | number)[], value: unknown) {
    setYamlChanged(setWorkflowTaskField(yaml, taskName, path, value));
  }

  function updateCommand(taskName: string, value: string) {
    const lines = value.split(/\r\n|\n/);
    if (lines.at(-1) === "") lines.pop();
    updateTaskField(taskName, ["command"], lines);
  }

  function updateTaskName(taskName: string, nextName: string) {
    setYamlChanged(renameWorkflowTask(yaml, taskName, nextName));
    setSelectedTaskName(nextName);
  }

  function addTask() {
    const added = addWorkflowTask(yaml);
    if (!added.taskName) return;
    setYamlChanged(added.yaml);
    setSelectedTaskName(added.taskName);
  }

  function removeTask(taskName: string) {
    if (!window.confirm(`Delete task "${taskName}" and remove its dependency references?`)) return;
    const nextYaml = deleteWorkflowTask(yaml, taskName);
    setYamlChanged(nextYaml);
    const nextSpec = parseWorkflowYaml(nextYaml);
    const nextTasks = nextSpec.document ? normalizeWorkflowSpec(nextSpec.document.toJS()).spec.tasks : [];
    setSelectedTaskName(nextTasks[0]?.name ?? null);
  }

  function selectValidationIssue(issue: ValidationIssue) {
    const location = issue.path ? validationPathToLocation(yaml, issue.path) : null;
    if (location?.taskName) setSelectedTaskName(location.taskName);
    setActiveTab("yaml");
    setRevealLine(location?.startLineNumber);
  }

  const statusText = parsed.error
    ? `Invalid YAML at line ${String(parsed.error.line)}, column ${String(parsed.error.column)}`
    : validationStatus === "validating" ? "Validating…"
      : validationStatus === "valid" ? "Valid"
        : validationStatus === "errors" ? `${String(validationIssues.length)} validation errors`
          : validationStatus === "rate-limited" ? validationMessage || "Validation is rate limited; will retry"
            : validationStatus === "failed" ? validationMessage || "Validation failed"
              : "Validation pending";
  const canPublish = !dirty && !parsed.error && validationStatus === "valid" && validationIssues.length === 0;

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-border pb-3">
        <div>
          <p className="font-mono text-[10px] uppercase text-muted-foreground">Draft version {String(version.number)}</p>
          <p className="mt-1 text-sm font-semibold">{workflowName}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <StateBadge state="draft" />
          <Button type="button" variant="outline" disabled={!dirty || saving} onClick={() => { void saveDraft(); }}>
            {saving ? "Saving…" : "Save draft"}
          </Button>
          <PublishVersionDialogButton
            tenant={tenant}
            workflowId={workflowId}
            versionId={version.id}
            csrfToken={csrfToken}
            disabled={!canPublish || saving}
            onPublished={() => { router.push(versionPagePath); }}
          />
        </div>
      </header>

      {dirty ? <p className="border border-status-queued/40 bg-muted/30 p-2 font-mono text-[10px] text-muted-foreground">Unsaved changes · Ctrl/Cmd+S to save</p> : null}
      {saveMessage ? <p role="status" className="font-mono text-[10px] text-status-completed">{saveMessage}</p> : null}
      {saveError ? (
        <div role="alert" className="border border-destructive/40 bg-destructive/10 p-3 font-mono text-xs text-destructive">
          <p>{saveError}</p>
          {conflict === "stale" ? (
            <div className="mt-2 flex gap-2">
              <Button type="button" variant="outline" size="sm" onClick={() => { router.refresh(); }}>Reload latest (discard my changes)</Button>
              <Button type="button" variant="outline" size="sm" onClick={() => { void copyMyYaml(); }}>{copied ? "Copied" : "Copy my YAML"}</Button>
            </div>
          ) : null}
          {conflict === "immutable" ? (
            <Link className="mt-2 inline-block underline" href={versionPagePath}>Open this version</Link>
          ) : null}
        </div>
      ) : null}

      <section aria-label="Workflow validation" className="space-y-2 border border-border bg-card p-3">
        <p role="status" className={`font-mono text-[10px] uppercase tracking-[0.1em] ${validationStatus === "errors" || parsed.error ? "text-destructive" : "text-muted-foreground"}`}>
          {statusText}
        </p>
        {validationIssues.length > 0 ? (
          <ul className="grid gap-1">
            {validationIssues.map((issue, index) => (
              <li key={`${issue.path}-${issue.code}-${String(index)}`}>
                <button
                  type="button"
                  className="w-full border border-border px-2 py-1 text-left text-xs text-foreground hover:bg-muted"
                  onClick={() => { selectValidationIssue(issue); }}
                >
                  <span className="font-mono text-[10px] text-destructive">{issue.path || issue.code}</span>
                  <span className="ml-2">{issue.message}</span>
                </button>
              </li>
            ))}
          </ul>
        ) : null}
      </section>

      <Tabs value={activeTab} onValueChange={(value: string | null) => {
        if (value === "graph") setActiveTab("graph");
        else if (value === "yaml") setActiveTab("yaml");
      }}>
        <TabsList variant="line" className="h-8 w-full justify-start border-b border-border px-3">
          <TabsTrigger value="graph">Graph</TabsTrigger>
          <TabsTrigger value="yaml">YAML</TabsTrigger>
        </TabsList>
        <TabsContent value="graph" className="min-w-0 p-3">
          {!parsed.document || !spec ? (
            <div role="alert" className="border border-destructive/40 bg-destructive/10 p-4 text-sm text-destructive">
              {parsed.error ? `YAML parse error at line ${String(parsed.error.line)}, column ${String(parsed.error.column)}: ${parsed.error.message}` : "Workflow YAML could not be parsed."}
            </div>
          ) : (
            <div className="grid min-w-0 gap-3 lg:grid-cols-[minmax(0,1fr)_22rem]">
              <div className="min-w-0">
                <WorkflowGraph
                  spec={spec}
                  layout={layout}
                  selectedTask={activeTaskName}
                  onSelectTask={setSelectedTaskName}
                  showProperties={false}
                />
              </div>
              <aside aria-label="Editable task properties" className="min-w-0 space-y-3 border border-border bg-card p-3">
                <div className="flex items-center justify-between gap-2">
                  <h3 className="font-mono text-[11px] font-semibold uppercase tracking-[0.1em]">{task?.name ?? "Tasks"}</h3>
                  <Button type="button" variant="outline" size="sm" onClick={addTask}>Add task</Button>
                </div>
                {task ? (
                  <div className="grid gap-3">
                    <div className="grid gap-1">
                      <Label htmlFor="editor-task-name">Name</Label>
                      <Input id="editor-task-name" value={task.name} onChange={(event) => { updateTaskName(task.name, event.target.value); }} />
                    </div>
                    <div className="grid gap-1">
                      <Label htmlFor="editor-task-launch">Launch</Label>
                      <select id="editor-task-launch" className="h-8 border border-input bg-card px-2 font-mono text-xs" value={task.launch ?? "sbatch"} onChange={(event) => { updateTaskField(task.name, ["launch"], event.target.value); }}>
                        <option value="sbatch">sbatch</option>
                        <option value="srun">srun</option>
                      </select>
                    </div>
                    <div className="grid gap-1">
                      <Label htmlFor="editor-task-dependencies">Depends on</Label>
                      <select
                        id="editor-task-dependencies"
                        aria-label="Depends on"
                        multiple
                        size={Math.max(2, Math.min(5, spec.spec.tasks.length - 1))}
                        className="min-h-20 border border-input bg-card px-2 py-1 font-mono text-xs"
                        value={task.dependsOn ?? []}
                        onChange={(event) => {
                          updateTaskField(task.name, ["dependsOn"], Array.from(event.currentTarget.selectedOptions, (option) => option.value));
                        }}
                      >
                        {spec.spec.tasks.filter((item) => item.name !== task.name).map((item) => (
                          <option key={item.name} value={item.name}>{item.name}</option>
                        ))}
                      </select>
                    </div>
                    <div className="grid gap-1">
                      <Label htmlFor="editor-image-uri">Image URI</Label>
                      <Input id="editor-image-uri" value={task.image?.uri ?? ""} placeholder="Empty removes image" onChange={(event) => { updateTaskField(task.name, ["image", "uri"], event.target.value); }} />
                    </div>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <div className="grid gap-1">
                        <Label htmlFor="editor-resource-cpu">CPU</Label>
                        <Input id="editor-resource-cpu" type="number" min="1" value={task.resources?.cpu === undefined ? "" : String(task.resources.cpu)} onChange={(event) => { updateTaskField(task.name, ["resources", "cpu"], event.target.value ? Number(event.target.value) : ""); }} />
                      </div>
                      <div className="grid gap-1">
                        <Label htmlFor="editor-resource-nodes">Nodes</Label>
                        <Input id="editor-resource-nodes" type="number" min="1" value={task.resources?.nodes === undefined ? "" : String(task.resources.nodes)} onChange={(event) => { updateTaskField(task.name, ["resources", "nodes"], event.target.value ? Number(event.target.value) : ""); }} />
                      </div>
                      <div className="grid gap-1">
                        <Label htmlFor="editor-resource-memory">Memory</Label>
                        <Input id="editor-resource-memory" value={task.resources?.memory ?? ""} placeholder="1Gi" onChange={(event) => { updateTaskField(task.name, ["resources", "memory"], event.target.value); }} />
                      </div>
                      <div className="grid gap-1">
                        <Label htmlFor="editor-resource-walltime">Walltime</Label>
                        <Input id="editor-resource-walltime" value={task.resources?.walltime ?? ""} placeholder="5m" onChange={(event) => { updateTaskField(task.name, ["resources", "walltime"], event.target.value); }} />
                      </div>
                      <div className="grid gap-1 sm:col-span-2">
                        <Label htmlFor="editor-resource-gpu">GPU count</Label>
                        <Input id="editor-resource-gpu" type="number" min="1" value={task.resources?.gpu?.count === undefined ? "" : String(task.resources.gpu.count)} onChange={(event) => { updateTaskField(task.name, ["resources", "gpu", "count"], event.target.value ? Number(event.target.value) : ""); }} />
                      </div>
                    </div>
                    <div className="grid gap-1">
                      <Label htmlFor="editor-task-script">Inline script</Label>
                      <textarea id="editor-task-script" value={inlineScriptText(task)} onChange={(event) => { updateTaskField(task.name, ["script"], event.target.value); }} className="min-h-24 w-full resize-y border border-input bg-card px-2 py-2 font-mono text-xs outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50" />
                    </div>
                    <div className="grid gap-1">
                      <Label htmlFor="editor-task-command">Command (one argument per line)</Label>
                      <textarea id="editor-task-command" value={commandText(task)} onChange={(event) => { updateCommand(task.name, event.target.value); }} className="min-h-24 w-full resize-y border border-input bg-card px-2 py-2 font-mono text-xs outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50" />
                    </div>
                    <Button type="button" variant="destructive" onClick={() => { removeTask(task.name); }}>Delete task</Button>
                  </div>
                ) : <p className="text-xs text-muted-foreground">Add a task to begin editing the graph.</p>}
              </aside>
            </div>
          )}
        </TabsContent>
        <TabsContent value="yaml" className="min-w-0 p-3">
          <WorkflowYamlEditor value={yaml} readOnly={false} onChange={setYamlChanged} markers={markers} revealLine={revealLine} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
