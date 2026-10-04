"use client";

import type { ReactNode } from "react";
import { Box, Ellipsis, Play, Server } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

export function EditorToolbar({
  status,
  statusKind,
  dirty,
  layoutDirty,
  saving,
  onAddTask,
  onAddService,
  onStartTest,
  startTestDisabled,
  onDownload,
  onDiscard,
  onCopy,
  onSave,
  publishAction,
}: {
  status: string;
  statusKind: "neutral" | "success" | "error" | "pending";
  dirty: boolean;
  layoutDirty: boolean;
  saving: boolean;
  onAddTask: () => void;
  onAddService: () => void;
  onStartTest: () => void;
  startTestDisabled: boolean;
  onDownload: () => void;
  onDiscard: () => void;
  onCopy: () => void;
  onSave: () => void;
  publishAction: ReactNode;
}) {
  const nodeKinds = [
    { label: "Task", Icon: Box, onAdd: onAddTask },
    { label: "Service", Icon: Server, onAdd: onAddService },
  ] as const;
  return (
    <div className="flex min-h-11 flex-wrap items-center justify-between gap-2 border-b border-border bg-card px-2 py-1.5">
      <div className="flex items-center gap-1">
        {nodeKinds.map(({ label, Icon, onAdd }) => (
          <Button key={label} type="button" variant="outline" size="sm" className="h-7 gap-2 px-2 font-mono text-[10px] uppercase tracking-[0.06em]" onClick={onAdd}>
            <Icon aria-hidden="true" className="size-3.5" />
            {label}
          </Button>
        ))}
      </div>
      <div className="flex flex-wrap items-center justify-end gap-1.5">
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button type="button" variant="outline" size="icon-sm" aria-label="Editor menu" className="size-7" />}>
            <Ellipsis aria-hidden="true" className="size-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-48">
            <DropdownMenuItem onClick={onDownload}>Download YAML</DropdownMenuItem>
            <DropdownMenuItem disabled={!dirty && !layoutDirty} onClick={onDiscard}>Discard changes</DropdownMenuItem>
            <DropdownMenuItem onClick={onCopy}>Copy YAML</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <span
          role="status"
          className={`max-w-40 truncate border px-2 py-1 font-mono text-[9px] uppercase tracking-[0.06em] ${
            statusKind === "error" ? "border-destructive/50 bg-destructive/10 text-destructive"
              : statusKind === "success" ? "border-status-completed/40 bg-status-completed/10 text-status-completed"
                : statusKind === "pending" ? "border-primary/40 bg-primary/10 text-primary"
                  : "border-border bg-muted/40 text-muted-foreground"
          }`}
        >
          {status}
        </span>
        <Button type="button" variant="outline" size="sm" className="h-7 px-2 font-mono text-[10px] uppercase tracking-[0.06em]" disabled={(!dirty && !layoutDirty) || saving} onClick={onSave}>
          {saving ? "Saving…" : "Save"}
        </Button>
        <span title={startTestDisabled ? "Save and fix errors first" : undefined}>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-7 gap-1.5 px-2 font-mono text-[10px] uppercase tracking-[0.06em]"
            disabled={startTestDisabled}
            onClick={onStartTest}
          >
            <Play aria-hidden="true" className="size-3" />
            Start test run
          </Button>
        </span>
        {publishAction}
      </div>
    </div>
  );
}
