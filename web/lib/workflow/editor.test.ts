import { describe, expect, it } from "vitest";
import {
  addWorkflowTask,
  deleteWorkflowTask,
  parseWorkflowYaml,
  renameWorkflowTask,
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

  it("reports local YAML parse errors with line and column", () => {
    const parsed = parseWorkflowYaml("spec:\n  tasks: [\n");
    expect(parsed.document).toBeNull();
    expect(parsed.error?.line).toBeGreaterThan(0);
    expect(parsed.error?.column).toBeGreaterThan(0);
  });
});
