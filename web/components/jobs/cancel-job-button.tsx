"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
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

export function CancelJobButton({
  tenant,
  project,
  job,
  csrfToken,
}: {
  tenant: string;
  project: string;
  job: string;
  csrfToken: string;
}) {
  const router = useRouter();
  const [message, setMessage] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function cancel() {
    setPending(true);
    setMessage(null);
    try {
      const path = `/api/bff/tenants/${encodeURIComponent(tenant)}/projects/${encodeURIComponent(project)}/jobs/${encodeURIComponent(job)}/cancel`;
      const response = await fetch(path, {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        headers: { Accept: "application/json", "X-CSRF-Token": csrfToken },
      });
      if (!response.ok) {
        const body: unknown = await response.json().catch(() => null);
        const error = body && typeof body === "object" ? (body as { error?: { code?: unknown } }).error : undefined;
        setMessage(typeof error?.code === "string" ? error.code : "CANCEL_REJECTED");
        return;
      }
      router.refresh();
    } catch {
      setMessage("CANCEL_UNAVAILABLE");
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="space-y-2">
      <AlertDialog>
        <AlertDialogTrigger render={<Button type="button" variant="destructive" disabled={pending} />}>Cancel job</AlertDialogTrigger>
        <AlertDialogContent className="w-[min(92vw,28rem)] border-primary/25 bg-popover shadow-none">
          <AlertDialogHeader className="text-left">
            <AlertDialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Cancel this job?</AlertDialogTitle>
            <AlertDialogDescription className="mt-2 text-sm text-muted-foreground">
              Custos will request cancellation from Slurm. This action cannot be undone.
            </AlertDialogDescription>
            {message ? <p role="alert" className="font-mono text-xs text-destructive">CANCEL REJECTED: {message}</p> : null}
          </AlertDialogHeader>
          <AlertDialogFooter className="border-border bg-muted/30">
            <AlertDialogCancel>Keep job</AlertDialogCancel>
            <AlertDialogAction type="button" variant="destructive" disabled={pending} onClick={() => { void cancel(); }}>
              {pending ? "Requesting…" : "Confirm cancel"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
