"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { toast } from "sonner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { extractErrorEnvelope } from "@/lib/api/error-details";

export function CancelWorkflowExecutionButton({ tenant, execution, csrfToken }: {
  tenant: string;
  execution: string;
  csrfToken: string;
}) {
  const router = useRouter();
  const [pending, setPending] = useState(false);

  async function cancel() {
    setPending(true);
    try {
      const response = await fetch(`/api/bff/tenants/${encodeURIComponent(tenant)}/workflow-executions/${encodeURIComponent(execution)}/cancel`, {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        headers: { Accept: "application/json", "X-CSRF-Token": csrfToken },
      });
      if (response.status === 202) {
        router.refresh();
        return;
      }
      const body: unknown = await response.json().catch(() => null);
      const code = extractErrorEnvelope(body).code ?? `HTTP_${String(response.status)}`;
      if (response.status === 403) toast.error("You do not have permission to cancel this execution.");
      else if (response.status === 409) {
        toast.message("This execution is already terminal.");
        router.refresh();
      } else toast.error(`Cancel rejected: ${code}`);
    } catch {
      toast.error("The cancellation request is unavailable.");
    } finally {
      setPending(false);
    }
  }

  return (
    <AlertDialog>
      <AlertDialogTrigger render={<Button type="button" variant="destructive" disabled={pending} />}>Cancel execution</AlertDialogTrigger>
      <AlertDialogContent className="w-[min(92vw,28rem)] border-primary/25 bg-popover shadow-none">
        <AlertDialogHeader className="text-left">
          <AlertDialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Cancel this execution?</AlertDialogTitle>
          <AlertDialogDescription className="mt-2 text-sm text-muted-foreground">
            Custos will cancel queued tasks and request cancellation for active jobs.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter className="border-border bg-muted/30">
          <AlertDialogCancel>Keep execution</AlertDialogCancel>
          <AlertDialogAction type="button" variant="destructive" disabled={pending} onClick={() => { void cancel(); }}>
            {pending ? "Requesting…" : "Confirm cancel"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
