"use client";

import "@/lib/workflow/monaco";
import Editor from "@monaco-editor/react";

export function WorkflowYamlEditor({ value }: { value: string }) {
  return (
    <div aria-label="Workflow specification YAML" className="min-w-0 overflow-hidden border border-border bg-card">
      <Editor
        height="34rem"
        language="yaml"
        theme="custos-dark"
        value={value}
        options={{
          automaticLayout: true,
          domReadOnly: true,
          fontFamily: "IBM Plex Mono, monospace",
          fontSize: 11,
          lineNumbers: "on",
          minimap: { enabled: false },
          readOnly: true,
          scrollBeyondLastLine: false,
          wordWrap: "on",
        }}
      />
    </div>
  );
}
