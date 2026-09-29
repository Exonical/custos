"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import type { Project } from "@/lib/api/client";
import { CursorPagination } from "@/components/cursor-pagination";
import { RefreshJobButton } from "@/components/jobs/refresh-job-button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatUtcDateTime } from "@/lib/format";

export function ProjectsTable({ tenant, projects, cursor, search, nextCursor }: {
  tenant: string;
  projects: Project[];
  cursor: string;
  search: string;
  nextCursor: string | null;
}) {
  const pathname = usePathname();
  const router = useRouter();
  const [filter, setFilter] = useState(search);
  const rows = useMemo(() => {
    const value = filter.trim().toLocaleLowerCase();
    return value ? projects.filter((project) => `${project.name} ${project.slug}`.toLocaleLowerCase().includes(value)) : projects;
  }, [filter, projects]);

  function updateFilter(value: string) {
    setFilter(value);
    const params = new URLSearchParams();
    if (value.trim()) params.set("q", value.trim());
    if (cursor) params.set("cursor", cursor);
    router.replace(`${pathname}${params.size ? `?${params.toString()}` : ""}`, { scroll: false });
  }

  return (
    <section className="space-y-4">
      <h1 className="sr-only">Projects</h1>
      <div className="flex flex-wrap items-end gap-3 border-y border-border py-4">
        <div className="grid min-w-56 flex-1 gap-2">
          <label htmlFor="projects-search" className="font-mono text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">Search</label>
          <Input id="projects-search" aria-label="Filter this page by project name or slug" placeholder="Filter this page…" value={filter} onChange={(event) => { updateFilter(event.target.value); }} />
        </div>
        <RefreshJobButton size="icon-sm" />
      </div>
      <div className="overflow-hidden border border-border bg-card">
        <Table containerClassName="max-h-[65vh]">
          <caption className="sr-only">Projects available in this tenant</caption>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Name</TableHead>
              <TableHead>Slug</TableHead>
              <TableHead>State</TableHead>
              <TableHead>Created</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow><TableCell colSpan={4} className="py-10 text-center">
                <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No projects</p>
                <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
              </TableCell></TableRow>
            ) : rows.map((project) => (
              <TableRow key={project.id}>
                <TableCell><Link className="font-medium text-foreground underline-offset-4 hover:text-primary hover:underline" href={`/t/${encodeURIComponent(tenant)}/projects/${encodeURIComponent(project.slug)}`}>{project.name}</Link></TableCell>
                <TableCell className="font-mono text-xs">{project.slug}</TableCell>
                <TableCell><span className={project.state === "archived" ? "font-mono text-[10px] uppercase text-muted-foreground" : "font-mono text-[10px] uppercase text-status-active"}>{project.state}</span></TableCell>
                <TableCell className="font-mono text-xs tabular-nums">{formatUtcDateTime(project.created_at)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <CursorPagination pathname={pathname} query={filter.trim() ? { q: filter.trim() } : {}} cursor={cursor} nextCursor={nextCursor} countLabel={`${String(rows.length)} projects`} />
      </div>
    </section>
  );
}
