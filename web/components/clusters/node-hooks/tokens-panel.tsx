"use client";

import { useState } from "react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { CreateTokenDialog } from "@/components/clusters/node-hooks/create-token-dialog";
import { createNodeToken, loadNodeTokens, revokeNodeToken } from "@/lib/nodehooks/client";
import { relativeTime, type NodeToken } from "@/lib/nodehooks/config";

export function NodeTokensPanel({ clusterId, initial, csrfToken, editable }: {
  clusterId: string;
  initial: NodeToken[];
  csrfToken: string;
  editable: boolean;
}) {
  const [tokens, setTokens] = useState(initial);
  const [error, setError] = useState("");

  async function refresh() {
    const next = await loadNodeTokens(clusterId);
    if (next) setTokens(next);
    else setError("Could not refresh the token list.");
  }

  async function revoke(id: string) {
    setError("");
    const problem = await revokeNodeToken(clusterId, id, csrfToken);
    if (problem) setError(problem);
    await refresh();
  }

  return (
    <section aria-labelledby="node-tokens-heading" className="space-y-3 border-t border-border pt-5">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 id="node-tokens-heading" className="font-mono text-xs font-semibold uppercase tracking-[0.1em]">Node tokens</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Used by custos-node-sync to pull the bundle. A token is read-only and valid for this cluster only.
          </p>
        </div>
        {editable ? (
          <CreateTokenDialog
            onCreate={(name) => createNodeToken(clusterId, name, csrfToken)}
            onCreated={() => { void refresh(); }}
          />
        ) : null}
      </header>
      {error ? <p role="alert" className="font-mono text-xs text-destructive">{error}</p> : null}
      {tokens.length === 0 ? (
        <p className="border border-border p-3 font-mono text-[10px] text-muted-foreground">No node tokens.</p>
      ) : (
        <div className="overflow-hidden border border-border bg-card">
          <Table>
            <caption className="sr-only">Node tokens</caption>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Name</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead>State</TableHead>
                <TableHead className="w-24"><span className="sr-only">Actions</span></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tokens.map((token) => (
                <TableRow key={token.id}>
                  <TableCell className="font-mono text-xs">{token.name}</TableCell>
                  <TableCell className="font-mono text-[10px] text-muted-foreground">{relativeTime(token.created_at)}</TableCell>
                  <TableCell className="font-mono text-[10px] text-muted-foreground">{relativeTime(token.last_used_at)}</TableCell>
                  <TableCell>
                    {token.revoked_at
                      ? <Badge variant="outline">Revoked {relativeTime(token.revoked_at)}</Badge>
                      : <Badge variant="secondary">Active</Badge>}
                  </TableCell>
                  <TableCell>
                    {editable && !token.revoked_at ? (
                      <AlertDialog>
                        <AlertDialogTrigger render={<Button type="button" variant="outline" size="sm" aria-label={`Revoke ${token.name}`} />}>Revoke</AlertDialogTrigger>
                        <AlertDialogContent className="w-[min(92vw,28rem)] border-primary/25 bg-popover shadow-none">
                          <AlertDialogHeader className="text-left">
                            <AlertDialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Revoke {token.name}?</AlertDialogTitle>
                            <AlertDialogDescription className="mt-2 text-sm text-muted-foreground">
                              Nodes using this token stop receiving bundle updates (HTTP 401) until they are given a new token.
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter className="border-border bg-muted/30">
                            <AlertDialogCancel>Keep token</AlertDialogCancel>
                            <AlertDialogAction type="button" variant="destructive" onClick={() => { void revoke(token.id); }}>Revoke token</AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                    ) : null}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </section>
  );
}
