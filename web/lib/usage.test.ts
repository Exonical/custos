import { describe, expect, it } from "vitest";
import type { AccountingUsageRow } from "@/lib/api/client";
import { allocationBudgetMeter, normalizeBarPercent, summarizeUsageRows } from "@/lib/usage";

function row(values: Partial<AccountingUsageRow> = {}): AccountingUsageRow {
  return {
    key: "2026-09-28",
    jobs: 1,
    failed: 0,
    cpu_hours: 1,
    gpu_hours: 0,
    node_hours: 1,
    mem_gb_hours: 2,
    wait_hours: 0.5,
    run_hours: 1,
    ...values,
  };
}

describe("summarizeUsageRows", () => {
  it("sums additive usage and reports peak per-row wait percentiles", () => {
    expect(summarizeUsageRows([
      row({ jobs: 3, cpu_hours: 4.5, gpu_hours: 1.25, wait_p50: 120, wait_p90: 360 }),
      row({ jobs: 2, cpu_hours: 1.5, gpu_hours: 0.75, wait_p50: 90, wait_p90: 480 }),
    ])).toEqual({ jobs: 5, cpu_hours: 6, gpu_hours: 2, peak_wait_p50: 120, peak_wait_p90: 480 });
  });

  it("returns zero totals and missing percentiles for an empty result", () => {
    expect(summarizeUsageRows([])).toEqual({ jobs: 0, cpu_hours: 0, gpu_hours: 0, peak_wait_p50: null, peak_wait_p90: null });
  });
});

describe("allocationBudgetMeter", () => {
  it("applies amber and destructive thresholds while clamping the bar", () => {
    expect(allocationBudgetMeter(80, 100)).toEqual({ percent: 80, tone: "normal" });
    expect(allocationBudgetMeter(80.1, 100)).toEqual({ percent: 80.1, tone: "warning" });
    expect(allocationBudgetMeter(100, 100)).toEqual({ percent: 100, tone: "warning" });
    expect(allocationBudgetMeter(120, 100)).toEqual({ percent: 100, tone: "destructive" });
    expect(allocationBudgetMeter(-10, 100)).toEqual({ percent: 0, tone: "normal" });
    expect(allocationBudgetMeter(1, 0)).toEqual({ percent: 100, tone: "destructive" });
  });
});

describe("normalizeBarPercent", () => {
  it("normalizes ranked values to a clamped percentage", () => {
    expect(normalizeBarPercent(25, 100)).toBe(25);
    expect(normalizeBarPercent(120, 100)).toBe(100);
    expect(normalizeBarPercent(-1, 100)).toBe(0);
    expect(normalizeBarPercent(5, 0)).toBe(0);
  });
});
