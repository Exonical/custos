"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { validateExtraOption, type MountRow } from "@/lib/nodehooks/config";

function FieldError({ message }: { message: string | undefined }) {
  return message ? <p role="alert" className="text-[10px] text-destructive">{message}</p> : null;
}

function OptionChips({ id, options, editable, onChange, error }: {
  id: string;
  options: string[];
  editable: boolean;
  onChange: (options: string[]) => void;
  error: string | undefined;
}) {
  const [text, setText] = useState("");
  const [problem, setProblem] = useState("");

  function commit() {
    const value = text.trim().replace(/,$/, "");
    if (!value) return;
    const reason = validateExtraOption(value);
    if (reason) {
      setProblem(reason);
      return;
    }
    setProblem("");
    setText("");
    if (!options.includes(value)) onChange([...options, value]);
  }

  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>Extra options</Label>
      <div className="flex flex-wrap items-center gap-1.5">
        {options.map((option) => (
          <span key={option} className="inline-flex items-center gap-1 border border-border bg-muted/40 px-1.5 py-0.5 font-mono text-[10px]">
            {option}
            {editable ? (
              <button
                type="button"
                aria-label={`Remove option ${option}`}
                className="text-muted-foreground hover:text-foreground"
                onClick={() => { onChange(options.filter((item) => item !== option)); }}
              >
                x
              </button>
            ) : null}
          </span>
        ))}
        {editable ? (
          <Input
            id={id}
            className="h-7 w-36"
            value={text}
            placeholder="hard, nfsvers=4.2"
            onChange={(event) => { setText(event.target.value); }}
            onBlur={commit}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === ",") {
                event.preventDefault();
                commit();
              }
            }}
          />
        ) : null}
      </div>
      <FieldError message={problem || error} />
    </div>
  );
}

export function MountRowEditor({ row, errors, editable, hardened, label, onChange, onRemove }: {
  row: MountRow;
  errors: Record<string, string>;
  editable: boolean;
  hardened: boolean;
  label: string;
  onChange: (update: Partial<MountRow>) => void;
  onRemove: () => void;
}) {
  const prefix = row.id;
  return (
    <div className="grid gap-3 border border-border bg-card p-3" aria-label={label} data-testid="mount-row">
      <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_7rem_minmax(0,2fr)_minmax(0,1.4fr)]">
        <div className="grid gap-1.5">
          <Label htmlFor={`${prefix}-name`}>Name</Label>
          <Input id={`${prefix}-name`} value={row.name} disabled={!editable} placeholder="apps"
            aria-invalid={errors.name ? true : undefined}
            onChange={(event) => { onChange({ name: event.target.value }); }} />
          <FieldError message={errors.name} />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor={`${prefix}-fstype`}>Type</Label>
          <Select value={row.fstype} disabled={!editable} onValueChange={(value) => {
            if (value === "nfs" || value === "nfs4") onChange({ fstype: value });
          }}>
            <SelectTrigger id={`${prefix}-fstype`} aria-label={`${label} filesystem type`} className="h-8">
              <SelectValue>{row.fstype}</SelectValue>
            </SelectTrigger>
            <SelectContent align="start">
              <SelectItem value="nfs4">nfs4</SelectItem>
              <SelectItem value="nfs">nfs</SelectItem>
            </SelectContent>
          </Select>
          <FieldError message={errors.fstype} />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor={`${prefix}-source`}>Source</Label>
          <Input id={`${prefix}-source`} value={row.source} disabled={!editable} placeholder="server:/export/path"
            aria-invalid={errors.source ? true : undefined}
            onChange={(event) => { onChange({ source: event.target.value }); }} />
          <FieldError message={errors.source} />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor={`${prefix}-target`}>Target</Label>
          <Input id={`${prefix}-target`} value={row.target} disabled={!editable} placeholder="/mnt/data"
            aria-invalid={errors.target ? true : undefined}
            onChange={(event) => { onChange({ target: event.target.value }); }} />
          <FieldError message={errors.target} />
        </div>
      </div>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid gap-2">
          <label className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.08em]">
            <input
              type="checkbox"
              checked={row.readOnly}
              disabled={!editable}
              onChange={(event) => { onChange({ readOnly: event.target.checked }); }}
            />
            Read-only
          </label>
          {hardened ? (
            <p className="text-[10px] text-muted-foreground">nosuid and nodev are always applied to tenant mounts.</p>
          ) : null}
        </div>
        <OptionChips
          id={`${prefix}-options`}
          options={row.extraOptions}
          editable={editable}
          error={errors.options}
          onChange={(extraOptions) => { onChange({ extraOptions }); }}
        />
        {editable ? (
          <Button type="button" variant="outline" aria-label={`Remove ${label}`} onClick={onRemove}>Remove</Button>
        ) : null}
      </div>
      <FieldError message={errors.row} />
    </div>
  );
}
