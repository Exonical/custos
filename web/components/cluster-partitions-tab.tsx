import type { PartitionList } from "@/lib/api/client";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatUtcDateTime } from "@/lib/format";

export function ClusterPartitionsTab({ partitions }: { partitions: PartitionList["items"] }) {
  return (
    <div className="overflow-hidden bg-card">
      <Table containerClassName="max-h-[60vh]">
        <caption className="sr-only">Partitions visible on this cluster</caption>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Name</TableHead>
            <TableHead>Attributes</TableHead>
            <TableHead>Synced</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {partitions.length === 0 ? (
            <TableRow><TableCell colSpan={3} className="py-10 text-center">
              <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No partitions</p>
              <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
            </TableCell></TableRow>
          ) : partitions.map((partition) => (
            <TableRow key={partition.name}>
              <TableCell className="font-mono text-xs">{partition.name}</TableCell>
              <TableCell><pre className="max-w-[34rem] truncate font-mono text-[10px]">{JSON.stringify(partition.attributes)}</pre></TableCell>
              <TableCell className="font-mono text-xs tabular-nums">{formatUtcDateTime(partition.synced_at)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
