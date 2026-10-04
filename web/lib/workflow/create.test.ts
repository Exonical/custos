import { describe, expect, it } from "vitest";
import { slugifyWorkflowMetadataName, starterWorkflowSpec, validateWorkflowName } from "./create";

describe("workflow creation helpers", () => {
  it("slugifies metadata names and enforces workflow-name rules", () => {
    expect(slugifyWorkflowMetadataName("  My Workflow / V1  ")).toBe("my-workflow-v1");
    expect(slugifyWorkflowMetadataName("x".repeat(70))).toBe("x".repeat(63));
    expect(slugifyWorkflowMetadataName("!!!", "hello")).toBe("hello");
    expect(validateWorkflowName("my-workflow")).toBeUndefined();
    expect(validateWorkflowName("My workflow")).toContain("lowercase");
    expect(validateWorkflowName("x".repeat(64))).toContain("63");
  });

  it("builds the starter workflow document with its hello task", () => {
    expect(starterWorkflowSpec("Hello from my workflow")).toEqual({
      apiVersion: "custos.io/v1alpha1",
      kind: "Workflow",
      metadata: { name: "hello-from-my-workflow" },
      spec: {
        tasks: [{
          name: "hello",
          launch: "sbatch",
          resources: { cpu: 1, memory: "1GiB", walltime: "10m" },
          script: "#!/bin/bash\necho \"Hello from $(hostname)\"\n",
        }],
      },
    });
  });
});
