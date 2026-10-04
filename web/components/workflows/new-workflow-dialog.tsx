"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { Project } from "@/lib/api/client";
import {
  createWorkflowWithDraft,
  starterWorkflowSpec,
  validateWorkflowName,
  WORKFLOW_NAME_MAX_LENGTH,
  type WorkflowCreationFailure,
} from "@/lib/workflow/create";
import { WorkflowCreationError } from "./workflow-creation-error";

export function NewWorkflowDialog({ tenant, projects, csrfToken }: {
  tenant: string;
  projects: Project[];
  csrfToken: string;
}) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [project, setProject] = useState(projects[0]?.id ?? "");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [failure, setFailure] = useState<WorkflowCreationFailure | null>(null);

  function changeOpen(next: boolean) {
    setOpen(next);
    if (next) {
      setProject(projects[0]?.id ?? "");
      setName("");
      setDescription("");
      setFailure(null);
    }
  }

  async function submit() {
    if (!project) {
      setFailure({ kind: "workflow", message: "Choose a project and enter a workflow name." });
      return;
    }
    const normalizedName = name.trim();
    const nameError = validateWorkflowName(normalizedName);
    if (nameError) {
      setFailure({ kind: "workflow", message: nameError });
      return;
    }

    setSubmitting(true);
    setFailure(null);
    try {
      const result = await createWorkflowWithDraft(
        tenant,
        {
          project,
          name: normalizedName,
          ...(description.trim() ? { description: description.trim() } : {}),
        },
        starterWorkflowSpec(normalizedName),
        csrfToken,
      );
      if (!result.ok) {
        setFailure(result.failure);
        return;
      }
      setOpen(false);
      router.push(`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(result.workflowId)}/versions/${encodeURIComponent(result.versionId)}/edit`);
    } catch {
      setFailure({ kind: "network", message: "The request could not be sent. Check your connection and try again." });
    } finally {
      setSubmitting(false);
    }
  }

  const trigger = (
    <span
      className="inline-flex"
      tabIndex={projects.length === 0 ? 0 : undefined}
      title={projects.length === 0 ? "An active project is required to create a workflow." : undefined}
    >
      <DialogTrigger render={<Button type="button" variant="default" disabled={projects.length === 0} />}>
        New workflow
      </DialogTrigger>
    </span>
  );

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      {trigger}
      <DialogContent className="w-[min(94vw,34rem)] sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">New workflow</DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">
            Creates a workflow and opens its first draft in the editor.
          </DialogDescription>
        </DialogHeader>
        <form className="grid gap-4" onSubmit={(event) => { event.preventDefault(); void submit(); }}>
          <div className="grid gap-1.5">
            <Label htmlFor="new-workflow-project">Project</Label>
            <Select value={project || "none"} onValueChange={(value) => { if (typeof value === "string") setProject(value === "none" ? "" : value); }}>
              <SelectTrigger id="new-workflow-project" aria-label="Project" className="h-8">
                <SelectValue>{projects.find((item) => item.id === project)?.name ?? "Choose a project"}</SelectValue>
              </SelectTrigger>
              <SelectContent align="start">
                {projects.map((item) => <SelectItem key={item.id} value={item.id}>{item.name}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="new-workflow-name">Name</Label>
            <Input
              id="new-workflow-name"
              value={name}
              maxLength={WORKFLOW_NAME_MAX_LENGTH}
              placeholder="my-workflow"
              aria-describedby="new-workflow-name-help"
              onChange={(event) => { setName(event.target.value); }}
            />
            <p id="new-workflow-name-help" className="text-[10px] text-muted-foreground">
              1–63 lowercase letters, numbers, or hyphens; start and end with a letter or number.
            </p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="new-workflow-description">Description · optional</Label>
            <textarea
              id="new-workflow-description"
              value={description}
              rows={3}
              onChange={(event) => { setDescription(event.target.value); }}
              className="w-full resize-y border border-input bg-card px-2 py-2 text-sm text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50"
            />
          </div>
          {failure ? (
            <p role="alert" className="border border-destructive/40 bg-destructive/10 p-2 font-mono text-xs text-destructive">
              <WorkflowCreationError tenant={tenant} failure={failure} />
            </p>
          ) : null}
          <div className="flex justify-end gap-2 border-t border-border pt-3">
            <Button type="button" variant="outline" onClick={() => { changeOpen(false); }}>Cancel</Button>
            <Button type="submit" disabled={submitting || projects.length === 0}>
              {submitting ? "Creating…" : "Create workflow"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
