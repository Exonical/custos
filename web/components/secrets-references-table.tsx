import type { SecretConnector, SecretReference } from "@/lib/api/client";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatUtcDateTime } from "@/lib/format";

export function SecretsReferencesTable({ references, connectors }: {
  references: SecretReference[];
  connectors: SecretConnector[];
}) {
  const connectorById = new Map(connectors.map((connector) => [connector.id, connector.name]));
  return (
    <div className="overflow-hidden bg-card">
      <Table containerClassName="max-h-[60vh]">
        <caption className="sr-only">Secret reference metadata for this tenant</caption>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Name</TableHead>
            <TableHead>Connector</TableHead>
            <TableHead>Path / key</TableHead>
            <TableHead>Kind</TableHead>
            <TableHead>Updated</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {references.length === 0 ? (
            <TableRow><TableCell colSpan={5} className="py-10 text-center">
              <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No references</p>
              <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
            </TableCell></TableRow>
          ) : references.map((reference) => (
            <TableRow key={reference.id}>
              <TableCell>{reference.name}</TableCell>
              <TableCell className="font-mono text-xs">{connectorById.get(reference.connector_id) ?? reference.connector_id}</TableCell>
              <TableCell className="font-mono text-xs">{`${reference.path} · ${reference.key}`}</TableCell>
              <TableCell className="font-mono text-xs">{reference.kind}</TableCell>
              <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(reference.updated_at)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
