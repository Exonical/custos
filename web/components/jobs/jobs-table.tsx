"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useMemo } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { tableFeatures, useTable, type ColumnDef } from "@tanstack/react-table";
import { z } from "zod";
import type { Job, ProjectList } from "@/lib/api/client";
import { Badge, StateBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const jobTableFeatures = tableFeatures({});
type JobTableFeatures = typeof jobTableFeatures;

const stateValues = ["", "SUBMITTING", "QUEUED", "RUNNING", "COMPLETED", "FAILED", "CANCELED"] as const;
const filtersSchema = z.object({ project: z.string(), state: z.enum(stateValues) });
type FilterValues = z.infer<typeof filtersSchema>;

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : new Intl.DateTimeFormat("en-US", { dateStyle: "medium", timeStyle: "short", timeZone: "UTC" }).format(date);
}

export function JobsTable({
  tenant,
  jobs,
  projects,
  selectedProject,
  selectedState,
  nextCursor,
}: {
  tenant: string;
  jobs: Job[];
  projects: ProjectList["items"];
  selectedProject: string;
  selectedState: string;
  nextCursor: string | null;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const form = useForm<FilterValues>({
    resolver: zodResolver(filtersSchema),
    defaultValues: {
      project: selectedProject,
      state: stateValues.includes(selectedState as (typeof stateValues)[number])
        ? (selectedState as (typeof stateValues)[number])
        : "",
    },
  });

  const columns = useMemo<ColumnDef<JobTableFeatures, Job>[]>(() => [
    {
      accessorKey: "name",
      header: "Job",
      cell: ({ row }) => {
        const project = projects.find((item) => item.id === row.original.project_id);
        const projectRef = project?.slug ?? row.original.project_id;
        const query = new URLSearchParams({ project: projectRef });
        return (
          <Link className="font-semibold text-teal-800 hover:underline" href={`/t/${encodeURIComponent(tenant)}/jobs/${row.original.id}?${query.toString()}`}>
            {row.original.name || row.original.id.slice(0, 8)}
          </Link>
        );
      },
    },
    { accessorKey: "state", header: "State", cell: ({ row }) => <StateBadge state={row.original.state} /> },
    {
      accessorKey: "cluster_id",
      header: "Cluster",
      cell: ({ row }) => <span className="font-mono text-xs">{row.original.cluster_id.slice(0, 8)}</span>,
    },
    { accessorKey: "created_at", header: "Created", cell: ({ row }) => formatTime(row.original.created_at) },
  ], [projects, tenant]);

  const table = useTable({ features: jobTableFeatures, data: jobs, columns });

  const applyFilters = form.handleSubmit((values) => {
    const params = new URLSearchParams();
    if (values.project) params.set("project", values.project);
    if (values.state) params.set("state", values.state);
    router.push(`${pathname}${params.size ? `?${params.toString()}` : ""}`);
  });

  function nextPage() {
    if (!nextCursor) return;
    const params = new URLSearchParams();
    const values = form.getValues();
    if (values.project) params.set("project", values.project);
    if (values.state) params.set("state", values.state);
    params.set("cursor", nextCursor);
    router.push(`${pathname}?${params.toString()}`);
  }

  return (
    <section className="space-y-4">
      <header>
        <p className="text-sm font-semibold uppercase tracking-wide text-teal-700">Queue</p>
        <h1 className="mt-1 text-3xl font-bold tracking-tight">Jobs</h1>
      </header>
      <form className="flex flex-wrap items-end gap-3 rounded-xl border border-slate-200 bg-white p-4" onSubmit={(event) => { void applyFilters(event); }}>
        <label className="grid gap-1 text-sm font-medium text-slate-700">
          Project
          <select className="h-10 min-w-48 rounded-md border border-slate-300 bg-white px-3" {...form.register("project")}>
            <option value="">All projects</option>
            {projects.map((project) => <option key={project.id} value={project.slug}>{project.name}</option>)}
          </select>
        </label>
        <label className="grid gap-1 text-sm font-medium text-slate-700">
          State
          <select className="h-10 min-w-40 rounded-md border border-slate-300 bg-white px-3" {...form.register("state")}>
            <option value="">All states</option>
            {stateValues.filter(Boolean).map((state) => <option key={state} value={state}>{state}</option>)}
          </select>
        </label>
        <Button type="submit" size="sm">Apply filters</Button>
        <Badge className="bg-slate-100 text-slate-600">Keyset pagination</Badge>
      </form>

      <div className="overflow-hidden rounded-xl border border-slate-200 bg-white">
        <div className="overflow-x-auto">
          <Table>
            <caption className="sr-only">Jobs visible to this account</caption>
            <TableHeader>
              {table.getHeaderGroups().map((group) => (
                <TableRow key={group.id} className="hover:bg-transparent">
                  {group.headers.map((header) => (
                    <TableHead key={header.id}>
                      {header.isPlaceholder ? null : <table.FlexRender header={header} />}
                    </TableHead>
                  ))}
                </TableRow>
              ))}
            </TableHeader>
            <TableBody>
              {table.getRowModel().rows.length === 0 ? (
                <TableRow><TableCell colSpan={columns.length} className="py-10 text-center text-slate-500">No jobs match these filters.</TableCell></TableRow>
              ) : table.getRowModel().rows.map((row) => (
                <TableRow key={row.id}>
                  {row.getAllCells().map((cell) => (
                    <TableCell key={cell.id}><table.FlexRender cell={cell} /></TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
        <div className="flex items-center justify-between border-t border-slate-100 px-4 py-3 text-sm text-slate-600">
          <span>{jobs.length} jobs in this page</span>
          <Button type="button" size="sm" variant="outline" disabled={!nextCursor} onClick={nextPage}>Next page</Button>
        </div>
      </div>
    </section>
  );
}
