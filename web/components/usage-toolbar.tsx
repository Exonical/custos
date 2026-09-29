"use client";

import { usePathname, useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { formatUtcDate } from "@/lib/format";

export type UsageGroupBy = "user" | "project" | "cluster" | "account" | "partition" | "day";
export type UsageMetric = "cpu_seconds" | "gpu_seconds" | "jobs";
export type UsageTopBy = "user" | "project";
export type UsageRangePreset = "7d" | "30d" | "90d";

const presets: ReadonlyArray<{ value: UsageRangePreset; days: number; label: string }> = [
  { value: "7d", days: 7, label: "7d" },
  { value: "30d", days: 30, label: "30d" },
  { value: "90d", days: 90, label: "90d" },
];

export function UsageToolbar({ from, to, groupBy, range, metric, topBy }: {
  from: string;
  to: string;
  groupBy: UsageGroupBy;
  range: UsageRangePreset;
  metric: UsageMetric;
  topBy: UsageTopBy;
}) {
  const router = useRouter();
  const pathname = usePathname();

  function updateParams(values: Partial<Record<"range" | "from" | "to" | "group_by" | "metric" | "by", string>>) {
    const params = new URLSearchParams(window.location.search);
    params.delete("cursor");
    for (const [key, value] of Object.entries(values)) {
      if (value) params.set(key, value);
    }
    router.push(`${pathname}?${params.toString()}`);
  }

  function setRange(preset: typeof presets[number]) {
    const toDate = new Date();
    const fromDate = new Date(toDate.getTime() - preset.days * 86_400_000);
    updateParams({ range: preset.value, from: fromDate.toISOString(), to: toDate.toISOString() });
  }

  return (
    <div className="flex flex-wrap items-end gap-4 border-y border-border py-4">
      <div className="grid gap-2">
        <Label>DATE RANGE · UTC</Label>
        <div className="flex items-center gap-1">
          {presets.map((preset) => (
            <Button
              key={preset.value}
              type="button"
              variant="outline"
              size="sm"
              aria-pressed={range === preset.value}
              className={range === preset.value ? "border-primary/50 text-primary" : undefined}
              onClick={() => { setRange(preset); }}
            >
              {preset.label}
            </Button>
          ))}
        </div>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="usage-group">Group by</Label>
        <Select value={groupBy} onValueChange={(value) => { if (typeof value === "string") updateParams({ group_by: value }); }}>
          <SelectTrigger id="usage-group" aria-label="Group by" className="h-8 min-w-36">
            <SelectValue>{groupBy}</SelectValue>
          </SelectTrigger>
          <SelectContent align="start">
            <SelectItem value="day">Day</SelectItem>
            <SelectItem value="user">User</SelectItem>
            <SelectItem value="project">Project</SelectItem>
            <SelectItem value="cluster">Cluster</SelectItem>
            <SelectItem value="account">Account</SelectItem>
            <SelectItem value="partition">Partition</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="usage-top-metric">Top metric</Label>
        <Select value={metric} onValueChange={(value) => { if (typeof value === "string") updateParams({ metric: value }); }}>
          <SelectTrigger id="usage-top-metric" aria-label="Top metric" className="h-8 min-w-36">
            <SelectValue>{metric === "cpu_seconds" ? "CPU hours" : metric === "gpu_seconds" ? "GPU hours" : "Jobs"}</SelectValue>
          </SelectTrigger>
          <SelectContent align="start">
            <SelectItem value="cpu_seconds">CPU hours</SelectItem>
            <SelectItem value="gpu_seconds">GPU hours</SelectItem>
            <SelectItem value="jobs">Jobs</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="usage-top-by">Top by</Label>
        <Select value={topBy} onValueChange={(value) => { if (typeof value === "string") updateParams({ by: value }); }}>
          <SelectTrigger id="usage-top-by" aria-label="Top by" className="h-8 min-w-32">
            <SelectValue>{topBy === "user" ? "User" : "Project"}</SelectValue>
          </SelectTrigger>
          <SelectContent align="start">
            <SelectItem value="user">User</SelectItem>
            <SelectItem value="project">Project</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <p className="ml-auto font-mono text-[10px] uppercase tracking-[0.06em] text-muted-foreground">{formatUtcDate(from)} – {formatUtcDate(to)}</p>
    </div>
  );
}
