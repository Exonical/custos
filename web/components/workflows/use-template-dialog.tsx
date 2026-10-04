"use client";

import { useState, type ReactNode } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { Project, WorkflowTemplate } from "@/lib/api/client";
import {
  createWorkflowWithDraft,
  slugifyWorkflowMetadataName,
  WORKFLOW_NAME_MAX_LENGTH,
} from "@/lib/workflow/create";
import { WorkflowCreationError } from "./workflow-creation-error";

export function UseTemplateDialog({ tenant, template, projects, csrfToken }: {
  tenant: string;
  template: WorkflowTemplate;
  projects: Project[];
  csrfToken: string;
}) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [project, setProject] = useState(projects[0]?.id ?? "");
  const [name, setName] = useState(slugifyWorkflowMetadataName(template.title, "workflow"));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<ReactNode>(null);

  function changeOpen(next: boolean) {
    setOpen(next);
    if (next) {
      setError(null);
      setName(slugifyWorkflowMetadataName(template.title, "workflow"));
    }
  }

  async function submit() {
    if (!project || !name.trim()) {
      setError("Choose a project and enter a workflow name.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const result = await createWorkflowWithDraft(
        tenant,
        { project, name: name.trim(), description: template.summary },
        template.spec,
        csrfToken,
      );
      if (!result.ok) {
        setError(<WorkflowCreationError tenant={tenant} failure={result.failure} />);
        return;
      }
      setOpen(false);
      router.push(`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(result.workflowId)}`);
    } catch {
      setError("The request could not be sent. Check your connection and try again.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogTrigger render={<Button type="button" variant="default" disabled={projects.length === 0} />}>Use template</DialogTrigger>
      <DialogContent className="w-[min(94vw,32rem)] sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Use {template.title}</DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">Creates a workflow with this template as its first draft version.</DialogDescription>
        </DialogHeader>
        <form className="grid gap-4" onSubmit={(event) => { event.preventDefault(); void submit(); }}>
          <div className="grid gap-1.5">
            <Label htmlFor="use-template-project">Project</Label>
            <Select value={project} onValueChange={(value) => { if (typeof value === "string") setProject(value); }}>
              <SelectTrigger id="use-template-project" aria-label="Project" className="h-8">
                <SelectValue>{projects.find((item) => item.id === project)?.name ?? "Choose a project"}</SelectValue>
              </SelectTrigger>
              <SelectContent align="start">
                {projects.map((item) => <SelectItem key={item.id} value={item.id}>{item.name}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="use-template-name">Workflow name</Label>
            <Input id="use-template-name" value={name} maxLength={WORKFLOW_NAME_MAX_LENGTH} onChange={(event) => { setName(event.target.value); }} />
          </div>
          {error ? <p role="alert" className="border border-destructive/40 bg-destructive/10 p-2 font-mono text-xs text-destructive">{error}</p> : null}
          <div className="flex justify-end gap-2 border-t border-border pt-3">
            <Button type="button" variant="outline" onClick={() => { changeOpen(false); }}>Cancel</Button>
            <Button type="submit" variant="default" disabled={submitting}>{submitting ? "Creating…" : "Create workflow"}</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
