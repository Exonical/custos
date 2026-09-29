"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import type { TenantCluster } from "@/lib/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { RefreshJobButton } from "@/components/jobs/refresh-job-button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export function ClustersTable({ tenant, clusters, search }: { tenant: string; clusters: TenantCluster[]; search: string }) {
  const pathname = usePathname();
  const router = useRouter();
  const [filter, setFilter] = useState(search);
  const [page, setPage] = useState(0);
  const pageSize = 20;
  const rows = useMemo(() => {
    const value = filter.trim().toLocaleLowerCase();
    return value ? clusters.filter((cluster) => `${cluster.display_name} ${cluster.name}`.toLocaleLowerCase().includes(value)) : clusters;
  }, [clusters, filter]);
  const visibleRows = useMemo(() => rows.slice(page * pageSize, (page + 1) * pageSize), [page, pageSize, rows]);
  const pageCount = Math.max(1, Math.ceil(rows.length / pageSize));

  function updateFilter(value: string) {
    setFilter(value);
    setPage(0);
    const params = new URLSearchParams();
    if (value.trim()) params.set("q", value.trim());
    router.replace(`${pathname}${params.size ? `?${params.toString()}` : ""}`, { scroll: false });
  }

  return (
    <section className="space-y-4">
      <h1 className="sr-only">Clusters</h1>
      <div className="flex flex-wrap items-end gap-3 border-y border-border py-4">
        <div className="grid min-w-56 flex-1 gap-2">
          <label htmlFor="clusters-search" className="font-mono text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">Search</label>
          <Input id="clusters-search" aria-label="Filter this page by cluster name or slug" placeholder="Filter this page…" value={filter} onChange={(event) => { updateFilter(event.target.value); }} />
        </div>
        <RefreshJobButton size="icon-sm" />
      </div>
      <div className="overflow-hidden border border-border bg-card">
        <Table containerClassName="max-h-[65vh]">
          <caption className="sr-only">Clusters visible to this tenant</caption>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Name</TableHead>
              <TableHead>Slug</TableHead>
              <TableHead>Slurm version</TableHead>
              <TableHead>Status</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow><TableCell colSpan={4} className="py-10 text-center">
                <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No clusters</p>
                <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
              </TableCell></TableRow>
            ) : visibleRows.map((cluster) => (
              <TableRow key={cluster.id}>
                <TableCell><Link className="font-medium text-foreground underline-offset-4 hover:text-primary hover:underline" href={`/t/${encodeURIComponent(tenant)}/clusters/${encodeURIComponent(cluster.name)}`}>{cluster.display_name}</Link></TableCell>
                <TableCell className="font-mono text-xs">{cluster.name}</TableCell>
                <TableCell className="font-mono text-xs">{cluster.slurm_version ?? "—"}</TableCell>
                <TableCell><Badge variant="outline" className={cluster.state === "active" ? "border-status-active/35 bg-status-active/10 text-status-active" : cluster.state === "degraded" || cluster.state === "unreachable" ? "border-status-degraded/35 bg-status-degraded/10 text-status-degraded" : "border-status-canceled/35 bg-status-canceled/10 text-status-canceled"}>{cluster.state}</Badge></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <div className="flex items-center justify-between gap-3 border-t border-border px-3 py-3 font-mono text-[10px] uppercase tracking-[0.06em] text-muted-foreground">
          <span>{rows.length} clusters · Page {page + 1} of {pageCount}</span>
          <div className="flex items-center gap-2">
            <Button type="button" size="sm" variant="outline" aria-label="Previous page" disabled={page === 0} onClick={() => { setPage((value) => Math.max(0, value - 1)); }}>Previous</Button>
            <Button type="button" size="sm" variant="outline" aria-label="Next page" disabled={page + 1 >= pageCount} onClick={() => { setPage((value) => Math.min(pageCount - 1, value + 1)); }}>Next</Button>
          </div>
        </div>
      </div>
    </section>
  );
}
