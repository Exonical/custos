import type { SecretConnector } from "@/lib/api/client";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatUtcDateTime } from "@/lib/format";

export function SecretsConnectorsTable({ connectors }: { connectors: SecretConnector[] }) {
  return (
    <div className="overflow-hidden bg-card">
      <Table containerClassName="max-h-[60vh]">
        <caption className="sr-only">Secret connector metadata for this tenant</caption>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Name</TableHead>
            <TableHead>Provider</TableHead>
            <TableHead>Credential</TableHead>
            <TableHead>Address</TableHead>
            <TableHead>Namespace</TableHead>
            <TableHead>Created</TableHead>
            <TableHead>Updated</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {connectors.length === 0 ? (
            <TableRow><TableCell colSpan={7} className="py-10 text-center">
              <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No connectors</p>
              <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
            </TableCell></TableRow>
          ) : connectors.map((connector) => (
            <TableRow key={connector.id}>
              <TableCell>{connector.name}</TableCell>
              <TableCell className="font-mono text-xs">{connector.kind === "platform-openbao" ? "Platform default · OpenBao" : "Bring-your-own · OpenBao"}</TableCell>
              <TableCell className="font-mono text-xs">{connector.has_credential ? "Present" : "None"}</TableCell>
              <TableCell className="font-mono text-xs">{connector.config.address ?? "—"}</TableCell>
              <TableCell className="font-mono text-xs">{connector.config.namespace ?? "—"}</TableCell>
              <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(connector.created_at)}</TableCell>
              <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(connector.updated_at)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
