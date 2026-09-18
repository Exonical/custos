package validate_test

import (
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
		{"array on non-array", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["true"], array: { start: 0, end: 3 }}] }`,
			"ARRAY_ONLY_ON_ARRAY"},
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
		{"secrets fail closed", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec:
  secrets: { tok: { ref: "x", use: env, envName: T } }
  tasks: [{name: a, command: ["true"]}]`, "SECRETS_NOT_AVAILABLE"},
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
