import { stringify } from "yaml";
import type { NormalizedWorkflowSpec } from "./normalize";

export function stringifyWorkflowYaml(spec: NormalizedWorkflowSpec): string {
  return stringify({
    apiVersion: spec.apiVersion,
    kind: spec.kind,
    metadata: spec.metadata,
    spec: spec.spec,
  }, { lineWidth: 0 });
}
