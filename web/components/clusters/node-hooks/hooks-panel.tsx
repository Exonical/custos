"use client";

import dynamic from "next/dynamic";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { emptyHook, type HookRow } from "@/lib/nodehooks/config";

const ScriptEditor = dynamic(() => import("@/components/workflows/yaml-editor").then((module) => module.WorkflowYamlEditor), {
  ssr: false,
  loading: () => <div className="h-56 animate-pulse border border-border bg-muted/30" aria-label="Loading script editor" />,
});

function FieldError({ message }: { message: string | undefined }) {
  return message ? <p role="alert" className="text-[10px] text-destructive">{message}</p> : null;
}

export function HooksPanel({ hooks, errors, editable, onChange }: {
  hooks: HookRow[];
  errors: Record<string, Record<string, string>>;
  editable: boolean;
  onChange: (hooks: HookRow[]) => void;
}) {
  function update(id: string, patch: Partial<HookRow>) {
    onChange(hooks.map((hook) => hook.id === id ? { ...hook, ...patch } : hook));
  }
  return (
    <section aria-labelledby="node-hooks-custom-heading" className="space-y-3 border-t border-border pt-5">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 id="node-hooks-custom-heading" className="font-mono text-xs font-semibold uppercase tracking-[0.1em]">Custom hooks</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Extra Prolog and Epilog snippets. Lower order runs earlier within a phase.
          </p>
        </div>
        {editable ? (
          <Button type="button" variant="outline" onClick={() => { onChange([...hooks, emptyHook()]); }}>Add hook</Button>
        ) : null}
      </header>
      <p role="note" className="border border-status-degraded/40 bg-status-degraded/10 p-2 text-xs text-status-degraded">
        Hooks run as root on every compute node of this cluster and a non-zero exit drains the node. Review them like any privileged change.
      </p>
      {hooks.length === 0 ? (
        <p className="border border-border p-3 font-mono text-[10px] text-muted-foreground">No custom hooks.</p>
      ) : (
        <div className="grid gap-3">
          {hooks.map((hook, index) => {
            const rowErrors = errors[hook.id] ?? {};
            const label = `hook ${hook.name || String(index + 1)}`;
            return (
              <div key={hook.id} className="grid gap-3 border border-border bg-card p-3" aria-label={label} data-testid="hook-row">
                <div className="grid gap-3 md:grid-cols-[minmax(0,2fr)_9rem_6rem_auto] md:items-start">
                  <div className="grid gap-1.5">
                    <Label htmlFor={`${hook.id}-name`}>Hook name</Label>
                    <Input id={`${hook.id}-name`} value={hook.name} disabled={!editable} placeholder="metrics"
                      aria-invalid={rowErrors.name ? true : undefined}
                      onChange={(event) => { update(hook.id, { name: event.target.value }); }} />
                    <FieldError message={rowErrors.name} />
                  </div>
                  <div className="grid gap-1.5">
                    <Label htmlFor={`${hook.id}-phase`}>Phase</Label>
                    <Select value={hook.phase} disabled={!editable} onValueChange={(value) => {
                      if (value === "prolog" || value === "epilog") update(hook.id, { phase: value });
                    }}>
                      <SelectTrigger id={`${hook.id}-phase`} aria-label={`${label} phase`} className="h-8">
                        <SelectValue>{hook.phase}</SelectValue>
                      </SelectTrigger>
                      <SelectContent align="start">
                        <SelectItem value="prolog">prolog</SelectItem>
                        <SelectItem value="epilog">epilog</SelectItem>
                      </SelectContent>
                    </Select>
                    <FieldError message={rowErrors.phase} />
                  </div>
                  <div className="grid gap-1.5">
                    <Label htmlFor={`${hook.id}-order`}>Order (0-99)</Label>
                    <Input id={`${hook.id}-order`} value={hook.order} inputMode="numeric" disabled={!editable}
                      aria-invalid={rowErrors.order ? true : undefined}
                      onChange={(event) => { update(hook.id, { order: event.target.value }); }} />
                    <FieldError message={rowErrors.order} />
                  </div>
                  {editable ? (
                    <Button type="button" variant="outline" className="md:mt-5" aria-label={`Remove ${label}`}
                      onClick={() => { onChange(hooks.filter((item) => item.id !== hook.id)); }}>
                      Remove
                    </Button>
                  ) : null}
                </div>
                <div className="grid gap-1.5">
                  <Label>Script</Label>
                  <ScriptEditor
                    value={hook.script}
                    readOnly={!editable}
                    language="shell"
                    height="14rem"
                    label={`Script for ${label}`}
                    onChange={(script) => { update(hook.id, { script }); }}
                  />
                  <FieldError message={rowErrors.script} />
                </div>
                <FieldError message={rowErrors.row} />
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
