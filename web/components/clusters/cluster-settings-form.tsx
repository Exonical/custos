"use client";

import { useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { detailText } from "@/lib/api/error-details";
import { sendJson } from "@/lib/api/bff-fetch";
import {
  clusterSettingsDraft,
  validateClusterSettings,
  type ClusterSettingsDraft,
  type ContainerRuntime,
  type SoftwareModule,
} from "@/lib/clusters/settings";

type ApiErrorBody = { error?: { code?: unknown; details?: unknown; message?: unknown } };

function errorDetails(payload: unknown): string {
  const error = payload && typeof payload === "object" ? (payload as ApiErrorBody).error : undefined;
  const details = Array.isArray(error?.details) ? detailText(error.details) : "";
  return details || (typeof error?.code === "string" ? error.code : "Request rejected");
}

function fieldError(errors: Record<string, string>, path: string) {
  return errors[path] ? <p role="alert" className="text-[10px] text-destructive">{errors[path]}</p> : null;
}

type ClusterSettingsFormProps = {
  clusterId: string;
  version: number;
  containerRuntime: ContainerRuntime | null;
  softwareModules: SoftwareModule[] | undefined;
  csrfToken: string;
  editable: boolean;
};

export function ClusterSettingsForm(props: ClusterSettingsFormProps) {
  const router = useRouter();
  const [saved, setSaved] = useState(false);
  const [resetKey, setResetKey] = useState(0);
  return (
    <ClusterSettingsEditor
      key={`${String(props.version)}-${String(resetKey)}`}
      {...props}
      saved={saved}
      onSavedChange={setSaved}
      onRefresh={() => { router.refresh(); }}
      onReload={() => {
        setSaved(false);
        setResetKey((current) => current + 1);
        router.refresh();
      }}
    />
  );
}

function ClusterSettingsEditor({
  clusterId,
  version,
  containerRuntime,
  softwareModules,
  csrfToken,
  editable,
  saved,
  onSavedChange,
  onRefresh,
  onReload,
}: ClusterSettingsFormProps & {
  saved: boolean;
  onSavedChange: (saved: boolean) => void;
  onRefresh: () => void;
  onReload: () => void;
}) {
  const initialDraft = useMemo(() => clusterSettingsDraft(containerRuntime, softwareModules), [containerRuntime, softwareModules]);
  const [draft, setDraft] = useState<ClusterSettingsDraft>(initialDraft);
  const [savedDraft, setSavedDraft] = useState<ClusterSettingsDraft>(initialDraft);
  const [currentVersion, setCurrentVersion] = useState(version);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const validation = useMemo(() => validateClusterSettings(draft), [draft]);
  const pristine = JSON.stringify(draft) === JSON.stringify(savedDraft);

  function updateDraft(update: Partial<ClusterSettingsDraft>) {
    setDraft((current) => ({ ...current, ...update }));
    onSavedChange(false);
    setError("");
    setConflict(false);
  }

  function updateModule(id: string, update: Partial<ClusterSettingsDraft["softwareModules"][number]>) {
    updateDraft({
      softwareModules: draft.softwareModules.map((module) => module.id === id ? { ...module, ...update } : module),
    });
  }

  async function save() {
    if (!editable || pristine) return;
    onSavedChange(false);
    setError("");
    setConflict(false);
    if (!validation.valid) {
      setError("Fix the highlighted settings before saving.");
      return;
    }
    setSaving(true);
    try {
      const response = await sendJson("PATCH", `/api/bff/clusters/${encodeURIComponent(clusterId)}`, {
        version: currentVersion,
        container_runtime: validation.payload.container_runtime,
        software_modules: validation.payload.software_modules,
      }, csrfToken);
      if (response.status === 409) {
        setConflict(true);
        setError("This cluster changed since you loaded it.");
        return;
      }
      if (response.status === 403) {
        setError("You need cluster.manage permission to change these settings.");
        return;
      }
      if (response.status === 422) {
        setError(errorDetails(response.payload));
        return;
      }
      if (response.status !== 200) {
        setError(errorDetails(response.payload));
        return;
      }
      const result = response.payload && typeof response.payload === "object"
        ? response.payload as { version?: unknown }
        : {};
      const nextVersion = typeof result.version === "number" ? result.version : currentVersion + 1;
      setCurrentVersion(nextVersion);
      setSavedDraft(draft);
      onSavedChange(true);
      onRefresh();
    } catch {
      setError("The request could not be sent. Check your connection and try again.");
    } finally {
      setSaving(false);
    }
  }

  return (
    <section aria-label="Cluster settings" className="space-y-6 p-4">
      {!editable ? (
        <p className="border border-border bg-muted/40 p-3 text-xs text-muted-foreground">
          These settings are read-only. Changing them requires the platform-admin role with cluster.manage permission.
        </p>
      ) : null}
      <section aria-labelledby="container-runtime-heading" className="space-y-4">
        <header>
          <h2 id="container-runtime-heading" className="font-mono text-xs font-semibold uppercase tracking-[0.1em]">Container runtime</h2>
          <p className="mt-1 text-xs text-muted-foreground">An empty image-prefix list allows any otherwise-valid image URI.</p>
        </header>
        <label className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.08em] text-foreground">
          <input
            type="checkbox"
            checked={draft.containerEnabled}
            disabled={!editable}
            onChange={(event) => { updateDraft({ containerEnabled: event.target.checked }); }}
          />
          Enable container runtime
        </label>
        {draft.containerEnabled ? (
          <div className="grid gap-4 md:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="cluster-runtime-type">Runtime type</Label>
              <Select value={draft.runtimeType} disabled={!editable} onValueChange={(value) => {
                if (value === "apptainer" || value === "pyxis") updateDraft({ runtimeType: value });
              }}>
                <SelectTrigger id="cluster-runtime-type" aria-label="Runtime type" className="h-8">
                  <SelectValue>{draft.runtimeType}</SelectValue>
                </SelectTrigger>
                <SelectContent align="start">
                  <SelectItem value="apptainer">Apptainer</SelectItem>
                  <SelectItem value="pyxis">Pyxis</SelectItem>
                </SelectContent>
              </Select>
              {fieldError(validation.errors, "runtimeType")}
            </div>
            {draft.runtimeType === "apptainer" ? (
              <div className="grid gap-1.5">
                <Label htmlFor="cluster-runtime-binary">Apptainer binary</Label>
                <Input
                  id="cluster-runtime-binary"
                  value={draft.binary}
                  placeholder="apptainer"
                  disabled={!editable}
                  onChange={(event) => { updateDraft({ binary: event.target.value }); }}
                />
                {fieldError(validation.errors, "binary")}
              </div>
            ) : null}
            <div className="grid gap-1.5 md:col-span-2">
              <Label htmlFor="cluster-image-prefixes">Allowed image prefixes</Label>
              <textarea
                id="cluster-image-prefixes"
                value={draft.allowedImagePrefixes}
                placeholder={"oras://registry.example.org/team/\ndocker://registry.example.org/team/"}
                disabled={!editable}
                onChange={(event) => { updateDraft({ allowedImagePrefixes: event.target.value }); }}
                className="min-h-24 w-full resize-y border border-input bg-card px-3 py-2 font-mono text-xs outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50"
              />
              <p className="text-[10px] text-muted-foreground">One prefix per line. Leave empty to allow any valid image URI.</p>
              {fieldError(validation.errors, "allowedImagePrefixes")}
            </div>
            <div className="grid gap-3 md:col-span-2 sm:grid-cols-2">
              <label className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.08em] text-foreground">
                <input
                  type="checkbox"
                  checked={draft.requireDigest}
                  disabled={!editable}
                  onChange={(event) => { updateDraft({ requireDigest: event.target.checked }); }}
                />
                Require sha256 image digest
              </label>
              <label className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.08em] text-foreground">
                <input
                  type="checkbox"
                  checked={draft.slurmInContainer}
                  disabled={!editable}
                  onChange={(event) => { updateDraft({ slurmInContainer: event.target.checked }); }}
                />
                Slurm client available in image
              </label>
              <p className="text-[10px] text-muted-foreground sm:col-span-2">Enable this for generic multinode tasks using an image; the remote launcher runs srun from inside the container.</p>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="cluster-mpi-plugin">MPI plugin</Label>
              <Input
                id="cluster-mpi-plugin"
                value={draft.mpiPlugin}
                placeholder="pmix"
                disabled={!editable}
                onChange={(event) => { updateDraft({ mpiPlugin: event.target.value }); }}
              />
              {fieldError(validation.errors, "mpiPlugin")}
            </div>
          </div>
        ) : <p className="text-xs text-muted-foreground">No container runtime is configured.</p>}
      </section>

      <section aria-labelledby="software-modules-heading" className="space-y-4 border-t border-border pt-5">
        <header className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 id="software-modules-heading" className="font-mono text-xs font-semibold uppercase tracking-[0.1em]">Software modules</h2>
            <p className="mt-1 text-xs text-muted-foreground">Map workflow software names and optional versions to site modules. An empty list is allowed.</p>
          </div>
          {editable ? (
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                updateDraft({ softwareModules: [...draft.softwareModules, { id: crypto.randomUUID(), name: "", version: "", modules: "" }] });
              }}
            >
              Add module
            </Button>
          ) : null}
        </header>
        {draft.softwareModules.length === 0 ? (
          <p className="border border-border p-3 font-mono text-[10px] text-muted-foreground">No software module mappings.</p>
        ) : (
          <div className="grid gap-3">
            {draft.softwareModules.map((module, index) => {
              const rowPath = `softwareModules.${module.id || String(index)}`;
              return (
                <div key={module.id} className="grid gap-3 border border-border bg-card p-3 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,2fr)_auto]">
                  <div className="grid gap-1.5">
                    <Label htmlFor={`${module.id}-name`}>Name</Label>
                    <Input id={`${module.id}-name`} value={module.name} disabled={!editable} onChange={(event) => { updateModule(module.id, { name: event.target.value }); }} />
                    {fieldError(validation.errors, `${rowPath}.name`)}
                  </div>
                  <div className="grid gap-1.5">
                    <Label htmlFor={`${module.id}-version`}>Version</Label>
                    <Input id={`${module.id}-version`} value={module.version} placeholder="any" disabled={!editable} onChange={(event) => { updateModule(module.id, { version: event.target.value }); }} />
                    {fieldError(validation.errors, `${rowPath}.version`)}
                  </div>
                  <div className="grid gap-1.5">
                    <Label htmlFor={`${module.id}-modules`}>Modules</Label>
                    <Input id={`${module.id}-modules`} value={module.modules} placeholder="gcc/13 openmpi/5" disabled={!editable} onChange={(event) => { updateModule(module.id, { modules: event.target.value }); }} />
                    <p className="text-[10px] text-muted-foreground">Separate 1–16 module names with spaces or commas.</p>
                    {fieldError(validation.errors, `${rowPath}.modules`)}
                  </div>
                  {editable ? (
                    <Button
                      type="button"
                      variant="outline"
                      aria-label={`Remove ${module.name || `module row ${String(index + 1)}`}`}
                      onClick={() => { updateDraft({ softwareModules: draft.softwareModules.filter((item) => item.id !== module.id) }); }}
                    >
                      Remove
                    </Button>
                  ) : null}
                </div>
              );
            })}
          </div>
        )}
      </section>

      {error ? (
        <div role="alert" className="border border-destructive/40 bg-destructive/10 p-3 font-mono text-xs text-destructive">
          <p>{error}</p>
          {conflict ? <Button type="button" variant="outline" className="mt-2" onClick={onReload}>Reload</Button> : null}
        </div>
      ) : null}
      {editable ? (
        <footer className="flex items-center justify-between border-t border-border pt-4">
          <div aria-live="polite" className="font-mono text-[10px] uppercase text-muted-foreground">
            {saved ? "Saved" : `Version ${String(currentVersion)}`}
          </div>
          <Button type="button" disabled={saving || pristine} onClick={() => { void save(); }}>
            {saving ? "Saving…" : "Save"}
          </Button>
        </footer>
      ) : null}
    </section>
  );
}
