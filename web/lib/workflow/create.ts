import { errorText, postJson } from "@/lib/api/bff-fetch";
import type { CustosWorkflow } from "@/lib/workflow/spec";

export const WORKFLOW_NAME_PATTERN = /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/;
export const WORKFLOW_NAME_MAX_LENGTH = 63;

export type WorkflowCreationFailure =
  | { kind: "workflow"; message: string }
  | { kind: "draft"; workflowId: string; message: string }
  | { kind: "network"; message: string };

export type WorkflowCreationResult =
  | { ok: true; workflowId: string; versionId: string }
  | { ok: false; failure: WorkflowCreationFailure };

export type WorkflowDraftResult =
  | { ok: true; versionId: string }
  | { ok: false; message: string };

export function validateWorkflowName(name: string): string | undefined {
  if (name.length === 0) return "Workflow name is required.";
  if (name.length > WORKFLOW_NAME_MAX_LENGTH || !WORKFLOW_NAME_PATTERN.test(name)) {
    return "Use 1–63 lowercase letters, numbers, or hyphens; start and end with a letter or number.";
  }
  return undefined;
}

export function slugifyWorkflowMetadataName(value: string, fallback = "task"): string {
  let name = value.trim().toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-+|-+$/g, "");
  if (!name) name = fallback;
  if (name.length > WORKFLOW_NAME_MAX_LENGTH) {
    name = name.slice(0, WORKFLOW_NAME_MAX_LENGTH).replace(/-+$/g, "");
  }
  return name || "task";
}

export function starterWorkflowSpec(workflowName: string): CustosWorkflow {
  return {
    apiVersion: "custos.io/v1alpha1",
    kind: "Workflow",
    metadata: { name: slugifyWorkflowMetadataName(workflowName, "hello") },
    spec: {
      tasks: [{
        name: "hello",
        launch: "sbatch",
        resources: { cpu: 1, memory: "1GiB", walltime: "10m" },
        script: "#!/bin/bash\necho \"Hello from $(hostname)\"\n",
      }],
    },
  };
}

export async function createWorkflowDraft(
  tenant: string,
  workflowId: string,
  spec: unknown,
  csrfToken: string,
): Promise<WorkflowDraftResult> {
  const tenantPath = `/api/bff/tenants/${encodeURIComponent(tenant)}`;
  try {
    const response = await postJson(
      `${tenantPath}/workflows/${encodeURIComponent(workflowId)}/versions`,
      spec,
      csrfToken,
    );
    if (response.status !== 201) {
      return { ok: false, message: errorText(response.payload, response.status, "create versions") };
    }
    const versionId = response.payload && typeof response.payload === "object"
      ? (response.payload as { id?: unknown }).id
      : undefined;
    return typeof versionId === "string"
      ? { ok: true, versionId }
      : { ok: false, message: "The API created a draft without returning a version ID." };
  } catch {
    return { ok: false, message: "The request could not be sent. Check your connection and try again." };
  }
}

export async function createWorkflowWithDraft(
  tenant: string,
  workflow: { project: string; name: string; description?: string },
  spec: unknown,
  csrfToken: string,
): Promise<WorkflowCreationResult> {
  const name = workflow.name.trim();
  const nameError = validateWorkflowName(name);
  if (!workflow.project || nameError) {
    return {
      ok: false,
      failure: { kind: "workflow", message: nameError ?? "Choose a project and enter a workflow name." },
    };
  }
  const tenantPath = `/api/bff/tenants/${encodeURIComponent(tenant)}`;
  try {
    const created = await postJson(`${tenantPath}/workflows`, {
      project: workflow.project,
      name,
      ...(workflow.description !== undefined ? { description: workflow.description } : {}),
    }, csrfToken);
    const workflowId = created.status === 201 && created.payload && typeof created.payload === "object"
      ? (created.payload as { id?: unknown }).id
      : undefined;
    if (typeof workflowId !== "string") {
      return {
        ok: false,
        failure: { kind: "workflow", message: errorText(created.payload, created.status, "create workflows") },
      };
    }

    try {
      const draft = await createWorkflowDraft(tenant, workflowId, spec, csrfToken);
      return draft.ok
        ? { ok: true, workflowId, versionId: draft.versionId }
        : { ok: false, failure: { kind: "draft", workflowId, message: draft.message } };
    } catch {
      return {
        ok: false,
        failure: {
          kind: "draft",
          workflowId,
          message: "The request could not be sent. Check your connection and try again.",
        },
      };
    }
  } catch {
    return {
      ok: false,
      failure: { kind: "network", message: "The request could not be sent. Check your connection and try again." },
    };
  }
}
