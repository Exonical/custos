import Link from "next/link";
import type { Workflow, WorkflowExecution, WorkflowVersion } from "@/lib/api/client";
import { Badge, StateBadge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDurationBetween, formatUtcDateTime } from "@/lib/format";

export function WorkflowExecutionsTable({ tenant, executions, workflows, versions }: {
  tenant: string;
  executions: WorkflowExecution[];
  workflows: Workflow[];
  versions: WorkflowVersion[];
}) {
  const workflowById = new Map(workflows.map((workflow) => [workflow.id, workflow]));
  const versionById = new Map(versions.map((version) => [version.id, version]));
  return (
    <div className="overflow-hidden bg-card">
      <Table containerClassName="max-h-[60vh]">
        <caption className="sr-only">Workflow executions</caption>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>ID</TableHead>
            <TableHead>Workflow</TableHead>
            <TableHead>Version</TableHead>
            <TableHead>State</TableHead>
            <TableHead>Requested</TableHead>
            <TableHead>Started</TableHead>
            <TableHead>Ended · duration</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {executions.length === 0 ? (
            <TableRow><TableCell colSpan={7} className="py-10 text-center">
              <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No executions</p>
              <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
            </TableCell></TableRow>
          ) : executions.map((execution) => {
            const workflow = workflowById.get(execution.workflowId);
            const version = versionById.get(execution.workflowVersionId);
            return (
              <TableRow key={execution.id}>
                <TableCell>
                  <Link className="font-mono text-xs text-primary hover:underline" href={`/t/${encodeURIComponent(tenant)}/executions/${encodeURIComponent(execution.id)}`} title={execution.id}>
                    {execution.id.slice(0, 8)}
                  </Link>
                </TableCell>
                <TableCell>
                  <Link className="text-xs hover:text-primary" href={`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(execution.workflowId)}`}>
                    {workflow?.name ?? execution.workflowId}
                  </Link>
                </TableCell>
                <TableCell>
                  {version ? <Link className="font-mono text-xs hover:text-primary" href={`/t/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(execution.workflowId)}/versions/${encodeURIComponent(version.id)}`}>v{version.number}</Link> : <span className="font-mono text-xs text-muted-foreground">—</span>}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <StateBadge state={execution.state} />
                    {execution.test ? <Badge variant="outline" className="border-primary/40 bg-primary/10 text-primary">TEST</Badge> : null}
                  </div>
                </TableCell>
                <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(execution.createdAt)}</TableCell>
                <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(execution.startedAt)}</TableCell>
                <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(execution.endedAt)} · {formatDurationBetween(execution.startedAt, execution.endedAt)}</TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
