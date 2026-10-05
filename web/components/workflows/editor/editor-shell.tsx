"use client";

import dynamic from "next/dynamic";
import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { PublishVersionDialogButton } from "@/components/workflows/workflow-version-actions";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { sendJson, sendText } from "@/lib/api/bff-fetch";
import { extractErrorEnvelope } from "@/lib/api/error-details";
import type { components } from "@/lib/api/schema";
import { addWorkflowDependency, addWorkflowTask, deleteWorkflowTask, layoutPositionsFromValue, parseWorkflowYaml, removeWorkflowDependency, removeWorkflowTaskLayout, renameWorkflowTaskLayout, setTaskService, setWorkflowTaskField, validationPathToLocation, type WorkflowPositionMap } from "@/lib/workflow/editor";
import { buildWorkflowGraph } from "@/lib/workflow/graph";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";
import { sanitizeDownloadFilename } from "@/lib/download";
import type { WorkflowYamlMarker } from "@/components/workflows/yaml-editor";
import { EditorToolbar } from "./editor-toolbar";
import type { WorkflowEditorIssue, WorkflowEditorPanelTab } from "./editor-types";
import { TaskPropertiesPanel } from "./task-properties-panel";
import { WorkflowPropertiesPanel } from "./workflow-properties-panel";
import { WorkflowTestRunDialog } from "./workflow-test-run-dialog";

const WorkflowYamlEditor = dynamic(() => import("@/components/workflows/yaml-editor").then((module) => module.WorkflowYamlEditor), {
  ssr: false,
  loading: () => <div className="grid h-full min-h-64 place-items-center border border-border bg-card font-mono text-[10px] text-muted-foreground">Loading YAML editor…</div>,
});
const EditorCanvas = dynamic(() => import("./editor-canvas").then((module) => module.EditorCanvas), {
  ssr: false,
  loading: () => <div className="grid h-full place-items-center border border-border bg-card font-mono text-[10px] text-muted-foreground">Loading workflow canvas…</div>,
});

type ValidationStatus = "idle" | "validating" | "valid" | "errors" | "rate-limited" | "failed";
type SaveConflict = "stale" | "immutable" | "draft-locked" | null;
type WorkflowVersion = components["schemas"]["WorkflowVersion"];

function issuesFrom(value: unknown): WorkflowEditorIssue[] {
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

function responseError(payload: unknown): { code: string; message: string; details: WorkflowEditorIssue[] } {
  const error = extractErrorEnvelope(payload);
  return {
    code: error.code ?? "REQUEST_REJECTED",
    message: error.message ?? "Request rejected",
    details: issuesFrom(error.details),
  };
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

export function EditorShell({
  tenant,
  workflowId,
  workflowName,
  version,
  yaml: initialYaml,
  csrfToken,
}: {
  tenant: string;
  workflowId: string;
  workflowName: string;
  version: WorkflowVersion;
  yaml: string;
  csrfToken: string;
}) {
  const router = useRouter();
  const [yaml, setYaml] = useState(initialYaml);
  const [savedYaml, setSavedYaml] = useState(initialYaml);
  const [currentVersion, setCurrentVersion] = useState(version.version);
  const initialPositions = useMemo(() => layoutPositionsFromValue(version.layout), [version.layout]);
  const [positions, setPositions] = useState<WorkflowPositionMap>(initialPositions);
  const [savedPositions, setSavedPositions] = useState<WorkflowPositionMap>(initialPositions);
  const [layoutDirty, setLayoutDirty] = useState(false);
  const [panel, setPanel] = useState<"properties" | "yaml">("properties");
  const [workflowTab, setWorkflowTab] = useState<WorkflowEditorPanelTab>("info");
  const [taskTab, setTaskTab] = useState<WorkflowEditorPanelTab>("task");
  const [selectedTaskName, setSelectedTaskName] = useState<string | null>(null);
  const [validationStatus, setValidationStatus] = useState<ValidationStatus>("idle");
  const [validationIssues, setValidationIssues] = useState<WorkflowEditorIssue[]>([]);
  const [validationMessage, setValidationMessage] = useState("");
  const [saveMessage, setSaveMessage] = useState("");
  const [saveError, setSaveError] = useState("");
  const [conflict, setConflict] = useState<SaveConflict>(null);
  const [lockedExecutionId, setLockedExecutionId] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [saving, setSaving] = useState(false);
  const [testRunOpen, setTestRunOpen] = useState(false);
  const [canvasMessage, setCanvasMessage] = useState("");
  const [revealLine, setRevealLine] = useState<number | undefined>();
  const validationSequence = useRef(0);
  const validationAbort = useRef<AbortController | null>(null);
  const canvasMessageTimer = useRef<number | null>(null);
  const tenantPath = `/api/bff/tenants/${encodeURIComponent(tenant)}`;
  const versionPath = `${tenantPath}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(version.id)}`;
  const versionPagePath = `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(version.id)}`;
  const dirty = yaml !== savedYaml;
  const parsed = useMemo(() => parseWorkflowYaml(yaml), [yaml]);
  const spec = useMemo(() => parsed.document ? normalizeWorkflowSpec(parsed.document.toJS()) : null, [parsed]);
  const selectedTask = spec && selectedTaskName
    ? spec.spec.tasks.find((task) => task.name === selectedTaskName)
    : undefined;
  const taskErrors = useMemo(() => {
    const errors: Record<string, string[]> = {};
    for (const issue of validationIssues) {
      if (!issue.path) continue;
      const taskName = validationPathToLocation(yaml, issue.path)?.taskName;
      if (!taskName) continue;
      (errors[taskName] ??= []).push(issue.message);
    }
    return errors;
  }, [validationIssues, yaml]);
  const layout = useMemo(() => ({ nodes: positions }), [positions]);
  const graphPositions = useMemo(() => {
    if (!spec) return positions;
    const graph = buildWorkflowGraph(spec, layout, {}, selectedTaskName, taskErrors);
    return Object.fromEntries(graph.nodes.map((node) => [node.id, { x: node.position.x, y: node.position.y }]));
  }, [layout, positions, selectedTaskName, spec, taskErrors]);
  const layoutChanged = layoutDirty || JSON.stringify(positions) !== JSON.stringify(savedPositions);
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
    setLockedExecutionId(null);
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
        const details = responseError(payload);
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
    const timeout = window.setTimeout(() => { void runValidation(yaml, sequence, controller); }, 800);
    return () => {
      window.clearTimeout(timeout);
      controller.abort();
    };
  }, [parsed.document, runValidation, yaml]);

  useEffect(() => {
    if (!dirty && !layoutChanged) return;
    const preventUnload = (event: BeforeUnloadEvent) => { event.preventDefault(); };
    window.addEventListener("beforeunload", preventUnload);
    return () => { window.removeEventListener("beforeunload", preventUnload); };
  }, [dirty, layoutChanged]);

  function showCanvasMessage(message: string) {
    setCanvasMessage(message);
    if (canvasMessageTimer.current !== null) window.clearTimeout(canvasMessageTimer.current);
    canvasMessageTimer.current = window.setTimeout(() => { setCanvasMessage(""); }, 3_000);
  }

  function addTask() {
    const added = addWorkflowTask(yaml);
    if (!added.taskName) return;
    setYamlChanged(added.yaml);
    setSelectedTaskName(added.taskName);
    setTaskTab("task");
    setPanel("properties");
  }

  function addServiceTask() {
    const added = addWorkflowTask(yaml);
    if (!added.taskName) return;
    let nextYaml = setTaskService(added.yaml, added.taskName);
    nextYaml = setWorkflowTaskField(nextYaml, added.taskName, ["launch"], "sbatch");
    nextYaml = setWorkflowTaskField(nextYaml, added.taskName, ["resources", "walltime"], "1h");
    nextYaml = setWorkflowTaskField(nextYaml, added.taskName, ["script"], "#!/bin/bash\nexec sleep infinity\n");
    setYamlChanged(nextYaml);
    setSelectedTaskName(added.taskName);
    setTaskTab("task");
    setPanel("properties");
  }

  function addDependency(target: string, dependency: string) {
    const result = addWorkflowDependency(yaml, target, dependency);
    if (result.error) {
      showCanvasMessage(result.error === "cycle" ? "That connection would create a cycle."
        : result.error === "duplicate" ? "That dependency already exists."
          : result.error === "self" ? "A task cannot depend on itself."
            : "One of these tasks no longer exists.");
      return;
    }
    setYamlChanged(result.yaml);
    setSelectedTaskName(target);
  }

  function removeDependency(target: string, dependency: string) {
    setYamlChanged(removeWorkflowDependency(yaml, target, dependency));
  }

  function deleteTask(taskName: string) {
    if (!window.confirm(`Delete task "${taskName}" and remove its dependency references?`)) return;
    setYamlChanged(deleteWorkflowTask(yaml, taskName));
    setPositions((current) => removeWorkflowTaskLayout(current, taskName));
    setLayoutDirty(true);
    setSelectedTaskName(null);
  }

  function downloadYaml() {
    const objectUrl = URL.createObjectURL(new Blob([yaml], { type: "application/yaml;charset=utf-8" }));
    const anchor = document.createElement("a");
    anchor.href = objectUrl;
    anchor.download = sanitizeDownloadFilename(`${workflowName}.yaml`, "workflow.yaml");
    anchor.style.display = "none";
    document.body.append(anchor);
    anchor.click();
    anchor.remove();
    window.setTimeout(() => { URL.revokeObjectURL(objectUrl); }, 0);
  }

  function discardChanges() {
    setYamlChanged(savedYaml);
    setPositions(savedPositions);
    setLayoutDirty(false);
    setSaveError("");
  }

  const saveDraft = useCallback(async () => {
    if ((!dirty && !layoutChanged) || saving) return;
    setSaving(true);
    setSaveError("");
    setSaveMessage("");
    setConflict(null);
    let expectedVersion = currentVersion;
    let shouldSaveLayout = layoutChanged;
    try {
      if (dirty) {
        const response = await sendText("PUT", versionPath, yaml, "application/yaml", csrfToken, {
          "X-Expected-Version": String(expectedVersion),
        });
        if (response.status !== 200) {
          const details = responseError(response.payload);
          if (response.status === 409 && details.code === "DRAFT_LOCKED") {
            const executionId = details.message.match(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i)?.[0] ?? null;
            setConflict("draft-locked");
            setLockedExecutionId(executionId);
            setSaveError(executionId
              ? `This draft is locked while test run ${executionId} is active`
              : "This draft is locked while a test run is active");
            return;
          }
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
          return;
        }
        const body = response.payload && typeof response.payload === "object"
          ? response.payload as { version?: unknown }
          : {};
        expectedVersion = typeof body.version === "number" ? body.version : expectedVersion + 1;
        setCurrentVersion(expectedVersion);
        setSavedYaml(yaml);
        shouldSaveLayout = true;
        setLayoutDirty(true);
      }
      if (shouldSaveLayout) {
        const response = await sendJson("PUT", `${versionPath}/layout`, { nodes: graphPositions }, csrfToken, {
          "X-Expected-Version": String(expectedVersion),
        });
        if (response.status !== 204) {
          const details = responseError(response.payload);
          if (response.status === 409) {
            setConflict("stale");
            setSaveError("Someone else saved this draft.");
            setLayoutDirty(true);
            return;
          }
          setSaveError(response.status === 403 ? "You do not have permission to save this workflow layout." : details.message);
          setLayoutDirty(true);
          return;
        }
        expectedVersion += 1;
        setCurrentVersion(expectedVersion);
        setPositions(graphPositions);
        setSavedPositions(graphPositions);
        setLayoutDirty(false);
      }
      setSaveMessage("Saved");
    } catch {
      setSaveError("The request could not be sent. Check your connection and try again.");
    } finally {
      setSaving(false);
    }
  }, [csrfToken, currentVersion, dirty, graphPositions, layoutChanged, saving, versionPath, yaml]);

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

  async function copyYaml() {
    try {
      await navigator.clipboard.writeText(yaml);
      setCopied(true);
      showCanvasMessage("YAML copied to clipboard.");
    } catch {
      setSaveError("Clipboard access is unavailable; copy the YAML from the editor.");
    }
  }

  function selectIssue(issue: WorkflowEditorIssue) {
    const location = issue.path ? validationPathToLocation(yaml, issue.path) : null;
    if (location?.taskName) setSelectedTaskName(location.taskName);
    setPanel("yaml");
    setRevealLine(location?.startLineNumber);
  }

  const statusText = parsed.error
    ? `YAML error ${String(parsed.error.line)}:${String(parsed.error.column)}`
    : validationStatus === "validating" ? "Validating…"
      : validationStatus === "valid" ? "Valid"
        : validationStatus === "errors" ? `${String(validationIssues.length)} errors`
          : validationStatus === "rate-limited" ? validationMessage || "Rate limited · retrying"
            : validationStatus === "failed" ? validationMessage || "Validation failed"
              : "Validation pending";
  const statusKind = parsed.error || validationStatus === "errors" || validationStatus === "failed"
    ? "error"
    : validationStatus === "valid" ? "success"
      : validationStatus === "validating" || validationStatus === "rate-limited" ? "pending"
        : "neutral";
  const publishDisabled = dirty || layoutChanged || parsed.error !== null || validationStatus !== "valid" || validationIssues.length > 0;
  const testRunDisabled = dirty || layoutChanged || parsed.error !== null
    || validationStatus !== "valid" || validationIssues.length > 0 || saving;
  const nodeErrors = useMemo(() => {
    const errors: Record<string, string[]> = {};
    for (const issue of validationIssues) {
      if (!issue.path) continue;
      const taskName = validationPathToLocation(yaml, issue.path)?.taskName;
      if (taskName) (errors[taskName] ??= []).push(issue.message);
    }
    return errors;
  }, [validationIssues, yaml]);

  return (
    <div className="space-y-2">
      {saveError ? (
        <div role="alert" className="flex flex-wrap items-center justify-between gap-2 border border-destructive/40 bg-destructive/10 p-2 font-mono text-[10px] text-destructive">
          <span>{saveError}</span>
          {conflict === "stale" ? (
            <div className="flex gap-1.5">
              <Button type="button" variant="outline" size="sm" onClick={() => { router.refresh(); }}>Reload latest (discard my changes)</Button>
              <Button type="button" variant="outline" size="sm" onClick={() => { void copyYaml(); }}>{copied ? "Copied" : "Copy my YAML"}</Button>
            </div>
          ) : null}
          {conflict === "immutable" ? <Link className="underline" href={versionPagePath}>Open this version</Link> : null}
          {conflict === "draft-locked" ? (
            <Link className="underline" href={lockedExecutionId
              ? `/t/${encodeURIComponent(tenant)}/executions/${encodeURIComponent(lockedExecutionId)}`
              : `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}?tab=executions`}>
              {lockedExecutionId ? "View active test run" : "View workflow executions"}
            </Link>
          ) : null}
        </div>
      ) : null}
      {saveMessage ? <p role="status" className="font-mono text-[9px] text-status-completed">{saveMessage}</p> : null}
      {validationIssues.length > 0 ? (
        <div aria-label="Workflow validation errors" className="border border-destructive/30 bg-card p-2">
          <ul className="grid gap-1">
            {validationIssues.map((issue, index) => (
              <li key={`${issue.path}-${issue.code}-${String(index)}`}>
                <button type="button" className="w-full text-left font-mono text-[9px] text-destructive hover:underline" onClick={() => { selectIssue(issue); }}>
                  {issue.path ? `${issue.path} · ` : ""}{issue.code}: {issue.message}
                </button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <section aria-label="Workflow draft editor" className="h-auto min-h-[54rem] overflow-hidden border border-border bg-card lg:h-[calc(100dvh-13rem)] lg:min-h-[32rem]">
        <div className="grid h-full min-h-0 grid-cols-1 grid-rows-[minmax(26rem,1fr)_minmax(28rem,1fr)] lg:grid-cols-[minmax(0,3fr)_minmax(380px,2fr)] lg:grid-rows-1">
          <div className="flex min-h-0 flex-col">
            <EditorToolbar
              status={statusText}
              statusKind={statusKind}
              dirty={dirty}
              layoutDirty={layoutChanged}
              saving={saving}
              onAddTask={addTask}
              onAddService={addServiceTask}
              onStartTest={() => { setTestRunOpen(true); }}
              startTestDisabled={testRunDisabled}
              onDownload={downloadYaml}
              onDiscard={discardChanges}
              onCopy={() => { void copyYaml(); }}
              onSave={() => { void saveDraft(); }}
              publishAction={
                <PublishVersionDialogButton
                  tenant={tenant}
                  workflowId={workflowId}
                  versionId={version.id}
                  csrfToken={csrfToken}
                  disabled={publishDisabled || saving}
                  onPublished={() => { router.push(versionPagePath); }}
                />
              }
            />
            <div className="min-h-0 flex-1 p-2">
              {spec ? (
                <EditorCanvas
                  spec={spec}
                  layout={layout}
                  selectedTask={selectedTaskName}
                  taskErrors={nodeErrors}
                  message={canvasMessage}
                  onSelectTask={(name) => {
                    setSelectedTaskName(name);
                    if (name) {
                      setTaskTab("task");
                      setPanel("properties");
                    }
                  }}
                  onAddDependency={addDependency}
                  onRemoveDependency={removeDependency}
                  onLayoutChange={(next) => { setPositions(next); setLayoutDirty(true); }}
                  onDeleteTask={deleteTask}
                />
              ) : (
                <div className="grid h-full place-items-center border border-border bg-card p-4 font-mono text-xs text-destructive">
                  {parsed.error ? `YAML parse error at line ${String(parsed.error.line)}, column ${String(parsed.error.column)}: ${parsed.error.message}` : "Workflow YAML could not be parsed."}
                </div>
              )}
            </div>
          </div>
          <aside className="flex min-h-0 flex-col border-t border-border lg:border-l lg:border-t-0">
            <Tabs value={panel} onValueChange={(value: string | null) => { if (value === "properties" || value === "yaml") setPanel(value); }} className="flex min-h-0 flex-1 flex-col">
              <TabsList variant="line" className="h-9 w-full shrink-0 justify-start border-b border-border px-2">
                <TabsTrigger value="properties">Properties</TabsTrigger>
                <TabsTrigger value="yaml">YAML</TabsTrigger>
              </TabsList>
              <TabsContent value="properties" className="min-h-0 flex-1 overflow-y-auto">
                {parsed.error || !spec ? (
                  <p className="m-3 border border-destructive/40 bg-destructive/10 p-3 font-mono text-[10px] text-destructive">
                    {parsed.error ? `YAML parse error at line ${String(parsed.error.line)}, column ${String(parsed.error.column)}: ${parsed.error.message}` : "Workflow YAML could not be parsed."}
                  </p>
                ) : selectedTask ? (
                  <TaskPropertiesPanel
                    key={selectedTask.name}
                    task={selectedTask}
                    tasks={spec.spec.tasks}
                    tenant={tenant}
                    secrets={spec.spec.secrets}
                    yaml={yaml}
                    issues={validationIssues}
                    activeTab={taskTab}
                    onTabChange={setTaskTab}
                    onYamlChange={setYamlChanged}
                    onTaskRenamed={(oldName, newName, nextYaml) => {
                      setYamlChanged(nextYaml);
                      setPositions((current) => renameWorkflowTaskLayout(current, oldName, newName));
                      setLayoutDirty(true);
                      setSelectedTaskName(newName);
                    }}
                    onTaskTypeChanged={(nextYaml) => { setYamlChanged(nextYaml); }}
                    onDependencyError={showCanvasMessage}
                  />
                ) : (
                  <WorkflowPropertiesPanel
                    spec={spec}
                    yaml={yaml}
                    issues={validationIssues}
                    activeTab={workflowTab}
                    onTabChange={setWorkflowTab}
                    onYamlChange={setYamlChanged}
                    versionNumber={version.number}
                    state="draft"
                  />
                )}
              </TabsContent>
              <TabsContent value="yaml" className="min-h-0 flex-1 p-2">
                <WorkflowYamlEditor
                  value={yaml}
                  readOnly={false}
                  onChange={setYamlChanged}
                  markers={markers}
                  revealLine={revealLine}
                  height="100%"
                  label="Editable workflow YAML"
                />
              </TabsContent>
            </Tabs>
          </aside>
        </div>
      </section>
      {spec ? (
        <WorkflowTestRunDialog
          open={testRunOpen}
          onOpenChange={setTestRunOpen}
          tenant={tenant}
          workflowId={workflowId}
          workflowName={workflowName}
          versionId={version.id}
          parameters={spec.spec.parameters}
          csrfToken={csrfToken}
        />
      ) : null}
    </div>
  );
}
