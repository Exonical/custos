"use client";

import { useCallback, useEffect, useRef } from "react";
import "@/lib/workflow/monaco";
import Editor from "@monaco-editor/react";
import { editor as monacoEditor, MarkerSeverity } from "monaco-editor";
import type { editor } from "monaco-editor";

export type WorkflowYamlMarker = Omit<editor.IMarkerData, "severity"> & {
  severity: "error" | "warning";
};

export function WorkflowYamlEditor({
  value,
  readOnly = true,
  onChange,
  markers = [],
  revealLine,
}: {
  value: string;
  readOnly?: boolean;
  onChange?: (value: string) => void;
  markers?: WorkflowYamlMarker[];
  revealLine?: number;
}) {
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null);

  const updateMarkers = useCallback((instance: editor.IStandaloneCodeEditor) => {
    const model = instance.getModel();
    if (!model) return;
    monacoEditor.setModelMarkers(model, "custos-validation", markers.map((marker) => ({
      ...marker,
      severity: marker.severity === "warning" ? MarkerSeverity.Warning : MarkerSeverity.Error,
    })));
  }, [markers]);

  useEffect(() => {
    if (editorRef.current) updateMarkers(editorRef.current);
  }, [updateMarkers]);

  useEffect(() => {
    if (!revealLine || !editorRef.current) return;
    editorRef.current.revealLineInCenter(revealLine);
    editorRef.current.setPosition({ lineNumber: revealLine, column: 1 });
    editorRef.current.focus();
  }, [revealLine]);

  return (
    <div aria-label="Workflow specification YAML" className="min-w-0 overflow-hidden border border-border bg-card">
      <Editor
        height="34rem"
        language="yaml"
        theme="custos-dark"
        value={value}
        onChange={(nextValue) => { onChange?.(nextValue ?? ""); }}
        onMount={(instance) => {
          editorRef.current = instance;
          updateMarkers(instance);
        }}
        options={{
          automaticLayout: true,
          domReadOnly: readOnly,
          fontFamily: "IBM Plex Mono, monospace",
          fontSize: 11,
          lineNumbers: "on",
          minimap: { enabled: false },
          readOnly,
          scrollBeyondLastLine: false,
          wordWrap: "on",
        }}
      />
    </div>
  );
}
