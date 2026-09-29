package workflowspec_test

import (
	"bytes"
	"crypto/sha256"
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

func TestEffectiveLaunchCompatibility(t *testing.T) {
	for _, tt := range []struct {
		name string
		task workflowspec.Task
		want string
	}{
		{name: "legacy mpi", task: workflowspec.Task{Type: "mpi"}, want: workflowspec.LaunchSrun},
		{name: "legacy batch", task: workflowspec.Task{Type: "batch"}, want: workflowspec.LaunchSbatch},
		{name: "omitted type", task: workflowspec.Task{}, want: workflowspec.LaunchSbatch},
		{name: "explicit sbatch overrides mpi alias", task: workflowspec.Task{Type: "mpi", Launch: workflowspec.LaunchSbatch}, want: workflowspec.LaunchSbatch},
		{name: "explicit srun", task: workflowspec.Task{Launch: workflowspec.LaunchSrun}, want: workflowspec.LaunchSrun},
		{name: "openmpi multinode", task: workflowspec.Task{Multinode: &workflowspec.Multinode{Implementation: "openmpi"}}, want: workflowspec.LaunchSrun},
		{name: "mpich multinode", task: workflowspec.Task{Multinode: &workflowspec.Multinode{Implementation: "mpich"}}, want: workflowspec.LaunchSrun},
		{name: "generic multinode", task: workflowspec.Task{Multinode: &workflowspec.Multinode{Implementation: "generic"}}, want: workflowspec.LaunchSbatch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.task.EffectiveLaunch(); got != tt.want {
				t.Fatalf("EffectiveLaunch() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegacyCanonicalSpecHashIsUnchanged(t *testing.T) {
	legacy := workflowspec.Workflow{
		APIVersion: workflowspec.APIVersionV1Alpha1,
		Kind:       workflowspec.KindWorkflow,
		Metadata:   workflowspec.Metadata{Name: "legacy"},
		Spec: workflowspec.Spec{Tasks: []workflowspec.Task{{
			Name: "run", Type: "mpi", Command: []string{"./a.out"},
		}}},
	}
	want := []byte(`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow","metadata":{"name":"legacy"},"spec":{"tasks":[{"name":"run","type":"mpi","resources":{},"command":["./a.out"]}]}}`)
	canonical, err := workflowspec.Canonical(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, want) {
		t.Fatalf("legacy canonical bytes changed:\n got %s\nwant %s", canonical, want)
	}
	got, err := workflowspec.SpecHash(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if wantHash := sha256.Sum256(want); got != wantHash {
		t.Fatalf("legacy spec hash changed: got %x want %x", got, wantHash)
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

func TestScriptObjectRejectsUnknownFields(t *testing.T) {
	bad := `{"apiVersion":"custos.io/v1alpha1","kind":"Workflow","metadata":{"name":"x"},"spec":{"tasks":[{"name":"run","script":{"inline":"echo hi","unexpected":true}}]}}`
	if _, err := workflowspec.Decode([]byte(bad), "application/json"); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("unknown ScriptRef field was not rejected: %v", err)
	}
}

func TestEnvListShorthand(t *testing.T) {
	w := decodeYAML(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: env-list}
spec:
  defaults:
    env: ["DEFAULT=a=b"]
  tasks:
    - name: run
      command: ["true"]
      env: ["PATH=/usr/bin:/bin", "VALUE=a=b"]
`)
	if w.Spec.Defaults.Env["DEFAULT"] != "a=b" ||
		w.Spec.Tasks[0].Env["PATH"] != "/usr/bin:/bin" ||
		w.Spec.Tasks[0].Env["VALUE"] != "a=b" {
		t.Fatalf("environment list was not split at the first equals: %+v", w)
	}
	canonical, err := workflowspec.Canonical(w)
	if err != nil || !strings.Contains(string(canonical), `"env":{"PATH":"/usr/bin:/bin","VALUE":"a=b"}`) {
		t.Fatalf("environment did not canonicalize to a map: %s, %v", canonical, err)
	}
	for _, bad := range []string{
		`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow","metadata":{"name":"x"},"spec":{"tasks":[{"name":"a","command":["true"],"env":["NO_EQUALS"]}]}}`,
		`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow","metadata":{"name":"x"},"spec":{"tasks":[{"name":"a","command":["true"],"env":["A=1","A=2"]}]}}`,
		`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow","metadata":{"name":"x"},"spec":{"tasks":[{"name":"a","command":["true"],"env":["BAD-NAME=1"]}]}}`,
	} {
		if _, err := workflowspec.Decode([]byte(bad), "application/json"); err == nil {
			t.Errorf("invalid environment list decoded: %s", bad)
		}
	}
}

func TestInlineScriptObjectCanonicalAndEffectiveLanguage(t *testing.T) {
	w := decodeYAML(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: inline}
spec:
  tasks:
    - name: run
      script: |
        #!/usr/bin/env sh
        echo inline
`)
	script := w.Spec.Tasks[0].Script
	if script == nil || !script.HasInline() || script.EffectiveLanguage() != workflowspec.LanguageSh || script.Language != "" {
		t.Fatalf("inline script/language: %+v", script)
	}
	canonical, err := workflowspec.Canonical(w)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(canonical, &root); err != nil {
		t.Fatal(err)
	}
	task := root["spec"].(map[string]any)["tasks"].([]any)[0].(map[string]any)
	object := task["script"].(map[string]any)
	if object["inline"] == nil || object["ref"] != nil {
		t.Fatalf("inline script did not canonicalize as an object: %s", canonical)
	}
	for _, tc := range []struct {
		source string
		want   workflowspec.Language
	}{
		{"#!/bin/bash\necho ok", workflowspec.LanguageBash},
		{"#!/usr/bin/env python3\nprint('ok')", workflowspec.LanguagePython},
		{"echo no shebang", workflowspec.LanguageBash},
	} {
		if got := (workflowspec.ScriptRef{Inline: tc.source}).EffectiveLanguage(); got != tc.want {
			t.Errorf("EffectiveLanguage(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestResolveMultinodeResources(t *testing.T) {
	mpi := workflowspec.Task{
		Multinode: &workflowspec.Multinode{Nodes: 2, Implementation: "openmpi"},
		Resources: workflowspec.TaskResources{CPU: 4, Memory: "1GB", Walltime: "5m"},
	}
	resolved, errs := mpi.ResolveResources("resources")
	if len(errs) != 0 || resolved.Nodes != 2 || resolved.Tasks != 8 ||
		resolved.TasksPerNode != 4 || resolved.CPUsPerTask != 1 ||
		resolved.MemoryPerNodeMiB != 954 || mpi.EffectiveLaunch() != workflowspec.LaunchSrun {
		t.Fatalf("MPI resolution: %+v errors=%+v launch=%s", resolved, errs, mpi.EffectiveLaunch())
	}
	generic := workflowspec.Task{
		Multinode: &workflowspec.Multinode{Nodes: 2, Implementation: "generic"},
		Resources: workflowspec.TaskResources{CPU: 2, Walltime: "5m"},
	}
	resolved, errs = generic.ResolveResources("resources")
	if len(errs) != 0 || resolved.Nodes != 2 || resolved.Tasks != 2 ||
		resolved.TasksPerNode != 1 || resolved.CPUsPerTask != 2 ||
		generic.EffectiveProcsPerNode() != 2 || generic.EffectiveLaunch() != workflowspec.LaunchSbatch {
		t.Fatalf("generic resolution: %+v errors=%+v", resolved, errs)
	}
	empty := workflowspec.Task{Multinode: &workflowspec.Multinode{Nodes: 1, Implementation: "generic"}}
	resolved, errs = empty.ResolveResources("resources")
	if len(errs) != 0 || resolved.Nodes != 1 || resolved.Tasks != 1 || resolved.CPUsPerTask != 1 {
		t.Fatalf("empty resource block did not resolve: %+v errors=%+v", resolved, errs)
	}
	bad := workflowspec.Task{
		Multinode: &workflowspec.Multinode{Nodes: 2, ProcsPerNode: 3, Implementation: "openmpi"},
		Resources: workflowspec.TaskResources{CPU: 4, Walltime: "5m"},
	}
	if _, errs := bad.ResolveResources("resources"); len(errs) == 0 || errs[0].Code != "MULTINODE_CPU_DIVISIBLE" {
		t.Fatalf("nondivisible MPI request errors = %+v", errs)
	}
	conflict := workflowspec.Task{
		Multinode: &workflowspec.Multinode{Nodes: 1, Implementation: "generic"},
		Resources: workflowspec.TaskResources{CPUsPerTask: 2},
	}
	if _, errs := conflict.ResolveResources("resources"); len(errs) == 0 || errs[0].Code != "MULTINODE_CONFLICT" {
		t.Fatalf("explicit cpusPerTask multinode conflict errors = %+v", errs)
	}
}
