import { describe, expect, it } from "vitest";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";
import { stringifyWorkflowYaml } from "@/lib/workflow/yaml";

describe("stringifyWorkflowYaml", () => {
  it("writes stable root key order while preserving task order", () => {
    const spec = normalizeWorkflowSpec({
      metadata: { name: "example" },
      spec: { tasks: [{ name: "first" }, { name: "second", dependsOn: ["first"] }] },
    });
    const yaml = stringifyWorkflowYaml(spec);
    expect(yaml.indexOf("apiVersion:")).toBeLessThan(yaml.indexOf("kind:"));
    expect(yaml.indexOf("kind:")).toBeLessThan(yaml.indexOf("metadata:"));
    expect(yaml.indexOf("metadata:")).toBeLessThan(yaml.indexOf("spec:"));
    expect(yaml.indexOf("name: first")).toBeLessThan(yaml.indexOf("name: second"));
  });
});
