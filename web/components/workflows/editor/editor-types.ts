export type WorkflowEditorIssue = {
  path: string;
  code: string;
  message: string;
};

export type WorkflowEditorPanelTab = "info" | "defaults" | "annotations" | "parameters" | "secrets" | "task" | "environment" | "resources";
