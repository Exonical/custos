import Link from "next/link";
import type { WorkflowVersion } from "@/lib/api/client";
import { StateBadge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatUtcDateTime } from "@/lib/format";

export function WorkflowVersionsTable({ tenant, workflow, versions }: { tenant: string; workflow: string; versions: WorkflowVersion[] }) {
  return (
    <div className="overflow-hidden bg-card">
      <Table containerClassName="max-h-[60vh]">
        <caption className="sr-only">Workflow versions</caption>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Version</TableHead>
            <TableHead>State</TableHead>
            <TableHead>Schema</TableHead>
            <TableHead>Spec hash</TableHead>
            <TableHead>Created</TableHead>
            <TableHead>Published</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {versions.length === 0 ? (
            <TableRow><TableCell colSpan={6} className="py-10 text-center">
              <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No versions</p>
              <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
            </TableCell></TableRow>
          ) : versions.map((version) => (
            <TableRow key={version.id}>
              <TableCell>
                <Link className="font-mono text-xs text-primary hover:underline" href={`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflow)}/versions/${encodeURIComponent(version.id)}`}>
                  v{version.number}
                </Link>
              </TableCell>
              <TableCell><StateBadge state={version.state} /></TableCell>
              <TableCell className="font-mono text-xs">{version.schemaVersion}</TableCell>
              <TableCell className="font-mono text-xs" title={version.specHash}>{version.specHash.slice(0, 19)}</TableCell>
              <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(version.createdAt)}</TableCell>
              <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(version.publishedAt)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
