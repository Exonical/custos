import { AllocationBudgetBar } from "@/components/allocation-budget-bar";
import { CursorPagination } from "@/components/cursor-pagination";
import { UsageToolbar, type UsageGroupBy, type UsageMetric, type UsageRangePreset, type UsageTopBy } from "@/components/usage-toolbar";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { createApiClient } from "@/lib/api/client";
import { resolveApiError } from "@/lib/api/resolve-api-error";
import { formatDurationSeconds, formatHours, formatSecondsAsHours, formatUtcDateTime } from "@/lib/format";
import { normalizeBarPercent, summarizeUsageRows } from "@/lib/usage";
import { requireServerSession } from "@/lib/session/server";
import { first } from "@/lib/search-params";

const rangeDays: Record<UsageRangePreset, number> = { "7d": 7, "30d": 30, "90d": 90 };
const groups: readonly UsageGroupBy[] = ["day", "user", "project", "cluster", "account", "partition"];
const metrics: readonly UsageMetric[] = ["cpu_seconds", "gpu_seconds", "jobs"];
const topByValues: readonly UsageTopBy[] = ["user", "project"];

function getRange(rangeValue: string, fromValue: string, toValue: string): { range: UsageRangePreset; from: string; to: string } {
  const range: UsageRangePreset = rangeValue === "30d" || rangeValue === "90d" ? rangeValue : "7d";
  const fromTime = Date.parse(fromValue);
  const toTime = Date.parse(toValue);
  if (Number.isFinite(fromTime) && Number.isFinite(toTime) && toTime > fromTime && toTime - fromTime <= 400 * 86_400_000) {
    return { range, from: new Date(fromTime).toISOString(), to: new Date(toTime).toISOString() };
  }
  const to = new Date();
  const from = new Date(to.getTime() - rangeDays[range] * 86_400_000);
  return { range, from: from.toISOString(), to: to.toISOString() };
}

export default async function UsagePage({
  params,
  searchParams,
}: {
  params: Promise<{ tenant: string }>;
  searchParams: Promise<{ range?: string | string[]; from?: string | string[]; to?: string | string[]; group_by?: string | string[]; cursor?: string | string[]; metric?: string | string[]; by?: string | string[] }>;
}) {
  const { tenant } = await params;
  const query = await searchParams;
  const range = getRange(first(query.range), first(query.from), first(query.to));
  const groupValue = first(query.group_by);
  const groupBy: UsageGroupBy = groups.includes(groupValue as UsageGroupBy) ? groupValue as UsageGroupBy : "day";
  const metricValue = first(query.metric);
  const metric: UsageMetric = metrics.includes(metricValue as UsageMetric) ? metricValue as UsageMetric : "cpu_seconds";
  const topByValue = first(query.by);
  const topBy: UsageTopBy = topByValues.includes(topByValue as UsageTopBy) ? topByValue as UsageTopBy : "user";
  const cursor = first(query.cursor);
  const returnTo = `/t/${encodeURIComponent(tenant)}/usage`;
  const session = await requireServerSession(returnTo);
  const api = createApiClient(session.accessToken);
  const [response, topResponse, allocationResponse, projectsResponse] = await Promise.all([
    api.GET("/tenants/{tenant}/accounting/usage", {
      params: {
        path: { tenant },
        query: { group_by: groupBy, from: range.from, to: range.to, limit: 200, cursor: cursor || undefined },
      },
    }),
    api.GET("/tenants/{tenant}/accounting/top", {
      params: { path: { tenant }, query: { metric, by: topBy, from: range.from, to: range.to, limit: 50 } },
    }),
    api.GET("/tenants/{tenant}/accounting/allocations", { params: { path: { tenant } } }),
    api.GET("/tenants/{tenant}/projects", { params: { path: { tenant }, query: { limit: 100 } } }),
  ]);
  if (response.error) return resolveApiError(response, { returnTo, view: "Tenant usage" });
  if (topResponse.error) return resolveApiError(topResponse, { returnTo, view: "Top accounting" });
  if (allocationResponse.error) return resolveApiError(allocationResponse, { returnTo, view: "Tenant allocations" });
  if (projectsResponse.error) return resolveApiError(projectsResponse, { returnTo, view: "Tenant projects" });

  const rows = response.data.items;
  const topRows = topResponse.data.items;
  const tenantAllocations = allocationResponse.data.items ?? [];
  const projectsByID = new Map(projectsResponse.data.items.map((project) => [project.id, project.name]));
  const summary = summarizeUsageRows(rows);
  const hasWaitPercentiles = summary.peak_wait_p50 !== null || summary.peak_wait_p90 !== null;
  const topMaximum = Math.max(0, ...topRows.map((row) => row.value));
  const paginationQuery = { range: range.range, from: range.from, to: range.to, group_by: groupBy, metric, by: topBy };

  return (
    <section className="space-y-5">
      <h1 className="sr-only">Usage</h1>
      <UsageToolbar from={range.from} to={range.to} groupBy={groupBy} range={range.range} metric={metric} topBy={topBy} />

      <section aria-label="Usage summary" className="grid grid-cols-2 gap-px border border-border bg-border sm:grid-cols-3 xl:grid-cols-5">
        {[
          { label: "CPU HOURS", value: formatHours(summary.cpu_hours) },
          { label: "GPU HOURS", value: formatHours(summary.gpu_hours) },
          { label: "JOBS", value: summary.jobs.toLocaleString("en-US") },
          ...(hasWaitPercentiles ? [
            { label: "PEAK DAILY WAIT P50", value: formatDurationSeconds(summary.peak_wait_p50) },
            { label: "PEAK DAILY WAIT P90", value: formatDurationSeconds(summary.peak_wait_p90) },
          ] : []),
        ].map((metric) => (
          <div key={metric.label} className="min-w-0 bg-card px-3 py-4">
            <p className="font-mono text-[9px] uppercase tracking-[0.1em] text-muted-foreground">{metric.label}</p>
            <p className="mt-2 font-mono text-xl font-medium tabular-nums">{metric.value}</p>
          </div>
        ))}
      </section>

      <section aria-labelledby="usage-table-heading" className="overflow-hidden border border-border bg-card">
        <header className="border-b border-border px-4 py-3">
          <h2 id="usage-table-heading" className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em]">Usage by {groupBy}</h2>
        </header>
        <Table containerClassName="max-h-[60vh]">
          <caption className="sr-only">Usage rows for the selected range</caption>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>{groupBy}</TableHead>
              <TableHead>Jobs</TableHead>
              <TableHead>Failed</TableHead>
              <TableHead>CPU hours</TableHead>
              <TableHead>GPU hours</TableHead>
              <TableHead>Node hours</TableHead>
              <TableHead>Memory GB hours</TableHead>
              <TableHead>Wait hours</TableHead>
              <TableHead>Run hours</TableHead>
              <TableHead>Wait P50</TableHead>
              <TableHead>Wait P90</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow><TableCell colSpan={11} className="py-10 text-center">
                <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No usage</p>
                <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
              </TableCell></TableRow>
            ) : rows.map((row) => (
              <TableRow key={row.key}>
                <TableCell className="font-mono text-xs">{row.key}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{row.jobs}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{row.failed}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{formatHours(row.cpu_hours)}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{formatHours(row.gpu_hours)}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{formatHours(row.node_hours)}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{formatHours(row.mem_gb_hours)}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{formatHours(row.wait_hours)}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{formatHours(row.run_hours)}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{formatDurationSeconds(row.wait_p50)}</TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{formatDurationSeconds(row.wait_p90)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <CursorPagination pathname={`/t/${encodeURIComponent(tenant)}/usage`} query={paginationQuery} cursor={cursor} nextCursor={response.data.next_cursor || null} countLabel={`${String(rows.length)} groups`} />
      </section>

      <section className="grid gap-0 border border-border bg-card lg:grid-cols-2">
        <div className="min-w-0 border-b border-border lg:border-b-0 lg:border-r">
          <header className="border-b border-border px-4 py-3">
            <h2 className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em]">Top consumers · by {topBy}</h2>
          </header>
          <Table containerClassName="max-h-[52vh]">
            <caption className="sr-only">Top {topBy} consumers by {metric}</caption>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Rank</TableHead>
                <TableHead>{topBy}</TableHead>
                <TableHead>Value</TableHead>
                <TableHead>Share</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {topRows.length === 0 ? (
                <TableRow><TableCell colSpan={4} className="py-8 text-center">
                  <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No top consumers</p>
                  <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
                </TableCell></TableRow>
              ) : topRows.map((row, index) => {
                const percent = normalizeBarPercent(row.value, topMaximum);
                const label = topBy === "project" ? projectsByID.get(row.key) ?? row.key : row.key;
                const value = metric === "jobs" ? row.value.toLocaleString("en-US") : formatSecondsAsHours(row.value);
                return (
                  <TableRow key={`${row.key}-${String(index)}`}>
                    <TableCell className="font-mono text-xs tabular-nums">{index + 1}</TableCell>
                    <TableCell className="font-mono text-xs">{label}</TableCell>
                    <TableCell className="font-mono text-xs tabular-nums">{value}</TableCell>
                    <TableCell>
                      <div role="meter" aria-label={`${label} share`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} className="h-1.5 w-24 overflow-hidden bg-muted">
                        <div className="h-full bg-primary" style={{ width: `${String(percent)}%` }} />
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
        <div className="min-w-0">
          <header className="border-b border-border px-4 py-3">
            <h2 className="font-mono text-[11px] font-semibold uppercase tracking-[0.12em]">Tenant allocations</h2>
          </header>
          <Table containerClassName="max-h-[52vh]">
            <caption className="sr-only">Allocation budgets and consumption visible to this tenant</caption>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Project</TableHead>
                <TableHead>Resource</TableHead>
                <TableHead>Budget</TableHead>
                <TableHead>Consumed</TableHead>
                <TableHead>Remaining</TableHead>
                <TableHead>Enforcement</TableHead>
                <TableHead>Period</TableHead>
                <TableHead>State</TableHead>
                <TableHead>Usage</TableHead>
                <TableHead>As of</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tenantAllocations.length === 0 ? (
                <TableRow><TableCell colSpan={10} className="py-8 text-center">
                  <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No allocations</p>
                  <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
                </TableCell></TableRow>
              ) : tenantAllocations.map((item) => {
                const allocation = item.allocation;
                const projectName = projectsByID.get(allocation.project_id) ?? allocation.project_id;
                return (
                  <TableRow key={allocation.id}>
                    <TableCell>{projectName}</TableCell>
                    <TableCell><span className="font-mono text-xs uppercase">{allocation.unit}</span><span className="ml-2 text-xs text-muted-foreground">{allocation.name}</span></TableCell>
                    <TableCell className="font-mono text-xs tabular-nums">{formatHours(allocation.limit_amount)}</TableCell>
                    <TableCell className="font-mono text-xs tabular-nums">{formatHours(item.consumed)}</TableCell>
                    <TableCell className="font-mono text-xs tabular-nums">{formatHours(item.remaining)}</TableCell>
                    <TableCell className="font-mono text-xs uppercase">{allocation.enforcement}</TableCell>
                    <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(allocation.period_start)} – {formatUtcDateTime(allocation.period_end)}</TableCell>
                    <TableCell className="font-mono text-xs uppercase">{item.active ? "active" : "inactive"}</TableCell>
                    <TableCell><AllocationBudgetBar consumed={item.consumed} budget={allocation.limit_amount} /></TableCell>
                    <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(item.as_of)}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      </section>
    </section>
  );
}
