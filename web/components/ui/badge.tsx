import type { HTMLAttributes } from "react";
import { cn } from "@/lib/utils";

const stateStyles: Record<string, string> = {
  RUNNING: "bg-emerald-100 text-emerald-800",
  COMPLETED: "bg-sky-100 text-sky-800",
  FAILED: "bg-rose-100 text-rose-800",
  CANCELED: "bg-slate-200 text-slate-700",
  QUEUED: "bg-amber-100 text-amber-800",
  SUBMITTING: "bg-violet-100 text-violet-800",
};

export function Badge({ className, ...props }: HTMLAttributes<HTMLSpanElement>) {
  return <span className={cn("inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-semibold", className)} {...props} />;
}

export function StateBadge({ state }: { state: string }) {
  return <Badge className={stateStyles[state] ?? "bg-slate-100 text-slate-700"}>{state}</Badge>;
}
