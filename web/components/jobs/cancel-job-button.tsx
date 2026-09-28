"use client";

import * as AlertDialog from "@radix-ui/react-alert-dialog";
import { useRouter } from "next/navigation";
import { useState } from "react";
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
      <AlertDialog.Root>
        <AlertDialog.Trigger asChild>
          <Button type="button" variant="destructive" disabled={pending}>Cancel job</Button>
        </AlertDialog.Trigger>
        <AlertDialog.Portal>
          <AlertDialog.Overlay className="fixed inset-0 z-40 bg-slate-950/40" />
          <AlertDialog.Content className="fixed left-1/2 top-1/2 z-50 w-[min(92vw,28rem)] -translate-x-1/2 -translate-y-1/2 rounded-xl border border-slate-200 bg-white p-6 shadow-xl">
            <AlertDialog.Title className="text-lg font-semibold">Cancel this job?</AlertDialog.Title>
            <AlertDialog.Description className="mt-2 text-sm text-slate-600">
              Custos will request cancellation from Slurm. This action cannot be undone.
            </AlertDialog.Description>
            <div className="mt-6 flex justify-end gap-3">
              <AlertDialog.Cancel asChild><Button type="button" variant="outline">Keep job</Button></AlertDialog.Cancel>
              <AlertDialog.Action asChild>
                <Button type="button" variant="destructive" disabled={pending} onClick={() => { void cancel(); }}>
                  {pending ? "Requesting…" : "Confirm cancel"}
                </Button>
              </AlertDialog.Action>
            </div>
          </AlertDialog.Content>
        </AlertDialog.Portal>
      </AlertDialog.Root>
      {message ? <p role="alert" className="text-sm text-rose-700">Cancellation was not accepted ({message}).</p> : null}
    </div>
  );
}
