// @ts-check
import { readdirSync, readFileSync } from "node:fs";
import { Document, parseDocument } from "yaml";

/** @typedef {import("../lib/api/schema").components["schemas"]["WorkflowTemplate"]} WorkflowTemplate */
/** @typedef {import("../lib/api/schema").components["schemas"]["WorkflowTemplateSummary"]} WorkflowTemplateSummary */

// The mock serves the real embedded catalog so mock mode and the Go API
// never drift (ADR-024).
const catalogDir = new URL("../../internal/workflowtemplates/catalog/", import.meta.url);

/** @param {unknown} value */
function text(value) {
  return typeof value === "string" ? value : "";
}

/** @returns {WorkflowTemplate[]} */
export function loadWorkflowTemplates() {
  const entries = readdirSync(catalogDir)
    .filter((name) => name.endsWith(".yaml"))
    .map((name) => {
      const document = parseDocument(readFileSync(new URL(name, catalogDir), "utf8"));
      const file = /** @type {Record<string, unknown>} */ (document.toJS());
      const tagSource = file.tags && typeof file.tags === "object" ? Object.entries(file.tags) : [];
      /** @type {WorkflowTemplate} */
      const template = {
        id: text(file.id),
        title: text(file.title),
        summary: text(file.summary),
        description: text(file.description),
        tags: Object.fromEntries(tagSource.filter((entry) => typeof entry[1] === "string")),
        spec: /** @type {WorkflowTemplate["spec"]} */ (file.workflow),
        yaml: new Document(document.get("workflow", true)).toString(),
      };
      return { order: typeof file.order === "number" ? file.order : 0, template };
    });
  entries.sort((left, right) => left.order - right.order || left.template.id.localeCompare(right.template.id));
  return entries.map((entry) => entry.template);
}

/** @param {WorkflowTemplate} template @returns {WorkflowTemplateSummary} */
export function templateSummary(template) {
  return { id: template.id, title: template.title, summary: template.summary, description: template.description, tags: template.tags };
}
