"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { tableFeatures, useTable, type ColumnDef } from "@tanstack/react-table";
import type { Job, ProjectList, TenantClusterList } from "@/lib/api/client";
import { formatUtcDateTime } from "@/lib/format";
import { StateBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RefreshJobButton } from "@/components/jobs/refresh-job-button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const jobTableFeatures = tableFeatures({});
type JobTableFeatures = typeof jobTableFeatures;
const stateValues = ["all", "SUBMITTING", "QUEUED", "RUNNING", "COMPLETED", "FAILED", "CANCELED"] as const;

function buildPageUrl(pathname: string, project: string, state: string, cursor = "", search = "") {
  const params = new URLSearchParams();
  if (project !== "all") params.set("project", project);
  if (state !== "all") params.set("state", state);
  if (cursor) params.set("cursor", cursor);
  if (search.trim()) params.set("q", search.trim());
  return `${pathname}${params.size ? `?${params.toString()}` : ""}`;
}

export function JobsTable({
  tenant,
  jobs,
  projects,
  clusters,
  selectedProject,
  selectedState,
  cursor,
  search,
  nextCursor,
}: {
  tenant: string;
  jobs: Job[];
  projects: ProjectList["items"];
  clusters: TenantClusterList["items"];
  selectedProject: string;
  selectedState: string;
  cursor: string;
  search: string;
  nextCursor: string | null;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const projectFilter = selectedProject || "all";
  const stateFilter = stateValues.includes(selectedState as (typeof stateValues)[number]) && selectedState ? selectedState : "all";
  const [searchTerm, setSearchTerm] = useState(search);
  const [cursorHistory, setCursorHistory] = useState<string[]>([]);

  const clusterById = useMemo(() => new Map(clusters.map((cluster) => [cluster.id, cluster])), [clusters]);
  const filteredJobs = useMemo(() => {
    const query = searchTerm.trim().toLocaleLowerCase();
    return query ? jobs.filter((job) => job.name.toLocaleLowerCase().includes(query)) : jobs;
  }, [jobs, searchTerm]);

  const columns = useMemo<ColumnDef<JobTableFeatures, Job>[]>(() => [
    {
      accessorKey: "name",
      header: "Name",
      cell: ({ row }) => {
        const project = projects.find((item) => item.id === row.original.project_id);
        const projectRef = project?.slug ?? row.original.project_id;
        const query = new URLSearchParams({ project: projectRef });
        return (
          <Link className="font-medium text-foreground underline-offset-4 hover:text-primary hover:underline" href={`/t/${encodeURIComponent(tenant)}/jobs/${row.original.id}?${query.toString()}`}>
            {row.original.name || row.original.id.slice(0, 8)}
          </Link>
        );
      },
    },
    { accessorKey: "state", header: "State", cell: ({ row }) => <StateBadge state={row.original.state} /> },
    {
      accessorKey: "project_id",
      header: "Project",
      cell: ({ row }) => {
        const project = projects.find((item) => item.id === row.original.project_id);
        return <span>{project?.name || project?.slug || row.original.project_id}</span>;
      },
    },
    {
      accessorKey: "cluster_id",
      header: "Cluster",
      cell: ({ row }) => {
        const cluster = clusterById.get(row.original.cluster_id);
        const label = cluster?.name || cluster?.display_name || row.original.cluster_id;
        return <span className="font-mono text-xs tracking-[0.04em]" title={row.original.cluster_id}>{label}</span>;
      },
    },
    { accessorKey: "created_at", header: "Created", cell: ({ row }) => <span className="font-mono text-xs tabular-nums">{formatUtcDateTime(row.original.created_at)}</span> },
  ], [clusterById, projects, tenant]);

  const table = useTable({ features: jobTableFeatures, data: filteredJobs, columns });

  function updateFilters(project: string, state: string) {
    setCursorHistory([]);
    router.push(buildPageUrl(pathname, project, state, "", searchTerm));
  }

  function updateSearch(value: string) {
    setSearchTerm(value);
    router.replace(buildPageUrl(pathname, projectFilter, stateFilter, cursor, value), { scroll: false });
  }

  function nextPage() {
    if (!nextCursor) return;
    const history = [...cursorHistory, cursor];
    setCursorHistory(history);
    router.push(buildPageUrl(pathname, projectFilter, stateFilter, nextCursor, searchTerm));
  }

  function previousPage() {
    if (cursorHistory.length === 0) return;
    const history = cursorHistory.slice(0, -1);
    const previousCursor = cursorHistory[cursorHistory.length - 1] ?? "";
    setCursorHistory(history);
    router.push(buildPageUrl(pathname, projectFilter, stateFilter, previousCursor, searchTerm));
  }

  return (
    <section className="space-y-4">
      <h1 className="sr-only">Jobs</h1>
      <div className="flex flex-wrap items-end gap-3 border-y border-border py-4">
        <div className="grid min-w-56 flex-1 gap-2">
          <Label htmlFor="jobs-search">Search</Label>
          <Input id="jobs-search" aria-label="Filter this page by job name" placeholder="Filter this page…" value={searchTerm} onChange={(event) => { updateSearch(event.target.value); }} className="h-8 min-w-0" />
        </div>
        <div className="grid min-w-40 gap-2">
          <Label htmlFor="jobs-project">Project</Label>
          <Select value={projectFilter} onValueChange={(value) => { if (typeof value === "string") updateFilters(value, stateFilter); }}>
            <SelectTrigger id="jobs-project" aria-label="Project" className="h-8">
              <SelectValue>{projectFilter === "all" ? "All projects" : projects.find((project) => project.slug === projectFilter)?.name ?? projectFilter}</SelectValue>
            </SelectTrigger>
            <SelectContent align="start">
              <SelectItem value="all">All projects</SelectItem>
              {projects.map((project) => <SelectItem key={project.id} value={project.slug}>{project.name}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        <div className="grid min-w-36 gap-2">
          <Label htmlFor="jobs-state">State</Label>
          <Select value={stateFilter} onValueChange={(value) => { if (typeof value === "string") updateFilters(projectFilter, value); }}>
            <SelectTrigger id="jobs-state" aria-label="State" className="h-8">
              <SelectValue>{stateFilter === "all" ? "All states" : stateFilter}</SelectValue>
            </SelectTrigger>
            <SelectContent align="start">
              <SelectItem value="all">All states</SelectItem>
              {stateValues.filter((state) => state !== "all").map((state) => <SelectItem key={state} value={state}>{state}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        <RefreshJobButton size="icon-sm" />
      </div>

      <div className="overflow-hidden border border-border bg-card">
        <Table containerClassName="max-h-[65vh]">
          <caption className="sr-only">Jobs visible to this account</caption>
          <TableHeader>
            {table.getHeaderGroups().map((group) => (
              <TableRow key={group.id} className="hover:bg-transparent">
                {group.headers.map((header) => (
                  <TableHead key={header.id} className={header.id === "created_at" ? "hidden sm:table-cell" : undefined}>
                    {header.isPlaceholder ? null : <table.FlexRender header={header} />}
                  </TableHead>
                ))}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody>
            {table.getRowModel().rows.length === 0 ? (
              <TableRow><TableCell colSpan={columns.length} className="py-10 text-center">
                <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No jobs</p>
                <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
              </TableCell></TableRow>
            ) : table.getRowModel().rows.map((row) => (
              <TableRow key={row.id}>
                {row.getAllCells().map((cell) => (
                  <TableCell key={cell.id} className={cell.column.id === "created_at" ? "hidden sm:table-cell" : undefined}><table.FlexRender cell={cell} /></TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <div className="flex items-center justify-between gap-3 border-t border-border px-3 py-3 font-mono text-[10px] uppercase tracking-[0.06em] text-muted-foreground">
          <span>Showing {filteredJobs.length} of {jobs.length} · Page {cursorHistory.length + 1}</span>
          <div className="flex items-center gap-2">
            <Button type="button" size="sm" variant="outline" aria-label="Previous page" disabled={cursorHistory.length === 0} onClick={previousPage}>Previous</Button>
            <Button type="button" size="sm" variant="outline" aria-label="Next page" disabled={!nextCursor} onClick={nextPage}>Next</Button>
          </div>
        </div>
      </div>
    </section>
  );
}
