import { describe, expect, it } from "vitest";
import { isScalar, isSeq } from "yaml";
import {
  addWorkflowTask,
  addWorkflowDependency,
  deleteWorkflowTask,
  formatWorkflowMemory,
  layoutPositionsFromValue,
  parseWorkflowYaml,
  parseWorkflowMemory,
  removeWorkflowTaskLayout,
  renameWorkflowTask,
  renameWorkflowTaskLayout,
  clearPullSecret,
  normalizeServiceEnvName,
  removeSecretHandle,
  removeTaskService,
  setMemoryMode,
  setPullSecret,
  setSecretHandle,
  setWorkflowDefault,
  setWorkflowEnvironmentVariable,
  setWorkflowLabel,
  setTaskService,
  setWorkflowTaskType,
  setWorkflowTaskField,
  validationPathToLocation,
} from "@/lib/workflow/editor";

const source = `# Workflow comment
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata:
  name: demo
spec:
  tasks:
    # Prepare task comment
    - name: prepare
      launch: sbatch
    - name: run
      dependsOn: [prepare]
      resources:
        cpu: 2
      image:
        uri: oras://registry.example/image.sif
`;

describe("workflow editor document operations", () => {
  it("renames a task and references while preserving comments", () => {
    const edited = renameWorkflowTask(source, "prepare", "setup");
    expect(edited).toContain("# Workflow comment");
    expect(edited).toContain("# Prepare task comment");
    expect(edited).toContain("name: setup");
    expect(edited).toContain("dependsOn: [ setup ]");
  });

  it("deletes a task and removes its dependency references", () => {
    const edited = deleteWorkflowTask(source, "prepare");
    expect(edited).toContain("# Prepare task comment");
    expect(edited).not.toContain("name: prepare");
    expect(edited).not.toContain("dependsOn");
  });

  it("adds a unique default task and removes empty image fields", () => {
    const existing = source.replace("name: prepare", "name: task-1");
    const added = addWorkflowTask(existing);
    expect(added.taskName).toBe("task-2");
    expect(added.yaml).toContain("memory: 1Gi");
    expect(added.yaml).toContain("script: |");

    const withoutImage = setWorkflowTaskField(source, "run", ["image", "uri"], "");
    expect(withoutImage).not.toContain("image:");
    const updatedCpu = setWorkflowTaskField(source, "run", ["resources", "cpu"], 4);
    expect(updatedCpu).toContain("cpu: 4");
    expect(updatedCpu).toContain("# Workflow comment");
  });

  it("maps dotted and JSON-pointer validation paths to YAML ranges", () => {
    const dotted = validationPathToLocation(source, "spec.tasks[1].resources.cpu");
    const pointer = validationPathToLocation(source, "/spec/tasks/1/resources/cpu");
    expect(dotted).toEqual(pointer);
    expect(dotted?.taskName).toBe("run");
    expect(dotted?.startLineNumber).toBeGreaterThan(1);
  });

  it("changes task kinds and clears mutually exclusive task fields", () => {
    const withConflicts = source.replace("    - name: run\n", "    - name: run\n      launch: sbatch\n      array:\n        start: 0\n        end: 9\n")
      .replace("        cpu: 2", "        cpu: 2\n        nodes: 2\n        tasks: 4\n        tasksPerNode: 2\n        cpusPerTask: 2");
    const multinode = setWorkflowTaskType(withConflicts, "run", "multinode");
    expect(multinode).toContain("multinode:");
    expect(multinode).toContain("implementation: openmpi");
    expect(multinode).toContain("launch: srun");
    expect(multinode).not.toContain("array:");
    expect(multinode).not.toContain("tasksPerNode:");

    const defaultTask = setWorkflowTaskType(multinode, "run", "default");
    expect(defaultTask).not.toContain("multinode:");
    expect(defaultTask).not.toContain("array:");
  });

  it("rejects dependency self-links, duplicates, and cycles", () => {
    const duplicate = addWorkflowDependency(source, "run", "prepare");
    const self = addWorkflowDependency(source, "run", "run");
    const cycle = addWorkflowDependency(source, "prepare", "run");
    expect(duplicate.error).toBe("duplicate");
    expect(self.error).toBe("self");
    expect(cycle.error).toBe("cycle");
  });

  it("preserves map or list environment form and updates defaults and labels", () => {
    const mapEnv = source.replace("      launch: sbatch", "      launch: sbatch\n      env:\n        PATH: /bin");
    const updatedMap = setWorkflowEnvironmentVariable(mapEnv, "prepare", "OMP_NUM_THREADS", "4");
    const parsedMap = parseWorkflowYaml(updatedMap);
    expect(parsedMap.document?.getIn(["spec", "tasks", 0, "env", "OMP_NUM_THREADS"])).toBe("4");
    expect(setWorkflowEnvironmentVariable(updatedMap, "prepare", "PATH", undefined)).not.toContain("PATH:");

    const listEnv = source.replace("      launch: sbatch", "      launch: sbatch\n      env: [\"PATH=/bin\"]");
    const updatedList = setWorkflowEnvironmentVariable(listEnv, "prepare", "PATH", "/usr/bin");
    const parsedList = parseWorkflowYaml(updatedList);
    const listNode = parsedList.document?.getIn(["spec", "tasks", 0, "env"], true);
    expect(isSeq(listNode) ? listNode.items.flatMap((item) => isScalar(item) ? [item.value] : []) : []).toEqual(["PATH=/usr/bin"]);
    const withDefault = parseWorkflowYaml(setWorkflowDefault(updatedList, "partition", "compute"));
    expect(withDefault.document?.getIn(["spec", "defaults", "partition"])).toBe("compute");
    const withLabel = parseWorkflowYaml(setWorkflowLabel(updatedList, "owner", "research"));
    expect(withLabel.document?.getIn(["metadata", "labels", "owner"])).toBe("research");
  });

  it("parses/formats memory quantities and carries layout coordinates through task changes", () => {
    expect(parseWorkflowMemory("4GiB")).toEqual({ amount: "4", unit: "GiB" });
    expect(parseWorkflowMemory("4096Mi")).toEqual({ amount: "4096", unit: "MiB" });
    expect(formatWorkflowMemory("4", "GB")).toBe("4GB");

    const positions = layoutPositionsFromValue({ nodes: { prepare: { x: 20, y: 30 }, run: { x: 80, y: 40 } } });
    const legacyPositions = layoutPositionsFromValue({ nodes: [{ id: "run", position: { x: 80, y: 40 } }] });
    expect(legacyPositions.run).toEqual({ x: 80, y: 40 });
    const renamed = renameWorkflowTaskLayout(positions, "prepare", "setup");
    expect(renamed.setup).toEqual({ x: 20, y: 30 });
    expect(renamed.prepare).toBeUndefined();
    expect(removeWorkflowTaskLayout(renamed, "run").run).toBeUndefined();
  });

  it("sets and removes services while clearing service-unsupported task fields", () => {
    const conflicted = source.replace("    - name: run\n", `    - name: run
      array: {start: 0, end: 3}
      fanOut: {count: 2}
      retry: {attempts: 2}
      when: "true"
      outputs: {result: {type: file, path: result.out}}
`);
    const serviceYaml = setTaskService(conflicted, "run");
    const service = parseWorkflowYaml(serviceYaml).document?.toJS() as {
      spec?: { tasks?: Array<Record<string, unknown>> };
    };
    expect(service.spec?.tasks?.[1]?.service).toEqual({ autoStop: true });
    for (const field of ["array", "fanOut", "retry", "when", "outputs"]) {
      expect(service.spec?.tasks?.[1]?.[field]).toBeUndefined();
    }
    expect(removeTaskService(serviceYaml, "run")).not.toContain("service:");
  });

  it("sets and clears image pull credentials", () => {
    const withPassword = setPullSecret(source, "run", { passwordSecret: "registry-password" });
    const withBoth = setPullSecret(withPassword, "run", {
      usernameSecret: "registry-username",
      passwordSecret: "registry-password",
    });
    const spec = parseWorkflowYaml(withBoth).document?.toJS() as {
      spec?: { tasks?: Array<{ image?: { pullSecret?: unknown } }> };
    };
    expect(spec.spec?.tasks?.[1]?.image?.pullSecret).toEqual({
      usernameSecret: "registry-username",
      passwordSecret: "registry-password",
    });
    expect(clearPullSecret(withBoth, "run")).not.toContain("pullSecret:");
  });

  it("switches memory mode without retaining conflicting fields", () => {
    const cpuMemory = setMemoryMode(source, "run", "perCpu", "4GiB");
    const cpuSpec = parseWorkflowYaml(cpuMemory).document?.toJS() as {
      spec?: { tasks?: Array<{ resources?: Record<string, unknown> }> };
    };
    expect(cpuSpec.spec?.tasks?.[1]?.resources?.memoryPerCpu).toBe("4GiB");
    expect(cpuSpec.spec?.tasks?.[1]?.resources?.memory).toBeUndefined();

    const nodeMemory = setMemoryMode(cpuMemory, "run", "perNode");
    const nodeSpec = parseWorkflowYaml(nodeMemory).document?.toJS() as {
      spec?: { tasks?: Array<{ resources?: Record<string, unknown> }> };
    };
    expect(nodeSpec.spec?.tasks?.[1]?.resources?.memory).toBe("4GiB");
    expect(nodeSpec.spec?.tasks?.[1]?.resources?.memoryPerCpu).toBeUndefined();
    expect(nodeSpec.spec?.tasks?.[1]?.resources?.memoryPerNode).toBeUndefined();
  });

  it("adds, edits, removes secret handles and normalizes service env names", () => {
    const withSecret = setSecretHandle(source, "registry-token", { ref: "robot-token", use: "image_pull" });
    const secretSpec = parseWorkflowYaml(withSecret).document?.toJS() as {
      spec?: { secrets?: Record<string, unknown> };
    };
    expect(secretSpec.spec?.secrets?.["registry-token"]).toEqual({
      ref: "robot-token",
      use: "image_pull",
    });
    const edited = setSecretHandle(withSecret, "registry-token", { ref: "new-token", use: "env", envName: "TOKEN" });
    const editedSpec = parseWorkflowYaml(edited).document?.toJS() as {
      spec?: { secrets?: Record<string, unknown> };
    };
    expect(editedSpec.spec?.secrets?.["registry-token"]).toEqual({
      ref: "new-token",
      use: "env",
      envName: "TOKEN",
    });
    expect(removeSecretHandle(edited, "registry-token")).not.toContain("registry-token");
    expect(normalizeServiceEnvName("db-api.prod")).toBe("DB_API_PROD");
  });

  it("reports local YAML parse errors with line and column", () => {
    const parsed = parseWorkflowYaml("spec:\n  tasks: [\n");
    expect(parsed.document).toBeNull();
    expect(parsed.error?.line).toBeGreaterThan(0);
    expect(parsed.error?.column).toBeGreaterThan(0);
  });
});
