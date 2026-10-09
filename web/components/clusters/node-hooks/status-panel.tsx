"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { currentNodeCount, relativeTime, shortSha, type NodeStatusList } from "@/lib/nodehooks/config";

export function NodeStatusPanel({ status, loading, error, onRefresh }: {
  status: NodeStatusList;
  loading: boolean;
  error: string;
  onRefresh: () => void;
}) {
  const counts = currentNodeCount(status);
  return (
    <section aria-labelledby="node-status-heading" className="space-y-3 border-t border-border pt-5">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 id="node-status-heading" className="font-mono text-xs font-semibold uppercase tracking-[0.1em]">Node status</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            A node is stale when the bundle it last fetched differs from the bundle served now. Bindings change the bundle without changing the revision.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <span aria-live="polite" className="font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground">
            {counts.current} of {counts.total} nodes current
          </span>
          <Button type="button" variant="outline" disabled={loading} onClick={onRefresh}>
            {loading ? "Refreshing." : "Refresh status"}
          </Button>
        </div>
      </header>
      {error ? <p role="alert" className="font-mono text-xs text-destructive">{error}</p> : null}
      {status.items.length === 0 ? (
        <p className="border border-border p-3 font-mono text-[10px] text-muted-foreground">No node has pulled the bundle yet.</p>
      ) : (
        <div className="overflow-hidden border border-border bg-card">
          <Table>
            <caption className="sr-only">Node bundle status</caption>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Node</TableHead>
                <TableHead>Revision</TableHead>
                <TableHead>Bundle</TableHead>
                <TableHead>Fetched</TableHead>
                <TableHead>State</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {status.items.map((node) => (
                <TableRow key={node.node_name}>
                  <TableCell className="font-mono text-xs">{node.node_name}</TableCell>
                  <TableCell className="font-mono text-xs tabular-nums">r{node.revision}</TableCell>
                  <TableCell className="font-mono text-[10px] text-muted-foreground">{shortSha(node.bundle_sha256)}</TableCell>
                  <TableCell className="font-mono text-[10px] text-muted-foreground">{relativeTime(node.fetched_at)}</TableCell>
                  <TableCell>
                    {node.stale
                      ? <Badge variant="destructive">Stale</Badge>
                      : <Badge variant="secondary">Current</Badge>}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <p className="border-t border-border px-3 py-2 font-mono text-[10px] text-muted-foreground">
            Current bundle r{status.current_revision} {shortSha(status.current_bundle_sha256)}
          </p>
        </div>
      )}
    </section>
  );
}
