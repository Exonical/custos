"use client";

import Link from "next/link";
import type { WorkflowCreationFailure } from "@/lib/workflow/create";

export function WorkflowCreationError({ tenant, failure }: {
  tenant: string;
  failure: WorkflowCreationFailure;
}) {
  if (failure.kind !== "draft") return failure.message;
  const workflowHref = `/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(failure.workflowId)}`;
  return (
    <>
      The workflow was created, but its draft could not be saved: {failure.message}{" "}
      <Link className="text-primary underline" href={workflowHref}>Open the workflow</Link>.
    </>
  );
}
