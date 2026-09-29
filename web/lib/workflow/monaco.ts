import * as monaco from "monaco-editor";
import { loader } from "@monaco-editor/react";

const browserGlobal = globalThis as typeof globalThis & {
  MonacoEnvironment?: { getWorker: (workerId: string, label: string) => Worker };
};

browserGlobal.MonacoEnvironment = {
  getWorker: (_workerId, label) => new Worker(new URL("./editor.worker.ts", import.meta.url), { type: "module", name: `monaco-${label}` }),
};

loader.config({ monaco });
monaco.languages.register({ id: "yaml" });
monaco.languages.setMonarchTokensProvider("yaml", {
  tokenizer: {
    root: [
      [/^\s*#.*/, "comment"],
      [/^\s*[\w.-]+(?=\s*:)/, "type.identifier"],
      [/["'][^"']*["']/, "string"],
      [/\b(?:true|false|null)\b/, "keyword"],
      [/\b\d+\b/, "number"],
      [/[{}\[\],:]/, "delimiter"],
    ],
  },
});
monaco.editor.defineTheme("custos-dark", {
  base: "vs-dark",
  inherit: true,
  rules: [],
  colors: {
    "editor.background": "#070707",
    "editor.foreground": "#f2f2f2",
    "editorLineNumber.foreground": "#989898",
    "editorLineNumber.activeForeground": "#fa6e1d",
    "editorCursor.foreground": "#fa6e1d",
    "editor.selectionBackground": "#4b240d",
    "editorWidget.background": "#0f0f0f",
    "editorWidget.border": "#202020",
    "editorIndentGuide.background": "#202020",
  },
});
