"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { CreateTokenResult } from "@/lib/nodehooks/client";
import { NAME_PATTERN } from "@/lib/nodehooks/config";

export const AGENT_CONF_SNIPPET = [
  "CUSTOS_URL=https://custos.example.org",
  "CUSTOS_TOKEN_FILE=/etc/custos/node-token",
  "CUSTOS_CA_FILE=/etc/custos/ca.pem",
].join("\n");

// The secret lives only in this component's state and is dropped as soon as
// the dialog closes; it is never logged or handed to the parent.
export function CreateTokenDialog({ onCreate, onCreated, disabled = false }: {
  onCreate: (name: string) => Promise<CreateTokenResult>;
  onCreated: () => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [secret, setSecret] = useState<string | null>(null);
  const [copied, setCopied] = useState("");
  const nameValid = NAME_PATTERN.test(name);

  function changeOpen(next: boolean) {
    setOpen(next);
    if (!next) {
      setSecret(null);
      setName("");
      setError("");
      setCopied("");
      setPending(false);
    }
  }

  async function create() {
    if (!nameValid || pending) return;
    setPending(true);
    setError("");
    const result = await onCreate(name);
    setPending(false);
    if (!result.ok) {
      setError(result.message);
      return;
    }
    setSecret(result.created.token);
    onCreated();
  }

  async function copy(value: string, label: string) {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(label);
    } catch {
      setCopied("");
      setError("Copy failed; select the text and copy it manually.");
    }
  }

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogTrigger render={<Button type="button" variant="outline" disabled={disabled} />}>Create token</DialogTrigger>
      <DialogContent className="w-[min(94vw,36rem)] sm:max-w-xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">
            {secret === null ? "Create node token" : "Node token created"}
          </DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">
            {secret === null
              ? "A node token lets custos-node-sync download this cluster's bundle. It cannot call any other API."
              : "Store it on the node now. It will not be shown again."}
          </DialogDescription>
        </DialogHeader>
        {secret === null ? (
          <form
            className="grid gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              void create();
            }}
          >
            <div className="grid gap-1.5">
              <Label htmlFor="node-token-name">Token name</Label>
              <Input
                id="node-token-name"
                value={name}
                placeholder="rack-1"
                autoComplete="off"
                aria-invalid={name !== "" && !nameValid ? true : undefined}
                onChange={(event) => { setName(event.target.value); }}
              />
              {name !== "" && !nameValid ? (
                <p role="alert" className="text-[10px] text-destructive">Use lowercase letters, digits and dashes, starting with a letter or digit (up to 63 characters).</p>
              ) : null}
            </div>
            {error ? <p role="alert" className="font-mono text-xs text-destructive">{error}</p> : null}
            <DialogFooter>
              <Button type="submit" disabled={!nameValid || pending}>{pending ? "Creating." : "Create"}</Button>
            </DialogFooter>
          </form>
        ) : (
          <div className="grid gap-3">
            <p role="alert" className="border border-status-degraded/40 bg-status-degraded/10 p-2 font-mono text-[10px] uppercase tracking-[0.08em] text-status-degraded">
              This token will not be shown again.
            </p>
            <div className="grid gap-1.5">
              <Label htmlFor="node-token-value">Token</Label>
              <div className="flex gap-2">
                <Input id="node-token-value" readOnly value={secret} className="font-mono text-xs" />
                <Button type="button" variant="outline" onClick={() => { void copy(secret, "token"); }}>Copy</Button>
              </div>
              {copied === "token" ? <p aria-live="polite" className="text-[10px] text-muted-foreground">Copied.</p> : null}
            </div>
            <div className="grid gap-1.5">
              <p className="text-xs text-muted-foreground">
                Write the token to <code className="font-mono">/etc/custos/node-token</code> (root, mode 0600), then create
                <code className="font-mono"> /etc/custos/agent.conf</code>:
              </p>
              <pre className="overflow-x-auto border border-border bg-muted/40 p-2 font-mono text-[11px]">{AGENT_CONF_SNIPPET}</pre>
              <div>
                <Button type="button" variant="outline" onClick={() => { void copy(AGENT_CONF_SNIPPET, "conf"); }}>Copy agent.conf</Button>
                {copied === "conf" ? <span aria-live="polite" className="ml-2 text-[10px] text-muted-foreground">Copied.</span> : null}
              </div>
            </div>
            {error ? <p role="alert" className="font-mono text-xs text-destructive">{error}</p> : null}
            <DialogFooter>
              <Button type="button" onClick={() => { changeOpen(false); }}>Done</Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
