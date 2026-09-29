package validate_test

import (
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/validate"
)

func wf(t *testing.T, yaml string) workflowspec.Workflow {
	t.Helper()
	w, err := workflowspec.Decode([]byte(yaml), "application/yaml")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return w
}

func codes(errs []workflowspec.FieldError) map[string]bool {
	m := map[string]bool{}
	for _, e := range errs {
		m[e.Code] = true
	}
	return m
}

const base = `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  tasks:
    - name: a
      type: batch
      command: ["true"]
`

func TestStaticValid(t *testing.T) {
	if errs := validate.Static(wf(t, base)); len(errs) != 0 {
		t.Fatalf("valid spec produced errors: %v", errs)
	}
}

func TestArraySpecAndRuntimeReferenceAreNotLegacyTypeGated(t *testing.T) {
	w := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  tasks:
    - name: a
      launch: srun
      array: { start: 0, end: 3 }
      command: ["./run", "{{ array.taskId }}"]
`)
	if errs := validate.Static(w); len(errs) != 0 {
		t.Fatalf("array spec should be valid without a legacy type: %v", errs)
	}
}

func TestStaticErrorCodes(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"apiVersion", `
apiVersion: custos.io/v2
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["true"]}] }`, "API_VERSION"},
		{"kind", `
apiVersion: custos.io/v1alpha1
kind: Other
metadata: { name: x }
spec: { tasks: [{name: a, command: ["true"]}] }`, "KIND"},
		{"no tasks", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [] }`, "TASKS_REQUIRED"},
		{"bad name", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: "Bad_Name", command: ["true"]}] }`, "NAME_INVALID"},
		{"dup name", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  parameters: { p: { type: string } }
  tasks: [{name: p, command: ["true"]}]`, "NAME_DUPLICATE"},
		{"self dep", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, dependsOn: [a], command: ["true"]}] }`, "GRAPH_SELF_DEP"},
		{"unknown dep", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, dependsOn: [ghost], command: ["true"]}] }`, "GRAPH_UNKNOWN_DEP"},
		{"cycle", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  tasks:
    - { name: a, dependsOn: [b], command: ["true"] }
    - { name: b, dependsOn: [a], command: ["true"] }`, "GRAPH_CYCLE"},
		{"reserved type", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, type: interactive, command: ["true"]}] }`, "TASK_TYPE_RESERVED"},
		{"unknown type", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, type: warp, command: ["true"]}] }`, "TASK_TYPE_UNKNOWN"},
		{"xor", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["true"],
  script: { ref: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }}] }`,
			"WORKLOAD_XOR"},
		{"shell needs script", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, type: shell, command: ["true"]}] }`, "SHELL_SCRIPT_REQUIRED"},
		{"condition needs when", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, type: condition}] }`, "CONDITION_WHEN_REQUIRED"},
		{"condition no workload", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, type: condition, when: "true", command: ["true"]}] }`,
			"CONDITION_HAS_WORKLOAD"},
		{"retry state", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["true"], retry: { attempts: 2, on: [BOGUS] }}] }`,
			"RETRY_STATE"},
		{"array on condition", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, type: condition, when: "true", array: { start: 0, end: 3 }}] }`,
			"ARRAY_ONLY_ON_SLURM_TASK"},
		{"invalid launch", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, launch: mpirun, command: ["true"]}] }`,
			"LAUNCH_INVALID"},
		{"launch on condition", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, launch: srun, type: condition, when: "true"}] }`,
			"LAUNCH_INVALID"},
		{"array required", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, type: array, command: ["true"]}] }`, "ARRAY_REQUIRED"},
		{"unknown param", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["{{ parameters.nope }}"]}] }`,
			"REF_UNKNOWN_PARAMETER"},
		{"not ancestor", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  tasks:
    - { name: a, command: ["true"] }
    - { name: b, command: ["{{ tasks.a.state }}"] }`, "REF_NOT_ANCESTOR"},
		{"item scope", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["{{ item.index }}"]}] }`, "REF_ITEM_SCOPE"},
		{"array scope", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["{{ array.taskId }}"]}] }`,
			"REF_ARRAY_SCOPE"},
		{"secret use invalid", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  secrets: { tok: { ref: "x", use: nope, envName: T } }
  tasks: [{name: a, command: ["true"]}]`, "SECRET_USE_INVALID"},
		{"placement requirements", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  placement: { cluster: c1, requirements: { zone: us } }
  tasks: [{name: a, command: ["true"]}]`,
			"PLACEMENT_REQUIREMENTS_UNSUPPORTED"},
		{"fanout from", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["true"], fanOut: { count: 2, from: "x" }}] }`,
			"FANOUT_FROM_UNSUPPORTED"},
		{"execution strategy", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  execution: { strategy: bogus }
  tasks: [{name: a, command: ["true"]}]`, "EXECUTION_STRATEGY"},
		{"onDepFailure", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["true"], onDependencyFailure: skip}] }`,
			"ON_DEP_FAILURE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validate.Static(wf(t, tc.yaml))
			if !codes(errs)[tc.want] {
				t.Fatalf("want %s in %v", tc.want, errs)
			}
		})
	}
}

func TestTasksRefAncestorOK(t *testing.T) {
	w := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  tasks:
    - { name: a, command: ["true"] }
    - { name: b, dependsOn: [a], command: ["{{ tasks.a.state }}"] }
    - { name: c, dependsOn: [b], when: "{{ tasks.a.succeededCount > 0 }}", command: ["true"] }
`)
	if errs := validate.Static(w); len(errs) != 0 {
		t.Fatalf("ancestor refs rejected: %v", errs)
	}
}

func TestContextual(t *testing.T) {
	shellWf := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  tasks:
    - name: s
      type: shell
      script: { ref: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }
`)
	// Shell denied by default.
	errs := validate.Contextual(shellWf, validate.Context{})
	if !codes(errs)["SHELL_NOT_ALLOWED"] {
		t.Fatalf("shell not denied: %v", errs)
	}
	if errs := validate.Contextual(shellWf,
		validate.Context{ShellAllowed: true}); len(errs) != 0 {
		t.Fatalf("shell allowed but errors: %v", errs)
	}

	// Unbound placement cluster.
	placed := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  placement: { cluster: ghost }
  tasks: [{name: a, command: ["true"]}]
`)
	errs = validate.Contextual(placed, validate.Context{
		Cluster: func(string) (admission.Binding, validation.ClusterSnapshot, bool) {
			return admission.Binding{}, validation.ClusterSnapshot{}, false
		},
	})
	if !codes(errs)["PLACEMENT_CLUSTER_UNBOUND"] {
		t.Fatalf("unbound cluster not flagged: %v", errs)
	}
}

func TestContainerMultinodeAndAffinityValidation(t *testing.T) {
	valid := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: mpi}
spec:
  tasks:
    - name: run
      image: {uri: "oras://docker.io/example/mpi.sif@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
      script: |
        #!/bin/sh
        mpi_hello_world
      resources: {cpu: 4, cpuAffinity: numa, memory: 1GB, walltime: "00:05:00"}
      multinode: {nodes: 1, implementation: openmpi}
`)
	if errs := validate.Static(valid); len(errs) != 0 {
		t.Fatalf("valid container MPI spec rejected: %v", errs)
	}

	cases := []struct {
		name, yaml, code string
	}{
		{"bad image URI", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: bad-image}
spec: {tasks: [{name: run, image: {uri: "oras://host/a/../b"}, command: ["true"]}]}
`, "IMAGE_URI_INVALID"},
		{"image on condition", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: bad-image-kind}
spec: {tasks: [{name: gate, type: condition, when: "true", image: {uri: "oras://host/a"}}]}
`, "IMAGE_TASK_KIND"},
		{"empty inline script", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: empty-inline}
spec: {tasks: [{name: run, script: ""}]}
`, "SCRIPT_SOURCE"},
		{"script has both sources", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: both-sources}
spec: {tasks: [{name: run, script: {ref: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", language: bash, inline: "echo hi"}}]}
`, "SCRIPT_SOURCE"},
		{"invalid CPU affinity enum", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: affinity-enum}
spec: {tasks: [{name: run, launch: srun, resources: {cpuAffinity: NUMA, walltime: 5m}, command: ["true"]}]}
`, "CPU_AFFINITY_INVALID"},
		{"CPU affinity on condition", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: affinity-kind}
spec: {tasks: [{name: gate, type: condition, when: "true", resources: {cpuAffinity: numa}}]}
`, "CPU_AFFINITY_TASK_KIND"},
		{"array conflicts with multinode", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: bad-array}
spec: {tasks: [{name: run, array: {start: 1, end: 2}, multinode: {nodes: 2, implementation: openmpi}, resources: {walltime: 5m}, command: ["true"]}]}
`, "MULTINODE_CONFLICT"},
		{"zero procs per node", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: bad-procs}
spec: {tasks: [{name: run, multinode: {nodes: 2, implementation: generic, procsPerNode: 0}, resources: {walltime: 5m}, command: ["true"]}]}
`, "MULTINODE_INVALID"},
		{"launch conflicts with implementation", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: bad-launch}
spec: {tasks: [{name: run, launch: srun, multinode: {nodes: 2, implementation: generic}, resources: {walltime: 5m}, command: ["true"]}]}
`, "MULTINODE_LAUNCH"},
		{"CPU not divisible", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: bad-cpu}
spec: {tasks: [{name: run, multinode: {nodes: 2, implementation: openmpi, procsPerNode: 3}, resources: {cpu: 4, walltime: 5m}, command: ["true"]}]}
`, "MULTINODE_CPU_DIVISIBLE"},
		{"affinity on condition", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: bad-affinity-kind}
spec: {tasks: [{name: gate, type: condition, when: "true", resources: {cpuAffinity: numa}}]}
`, "CPU_AFFINITY_TASK_KIND"},
		{"multinode ref out of scope", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: bad-ref}
spec: {tasks: [{name: run, command: ["mpirun", "$MULTINODE_HOSTLIST"]}]}
`, "MULTINODE_REF"},
		{"multinode template in env", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: bad-env-ref}
spec: {tasks: [{name: run, multinode: {nodes: 2, implementation: generic}, env: {HOSTS: "{{ multinode.hostlist }}"}, resources: {walltime: 5m}, command: ["true"]}]}
`, "MULTINODE_REF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if errs := validate.Static(wf(t, tc.yaml)); !codes(errs)[tc.code] {
				t.Fatalf("want %s, got %v", tc.code, errs)
			}
		})
	}

	generic := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: generic}
spec: {tasks: [{name: run, multinode: {nodes: 2, implementation: generic}, resources: {cpu: 2, cpuAffinity: numa, walltime: 5m}, command: ["mpirun", "{{ multinode.hostlist }}", "$MULTINODE_TOTAL_SLOTS", "--host=$MULTINODE_HOSTLIST"]}]}
`)
	if errs := validate.Static(generic); len(errs) != 0 {
		t.Fatalf("generic runtime references rejected: %v", errs)
	}

	tooLarge := workflowspec.Workflow{
		APIVersion: workflowspec.APIVersionV1Alpha1, Kind: workflowspec.KindWorkflow,
		Metadata: workflowspec.Metadata{Name: "large-inline"},
		Spec: workflowspec.Spec{Tasks: []workflowspec.Task{{Name: "run",
			Script: &workflowspec.ScriptRef{Inline: strings.Repeat("x", validation.DefaultLimits.MaxScriptBytes+1)}}}},
	}
	if errs := validate.Static(tooLarge); !codes(errs)["SCRIPT_INLINE_TOO_LARGE"] {
		t.Fatalf("oversized inline script error = %v", errs)
	}
}
