"use client";

import { useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import type { Workflow, WorkflowVersion } from "@/lib/api/client";
import { detailText, extractErrorEnvelope } from "@/lib/api/error-details";
import type { WorkflowParameter } from "@/lib/workflow/normalize";
import { WorkflowParameterFields, useWorkflowParameterForm, workflowParameterValues } from "./workflow-parameter-form";

function errorText(value: unknown, status: number): string {
  const error = extractErrorEnvelope(value);
  const code = error.code ?? `HTTP_${String(status)}`;
  if (status === 403) return "You do not have permission to run this workflow.";
  if (status === 409) return `Run rejected: ${code}`;
  const details = status === 422 && error.details ? detailText(error.details) : "";
  if (details) return details;
  return `Run rejected: ${code}`;
}

export function WorkflowRunDialog({ tenant, workflow, version, parameters, csrfToken }: {
  tenant: string;
  workflow: Workflow;
  version: WorkflowVersion;
  parameters: Record<string, WorkflowParameter>;
  csrfToken: string;
}) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [serverError, setServerError] = useState<string | null>(null);
  const idempotencyKey = useRef<string | null>(null);
  const { form, defaults } = useWorkflowParameterForm(parameters);

  function changeOpen(next: boolean) {
    setOpen(next);
    if (next) {
      idempotencyKey.current = globalThis.crypto.randomUUID();
      form.reset(defaults);
      setServerError(null);
    } else {
      idempotencyKey.current = null;
    }
  }

  async function submit(values: Record<string, unknown>) {
    setServerError(null);
    const valuesForRequest = workflowParameterValues(values, parameters);
    try {
      const response = await fetch(`/api/bff/tenants/${encodeURIComponent(tenant)}/workflow-executions`, {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
          "X-CSRF-Token": csrfToken,
          "Idempotency-Key": idempotencyKey.current ?? globalThis.crypto.randomUUID(),
        },
        body: JSON.stringify({ workflow: workflow.id, version: version.id, parameters: valuesForRequest }),
      });
      if (response.status === 202) {
        const body: unknown = await response.json().catch(() => null);
        const execution = body && typeof body === "object" ? body as { id?: unknown } : {};
        if (typeof execution.id !== "string") {
          setServerError("The API accepted the run but did not return an execution ID.");
          return;
        }
        changeOpen(false);
        router.push(`/t/${encodeURIComponent(tenant)}/executions/${encodeURIComponent(execution.id)}`);
        return;
      }
      const body: unknown = await response.json().catch(() => null);
      setServerError(errorText(body, response.status));
    } catch {
      setServerError("The workflow run could not be submitted. Retry to replay this request safely.");
    }
  }


  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogTrigger render={<Button type="button" variant="default" />}>Run</DialogTrigger>
      <DialogContent className="max-h-[88vh] w-[min(94vw,38rem)] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Run {workflow.name}</DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">Published version {version.number} · parameters are validated by the server before execution.</DialogDescription>
        </DialogHeader>
        <form className="grid gap-4" onSubmit={(event) => { void form.handleSubmit(submit)(event); }}>
          <WorkflowParameterFields form={form} parameters={parameters} idPrefix="workflow-parameter" />
          {serverError ? <p role="alert" className="border border-destructive/40 bg-destructive/10 p-2 font-mono text-xs text-destructive">{serverError}</p> : null}
          <div className="flex justify-end gap-2 border-t border-border pt-3">
            <Button type="button" variant="outline" onClick={() => { changeOpen(false); }}>Close</Button>
            <Button type="submit" variant="default" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting ? "Submitting…" : "Run workflow"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
