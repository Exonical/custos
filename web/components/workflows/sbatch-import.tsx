"use client";

import dynamic from "next/dynamic";
import Link from "next/link";
import { useMemo, useState, type ChangeEvent, type ReactNode } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { WorkflowGraph } from "@/components/workflows/workflow-graph";
import type { Project } from "@/lib/api/client";
import { detailText } from "@/lib/api/error-details";
import { errorText, postJson } from "@/lib/api/bff-fetch";
import { sanitizeDownloadFilename } from "@/lib/download";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";
import { validateSbatchImport, type SbatchImportScript } from "@/lib/workflow/sbatch-import";
import type { components } from "@/lib/api/schema";

type ImportResponse = components["schemas"]["WorkflowSbatchImportResponse"];
type ImportedTask = components["schemas"]["WorkflowSbatchImportedTask"];
type Diagnostic = components["schemas"]["Diagnostic"];
type SelectedScript = SbatchImportScript & { id: string; byteLength: number };
type ServerError = { error?: { code?: unknown; details?: unknown } };

const WorkflowYamlEditor = dynamic(() => import("@/components/workflows/yaml-editor").then((module) => module.WorkflowYamlEditor), {
  ssr: false,
  loading: () => <div className="grid h-[34rem] place-items-center border border-border bg-card font-mono text-xs text-muted-foreground">Loading editor.</div>,
});

function importErrorText(value: unknown, status: number): string {
  if (status === 403) return "You do not have permission to import sbatch scripts, or sbatch import is disabled by policy.";
  if (status === 413) return "The import request is too large. Import fewer files at once.";
  if (status === 429) return "Import is rate limited. Try again later.";
  const error = value && typeof value === "object" ? (value as ServerError).error : undefined;
  const code = typeof error?.code === "string" ? error.code : `HTTP_${String(status)}`;
  const details = Array.isArray(error?.details) ? detailText(error.details) : "";
  if ((status === 400 || status === 422) && details) return details;
  if (status === 400 || status === 422) return code;
  return errorText(value, status, "import sbatch scripts");
}

function severityClass(severity: string): string {
  const destructive = severity === "ERROR" || severity === "POLICY_VIOLATION" || severity === "SECURITY_VIOLATION";
  return destructive
    ? "border-destructive/50 bg-destructive/10 text-destructive"
    : "border-border bg-muted text-muted-foreground";
}

function diagnosticLabel(diagnostic: Diagnostic): string {
  const position = diagnostic.line ? `line ${String(diagnostic.line)}: ` : "";
  return `${position}${diagnostic.code} - ${diagnostic.message}`;
}

function isImportResponse(value: unknown): value is ImportResponse {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const candidate = value as Partial<ImportResponse>;
  return candidate.spec !== undefined && typeof candidate.yaml === "string" && Array.isArray(candidate.tasks);
}

function proposalFilename(name: string): string {
  return sanitizeDownloadFilename(`${name}.yaml`, "workflow.yaml");
}

export function SbatchImport({ tenant, projects, csrfToken }: {
  tenant: string;
  projects: Project[];
  csrfToken: string;
}) {
  const router = useRouter();
  const [workflowName, setWorkflowName] = useState("");
  const [scripts, setScripts] = useState<SelectedScript[]>([]);
  const [selectionError, setSelectionError] = useState("");
  const [importError, setImportError] = useState("");
  const [importing, setImporting] = useState(false);
  const [result, setResult] = useState<ImportResponse | null>(null);
  const [selectedTask, setSelectedTask] = useState<string | null>(null);
  const [projectId, setProjectId] = useState(projects[0]?.id ?? "");
  const [createName, setCreateName] = useState("");
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<ReactNode>(null);
  const tenantPath = `/api/bff/tenants/${encodeURIComponent(tenant)}`;
  const validation = useMemo(
    () => validateSbatchImport(workflowName, scripts.map(({ filename, content }) => ({ filename, content }))),
    [workflowName, scripts],
  );
  const validationError = validation.valid ? "" : validation.error;
  const normalizedSpec = useMemo(() => result ? normalizeWorkflowSpec(result.spec) : null, [result]);

  async function selectFiles(event: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(event.currentTarget.files ?? []);
    event.currentTarget.value = "";
    if (files.length === 0) return;
    setSelectionError("");
    try {
      const selected = await Promise.all(files.map(async (file) => {
        const content = await file.text();
        return {
          id: crypto.randomUUID(),
          filename: file.name || "script.sbatch",
          content,
          byteLength: new TextEncoder().encode(content).byteLength,
        };
      }));
      setScripts((current) => [...current, ...selected]);
    } catch {
      setSelectionError("One or more files could not be read as UTF-8 text.");
    }
  }

  function removeScript(id: string) {
    setScripts((current) => current.filter((script) => script.id !== id));
  }

  function downloadProposal() {
    if (!result) return;
    const objectUrl = URL.createObjectURL(new Blob([result.yaml], { type: "application/yaml;charset=utf-8" }));
    const anchor = document.createElement("a");
    anchor.href = objectUrl;
    anchor.download = proposalFilename(result.spec.metadata.name);
    anchor.style.display = "none";
    document.body.append(anchor);
    anchor.click();
    anchor.remove();
    window.setTimeout(() => {
      URL.revokeObjectURL(objectUrl);
    }, 0);
  }

  async function importScripts() {
    if (!validation.valid) {
      setImportError(validation.error);
      return;
    }
    setImporting(true);
    setImportError("");
    setCreateError(null);
    try {
      const response = await postJson(`${tenantPath}/workflow-imports/sbatch`, validation.request, csrfToken);
      if (response.status !== 200) {
        setImportError(importErrorText(response.payload, response.status));
        return;
      }
      if (!isImportResponse(response.payload)) {
        setImportError("The import response was incomplete. Try again.");
        return;
      }
      const imported = response.payload;
      setResult(imported);
      setSelectedTask(null);
      setCreateName(imported.spec.metadata.name);
    } catch {
      setImportError("The request could not be sent. Check your connection and try again.");
    } finally {
      setImporting(false);
    }
  }

  async function createWorkflow() {
    if (!result) return;
    if (!projectId || !createName.trim()) {
      setCreateError("Choose a project and enter a workflow name.");
      return;
    }
    setCreating(true);
    setCreateError(null);
    try {
      const created = await postJson(`${tenantPath}/workflows`, {
        project: projectId,
        name: createName.trim(),
      }, csrfToken);
      const workflowId = created.status === 201 && created.payload && typeof created.payload === "object"
        ? (created.payload as { id?: unknown }).id
        : undefined;
      if (typeof workflowId !== "string") {
        setCreateError(errorText(created.payload, created.status, "create workflows"));
        return;
      }
      const workflowHref = `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}`;
      const version = await postJson(`${tenantPath}/workflows/${encodeURIComponent(workflowId)}/versions`, result.spec, csrfToken);
      if (version.status !== 201) {
        setCreateError(
          <>
            The workflow was created, but its draft could not be saved: {errorText(version.payload, version.status, "create versions")}{" "}
            <Link className="text-primary underline" href={workflowHref}>Open the workflow</Link>.
          </>,
        );
        return;
      }
      router.push(workflowHref);
    } catch {
      setCreateError("The request could not be sent. Check your connection and try again.");
    } finally {
      setCreating(false);
    }
  }

  return (
    <div className="space-y-5">
      <section aria-label="Sbatch import" className="space-y-4 border border-border bg-card p-4">
        <p className="text-sm text-muted-foreground">Import only proposes a draft. Nothing is created until you choose Create workflow below.</p>
        <form className="grid gap-4" onSubmit={(event) => { event.preventDefault(); void importScripts(); }}>
          <div className="grid gap-1.5">
            <Label htmlFor="sbatch-import-name">Workflow name (optional)</Label>
            <Input id="sbatch-import-name" value={workflowName} maxLength={256} onChange={(event) => { setWorkflowName(event.target.value); }} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="sbatch-import-files">Sbatch scripts</Label>
            <Input
              id="sbatch-import-files"
              type="file"
              multiple
              accept=".sh,.sbatch,.slurm,.bash,text/x-shellscript,text/plain,*/*"
              onChange={(event) => { void selectFiles(event); }}
            />
            <p className="text-xs text-muted-foreground">Choose 1–20 files. Each file is limited to 256 KiB; the encoded request is limited to 2 MiB.</p>
          </div>
          {scripts.length > 0 ? (
            <ul aria-label="Selected scripts" className="divide-y divide-border border border-border">
              {scripts.map((script) => (
                <li key={script.id} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2">
                  <span className="min-w-0 truncate font-mono text-xs">{script.filename}</span>
                  <span className="font-mono text-[10px] text-muted-foreground">{script.byteLength.toLocaleString()} bytes</span>
                  <Button type="button" variant="outline" size="sm" aria-label={`Remove ${script.filename}`} onClick={() => { removeScript(script.id); }}>
                    Remove
                  </Button>
                </li>
              ))}
            </ul>
          ) : null}
          {selectionError ? <p role="alert" className="text-xs text-destructive">{selectionError}</p> : null}
          {validationError ? <p role="alert" className="text-xs text-destructive">{validationError}</p> : null}
          {importError ? <p role="alert" className="border border-destructive/40 bg-destructive/10 p-2 font-mono text-xs text-destructive">{importError}</p> : null}
          <div className="flex justify-end border-t border-border pt-3">
            <Button type="submit" variant="default" disabled={importing || !validation.valid}>
              {importing ? "Importing…" : "Import"}
            </Button>
          </div>
        </form>
      </section>

      {result && normalizedSpec ? (
        <section aria-label="Import proposal review" className="space-y-5 border border-border bg-card p-4">
          <header>
            <h2 className="font-mono text-sm font-semibold uppercase tracking-[0.1em]">Review draft proposal</h2>
            <p className="mt-1 text-sm text-muted-foreground">This proposal is read-only. No workflow or version exists until you choose Create workflow.</p>
          </header>

          <section aria-label="Imported files" className="overflow-hidden border border-border">
            <Table>
              <caption className="sr-only">Imported sbatch files and diagnostics</caption>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead>File</TableHead>
                  <TableHead>Task</TableHead>
                  <TableHead>Imported directives</TableHead>
                  <TableHead>Diagnostics</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {result.tasks.map((task: ImportedTask) => (
                  <TableRow key={`${task.filename}-${task.task}`}>
                    <TableCell className="font-mono text-xs">{task.filename}</TableCell>
                    <TableCell className="font-mono text-xs">{task.task}</TableCell>
                    <TableCell className="max-w-72 whitespace-normal text-xs">
                      {task.imported.length === 0 ? "None" : `${String(task.imported.length)}: ${task.imported.join(", ")}`}
                    </TableCell>
                    <TableCell className="min-w-64">
                      {task.diagnostics.length === 0 ? (
                        <span className="font-mono text-[10px] text-muted-foreground">No diagnostics</span>
                      ) : (
                        <ul className="grid gap-1">
                          {task.diagnostics.map((diagnostic, index) => (
                            <li key={`${diagnostic.code}-${String(index)}`} className={`border px-2 py-1 text-xs ${severityClass(diagnostic.severity)}`}>
                              <span className="font-mono text-[10px] uppercase">{diagnostic.severity}</span>
                              <span className="ml-2">{diagnosticLabel(diagnostic)}</span>
                            </li>
                          ))}
                        </ul>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </section>

          <section aria-label="Proposed workflow graph" className="space-y-2">
            <h3 className="font-mono text-[11px] font-semibold uppercase tracking-[0.1em]">Task graph</h3>
            <WorkflowGraph spec={normalizedSpec} selectedTask={selectedTask} onSelectTask={setSelectedTask} />
          </section>

          <section aria-label="Proposed workflow YAML" className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <h3 className="font-mono text-[11px] font-semibold uppercase tracking-[0.1em]">Proposed YAML</h3>
              <Button type="button" variant="outline" onClick={downloadProposal}>Download YAML</Button>
            </div>
            <WorkflowYamlEditor value={result.yaml} />
          </section>

          <form className="grid gap-4 border-t border-border pt-4" onSubmit={(event) => { event.preventDefault(); void createWorkflow(); }}>
            <div>
              <h3 className="font-mono text-[11px] font-semibold uppercase tracking-[0.1em]">Create workflow</h3>
              <p className="mt-1 text-xs text-muted-foreground">This saves the proposal as the workflow’s first draft version.</p>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="sbatch-import-project">Project</Label>
              <Select value={projectId || "none"} onValueChange={(value) => { if (typeof value === "string") setProjectId(value === "none" ? "" : value); }}>
                <SelectTrigger id="sbatch-import-project" aria-label="Project" className="h-8">
                  <SelectValue>{projects.find((project) => project.id === projectId)?.name ?? "Choose a project"}</SelectValue>
                </SelectTrigger>
                <SelectContent align="start">
                  {projects.map((project) => <SelectItem key={project.id} value={project.id}>{project.name}</SelectItem>)}
                </SelectContent>
              </Select>
              {projects.length === 0 ? <p className="text-xs text-muted-foreground">No active projects are available.</p> : null}
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="sbatch-create-workflow-name">Workflow name</Label>
              <Input id="sbatch-create-workflow-name" value={createName} maxLength={128} onChange={(event) => { setCreateName(event.target.value); }} />
            </div>
            {createError ? <p role="alert" className="border border-destructive/40 bg-destructive/10 p-2 font-mono text-xs text-destructive">{createError}</p> : null}
            <div className="flex justify-end border-t border-border pt-3">
              <Button type="submit" variant="default" disabled={creating || projects.length === 0}>
                {creating ? "Creating…" : "Create workflow"}
              </Button>
            </div>
          </form>
        </section>
      ) : null}
    </div>
  );
}
