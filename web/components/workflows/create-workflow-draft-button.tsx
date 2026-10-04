"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { createWorkflowDraft, starterWorkflowSpec } from "@/lib/workflow/create";

export function CreateWorkflowDraftButton({ tenant, workflowId, workflowName, csrfToken }: {
  tenant: string;
  workflowId: string;
  workflowName: string;
  csrfToken: string;
}) {
  const router = useRouter();
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");

  async function createDraft() {
    setCreating(true);
    setError("");
    try {
      const result = await createWorkflowDraft(tenant, workflowId, starterWorkflowSpec(workflowName), csrfToken);
      if (!result.ok) {
        setError(result.message);
        return;
      }
      router.push(`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflowId)}/versions/${encodeURIComponent(result.versionId)}/edit`);
    } finally {
      setCreating(false);
    }
  }

  return (
    <div className="grid justify-items-center gap-3">
      <p className="max-w-md text-sm text-muted-foreground">This workflow has no versions yet. Create a starter draft to begin editing.</p>
      {error ? <p role="alert" className="border border-destructive/40 bg-destructive/10 p-2 font-mono text-xs text-destructive">{error}</p> : null}
      <Button type="button" disabled={creating} onClick={() => { void createDraft(); }}>
        {creating ? "Creating…" : "Create draft"}
      </Button>
    </div>
  );
}
