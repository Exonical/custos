"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { Task } from "@/lib/workflow/spec";
import { clearPullSecret, setPullSecret, setSecretHandle, validationPathToLocation } from "@/lib/workflow/editor";
import type { WorkflowEditorIssue } from "./editor-types";

type SecretHandle = { ref: string; use: "env" | "wrapped_token" | "image_pull"; envName?: string };
type PullUsernameMode = "literal" | "secret";

function isAbsoluteImagePath(uri: string): boolean {
  return uri.startsWith("/") || /^[A-Za-z]:[\\/]/.test(uri);
}

function errorsFor(yaml: string, issues: WorkflowEditorIssue[], taskName: string, suffix: string[]) {
  return issues.filter((issue) => {
    return validationPathToLocation(yaml, issue.path)?.taskName === taskName
      && suffix.some((item) => issue.path.endsWith(item) || issue.path.includes(`${item}.`));
  });
}

export function imagePullReferenceNames(payload: unknown): string[] | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const items = (payload as Record<string, unknown>).items;
  if (!Array.isArray(items)) return undefined;
  return items.flatMap((entry) => {
    if (!entry || typeof entry !== "object") return [];
    const item = entry as Record<string, unknown>;
    return typeof item.name === "string" && Array.isArray(item.allowed_uses) && item.allowed_uses.includes("image_pull")
      ? [item.name]
      : [];
  });
}

function uniqueHandle(handles: string[]): string {
  let index = 1;
  while (handles.includes(`image_pull_${String(index)}`)) index += 1;
  return `image_pull_${String(index)}`;
}

export function ImagePullSecretEditor({
  task,
  tenant,
  yaml,
  issues,
  secrets,
  onYamlChange,
}: {
  task: Task;
  tenant: string;
  yaml: string;
  issues: WorkflowEditorIssue[];
  secrets: Record<string, SecretHandle>;
  onYamlChange: (yaml: string) => void;
}) {
  const uri = task.image?.uri ?? "";
  const pullSecret = task.image?.pullSecret;
  const [newSecretTarget, setNewSecretTarget] = useState<"username" | "password" | null>(null);
  const [newHandle, setNewHandle] = useState("");
  const [newReference, setNewReference] = useState("");
  const [referenceSuggestions, setReferenceSuggestions] = useState<string[]>([]);
  const [suggestionsUnavailable, setSuggestionsUnavailable] = useState(false);
  const imagePullHandles = Object.entries(secrets)
    .filter(([, secret]) => secret.use === "image_pull")
    .map(([handle]) => handle);
  const usernameMode: PullUsernameMode = pullSecret?.usernameSecret ? "secret" : "literal";
  const spansMultipleNodes = task.multinode !== undefined || (task.resources?.nodes ?? 1) > 1;

  useEffect(() => {
    if (!newSecretTarget) return undefined;
    let active = true;
    void fetch(`/api/bff/tenants/${encodeURIComponent(tenant)}/secret-references`, {
      method: "GET",
      credentials: "same-origin",
      cache: "no-store",
      headers: { Accept: "application/json" },
    }).then(async (response) => {
      if (response.status === 403) {
        if (active) setSuggestionsUnavailable(true);
        return;
      }
      if (!response.ok) {
        if (active) setSuggestionsUnavailable(true);
        return;
      }
      const payload: unknown = await response.json().catch(() => null);
      const names = imagePullReferenceNames(payload);
      if (!active || !names) return;
      setReferenceSuggestions(names);
    }).catch(() => {
      if (active) setSuggestionsUnavailable(true);
    });
    return () => { active = false; };
  }, [newSecretTarget, tenant]);

  if (!uri) return null;
  if (isAbsoluteImagePath(uri)) {
    return pullSecret ? (
      <div className="flex flex-wrap items-center justify-between gap-2 border border-destructive/40 bg-destructive/10 p-2">
        <p className="font-mono text-[9px] text-destructive">Pull credentials cannot be used with an absolute image path.</p>
        <Button type="button" variant="outline" size="sm" onClick={() => { onYamlChange(clearPullSecret(yaml, task.name)); }}>
          Clear pull credentials
        </Button>
      </div>
    ) : null;
  }

  function updatePullSecret(value: Partial<NonNullable<NonNullable<Task["image"]>["pullSecret"]>>) {
    onYamlChange(setPullSecret(yaml, task.name, value));
  }

  function setUsernameMode(mode: PullUsernameMode) {
    const next = { ...pullSecret };
    if (mode === "literal") {
      delete next.usernameSecret;
      next.username ??= "";
    } else {
      delete next.username;
      next.usernameSecret ??= "";
    }
    updatePullSecret(next);
  }

  function beginNewHandle(target: "username" | "password") {
    setNewSecretTarget(target);
    setNewHandle(uniqueHandle(Object.keys(secrets)));
    setNewReference("");
    setReferenceSuggestions([]);
    setSuggestionsUnavailable(false);
  }

  function addSecretHandle() {
    const handle = newHandle.trim();
    const ref = newReference.trim();
    if (!handle || !ref || Object.hasOwn(secrets, handle) || !newSecretTarget) return;
    let next = setSecretHandle(yaml, handle, { ref, use: "image_pull" });
    const pull = { ...pullSecret };
    if (newSecretTarget === "password") {
      pull.passwordSecret = handle;
    } else {
      delete pull.username;
      pull.usernameSecret = handle;
    }
    next = setPullSecret(next, task.name, pull);
    onYamlChange(next);
    setNewSecretTarget(null);
    setNewHandle("");
    setNewReference("");
  }

  if (!pullSecret) {
    return (
      <section className="space-y-3 border-t border-border pt-3">
        <div>
          <h3 className="font-mono text-[10px] font-semibold uppercase tracking-[0.08em]">Image pull credentials</h3>
          <p className="mt-1 break-all font-mono text-[9px] text-muted-foreground">{uri}</p>
        </div>
        {spansMultipleNodes ? (
          <p className="border border-status-queued/40 bg-status-queued/10 p-2 font-mono text-[9px] text-status-queued">
            Pull secrets are only supported on single-node tasks.
          </p>
        ) : null}
        <Button type="button" variant="outline" size="sm" onClick={() => { updatePullSecret({ username: "", passwordSecret: "" }); }}>
          Add pull credentials
        </Button>
      </section>
    );
  }

  const pullIssues = errorsFor(yaml, issues, task.name, [".image.pullSecret"]);

  return (
    <section className="space-y-3 border-t border-border pt-3">
      <div>
        <h3 className="font-mono text-[10px] font-semibold uppercase tracking-[0.08em]">Image pull credentials</h3>
        <p className="mt-1 break-all font-mono text-[9px] text-muted-foreground">{uri}</p>
      </div>
      {spansMultipleNodes ? (
        <p className="border border-status-queued/40 bg-status-queued/10 p-2 font-mono text-[9px] text-status-queued">
          Pull secrets are only supported on single-node tasks.
        </p>
      ) : null}
      <fieldset className="grid gap-2">
        <legend className="font-mono text-[9px] uppercase text-muted-foreground">Username</legend>
        <div role="radiogroup" aria-label="Pull-secret username mode" className="inline-flex w-fit border border-border">
          {(["literal", "secret"] as const).map((mode) => (
            <label
              key={mode}
              className={`flex cursor-pointer items-center border-r border-border px-2 py-1 font-mono text-[9px] uppercase last:border-r-0 ${usernameMode === mode ? "bg-primary/15 text-primary" : "bg-card text-muted-foreground hover:bg-muted"}`}
            >
              <input
                className="sr-only"
                type="radio"
                name={`pull-username-mode-${task.name}`}
                value={mode}
                checked={usernameMode === mode}
                onChange={() => { setUsernameMode(mode); }}
              />
              {mode === "literal" ? "Literal" : "Secret"}
            </label>
          ))}
        </div>
        {usernameMode === "literal" ? (
          <div className="grid gap-1">
            <Label htmlFor={`pull-username-${task.name}`}>Username</Label>
            <Input
              id={`pull-username-${task.name}`}
              autoComplete="off"
              value={pullSecret.username ?? ""}
              onChange={(event) => {
                const next = { ...pullSecret, username: event.target.value };
                delete next.usernameSecret;
                updatePullSecret(next);
              }}
            />
          </div>
        ) : (
          <div className="grid gap-1">
            <Label htmlFor={`pull-username-secret-${task.name}`}>Username secret handle</Label>
            <select
              id={`pull-username-secret-${task.name}`}
              value={pullSecret.usernameSecret ?? ""}
              onChange={(event) => {
                if (event.target.value === "__new__") beginNewHandle("username");
                else {
                  const next = { ...pullSecret, usernameSecret: event.target.value };
                  delete next.username;
                  updatePullSecret(next);
                }
              }}
              className="h-8 w-full border border-input bg-card px-2 font-mono text-[10px] text-foreground"
            >
              <option value="">Select a secret handle…</option>
              {imagePullHandles.map((handle) => <option key={handle} value={handle}>{handle}</option>)}
              {pullSecret.usernameSecret && !imagePullHandles.includes(pullSecret.usernameSecret)
                ? <option value={pullSecret.usernameSecret}>{pullSecret.usernameSecret} · invalid use</option>
                : null}
              <option value="__new__">New secret handle…</option>
            </select>
          </div>
        )}
      </fieldset>
      <div className="grid gap-1">
        <Label htmlFor={`pull-password-secret-${task.name}`}>Password secret handle · required</Label>
        <select
          id={`pull-password-secret-${task.name}`}
          value={pullSecret.passwordSecret}
          aria-required="true"
          onChange={(event) => {
            if (event.target.value === "__new__") beginNewHandle("password");
            else updatePullSecret({ ...pullSecret, passwordSecret: event.target.value });
          }}
          className="h-8 w-full border border-input bg-card px-2 font-mono text-[10px] text-foreground"
        >
          <option value="">Select a secret handle…</option>
          {imagePullHandles.map((handle) => <option key={handle} value={handle}>{handle}</option>)}
          {pullSecret.passwordSecret && !imagePullHandles.includes(pullSecret.passwordSecret)
            ? <option value={pullSecret.passwordSecret}>{pullSecret.passwordSecret} · invalid use</option>
            : null}
          <option value="__new__">New secret handle…</option>
        </select>
      </div>
      {newSecretTarget ? (
        <div className="grid gap-2 border border-border bg-muted/20 p-2">
          <p className="font-mono text-[9px] uppercase text-muted-foreground">New image-pull secret handle</p>
          <div className="grid gap-1">
            <Label htmlFor={`new-pull-handle-${task.name}`}>Handle name</Label>
            <Input id={`new-pull-handle-${task.name}`} value={newHandle} onChange={(event) => { setNewHandle(event.target.value); }} />
          </div>
          <div className="grid gap-1">
            <Label htmlFor={`new-pull-reference-${task.name}`}>Secret reference name</Label>
            <Input
              id={`new-pull-reference-${task.name}`}
              list={`image-pull-references-${task.name}`}
              value={newReference}
              onChange={(event) => { setNewReference(event.target.value); }}
              placeholder="registry-token"
            />
            <datalist id={`image-pull-references-${task.name}`}>
              {referenceSuggestions.map((name) => <option key={name} value={name} />)}
            </datalist>
            {suggestionsUnavailable ? <p className="text-[9px] text-muted-foreground">Reference suggestions unavailable; enter the reference name.</p> : null}
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" size="sm" onClick={() => { setNewSecretTarget(null); }}>Cancel</Button>
            <Button type="button" size="sm" disabled={!newHandle.trim() || !newReference.trim() || Object.hasOwn(secrets, newHandle.trim())} onClick={addSecretHandle}>Add handle</Button>
          </div>
        </div>
      ) : null}
      <Button type="button" variant="outline" size="sm" onClick={() => { onYamlChange(clearPullSecret(yaml, task.name)); }}>Clear pull credentials</Button>
      {pullIssues.length > 0 ? (
        <ul className="grid gap-0.5 text-[10px] text-destructive">
          {pullIssues.map((issue, index) => <li key={`${issue.code}-${String(index)}`}>{issue.message}</li>)}
        </ul>
      ) : null}
    </section>
  );
}
