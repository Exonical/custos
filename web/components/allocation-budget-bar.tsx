import { allocationBudgetMeter } from "@/lib/usage";
import { formatHours } from "@/lib/format";

export function AllocationBudgetBar({ consumed, budget }: { consumed: number; budget: number }) {
  const { percent, tone } = allocationBudgetMeter(consumed, budget);
  const barColor = tone === "destructive" ? "bg-destructive" : tone === "warning" ? "bg-status-queued" : "bg-primary";
  return (
    <div className="flex min-w-32 items-center gap-2">
      <div
        role="meter"
        aria-label="Allocation budget consumed"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
        aria-valuetext={`${formatHours(consumed)} of ${formatHours(budget)}`}
        className="h-1.5 min-w-16 flex-1 overflow-hidden bg-muted"
      >
        <div className={`h-full ${barColor}`} style={{ width: `${String(percent)}%` }} />
      </div>
      <span className="w-9 text-right font-mono text-[10px] tabular-nums">{Math.round(percent)}%</span>
    </div>
  );
}
