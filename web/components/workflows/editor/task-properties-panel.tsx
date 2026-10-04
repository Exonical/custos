"use client";

import dynamic from "next/dynamic";
import { useState, type ReactNode } from "react";
import { AlertTriangle, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  addWorkflowDependency,
  formatWorkflowMemory,
  normalizeServiceEnvName,
  parseWorkflowMemory,
  removeWorkflowDependency,
  removeTaskService,
  renameWorkflowTask,
  setMemoryMode,
  setTaskService,
  setWorkflowEnvironmentVariable,
  setWorkflowTaskField,
  setWorkflowTaskType,
  validationPathToLocation,
  type WorkflowMemoryMode,
  type WorkflowMemoryUnit,
  type WorkflowTaskArrayType,
} from "@/lib/workflow/editor";
import { taskKind } from "@/lib/workflow/normalize";
import type { SecretUse, Task } from "@/lib/workflow/spec";
import { ImagePullSecretEditor } from "./image-pull-secret-editor";
import type { WorkflowEditorIssue, WorkflowEditorPanelTab } from "./editor-types";

const WorkflowYamlEditor = dynamic(() => import("@/components/workflows/yaml-editor").then((module) => module.WorkflowYamlEditor), {
  ssr: false,
  loading: () => <div className="grid h-36 place-items-center border border-border bg-card font-mono text-[10px] text-muted-foreground">Loading script editor…</div>,
});

const taskTabs: Array<{ value: WorkflowEditorPanelTab; label: string; suffixes: string[] }> = [
  { value: "task", label: "Task", suffixes: [".name", ".launch", ".script", ".command", ".dependsOn", ".onDependencyFailure", ".resources.walltime", ".multinode", ".service"] },
  { value: "environment", label: "Environment", suffixes: [".image", ".image.uri", ".image.pullSecret", ".env", ".workingDirectory", ".stdout", ".stderr"] },
  { value: "resources", label: "Resources", suffixes: [".resources", ".resources.memoryPerCpu", ".multinode", ".array", ".partition", ".qos"] },
];

function matchesTask(yaml: string, taskName: string, issue: WorkflowEditorIssue): boolean {
  return issue.path !== "" && validationPathToLocation(yaml, issue.path)?.taskName === taskName;
}

function FieldErrors({ issues }: { issues: WorkflowEditorIssue[] }) {
  if (issues.length === 0) return null;
  return <ul className="mt-1 grid gap-0.5 text-[10px] text-destructive">{issues.map((issue, index) => <li key={`${issue.code}-${String(index)}`}>{issue.message}</li>)}</ul>;
}

function NativeSelect({ id, value, onChange, children, multiple = false, size }: {
  id: string;
  value: string | string[];
  onChange: (value: string | string[]) => void;
  children: ReactNode;
  multiple?: boolean;
  size?: number;
}) {
  return (
    <select
      id={id}
      value={value}
      multiple={multiple}
      size={size}
      onChange={(event) => {
        onChange(multiple
          ? Array.from(event.currentTarget.selectedOptions, (option) => option.value)
          : event.currentTarget.value);
      }}
      className="h-8 w-full border border-input bg-card px-2 font-mono text-[10px] text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50"
    >
      {children}
    </select>
  );
}

function SegmentButtons({ value, options, onChange }: {
  value: string;
  options: Array<{ value: string; label: string }>;
  onChange: (value: string) => void;
}) {
  return (
    <div role="group" className="inline-flex border border-border">
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          aria-pressed={value === option.value}
          onClick={() => { onChange(option.value); }}
          className={`border-r border-border px-2 py-1 font-mono text-[9px] uppercase tracking-[0.04em] last:border-r-0 ${
            value === option.value ? "bg-primary/15 text-primary" : "bg-card text-muted-foreground hover:bg-muted"
          }`}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}

function textValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function numericValue(value: unknown): string {
  return typeof value === "number" ? String(value) : "";
}

function envEntries(value: Task["env"]): Array<{ name: string; value: string }> {
  if (Array.isArray(value)) {
    return value.flatMap((entry) => {
      const separator = entry.indexOf("=");
      return separator > 0 ? [{ name: entry.slice(0, separator), value: entry.slice(separator + 1) }] : [];
    });
  }
  return value && typeof value === "object"
    ? Object.entries(value).map(([name, envValue]) => ({ name, value: envValue }))
    : [];
}

export function TaskPropertiesPanel({
  task,
  tasks,
  tenant,
  secrets,
  yaml,
  issues,
  activeTab,
  onTabChange,
  onYamlChange,
  onTaskRenamed,
  onTaskTypeChanged,
  onDependencyError,
}: {
  task: Task;
  tasks: Task[];
  tenant: string;
  secrets: Record<string, SecretUse>;
  yaml: string;
  issues: WorkflowEditorIssue[];
  activeTab: WorkflowEditorPanelTab;
  onTabChange: (tab: WorkflowEditorPanelTab) => void;
  onYamlChange: (yaml: string) => void;
  onTaskRenamed: (oldName: string, newName: string, yaml: string) => void;
  onTaskTypeChanged: (yaml: string) => void;
  onDependencyError: (message: string) => void;
}) {
  const [scriptMode, setScriptMode] = useState<"script" | "command">(task.command?.length ? "command" : "script");
  const taskIssues = issues.filter((issue) => matchesTask(yaml, task.name, issue));
  const isService = task.service !== undefined;
  const currentKind = taskKind(task);
  const canConvertToService = currentKind === undefined || currentKind === "shell";
  const taskMode: WorkflowTaskArrayType = task.multinode ? "multinode" : !isService && task.array ? "array" : "default";
  const env = envEntries(task.env);
  const defaultDependencies = task.dependsOn ?? [];
  const memoryMode: WorkflowMemoryMode = task.resources?.memoryPerCpu ? "perCpu" : "perNode";
  const memory = parseWorkflowMemory(task.resources?.memoryPerCpu ?? task.resources?.memory ?? task.resources?.memoryPerNode ?? "");

  function setField(path: readonly (string | number)[], value: unknown) {
    onYamlChange(setWorkflowTaskField(yaml, task.name, path, value));
  }

  function issuesFor(suffixes: string[]): WorkflowEditorIssue[] {
    return taskIssues.filter((issue) => suffixes.some((suffix) =>
      issue.path.endsWith(suffix) || issue.path.includes(`${suffix}.`)));
  }

  function hasTabIssues(suffixes: string[]): boolean {
    return issuesFor(suffixes).length > 0;
  }

  function setType(value: string) {
    const nextType = value as WorkflowTaskArrayType;
    const nextYaml = setWorkflowTaskType(yaml, task.name, nextType);
    onTaskTypeChanged(nextYaml);
  }

  function toggleService() {
    if (isService) {
      onTaskTypeChanged(removeTaskService(yaml, task.name));
      return;
    }
    const unsupported = [
      task.array && "array",
      task.fanOut && "fanOut",
      task.retry && "retry",
      task.when && "when",
      task.outputs && "outputs",
    ].filter((field): field is string => typeof field === "string");
    if (unsupported.length > 0
      && !window.confirm(`Converting this task to a service removes ${unsupported.join(", ")}. Continue?`)) return;
    onTaskTypeChanged(setTaskService(yaml, task.name));
  }

  function changeMemoryMode(mode: WorkflowMemoryMode) {
    onYamlChange(setMemoryMode(yaml, task.name, mode));
  }

  function changeMemoryValue(amount: string, unit = memory.unit) {
    onYamlChange(setMemoryMode(yaml, task.name, memoryMode, formatWorkflowMemory(amount, unit)));
  }

  function setImplementation(value: string) {
    const nextYaml = setWorkflowTaskField(yaml, task.name, ["multinode", "implementation"], value);
    const launch = value === "generic" ? "sbatch" : "srun";
    onYamlChange(setWorkflowTaskField(nextYaml, task.name, ["launch"], launch));
  }

  function changeEnvironmentName(oldName: string, nextName: string, value: string) {
    if (oldName === nextName) return;
    let nextYaml = setWorkflowEnvironmentVariable(yaml, task.name, oldName, undefined);
    if (nextName) nextYaml = setWorkflowEnvironmentVariable(nextYaml, task.name, nextName, value);
    onYamlChange(nextYaml);
  }

  function addEnvironmentVariable() {
    const existing = new Set(env.map((entry) => entry.name));
    let suffix = 1;
    while (existing.has(`ENV_${String(suffix)}`)) suffix += 1;
    onYamlChange(setWorkflowEnvironmentVariable(yaml, task.name, `ENV_${String(suffix)}`, ""));
  }

  function addDependency(value: string) {
    if (!value) return;
    const result = addWorkflowDependency(yaml, task.name, value);
    if (result.error) {
      onDependencyError(result.error === "cycle"
        ? "That dependency would create a cycle."
        : result.error === "duplicate"
          ? "That dependency already exists."
          : result.error === "self"
            ? "A task cannot depend on itself."
            : "The dependency task could not be found.");
      return;
    }
    onYamlChange(result.yaml);
  }

  function switchScriptMode(nextMode: "script" | "command") {
    if (nextMode === scriptMode) return;
    const hasContent = scriptMode === "script"
      ? typeof task.script === "string" ? task.script.length > 0 : task.script !== undefined
      : (task.command?.length ?? 0) > 0;
    if (hasContent && !window.confirm(`Switching to ${nextMode} will discard the current ${scriptMode}. Continue?`)) return;
    const nextYaml = scriptMode === "script"
      ? setWorkflowTaskField(yaml, task.name, ["script"], "")
      : setWorkflowTaskField(yaml, task.name, ["command"], []);
    onYamlChange(nextYaml);
    setScriptMode(nextMode);
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="space-y-3 border-b border-border p-3">
        <div>
          <p className="font-mono text-[9px] uppercase tracking-[0.1em] text-muted-foreground">Task type</p>
          <div className="mt-1.5">
            <SegmentButtons
              value={taskMode}
              options={isService
                ? [{ value: "default", label: "Default" }, { value: "multinode", label: "Multinode" }]
                : [{ value: "default", label: "Default" }, { value: "multinode", label: "Multinode" }, { value: "array", label: "Task array" }]}
              onChange={setType}
            />
          </div>
          {issuesFor([".multinode", ".array"]).length > 0 ? <FieldErrors issues={issuesFor([".multinode", ".array"])} /> : null}
        </div>
      </div>
      <Tabs
        value={activeTab}
        onValueChange={(value: string | null) => {
          if (value === "task") onTabChange("task");
          else if (value === "environment") onTabChange("environment");
          else if (value === "resources") onTabChange("resources");
        }}
        className="min-h-0 flex-1"
      >
        <TabsList variant="line" className="h-9 w-full justify-start border-b border-border px-2">
          {taskTabs.map((tab) => (
            <TabsTrigger key={tab.value} value={tab.value} className="gap-1 px-2 text-[9px]">
              {tab.label}
              {hasTabIssues(tab.suffixes) ? <AlertTriangle aria-label="Validation error" className="size-3 text-destructive" /> : null}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="task" className="min-h-0 space-y-4 overflow-y-auto p-3">
          <div className="grid gap-1.5">
            <Label htmlFor="editor-task-name">Name</Label>
            <Input id="editor-task-name" value={task.name} onChange={(event) => {
              const nextName = event.target.value;
              onTaskRenamed(task.name, nextName, renameWorkflowTask(yaml, task.name, nextName));
            }} />
            <FieldErrors issues={issuesFor([".name"])} />
          </div>
          <div className="grid gap-1.5">
            <Label>Launch</Label>
            <SegmentButtons
              value={task.launch ?? "sbatch"}
              options={[{ value: "sbatch", label: "sbatch" }, { value: "srun", label: "srun" }]}
              onChange={(value) => { setField(["launch"], value); }}
            />
            <FieldErrors issues={issuesFor([".launch"])} />
          </div>
          <div className="grid gap-1.5">
            <Label>Script</Label>
            <WorkflowYamlEditor
              value={scriptMode === "script" ? (typeof task.script === "string" ? task.script : task.script?.inline ?? "") : task.command?.join("\n") ?? ""}
              readOnly={false}
              language={scriptMode === "script" ? "shell" : "plaintext"}
              height="9rem"
              label={scriptMode === "script" ? "Inline task script" : "Task command arguments"}
              onChange={(value) => {
                if (scriptMode === "script") setField(["script"], value);
                else {
                  const args = value.split(/\r\n|\n/);
                  if (args.at(-1) === "") args.pop();
                  setField(["command"], args);
                }
              }}
              markers={issuesFor([".script", ".command"]).map((issue) => {
                const location = validationPathToLocation(yaml, issue.path);
                return location ? {
                  startLineNumber: location.startLineNumber,
                  startColumn: location.startColumn,
                  endLineNumber: location.endLineNumber,
                  endColumn: location.endColumn,
                  message: `${issue.code}: ${issue.message}`,
                  severity: "error" as const,
                } : null;
              }).filter((marker): marker is NonNullable<typeof marker> => marker !== null)}
            />
            <button type="button" className="justify-self-start font-mono text-[9px] text-primary underline" onClick={() => { switchScriptMode(scriptMode === "script" ? "command" : "script"); }}>
              {scriptMode === "script" ? "Switch to command" : "Switch to script"}
            </button>
            <FieldErrors issues={issuesFor([".script", ".command"])} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="editor-walltime">{isService ? "Walltime" : "Timeout"}</Label>
            <Input id="editor-walltime" value={task.resources?.walltime ?? ""} placeholder="1h, 30m, 1h30m" onChange={(event) => { setField(["resources", "walltime"], event.target.value); }} />
            <p className="text-[9px] text-muted-foreground">{isService ? "Required for service tasks. e.g. 1h, 30m, 1h30m" : "e.g. 1h, 30m, 1h30m"}</p>
            <FieldErrors issues={issuesFor([".resources.walltime"])} />
          </div>
          <section className="space-y-2 border-t border-border pt-3">
            <div className="flex items-center justify-between gap-2">
              <h3 className="font-mono text-[10px] font-semibold uppercase tracking-[0.08em]">{isService ? "Service" : "Task kind"}</h3>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!isService && !canConvertToService}
                title={!isService && !canConvertToService ? "Condition and reserved task kinds cannot be services" : undefined}
                onClick={toggleService}
              >
                {isService ? "Convert to task" : "Convert to service"}
              </Button>
            </div>
            {isService ? (
              <>
                <div className="flex items-center justify-between gap-3">
                  <div>
                    <Label htmlFor="editor-service-auto-stop">Auto stop</Label>
                    <p className="mt-0.5 text-[9px] text-muted-foreground">
                      Stop when every dependent task has finished. Off: runs until it exits or reaches its walltime.
                    </p>
                  </div>
                  <button
                    id="editor-service-auto-stop"
                    type="button"
                    role="switch"
                    aria-label="Auto stop"
                    aria-checked={task.service?.autoStop !== false}
                    onClick={() => { setField(["service", "autoStop"], task.service?.autoStop === false); }}
                    className={`relative h-5 w-9 shrink-0 border border-border transition-colors ${task.service?.autoStop === false ? "bg-muted" : "bg-primary"}`}
                  >
                    <span className={`absolute top-0.5 size-3.5 bg-card transition-transform ${task.service?.autoStop === false ? "left-0.5" : "left-[1.05rem]"}`} />
                  </button>
                </div>
                <div className="border border-border bg-muted/20 p-2">
                  <p className="font-mono text-[8px] uppercase text-muted-foreground">Variables available to dependents</p>
                  <p className="mt-1 break-all font-mono text-[9px] text-foreground">CUSTOS_SERVICE_{normalizeServiceEnvName(task.name)}_JOBID</p>
                  <p className="break-all font-mono text-[9px] text-foreground">CUSTOS_SERVICE_{normalizeServiceEnvName(task.name)}_HOST</p>
                </div>
                <FieldErrors issues={issuesFor([".service"])} />
              </>
            ) : (
              <p className="text-[9px] text-muted-foreground">Convert this task to a long-running service with an auto-stop lifecycle.</p>
            )}
          </section>
          <div className="grid gap-1.5">
            <Label htmlFor="editor-dep-failure">On dependency failure</Label>
            <NativeSelect id="editor-dep-failure" value={task.onDependencyFailure ?? ""} onChange={(value) => { if (typeof value === "string") setField(["onDependencyFailure"], value); }}>
              <option value="">Default</option>
              <option value="fail">Fail</option>
              <option value="run">Run</option>
            </NativeSelect>
            <FieldErrors issues={issuesFor([".onDependencyFailure"])} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="editor-dependency-select">Dependencies</Label>
            <div className="flex gap-2">
              <NativeSelect id="editor-dependency-select" value="" onChange={(value) => { if (typeof value === "string") addDependency(value); }}>
                <option value="">Add dependency…</option>
                {tasks.filter((item) => item.name !== task.name && !defaultDependencies.includes(item.name)).map((item) => <option key={item.name} value={item.name}>{item.name}</option>)}
              </NativeSelect>
            </div>
            {defaultDependencies.map((dependency) => (
              <div key={dependency} className="flex items-center justify-between border border-border px-2 py-1 font-mono text-[10px]">
                <span>{dependency}</span>
                <button type="button" aria-label={`Remove dependency ${dependency}`} onClick={() => { onYamlChange(removeWorkflowDependency(yaml, task.name, dependency)); }}>
                  <X aria-hidden="true" className="size-3" />
                </button>
              </div>
            ))}
            <FieldErrors issues={issuesFor([".dependsOn"])} />
          </div>
        </TabsContent>
        <TabsContent value="environment" className="min-h-0 space-y-4 overflow-y-auto p-3">
          <div className="grid gap-1.5">
            <Label htmlFor="editor-image-uri">Image URI</Label>
            <Input id="editor-image-uri" value={task.image?.uri ?? ""} placeholder="docker://registry/image:tag" onChange={(event) => { setField(["image", "uri"], event.target.value); }} />
            <p className="text-[9px] text-muted-foreground">docker://…, oras://… or an absolute .sif path. Leave empty to run on the host.</p>
            <FieldErrors issues={issuesFor([".image"])} />
          </div>
          {task.image?.uri ? (
            <ImagePullSecretEditor task={task} tenant={tenant} yaml={yaml} issues={taskIssues} secrets={secrets} onYamlChange={onYamlChange} />
          ) : null}
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label>Environment variables</Label>
              <Button type="button" variant="outline" size="sm" onClick={addEnvironmentVariable}>Add variable</Button>
            </div>
            {env.map((entry, index) => (
              <div key={`${String(index)}-${entry.name}`} className="grid grid-cols-[1fr_1fr_auto] gap-1.5">
                <Input aria-label={`Environment key ${entry.name || String(index + 1)}`} value={entry.name} placeholder="KEY" onChange={(event) => { changeEnvironmentName(entry.name, event.target.value, entry.value); }} />
                <Input aria-label={`Environment value ${entry.name || String(index + 1)}`} value={entry.value} placeholder="value" onChange={(event) => { onYamlChange(setWorkflowEnvironmentVariable(yaml, task.name, entry.name, event.target.value)); }} />
                <Button type="button" variant="ghost" size="icon-sm" aria-label={`Remove environment variable ${entry.name}`} onClick={() => { onYamlChange(setWorkflowEnvironmentVariable(yaml, task.name, entry.name, undefined)); }}>
                  <X aria-hidden="true" className="size-3" />
                </Button>
              </div>
            ))}
            <FieldErrors issues={issuesFor([".env"])} />
          </div>
          {([
            ["workingDirectory", "Working directory", "Absolute path"],
            ["stdout", "Stdout", "Output path"],
            ["stderr", "Stderr", "Error path"],
          ] as const).map(([key, label, placeholder]) => (
            <div key={key} className="grid gap-1.5">
              <Label htmlFor={`editor-${key}`}>{label}</Label>
              <Input id={`editor-${key}`} value={textValue(task[key])} placeholder={placeholder} onChange={(event) => { setField([key], event.target.value); }} />
              <FieldErrors issues={issuesFor([`.${key}`])} />
            </div>
          ))}
        </TabsContent>
        <TabsContent value="resources" className="min-h-0 space-y-4 overflow-y-auto p-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="editor-resource-cpu">CPU cores</Label>
              <Input id="editor-resource-cpu" type="number" min="1" value={numericValue(task.resources?.cpu)} onChange={(event) => { setField(["resources", "cpu"], event.target.value ? Number(event.target.value) : ""); }} />
              <FieldErrors issues={issuesFor([".resources.cpu"])} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="editor-cpu-affinity">CPU affinity</Label>
              <NativeSelect id="editor-cpu-affinity" value={task.resources?.cpuAffinity ?? ""} onChange={(value) => { if (typeof value === "string") setField(["resources", "cpuAffinity"], value); }}>
                <option value="">Default</option>
                <option value="none">none</option>
                <option value="core">core</option>
                <option value="socket">socket</option>
                <option value="numa">numa</option>
              </NativeSelect>
              <FieldErrors issues={issuesFor([".resources.cpuAffinity"])} />
            </div>
            <div className="grid gap-1.5 sm:col-span-2">
              <Label>Memory mode</Label>
              <SegmentButtons
                value={memoryMode}
                options={[{ value: "perNode", label: "Per node" }, { value: "perCpu", label: "Per CPU" }]}
                onChange={(value) => { changeMemoryMode(value as WorkflowMemoryMode); }}
              />
              <Label htmlFor="editor-memory-amount">{memoryMode === "perCpu" ? "Memory per CPU" : "Memory per node"}</Label>
              <div className="grid grid-cols-[minmax(0,1fr)_7rem] gap-1.5">
                <Input id="editor-memory-amount" inputMode="decimal" value={memory.amount} onChange={(event) => { changeMemoryValue(event.target.value); }} />
                <NativeSelect id="editor-memory-unit" value={memory.unit} onChange={(value) => { if (typeof value === "string") changeMemoryValue(memory.amount, value as WorkflowMemoryUnit); }}>
                  <option value="MiB">MiB</option>
                  <option value="GiB">GiB</option>
                  <option value="MB">MB</option>
                  <option value="GB">GB</option>
                </NativeSelect>
              </div>
              <FieldErrors issues={issuesFor([".resources.memory", ".resources.memoryPerCpu", ".resources.memoryPerNode"])} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="editor-gpu-count">GPUs</Label>
              <Input id="editor-gpu-count" type="number" min="1" value={numericValue(task.resources?.gpu?.count)} onChange={(event) => { setField(["resources", "gpu", "count"], event.target.value ? Number(event.target.value) : ""); }} />
              <FieldErrors issues={issuesFor([".resources.gpu"])} />
            </div>
            <label className="flex items-center gap-2 self-end pb-2 font-mono text-[9px] uppercase tracking-[0.06em]">
              <input type="checkbox" checked={task.resources?.exclusive ?? false} onChange={(event) => { setField(["resources", "exclusive"], event.target.checked); }} />
              Exclusive node
            </label>
            {([
              ["partition", "Partition", "Node pool"],
              ["qos", "QoS", "Quality of service"],
            ] as const).map(([key, label, placeholder]) => (
              <div key={key} className="grid gap-1.5">
                <Label htmlFor={`editor-${key}`}>{label}</Label>
                <Input id={`editor-${key}`} value={textValue(task[key])} placeholder={placeholder} onChange={(event) => { setField([key], event.target.value); }} />
                <FieldErrors issues={issuesFor([`.${key}`])} />
              </div>
            ))}
            <div className="grid gap-1.5">
              <Label htmlFor="editor-resource-constraints">Constraints</Label>
              <Input id="editor-resource-constraints" value={task.resources?.constraints ?? ""} placeholder="Slurm constraints" onChange={(event) => { setField(["resources", "constraints"], event.target.value); }} />
              <FieldErrors issues={issuesFor([".resources.constraints"])} />
            </div>
          </div>
          {taskMode === "default" ? (
            <div className="grid gap-3 border-t border-border pt-3 sm:grid-cols-2">
              {([
                ["nodes", "Nodes"],
                ["tasks", "Tasks"],
                ["tasksPerNode", "Tasks per node"],
                ["cpusPerTask", "CPUs per task"],
              ] as const).map(([key, label]) => (
                <div key={key} className="grid gap-1.5">
                  <Label htmlFor={`editor-${key}`}>{label}</Label>
                  <Input id={`editor-${key}`} type="number" min="1" value={numericValue(task.resources?.[key])} onChange={(event) => { setField(["resources", key], event.target.value ? Number(event.target.value) : ""); }} />
                  <FieldErrors issues={issuesFor([`.resources.${key}`])} />
                </div>
              ))}
            </div>
          ) : null}
          {taskMode === "multinode" ? (
            <div className="grid gap-3 border-t border-border pt-3 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="editor-multinode-nodes">Multinode nodes</Label>
                <Input id="editor-multinode-nodes" type="number" min="1" value={numericValue(task.multinode?.nodes)} onChange={(event) => { setField(["multinode", "nodes"], event.target.value ? Number(event.target.value) : ""); }} />
                <FieldErrors issues={issuesFor([".multinode.nodes"])} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="editor-multinode-implementation">Implementation</Label>
                <NativeSelect id="editor-multinode-implementation" value={task.multinode?.implementation ?? "openmpi"} onChange={(value) => { if (typeof value === "string") setImplementation(value); }}>
                  <option value="openmpi">openmpi</option>
                  <option value="mpich">mpich</option>
                  <option value="generic">generic</option>
                </NativeSelect>
                <FieldErrors issues={issuesFor([".multinode.implementation", ".launch"])} />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="editor-multinode-procs">Procs per node</Label>
                <Input id="editor-multinode-procs" type="number" min="1" value={numericValue(task.multinode?.procsPerNode)} onChange={(event) => { setField(["multinode", "procsPerNode"], event.target.value ? Number(event.target.value) : ""); }} />
                <FieldErrors issues={issuesFor([".multinode.procsPerNode"])} />
              </div>
              <p className="self-end pb-2 text-[9px] text-muted-foreground">CPU cores are per node.</p>
            </div>
          ) : null}
          {taskMode === "array" ? (
            <div className="grid gap-3 border-t border-border pt-3 sm:grid-cols-2">
              {([
                ["start", "Start", 0],
                ["end", "End", 9],
                ["step", "Step", undefined],
                ["maxConcurrent", "Max concurrent", undefined],
              ] as const).map(([key, label, defaultValue]) => (
                <div key={key} className="grid gap-1.5">
                  <Label htmlFor={`editor-array-${key}`}>{label}</Label>
                  <Input id={`editor-array-${key}`} type="number" min="0" value={numericValue(task.array?.[key] ?? defaultValue)} onChange={(event) => { setField(["array", key], event.target.value ? Number(event.target.value) : ""); }} />
                  <FieldErrors issues={issuesFor([`.array.${key}`])} />
                </div>
              ))}
            </div>
          ) : null}
        </TabsContent>
      </Tabs>
    </div>
  );
}
