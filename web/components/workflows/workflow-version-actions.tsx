"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Button, buttonVariants } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { detailText } from "@/lib/api/error-details";
import { postJson, sendText } from "@/lib/api/bff-fetch";

type ApiErrorBody = { error?: { code?: unknown; message?: unknown; details?: unknown } };

function errorParts(value: unknown, status: number, action: string): { message: string; details: string[] } {
  const error = value && typeof value === "object" ? (value as ApiErrorBody).error : undefined;
  const code = typeof error?.code === "string" ? error.code : `HTTP_${String(status)}`;
  const details = Array.isArray(error?.details)
    ? error.details.flatMap((detail) => {
      const text = detailText([detail]);
      return text ? [text] : [];
    })
    : [];
  if (status === 403) return { message: `You do not have permission to ${action}.`, details };
  return { message: typeof error?.message === "string" ? error.message : code, details };
}

export function PublishVersionDialogButton({
  tenant,
  workflowId,
  versionId,
  csrfToken,
  disabled = false,
  onPublished,
}: {
  tenant: string;
  workflowId: string;
  versionId: string;
  csrfToken: string;
  disabled?: boolean;
  onPublished?: () => void;
}) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<{ message: string; details: string[] } | null>(null);
  const tenantPath = `/api/bff/tenants/${encodeURIComponent(tenant)}`;

  async function publish() {
    setPending(true);
    setError(null);
    try {
      const response = await postJson(
        `${tenantPath}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(versionId)}/publish`,
        {},
        csrfToken,
      );
      if (response.status === 200) {
        setOpen(false);
        if (onPublished) onPublished();
        else router.push(`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(versionId)}`);
        return;
      }
      if (response.status === 409) {
        const parts = errorParts(response.payload, response.status, "publish workflow versions");
        setError({ ...parts, message: parts.message || "This version is no longer a draft." });
        return;
      }
      setError(errorParts(response.payload, response.status, "publish workflow versions"));
    } catch {
      setError({ message: "The request could not be sent. Check your connection and try again.", details: [] });
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { setOpen(next); if (next) setError(null); }}>
      <DialogTrigger render={<Button type="button" disabled={disabled} />}>Publish</DialogTrigger>
      <DialogContent className="w-[min(94vw,34rem)] sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.1em]">Publish workflow version?</DialogTitle>
          <DialogDescription>
            Publishing makes this version immutable and available for execution. The workflow validation and script checks will run before it is published.
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <div role="alert" className="border border-destructive/40 bg-destructive/10 p-3 text-xs text-destructive">
            <p>{error.message}</p>
            {error.details.length > 0 ? <ul className="mt-2 list-disc pl-5">{error.details.map((detail, index) => <li key={`${detail}-${String(index)}`}>{detail}</li>)}</ul> : null}
          </div>
        ) : null}
        <DialogFooter>
          <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
          <Button type="button" disabled={pending} onClick={() => { void publish(); }}>
            {pending ? "Publishing…" : "Confirm publish"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function WorkflowVersionLifecycleActions({
  tenant,
  workflowId,
  versionId,
  state,
  csrfToken,
}: {
  tenant: string;
  workflowId: string;
  versionId: string;
  state: "draft" | "published" | "deprecated";
  csrfToken: string;
}) {
  const router = useRouter();
  const [deprecateOpen, setDeprecateOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const tenantPath = `/api/bff/tenants/${encodeURIComponent(tenant)}`;
  const versionPath = `${tenantPath}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(versionId)}`;

  async function deprecate() {
    setPending(true);
    setError("");
    try {
      const response = await postJson(`${versionPath}/deprecate`, {}, csrfToken);
      if (response.status === 204) {
        setDeprecateOpen(false);
        router.refresh();
        return;
      }
      const parts = errorParts(response.payload, response.status, "deprecate workflow versions");
      setError(response.status === 409 ? "Only a published version can be deprecated." : parts.message);
    } catch {
      setError("The request could not be sent. Check your connection and try again.");
    } finally {
      setPending(false);
    }
  }

  async function createDraft() {
    setPending(true);
    setError("");
    try {
      const sourceResponse = await fetch(`/api/bff/${versionPath.replace(/^\/api\/bff\//, "")}`, {
        method: "GET",
        credentials: "same-origin",
        cache: "no-store",
        headers: { Accept: "application/yaml" },
      });
      if (!sourceResponse.ok) {
        const payload: unknown = await sourceResponse.json().catch(() => null);
        const parts = errorParts(payload, sourceResponse.status, "read workflow versions");
        setError(parts.details.join(" · ") || parts.message);
        return;
      }
      const yaml = await sourceResponse.text();
      const response = await sendText(
        "POST",
        `${tenantPath}/workflows/${encodeURIComponent(workflowId)}/versions`,
        yaml,
        "application/yaml",
        csrfToken,
      );
      const draftId = response.status === 201 && response.payload && typeof response.payload === "object"
        ? (response.payload as { id?: unknown }).id
        : undefined;
      if (typeof draftId !== "string") {
        const parts = errorParts(response.payload, response.status, "create workflow versions");
        setError(parts.details.join(" · ") || parts.message);
        return;
      }
      router.push(`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(draftId)}/edit`);
    } catch {
      setError("The request could not be sent. Check your connection and try again.");
    } finally {
      setPending(false);
    }
  }

  const editHref = `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(versionId)}/edit`;
  return (
    <div className="grid justify-items-end gap-2">
      <div className="flex flex-wrap justify-end gap-2">
        {state === "draft" ? (
          <>
            <Link className={buttonVariants({ variant: "outline" })} href={editHref}>Edit draft</Link>
            <PublishVersionDialogButton tenant={tenant} workflowId={workflowId} versionId={versionId} csrfToken={csrfToken} />
          </>
        ) : null}
        {state === "published" ? (
          <Dialog open={deprecateOpen} onOpenChange={(next) => { setDeprecateOpen(next); if (next) setError(""); }}>
            <DialogTrigger render={<Button type="button" variant="outline" disabled={pending} />}>Deprecate</DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle className="font-mono text-sm uppercase tracking-[0.1em]">Deprecate version?</DialogTitle>
                <DialogDescription>This published version will no longer be the current version for new workflow runs.</DialogDescription>
              </DialogHeader>
              {error ? <p role="alert" className="border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">{error}</p> : null}
              <DialogFooter>
                <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
                <Button type="button" variant="destructive" disabled={pending} onClick={() => { void deprecate(); }}>
                  {pending ? "Deprecating…" : "Confirm deprecate"}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        ) : null}
        {state !== "draft" ? (
          <Button type="button" variant="outline" disabled={pending} onClick={() => { void createDraft(); }}>
            {pending ? "Creating…" : "New draft from this version"}
          </Button>
        ) : null}
      </div>
      {error && state !== "published" ? <p role="alert" className="max-w-md text-right font-mono text-[10px] text-destructive">{error}</p> : null}
    </div>
  );
}
