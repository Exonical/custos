"use client";

import { useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { HooksPanel } from "@/components/clusters/node-hooks/hooks-panel";
import { MountRowEditor } from "@/components/clusters/node-hooks/mount-row";
import { NodeStatusPanel } from "@/components/clusters/node-hooks/status-panel";
import { NodeTokensPanel } from "@/components/clusters/node-hooks/tokens-panel";
import { downloadNodeBundle, loadNodeStatus, saveNodeConfig } from "@/lib/nodehooks/client";
import {
  configFromDraft,
  draftFromConfig,
  emptyMount,
  groupIssues,
  isDirty,
  relativeTime,
  shortSha,
  validateDraft,
  type FieldIssue,
  type IsolationMode,
  type Mechanism,
  type MountRow,
  type NodeConfigDraft,
  type NodeConfigView,
  type NodeStatusList,
  type NodeToken,
} from "@/lib/nodehooks/config";

export type TenantOption = { id: string; slug: string; name: string };

type EditorProps = {
  clusterId: string;
  clusterName: string;
  initialView: NodeConfigView;
  initialTokens: NodeToken[];
  initialStatus: NodeStatusList;
  tenants: TenantOption[];
  csrfToken: string;
};

const modeText: Record<IsolationMode, string> = {
  namespace: "Tenant shares are mounted only inside each job's private mount namespace. Needs Slurm 25.11+ with namespace/linux.",
  tenant_exclusive: "Tenant shares are mounted on the host; Slurm keeps other tenants' jobs off the node while a tenant is active.",
  node_exclusive: "Tenant shares are mounted on the host and every job gets the whole node, so nothing else can run beside it.",
};

function FieldError({ message }: { message: string | undefined }) {
  return message ? <p role="alert" className="text-[10px] text-destructive">{message}</p> : null;
}

export function NodeHooksEditor(props: EditorProps) {
  const router = useRouter();
  const [resetKey, setResetKey] = useState(0);
  return (
    <NodeHooksEditorInner
      key={`${String(props.initialView.version)}-${String(resetKey)}`}
      {...props}
      onReload={() => {
        setResetKey((current) => current + 1);
        router.refresh();
      }}
    />
  );
}

function NodeHooksEditorInner({
  clusterId, clusterName, initialView, initialTokens, initialStatus, tenants, csrfToken, onReload,
}: EditorProps & { onReload: () => void }) {
  const initialDraft = useMemo(() => draftFromConfig(initialView.config), [initialView]);
  const [draft, setDraft] = useState<NodeConfigDraft>(initialDraft);
  const [savedDraft, setSavedDraft] = useState<NodeConfigDraft>(initialDraft);
  const [view, setView] = useState(initialView);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [serverIssues, setServerIssues] = useState<FieldIssue[]>([]);
  const [attempted, setAttempted] = useState(false);
  const [status, setStatus] = useState(initialStatus);
  const [statusLoading, setStatusLoading] = useState(false);
  const [statusError, setStatusError] = useState("");
  const [bundleError, setBundleError] = useState("");
  const [bundleBusy, setBundleBusy] = useState(false);
  const [pickedTenant, setPickedTenant] = useState("");

  const dirty = isDirty(draft, savedDraft);
  const clientIssues = useMemo(() => (attempted ? validateDraft(draft) : []), [attempted, draft]);
  const grouped = useMemo(() => groupIssues([...clientIssues, ...serverIssues], draft), [clientIssues, serverIssues, draft]);
  const tenantLabel = (id: string) => {
    const tenant = tenants.find((item) => item.id === id);
    return tenant ? `${tenant.name} (${tenant.slug})` : id;
  };

  function edit(update: (current: NodeConfigDraft) => NodeConfigDraft) {
    setDraft(update);
    setSaved(false);
    setError("");
    setConflict(false);
    setServerIssues([]);
  }

  async function refreshStatus() {
    setStatusLoading(true);
    setStatusError("");
    const next = await loadNodeStatus(clusterId);
    if (next) setStatus(next);
    else setStatusError("Could not load node status.");
    setStatusLoading(false);
  }

  async function save() {
    if (!dirty || saving) return;
    setSaved(false);
    setError("");
    setConflict(false);
    setServerIssues([]);
    setAttempted(true);
    if (validateDraft(draft).length > 0) {
      setError("Fix the highlighted fields before saving.");
      return;
    }
    setSaving(true);
    const outcome = await saveNodeConfig(clusterId, view.version, configFromDraft(draft), csrfToken);
    setSaving(false);
    switch (outcome.kind) {
      case "saved":
        setView(outcome.view);
        setSavedDraft(draft);
        setSaved(true);
        setAttempted(false);
        void refreshStatus();
        return;
      case "conflict":
        setConflict(true);
        setError("This configuration changed since you loaded it.");
        return;
      case "forbidden":
        setError("You need the platform-admin role (cluster.manage) to change node hooks.");
        return;
      case "invalid": {
        setServerIssues(outcome.issues);
        const mapped = groupIssues(outcome.issues, draft);
        setError(mapped.unmapped.length > 0
          ? `${outcome.message} ${mapped.unmapped.join("; ")}`
          : `${outcome.message} Fix the highlighted fields.`);
        return;
      }
      case "error":
        setError(outcome.message);
    }
  }

  async function downloadBundle() {
    setBundleBusy(true);
    setBundleError("");
    try {
      await downloadNodeBundle(clusterId, clusterName, view.revision);
    } catch {
      setBundleError("Download failed. Try again.");
    } finally {
      setBundleBusy(false);
    }
  }

  function addTenant(tenantId: string) {
    if (!tenantId || draft.tenantGroups.includes(tenantId)) return;
    edit((current) => ({
      ...current,
      tenantGroups: [...current.tenantGroups, tenantId],
      tenantMounts: [...current.tenantMounts, { ...emptyMount(false), tenant: tenantId }],
    }));
    setPickedTenant("");
  }

  function updateShared(id: string, update: Partial<MountRow>) {
    edit((current) => ({ ...current, shared: current.shared.map((row) => row.id === id ? { ...row, ...update } : row) }));
  }

  function updateTenantMount(id: string, update: Partial<MountRow>) {
    edit((current) => ({
      ...current,
      tenantMounts: current.tenantMounts.map((row) => row.id === id ? { ...row, ...update } : row),
    }));
  }

  function removeTenant(tenantId: string) {
    edit((current) => ({
      ...current,
      tenantGroups: current.tenantGroups.filter((id) => id !== tenantId),
      tenantMounts: current.tenantMounts.filter((row) => row.tenant !== tenantId),
    }));
  }

  const availableTenants = tenants.filter((tenant) => !draft.tenantGroups.includes(tenant.id));
  const mechanismRequirement = draft.mechanism === "mcs_label"
    ? "slurm.conf needs MCSPlugin=mcs/label and MCSParameters=ondemand,select. mcs/label does not enforce labels; add a job_submit filter so only Custos can set tenant labels."
    : "Each tenant needs its own Slurm service user, because --exclusive=user separates jobs by Slurm user only.";

  return (
    <section aria-label="Node hooks" className="space-y-6 p-4">
      <p role="note" className="border border-border bg-muted/40 p-3 text-xs text-muted-foreground">
        Node hooks install root-run Prolog/Epilog scripts that mount NFS shares for tenants. The server validates everything; checks here are for quick feedback.
      </p>
      {view.warnings.length > 0 ? (
        <div className="grid gap-2" aria-label="Warnings">
          {view.warnings.map((warning) => (
            <div key={warning.code} role="status" className="border border-status-degraded/40 bg-status-degraded/10 p-3 text-xs text-status-degraded">
              <p className="font-mono text-[10px] uppercase tracking-[0.08em]">{warning.code}</p>
              <p className="mt-1">{warning.message}</p>
            </div>
          ))}
        </div>
      ) : null}

      <section aria-labelledby="node-isolation-heading" className="space-y-4">
        <header>
          <h2 id="node-isolation-heading" className="font-mono text-xs font-semibold uppercase tracking-[0.1em]">Isolation</h2>
          <p className="mt-1 text-xs text-muted-foreground">Changing the mode requires drained nodes.</p>
        </header>
        <div className="grid gap-4 md:grid-cols-2">
          <div className="grid gap-1.5">
            <Label htmlFor="node-isolation-mode">Mode</Label>
            <Select value={draft.isolationMode} onValueChange={(value) => {
              if (value === "namespace" || value === "tenant_exclusive" || value === "node_exclusive") {
                edit((current) => ({ ...current, isolationMode: value }));
              }
            }}>
              <SelectTrigger id="node-isolation-mode" aria-label="Isolation mode" className="h-8">
                <SelectValue>{draft.isolationMode}</SelectValue>
              </SelectTrigger>
              <SelectContent align="start">
                <SelectItem value="namespace">namespace</SelectItem>
                <SelectItem value="tenant_exclusive">tenant_exclusive</SelectItem>
                <SelectItem value="node_exclusive">node_exclusive</SelectItem>
              </SelectContent>
            </Select>
            <FieldError message={grouped.general.isolation_mode} />
            <ul className="grid gap-1 text-[10px] text-muted-foreground">
              {(Object.keys(modeText) as IsolationMode[]).map((mode) => (
                <li key={mode} className={mode === draft.isolationMode ? "text-foreground" : undefined}>
                  <span className="font-mono">{mode}</span>: {modeText[mode]}
                </li>
              ))}
            </ul>
          </div>
          <div className="grid content-start gap-1.5">
            <Label htmlFor="node-mount-timeout">Mount timeout (seconds)</Label>
            <Input id="node-mount-timeout" value={draft.timeout} inputMode="numeric"
              aria-invalid={grouped.general.mount_timeout_seconds ? true : undefined}
              onChange={(event) => { edit((current) => ({ ...current, timeout: event.target.value })); }} />
            <p className="text-[10px] text-muted-foreground">5 to 300. Slurm&apos;s namespace script waits must be at least this plus 5.</p>
            <FieldError message={grouped.general.mount_timeout_seconds} />
          </div>
        </div>
        {draft.isolationMode === "tenant_exclusive" ? (
          <fieldset className="grid gap-2 border border-border p-3">
            <legend className="px-1 font-mono text-[10px] uppercase tracking-[0.08em]">Tenant-exclusive mechanism</legend>
            {(["mcs_label", "user"] as Mechanism[]).map((value) => (
              <label key={value} className="flex items-center gap-2 text-xs">
                <input
                  type="radio"
                  name="node-mechanism"
                  value={value}
                  checked={draft.mechanism === value}
                  onChange={() => { edit((current) => ({ ...current, mechanism: value })); }}
                />
                <span className="font-mono">{value}</span>
                <span className="text-muted-foreground">{value === "mcs_label" ? "(default) submits --exclusive=mcs with the tenant as label" : "submits --exclusive=user"}</span>
              </label>
            ))}
            <p className="text-[10px] text-muted-foreground">{mechanismRequirement}</p>
            <FieldError message={grouped.general.tenant_exclusive_mechanism} />
          </fieldset>
        ) : null}
      </section>

      <section aria-labelledby="node-shared-heading" className="space-y-3 border-t border-border pt-5">
        <header className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 id="node-shared-heading" className="font-mono text-xs font-semibold uppercase tracking-[0.1em]">Shared mounts</h2>
            <p className="mt-1 text-xs text-muted-foreground">Mounted for every job, including non-Custos accounts, if not already mounted. Never unmounted.</p>
          </div>
          <Button type="button" variant="outline" onClick={() => {
            edit((current) => ({ ...current, shared: [...current.shared, emptyMount(true)] }));
          }}>Add shared mount</Button>
        </header>
        {draft.shared.length === 0 ? (
          <p className="border border-border p-3 font-mono text-[10px] text-muted-foreground">No shared mounts.</p>
        ) : (
          <div className="grid gap-3">
            {draft.shared.map((row, index) => (
              <MountRowEditor
                key={row.id}
                row={row}
                label={`shared mount ${row.name || String(index + 1)}`}
                errors={grouped.rows[row.id] ?? {}}
                editable
                hardened={false}
                onChange={(update) => { updateShared(row.id, update); }}
                onRemove={() => { edit((current) => ({ ...current, shared: current.shared.filter((item) => item.id !== row.id) })); }}
              />
            ))}
          </div>
        )}
      </section>

      <section aria-labelledby="node-tenant-heading" className="space-y-3 border-t border-border pt-5">
        <header className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 id="node-tenant-heading" className="font-mono text-xs font-semibold uppercase tracking-[0.1em]">Tenant mounts</h2>
            <p className="mt-1 text-xs text-muted-foreground">
              Resolved through the job&apos;s Slurm account. The same target may be used by different tenants.
            </p>
          </div>
          <div className="flex items-end gap-2">
            <div className="grid gap-1.5">
              <Label htmlFor="node-add-tenant">Add tenant</Label>
              <Select value={pickedTenant} disabled={availableTenants.length === 0} onValueChange={(value) => { addTenant(value ?? ""); }}>
                <SelectTrigger id="node-add-tenant" aria-label="Add tenant" className="h-8 w-56">
                  <SelectValue placeholder={availableTenants.length === 0 ? "No more tenants" : "Choose a tenant"} />
                </SelectTrigger>
                <SelectContent align="end">
                  {availableTenants.map((tenant) => (
                    <SelectItem key={tenant.id} value={tenant.id}>{tenant.name} ({tenant.slug})</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
        </header>
        {draft.tenantGroups.length === 0 ? (
          <p className="border border-border p-3 font-mono text-[10px] text-muted-foreground">No tenant mounts.</p>
        ) : draft.tenantGroups.map((tenantId) => {
          const rows = draft.tenantMounts.filter((row) => row.tenant === tenantId);
          return (
            <div key={tenantId} className="space-y-3 border border-border p-3" role="group" aria-label={`Tenant ${tenantLabel(tenantId)}`}>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <h3 className="font-mono text-[10px] uppercase tracking-[0.1em]">{tenantLabel(tenantId)}</h3>
                <div className="flex gap-2">
                  <Button type="button" variant="outline" onClick={() => {
                    edit((current) => ({ ...current, tenantMounts: [...current.tenantMounts, { ...emptyMount(false), tenant: tenantId }] }));
                  }}>Add mount</Button>
                  <Button type="button" variant="outline" aria-label={`Remove tenant ${tenantLabel(tenantId)}`} onClick={() => { removeTenant(tenantId); }}>Remove tenant</Button>
                </div>
              </div>
              {rows.length === 0 ? (
                <p className="font-mono text-[10px] text-muted-foreground">No mounts for this tenant yet.</p>
              ) : rows.map((row, index) => (
                <MountRowEditor
                  key={row.id}
                  row={row}
                  label={`tenant mount ${row.name || String(index + 1)}`}
                  errors={grouped.rows[row.id] ?? {}}
                  editable
                  hardened
                  onChange={(update) => { updateTenantMount(row.id, update); }}
                  onRemove={() => { edit((current) => ({ ...current, tenantMounts: current.tenantMounts.filter((item) => item.id !== row.id) })); }}
                />
              ))}
            </div>
          );
        })}
      </section>

      <HooksPanel
        hooks={draft.hooks}
        errors={grouped.rows}
        editable
        onChange={(hooks) => { edit((current) => ({ ...current, hooks })); }}
      />

      <section aria-labelledby="node-bundle-heading" className="space-y-3 border-t border-border pt-5">
        <header className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 id="node-bundle-heading" className="font-mono text-xs font-semibold uppercase tracking-[0.1em]">Bundle</h2>
            <p className="mt-1 text-xs text-muted-foreground">The saved configuration rendered as an installable archive. Unsaved edits are not included.</p>
          </div>
          <div className="flex items-center gap-2">
            <InstallHelp />
            <Button type="button" variant="outline" disabled={bundleBusy} onClick={() => { void downloadBundle(); }}>
              {bundleBusy ? "Downloading." : "Download bundle"}
            </Button>
          </div>
        </header>
        {bundleError ? <p role="alert" className="font-mono text-xs text-destructive">{bundleError}</p> : null}
      </section>

      <NodeTokensPanel clusterId={clusterId} initial={initialTokens} csrfToken={csrfToken} editable />
      <NodeStatusPanel status={status} loading={statusLoading} error={statusError} onRefresh={() => { void refreshStatus(); }} />

      {error ? (
        <div role="alert" className="border border-destructive/40 bg-destructive/10 p-3 font-mono text-xs text-destructive">
          <p>{error}</p>
          {conflict ? (
            <AlertDialog>
              <AlertDialogTrigger render={<Button type="button" variant="outline" className="mt-2" />}>Reload</AlertDialogTrigger>
              <AlertDialogContent className="w-[min(92vw,28rem)] border-primary/25 bg-popover shadow-none">
                <AlertDialogHeader className="text-left">
                  <AlertDialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Discard your edits?</AlertDialogTitle>
                  <AlertDialogDescription className="mt-2 text-sm text-muted-foreground">
                    Reloading replaces the form with the current server configuration. Your unsaved changes are lost.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter className="border-border bg-muted/30">
                  <AlertDialogCancel>Keep editing</AlertDialogCancel>
                  <AlertDialogAction type="button" variant="destructive" onClick={onReload}>Discard and reload</AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          ) : null}
        </div>
      ) : null}
      <footer className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-4">
        <dl aria-live="polite" className="flex flex-wrap gap-x-5 gap-y-1 font-mono text-[10px] uppercase text-muted-foreground">
          <div className="flex gap-1.5"><dt>Revision</dt><dd data-testid="node-revision" className="text-foreground">{view.revision}</dd></div>
          <div className="flex gap-1.5"><dt>Version</dt><dd className="text-foreground">{view.version}</dd></div>
          <div className="flex gap-1.5"><dt>Content</dt><dd className="text-foreground">{shortSha(view.content_sha256)}</dd></div>
          <div className="flex gap-1.5"><dt>Updated</dt><dd className="text-foreground">{relativeTime(view.updated_at)}{view.updated_by ? ` by ${view.updated_by.slice(0, 8)}` : ""}</dd></div>
          {dirty ? <div className="text-status-degraded">Unsaved changes</div> : null}
          {saved ? <div className="text-status-completed">Saved</div> : null}
        </dl>
        <Button type="button" disabled={saving || !dirty} onClick={() => { void save(); }}>
          {saving ? "Saving." : "Save"}
        </Button>
      </footer>
    </section>
  );
}

function InstallHelp() {
  return (
    <Dialog>
      <DialogTrigger render={<Button type="button" variant="ghost" />}>Install help</DialogTrigger>
      <DialogContent className="w-[min(94vw,34rem)] sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Installing the bundle</DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">
            The archive contains a README.md with the exact steps for this revision.
          </DialogDescription>
        </DialogHeader>
        <ol className="list-decimal space-y-1.5 pl-5 text-xs text-muted-foreground">
          <li>Unpack as root: <code className="font-mono">tar -xzf custos-node-&lt;cluster&gt;-r&lt;rev&gt;.tar.gz -C /</code></li>
          <li>Set <code className="font-mono">Prolog=</code>, <code className="font-mono">Epilog=</code> and <code className="font-mono">PrologFlags=Alloc,Contain</code> in slurm.conf, then restart slurmd.</li>
          <li>Optionally enable <code className="font-mono">custos-node-sync.timer</code> with a node token so nodes pull updates.</li>
        </ol>
        <p className="text-xs text-muted-foreground">Open <code className="font-mono">README.md</code> inside the archive for the namespace.yaml and MCS snippets that match your mode.</p>
      </DialogContent>
    </Dialog>
  );
}
