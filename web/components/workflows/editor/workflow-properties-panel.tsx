"use client";

import { AlertTriangle, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  setWorkflowDefault,
  setWorkflowDocumentField,
  setWorkflowEnvironmentVariable,
  removeSecretHandle,
  setSecretHandle,
} from "@/lib/workflow/editor";
import type { NormalizedWorkflowSpec } from "@/lib/workflow/normalize";
import type { SecretUse } from "@/lib/workflow/spec";
import type { WorkflowEditorIssue, WorkflowEditorPanelTab } from "./editor-types";

const workflowTabs: Array<{ value: WorkflowEditorPanelTab; label: string; pathPrefix: string }> = [
  { value: "info", label: "Info", pathPrefix: "metadata." },
  { value: "defaults", label: "Defaults", pathPrefix: "spec.defaults." },
  { value: "annotations", label: "Annotations", pathPrefix: "metadata.labels." },
  { value: "parameters", label: "Parameters", pathPrefix: "spec.parameters." },
  { value: "secrets", label: "Secrets", pathPrefix: "spec.secrets." },
];

function errorList(issues: WorkflowEditorIssue[]) {
  return issues.length > 0
    ? <ul className="mt-1 grid gap-0.5 text-[10px] text-destructive">{issues.map((issue, index) => <li key={`${issue.code}-${String(index)}`}>{issue.message}</li>)}</ul>
    : null;
}

function uniqueName(names: string[], stem: string): string {
  let suffix = 1;
  while (names.includes(`${stem}_${String(suffix)}`)) suffix += 1;
  return `${stem}_${String(suffix)}`;
}

export function WorkflowPropertiesPanel({
  spec,
  yaml,
  issues,
  activeTab,
  onTabChange,
  onYamlChange,
  versionNumber,
  state,
}: {
  spec: NormalizedWorkflowSpec;
  yaml: string;
  issues: WorkflowEditorIssue[];
  activeTab: WorkflowEditorPanelTab;
  onTabChange: (tab: WorkflowEditorPanelTab) => void;
  onYamlChange: (yaml: string) => void;
  versionNumber: number;
  state: string;
}) {
  function issuesFor(prefix: string): WorkflowEditorIssue[] {
    return issues.filter((issue) => issue.path === prefix.slice(0, -1) || issue.path.startsWith(prefix));
  }

  function setDefault(key: string, value: unknown) {
    onYamlChange(setWorkflowDefault(yaml, key, value));
  }

  function setLabel(key: string, value: string | undefined) {
    onYamlChange(setWorkflowDocumentField(yaml, ["metadata", "labels", key], value));
  }

  function setEnvironment(name: string, value: string | undefined) {
    onYamlChange(setWorkflowEnvironmentVariable(yaml, null, name, value));
  }

  function setSecret(handle: string, secret: SecretUse) {
    onYamlChange(setSecretHandle(yaml, handle, secret));
  }

  function addSecret() {
    const handle = uniqueName(Object.keys(spec.spec.secrets), "secret");
    setSecret(handle, { ref: handle, use: "env" });
  }

  const labels = Object.entries(spec.metadata.labels ?? {});
  const defaults = spec.spec.defaults ?? {};
  const environment = defaults.env && !Array.isArray(defaults.env) ? Object.entries(defaults.env) : [];
  const errorsByTab = new Map(workflowTabs.map((tab) => [tab.value, issuesFor(tab.pathPrefix).length > 0]));

  return (
    <div className="flex h-full min-h-0 flex-col">
      <Tabs
        value={activeTab}
        onValueChange={(value: string | null) => {
          if (value === "info") onTabChange("info");
          else if (value === "defaults") onTabChange("defaults");
          else if (value === "annotations") onTabChange("annotations");
          else if (value === "parameters") onTabChange("parameters");
          else if (value === "secrets") onTabChange("secrets");
        }}
        className="min-h-0 flex-1"
      >
        <TabsList variant="line" className="h-9 w-full justify-start border-b border-border px-2">
          {workflowTabs.map((tab) => (
            <TabsTrigger key={tab.value} value={tab.value} className="gap-1 px-2 text-[9px]">
              {tab.label}
              {errorsByTab.get(tab.value) ? <AlertTriangle aria-label="Validation error" className="size-3 text-destructive" /> : null}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="info" className="min-h-0 space-y-4 overflow-y-auto p-3">
          <div className="grid gap-1.5">
            <Label htmlFor="workflow-metadata-name">Name</Label>
            <Input id="workflow-metadata-name" value={spec.metadata.name} onChange={(event) => { onYamlChange(setWorkflowDocumentField(yaml, ["metadata", "name"], event.target.value)); }} />
            {errorList(issuesFor("metadata.name"))}
          </div>
          <div className="grid grid-cols-2 gap-2">
            {[
              ["Version", String(versionNumber)],
              ["State", state],
              ["Tasks", String(spec.spec.tasks.length)],
              ["Parameters", String(Object.keys(spec.spec.parameters).length)],
              ["Secrets", String(Object.keys(spec.spec.secrets).length)],
            ].map(([label, value]) => (
              <div key={label} className="border border-border bg-muted/20 px-2 py-2">
                <p className="font-mono text-[8px] uppercase tracking-[0.08em] text-muted-foreground">{label}</p>
                <p className="mt-1 font-mono text-[10px] text-foreground">{value}</p>
              </div>
            ))}
          </div>
        </TabsContent>
        <TabsContent value="defaults" className="min-h-0 space-y-4 overflow-y-auto p-3">
          {([
            ["partition", "Partition"],
            ["qos", "QoS"],
            ["account", "Account"],
            ["workingDirectory", "Working directory"],
          ] as const).map(([key, label]) => (
            <div key={key} className="grid gap-1.5">
              <Label htmlFor={`workflow-default-${key}`}>{label}</Label>
              <Input
                id={`workflow-default-${key}`}
                value={typeof defaults[key] === "string" ? defaults[key] : ""}
                onChange={(event) => { setDefault(key, event.target.value); }}
              />
              {errorList(issuesFor(`spec.defaults.${key}`))}
            </div>
          ))}
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label>Environment variables</Label>
              <Button type="button" variant="outline" size="sm" onClick={() => {
                const name = uniqueName(environment.map(([key]) => key), "ENV");
                setEnvironment(name, "");
              }}>
                Add variable
              </Button>
            </div>
            {environment.map(([name, value]) => (
              <div key={name} className="grid grid-cols-[1fr_1fr_auto] gap-1.5">
                <Input aria-label={`Default environment key ${name}`} value={name} onChange={(event) => {
                  const next = setWorkflowEnvironmentVariable(yaml, null, name, undefined);
                  onYamlChange(event.target.value ? setWorkflowEnvironmentVariable(next, null, event.target.value, value) : next);
                }} />
                <Input aria-label={`Default environment value ${name}`} value={value} onChange={(event) => { setEnvironment(name, event.target.value); }} />
                <Button type="button" variant="ghost" size="icon-sm" aria-label={`Remove default environment variable ${name}`} onClick={() => { setEnvironment(name, undefined); }}>
                  <X aria-hidden="true" className="size-3" />
                </Button>
              </div>
            ))}
            {errorList(issuesFor("spec.defaults.env"))}
          </div>
        </TabsContent>
        <TabsContent value="annotations" className="min-h-0 space-y-4 overflow-y-auto p-3">
          <div className="flex items-center justify-between">
            <div>
              <h3 className="font-mono text-[10px] font-semibold uppercase tracking-[0.08em]">Workflow labels</h3>
              <p className="mt-1 text-[9px] text-muted-foreground">Key/value annotations stored in metadata.labels.</p>
            </div>
            <Button type="button" variant="outline" size="sm" onClick={() => { setLabel(uniqueName(labels.map(([key]) => key), "label"), ""); }}>Add label</Button>
          </div>
          {labels.map(([key, value]) => (
            <div key={key} className="grid grid-cols-[1fr_1fr_auto] gap-1.5">
              <Input aria-label={`Label key ${key}`} value={key} onChange={(event) => {
                const next = setWorkflowDocumentField(yaml, ["metadata", "labels", key], undefined);
                onYamlChange(event.target.value ? setWorkflowDocumentField(next, ["metadata", "labels", event.target.value], value) : next);
              }} />
              <Input aria-label={`Label value ${key}`} value={value} onChange={(event) => { setLabel(key, event.target.value); }} />
              <Button type="button" variant="ghost" size="icon-sm" aria-label={`Remove label ${key}`} onClick={() => { setLabel(key, undefined); }}>
                <X aria-hidden="true" className="size-3" />
              </Button>
            </div>
          ))}
          {labels.length === 0 ? <p className="border border-border p-3 font-mono text-[9px] text-muted-foreground">No labels set.</p> : null}
          {errorList(issuesFor("metadata.labels"))}
        </TabsContent>
        <TabsContent value="parameters" className="min-h-0 space-y-2 overflow-y-auto p-3">
          {Object.entries(spec.spec.parameters).length === 0 ? (
            <p className="border border-border p-3 font-mono text-[9px] text-muted-foreground">No parameters declared.</p>
          ) : Object.entries(spec.spec.parameters).map(([name, parameter]) => (
            <div key={name} className="grid grid-cols-2 gap-2 border border-border p-2">
              <div>
                <p className="font-mono text-[8px] uppercase text-muted-foreground">Name</p>
                <p className="mt-1 font-mono text-[10px]">{name}</p>
              </div>
              <div>
                <p className="font-mono text-[8px] uppercase text-muted-foreground">Type</p>
                <p className="mt-1 font-mono text-[10px]">{parameter.type}</p>
              </div>
              <div>
                <p className="font-mono text-[8px] uppercase text-muted-foreground">Default</p>
                <p className="mt-1 font-mono text-[10px]">{parameter.default === undefined ? "—" : JSON.stringify(parameter.default)}</p>
              </div>
              <div>
                <p className="font-mono text-[8px] uppercase text-muted-foreground">Required</p>
                <p className="mt-1 font-mono text-[10px]">{parameter.required ? "yes" : "no"}</p>
              </div>
            </div>
          ))}
          {errorList(issuesFor("spec.parameters"))}
        </TabsContent>
        <TabsContent value="secrets" className="min-h-0 space-y-3 overflow-y-auto p-3">
          <div className="flex items-center justify-between gap-2">
            <div>
              <h3 className="font-mono text-[10px] font-semibold uppercase tracking-[0.08em]">Secret handles</h3>
              <p className="mt-1 text-[9px] text-muted-foreground">Handles refer to SecretReferences; values are never stored in the workflow.</p>
            </div>
            <Button type="button" variant="outline" size="sm" onClick={addSecret}>Add handle</Button>
          </div>
          {Object.entries(spec.spec.secrets).map(([handle, secret]) => (
            <div key={handle} className="grid gap-2 border border-border bg-card p-2">
              <div className="flex items-center justify-between gap-2">
                <code className="break-all font-mono text-[10px]">{handle}</code>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Remove secret handle ${handle}`}
                  onClick={() => { onYamlChange(removeSecretHandle(yaml, handle)); }}
                >
                  <X aria-hidden="true" className="size-3" />
                </Button>
              </div>
              <div className="grid gap-1">
                <Label htmlFor={`workflow-secret-ref-${handle}`}>Secret reference</Label>
                <Input
                  id={`workflow-secret-ref-${handle}`}
                  value={secret.ref}
                  onChange={(event) => {
                    setSecret(handle, { ...secret, ref: event.target.value });
                  }}
                />
              </div>
              <div className="grid gap-1">
                <Label htmlFor={`workflow-secret-use-${handle}`}>Use</Label>
                <select
                  id={`workflow-secret-use-${handle}`}
                  value={secret.use}
                  onChange={(event) => {
                    const use = event.target.value as SecretUse["use"];
                    setSecret(handle, { ref: secret.ref, use, ...(use === "env" && secret.envName ? { envName: secret.envName } : {}) });
                  }}
                  className="h-8 w-full border border-input bg-card px-2 font-mono text-[10px] text-foreground"
                >
                  <option value="env">env</option>
                  <option value="wrapped_token">wrapped_token</option>
                  <option value="image_pull">image_pull</option>
                </select>
              </div>
              {secret.use === "env" ? (
                <div className="grid gap-1">
                  <Label htmlFor={`workflow-secret-env-${handle}`}>Environment name</Label>
                  <Input
                    id={`workflow-secret-env-${handle}`}
                    value={secret.envName ?? ""}
                    onChange={(event) => {
                      setSecret(handle, { ...secret, envName: event.target.value });
                    }}
                  />
                </div>
              ) : null}
            </div>
          ))}
          {Object.keys(spec.spec.secrets).length === 0
            ? <p className="border border-border p-3 font-mono text-[9px] text-muted-foreground">No secret handles declared.</p>
            : null}
          {errorList(issuesFor("spec.secrets"))}
        </TabsContent>
      </Tabs>
    </div>
  );
}
