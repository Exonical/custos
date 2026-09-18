package workflowspec_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/workflowspec"
	"gopkg.in/yaml.v3"
)

const docYAML = `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata:
  name: gaussian-simulation
  labels: { domain: chemistry }
spec:
  parameters:
    molecule:   { type: string, required: true, pattern: "^[a-zA-Z0-9_-]{1,64}$" }
    iterations: { type: integer, default: 100, minimum: 1, maximum: 100000 }
    shards:     { type: integer, default: 4, minimum: 1, maximum: 256 }
  placement: { cluster: summit }
  defaults:
    partition: compute
    qos: normal
    workingDirectory: "{{ run.scratch }}"
    env:
      OMP_NUM_THREADS: "{{ task.resources.cpu }}"
  tasks:
    - name: prepare
      type: batch
      resources: { cpu: 4, memory: 8Gi, walltime: 30m }
      command: ["./prepare", "{{ parameters.molecule }}"]
      outputs:
        shardList: { type: file, path: "shards.json" }
    - name: simulate
      type: mpi
      dependsOn: [prepare]
      fanOut:
        count: "{{ parameters.shards }}"
      resources: { nodes: 8, tasksPerNode: 64, memoryPerNode: 128Gi, walltime: 4h }
      command: ["./simulate", "--shard", "{{ item.index }}"]
      retry: { attempts: 2, on: [FAILED, NODE_FAIL] }
    - name: merge
      type: batch
      dependsOn: [simulate]
      when: "{{ tasks.simulate.succeededCount }} > 0"
      resources: { cpu: 16, memory: 64Gi, walltime: 1h }
      command: ["./merge"]
    - name: train
      type: gpu
      dependsOn: [merge]
      resources: { gpu: { count: 4, type: a100 }, cpu: 32, memory: 256Gi, walltime: 12h }
      software:
        - { name: python, version: "3.13" }
      script: { ref: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", language: python }
      args: ["--epochs", "{{ parameters.iterations }}"]
      env: { WANDB_MODE: offline }
    - name: sweep
      type: array
      dependsOn: [merge]
      array: { start: 0, end: 99, maxConcurrent: 20 }
      command: ["./sweep", "{{ array.taskId }}"]
`

func decodeYAML(t *testing.T, s string) workflowspec.Workflow {
	t.Helper()
	w, err := workflowspec.Decode([]byte(s), "application/yaml")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return w
}

func TestDocExampleDecodes(t *testing.T) {
	w := decodeYAML(t, docYAML)
	if w.APIVersion != workflowspec.APIVersionV1Alpha1 ||
		w.Kind != workflowspec.KindWorkflow || len(w.Spec.Tasks) != 5 {
		t.Fatalf("doc: %+v", w.Spec.Tasks)
	}
	if w.Spec.Tasks[1].FanOut == nil ||
		w.Spec.Tasks[1].FanOut.Count != "{{ parameters.shards }}" {
		t.Fatalf("fanOut: %+v", w.Spec.Tasks[1].FanOut)
	}
	if w.Spec.Tasks[4].Array == nil || w.Spec.Tasks[4].Array.End != 99 ||
		w.Spec.Tasks[4].Array.MaxConcurrent != 20 {
		t.Fatalf("array: %+v", w.Spec.Tasks[4].Array)
	}
}

func TestCanonicalEquivalence(t *testing.T) {
	wy := decodeYAML(t, docYAML)
	// Round-trip through JSON with shuffled keys -> same canonical
	// form and hash.
	var m map[string]any
	if err := yaml.Unmarshal([]byte(docYAML), &m); err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(m)
	wj, err := workflowspec.Decode(j, "application/json")
	if err != nil {
		t.Fatalf("json decode: %v", err)
	}
	cy, _ := workflowspec.Canonical(wy)
	cj, _ := workflowspec.Canonical(wj)
	if !bytes.Equal(cy, cj) {
		t.Fatalf("canonical differs:\n%s\n%s", cy, cj)
	}
	hy, _ := workflowspec.SpecHash(wy)
	hj, _ := workflowspec.SpecHash(wj)
	if hy != hj {
		t.Fatal("spec hash differs across encodings")
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	bad := `{"apiVersion":"custos.io/v1alpha1","kind":"Workflow",
		"metadata":{"name":"x"},"spec":{"tasks":[{"name":"a","bogus":1}]}}`
	_, err := workflowspec.Decode([]byte(bad), "application/json")
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown field not rejected with path: %v", err)
	}
}
