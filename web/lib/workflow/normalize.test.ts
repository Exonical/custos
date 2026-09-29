import { describe, expect, it } from "vitest";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";

describe("normalizeWorkflowSpec", () => {
  it("normalizes incomplete or malformed API objects without throwing", () => {
    expect(normalizeWorkflowSpec(null)).toMatchObject({
      apiVersion: "custos.io/v1alpha1",
      kind: "Workflow",
      metadata: { name: "workflow" },
      spec: { parameters: {}, tasks: [], secrets: {} },
    });
    expect(normalizeWorkflowSpec({ spec: { tasks: [null, {}, { name: 2 }, { name: "ok", dependsOn: "bad", env: ["bad"] }] } })).toMatchObject({
      spec: { tasks: [{ name: "ok", launch: "sbatch", dependsOn: [] }] },
    });
  });

  it("normalizes optional image, multinode, affinity, and inline script fields", () => {
    const result = normalizeWorkflowSpec({
      metadata: { name: "mpi" },
      spec: {
        tasks: [
          {
            name: "openmpi",
            image: { uri: "oras://docker.io/example/mpi.sif" },
            script: "#!/bin/sh\necho mpi\n",
            resources: { cpu: 4, cpuAffinity: "numa" },
            multinode: { nodes: 1, implementation: "openmpi" },
          },
          {
            name: "generic",
            launch: "sbatch",
            image: { uri: "oras://docker.io/example/generic.sif" },
            script: { inline: "#!/bin/bash\necho generic\n" },
            multinode: { nodes: 2, implementation: "generic", procsPerNode: 3 },
          },
        ],
      },
    });
    expect(result.spec.tasks[0]).toMatchObject({
      launch: "srun",
      image: { uri: "oras://docker.io/example/mpi.sif" },
      script: { inline: "#!/bin/sh\necho mpi\n" },
      resources: { cpuAffinity: "numa" },
      multinode: { nodes: 1, implementation: "openmpi" },
    });
    expect(result.spec.tasks[1]).toMatchObject({
      launch: "sbatch",
      script: { inline: "#!/bin/bash\necho generic\n" },
      multinode: { nodes: 2, implementation: "generic", procsPerNode: 3 },
    });
  });

  it("preserves supported spec values and accepts number parameters defensively", () => {
    const result = normalizeWorkflowSpec({
      apiVersion: "custos.io/v1alpha1",
      kind: "Workflow",
      metadata: { name: "demo", labels: { team: "science", bad: 4 } },
      spec: {
        parameters: {
          shards: { type: "integer", required: true, minimum: 1, maximum: 8 },
          scale: { type: "number", default: 1.5 },
        },
        secrets: { token: { ref: "hf-token", use: "env", envName: "HF_TOKEN", value: "never-render" } },
        tasks: [
          { name: "prepare", type: "batch", command: ["./prepare"], env: { MODE: "{{ parameters.scale }}", bad: 1 } },
          { name: "train", type: "gpu", dependsOn: ["prepare", 5], retry: { attempts: 2, on: ["FAILED", "invalid"] } },
        ],
      },
    });
    expect(result.spec.parameters.scale.type).toBe("number");
    expect(result.metadata.labels).toEqual({ team: "science" });
    expect(result.spec.tasks[0]).toMatchObject({ command: ["./prepare"], env: { MODE: "{{ parameters.scale }}" } });
    expect(result.spec.tasks[1]).toMatchObject({ dependsOn: ["prepare"], retry: { attempts: 2, on: ["FAILED"] } });
    expect(result.spec.secrets.token).toEqual({ ref: "hf-token", use: "env", envName: "HF_TOKEN" });
    expect(JSON.stringify(result)).not.toContain("never-render");
  });
});
