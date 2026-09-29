import type { AccountingUsageRow } from "@/lib/api/client";

export type UsageSummary = Readonly<{
  jobs: number;
  cpu_hours: number;
  gpu_hours: number;
  peak_wait_p50: number | null;
  peak_wait_p90: number | null;
}>;

export type AllocationBudgetMeter = Readonly<{
  percent: number;
  tone: "normal" | "warning" | "destructive";
}>;

export function normalizeBarPercent(value: number, maximum: number): number {
  if (!Number.isFinite(value) || !Number.isFinite(maximum) || maximum <= 0) return 0;
  return Math.min(100, Math.max(0, (value / maximum) * 100));
}

export function allocationBudgetMeter(consumed: number, budget: number): AllocationBudgetMeter {
  const rawPercent = budget > 0 ? (consumed / budget) * 100 : consumed > 0 ? Number.POSITIVE_INFINITY : 0;
  const percent = Math.min(100, Math.max(0, Number.isFinite(rawPercent) ? rawPercent : 100));
  const tone = rawPercent > 100 ? "destructive" : rawPercent > 80 ? "warning" : "normal";
  return { percent, tone };
}

function maxNullable(current: number | null, value: number | null | undefined): number | null {
  if (value === null || value === undefined || !Number.isFinite(value)) return current;
  return current === null ? value : Math.max(current, value);
}

export function summarizeUsageRows(rows: readonly AccountingUsageRow[]): UsageSummary {
  return rows.reduce<UsageSummary>((summary, row) => ({
    jobs: summary.jobs + row.jobs,
    cpu_hours: summary.cpu_hours + row.cpu_hours,
    gpu_hours: summary.gpu_hours + row.gpu_hours,
    peak_wait_p50: maxNullable(summary.peak_wait_p50, row.wait_p50),
    peak_wait_p90: maxNullable(summary.peak_wait_p90, row.wait_p90),
  }), { jobs: 0, cpu_hours: 0, gpu_hours: 0, peak_wait_p50: null, peak_wait_p90: null });
}
