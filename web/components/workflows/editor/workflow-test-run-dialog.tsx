"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { detailText, extractErrorEnvelope } from "@/lib/api/error-details";
import type { WorkflowParameter } from "@/lib/workflow/normalize";
import { WorkflowParameterFields, useWorkflowParameterForm, workflowParameterValues } from "@/components/workflows/workflow-parameter-form";

function testRunError(value: unknown, status: number): string {
  const error = extractErrorEnvelope(value);
  if (status === 403) return "Test runs need workflow.create and workflow.execute on this project";
  const details = status === 422 && error.details ? detailText(error.details) : "";
  if (details) return details;
  return error.message ?? `Test run rejected: ${error.code ?? `HTTP_${String(status)}`}`;
}

export function WorkflowTestRunDialog({
  open,
  onOpenChange,
  tenant,
  workflowId,
  workflowName,
  versionId,
  parameters,
  csrfToken,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tenant: string;
  workflowId: string;
  workflowName: string;
  versionId: string;
  parameters: Record<string, WorkflowParameter>;
  csrfToken: string;
}) {
  const router = useRouter();
  const idempotencyKey = useRef<string | null>(null);
  const [serverError, setServerError] = useState<string | null>(null);
  const { form, defaults } = useWorkflowParameterForm(parameters);
  const { reset } = form;

  useEffect(() => {
    if (open) {
      idempotencyKey.current = globalThis.crypto.randomUUID();
      reset(defaults);
    } else {
      idempotencyKey.current = null;
    }
  }, [defaults, open, reset]);

  function changeOpen(next: boolean) {
    if (!next) setServerError(null);
    onOpenChange(next);
  }

  async function submit(values: Record<string, unknown>) {
    setServerError(null);
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
        body: JSON.stringify({
          workflow: workflowId,
          version: versionId,
          parameters: workflowParameterValues(values, parameters),
          test: true,
        }),
      });
      const payload: unknown = await response.json().catch(() => null);
      if (response.status === 201 || response.status === 202) {
        const execution = payload && typeof payload === "object" ? payload as { id?: unknown } : {};
        if (typeof execution.id !== "string") {
          setServerError("The API accepted the test run but did not return an execution ID.");
          return;
        }
        changeOpen(false);
        router.push(`/t/${encodeURIComponent(tenant)}/executions/${encodeURIComponent(execution.id)}`);
        return;
      }
      setServerError(testRunError(payload, response.status));
    } catch {
      setServerError("The test run could not be submitted. Retry to replay this request safely.");
    }
  }

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent className="max-h-[88vh] w-[min(94vw,38rem)] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Test {workflowName}</DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">
            Runs draft version {versionId.slice(0, 8)}. The draft spec is locked until this test run reaches a terminal state.
          </DialogDescription>
        </DialogHeader>
        <form className="grid gap-4" onSubmit={(event) => { void form.handleSubmit(submit)(event); }}>
          <WorkflowParameterFields form={form} parameters={parameters} idPrefix="workflow-test-parameter" />
          {serverError ? <p role="alert" className="border border-destructive/40 bg-destructive/10 p-2 font-mono text-xs text-destructive">{serverError}</p> : null}
          <div className="flex justify-end gap-2 border-t border-border pt-3">
            <Button type="button" variant="outline" onClick={() => { changeOpen(false); }}>Close</Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting ? "Submitting…" : "Start test run"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
