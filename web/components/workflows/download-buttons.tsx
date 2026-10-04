"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { detailText } from "@/lib/api/error-details";
import { BffDownloadError, downloadFromBff } from "@/lib/download";

function downloadErrorMessage(error: unknown): string {
  if (!(error instanceof BffDownloadError)) return "Download failed. Try again.";
  const details = Array.isArray(error.details) ? detailText(error.details) : "";
  return details || error.serverMessage || error.code;
}

export function DownloadWorkflowYamlButton({ tenant, workflow, version, workflowName, versionNumber }: {
  tenant: string;
  workflow: string;
  version: string;
  workflowName: string;
  versionNumber: number;
}) {
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState("");

  async function download() {
    setDownloading(true);
    setError("");
    try {
      await downloadFromBff(
        `tenants/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflow)}/versions/${encodeURIComponent(version)}`,
        {
          accept: "application/yaml",
          fallbackFilename: `${workflowName}-v${String(versionNumber)}.yaml`,
        },
      );
    } catch (reason) {
      setError(downloadErrorMessage(reason));
    } finally {
      setDownloading(false);
    }
  }

  return (
    <div className="grid justify-items-end gap-1">
      <Button type="button" variant="outline" disabled={downloading} onClick={() => { void download(); }}>
        {downloading ? "Downloading…" : "Download YAML"}
      </Button>
      {error ? <p role="alert" className="max-w-72 text-right font-mono text-[10px] text-destructive">{error}</p> : null}
    </div>
  );
}

export function DownloadTaskSbatchButton({ tenant, workflow, version, taskName }: {
  tenant: string;
  workflow: string;
  version: string;
  taskName: string;
}) {
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState("");

  async function download() {
    setDownloading(true);
    setError("");
    try {
      await downloadFromBff(
        `tenants/${encodeURIComponent(tenant)}/workflows/${encodeURIComponent(workflow)}/versions/${encodeURIComponent(version)}/tasks/${encodeURIComponent(taskName)}/sbatch`,
        { accept: "text/x-shellscript", fallbackFilename: `${taskName}.sbatch` },
      );
    } catch (reason) {
      setError(downloadErrorMessage(reason));
    } finally {
      setDownloading(false);
    }
  }

  return (
    <div className="mt-3 grid gap-1">
      <Button type="button" variant="outline" disabled={downloading} onClick={() => { void download(); }}>
        {downloading ? "Downloading…" : "Download sbatch"}
      </Button>
      {error ? <p role="alert" className="border border-destructive/40 bg-destructive/10 p-2 font-mono text-[10px] text-destructive">{error}</p> : null}
    </div>
  );
}
