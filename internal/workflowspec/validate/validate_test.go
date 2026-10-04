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

func TestServiceTaskStaticValidation(t *testing.T) {
	valid := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec:
  tasks:
    - name: prepare
      resources: {walltime: 30m}
      command: ["./prepare"]
    - name: db
      dependsOn: [prepare]
      service: {}
      launch: sbatch
      resources: {cpu: 2, walltime: 2h}
      script: |
        #!/bin/bash
        exec postgres -D "$CUSTOS_JOB_DIR/pg"
    - name: cache
      dependsOn: [db]
      service: {autoStop: false}
      multinode: {nodes: 2, implementation: generic}
      image: {uri: docker://registry.example.com/cache:1}
      resources: {cpu: 2, walltime: 1h}
      command: ["./cache"]
    - name: client
      dependsOn: [db, cache]
      resources: {walltime: 30m}
      command: ["./client"]
`)
	if errs := validate.Static(valid); len(errs) != 0 {
		t.Fatalf("valid service tasks rejected: %+v", errs)
	}
	autoStop := false
	valid.Spec.Tasks[1].Service.AutoStop = &autoStop
	if errs := validate.Static(valid); len(errs) != 0 {
		t.Fatalf("autoStop=false service rejected: %+v", errs)
	}

	for _, tc := range []struct {
		name string
		yaml string
		code string
	}{
		{
			name: "walltime required",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec: {tasks: [{name: db, service: {}, command: ["db"]}]}
`,
			code: "SERVICE_WALLTIME_REQUIRED",
		},
		{
			name: "array unsupported",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec: {tasks: [{name: db, service: {}, resources: {walltime: 2h}, array: {start: 0, end: 1}, command: ["db"]}]}
`,
			code: "SERVICE_FIELD_UNSUPPORTED",
		},
		{
			name: "fanout unsupported",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec: {tasks: [{name: db, service: {}, resources: {walltime: 2h}, fanOut: {count: 2}, command: ["db"]}]}
`,
			code: "SERVICE_FIELD_UNSUPPORTED",
		},
		{
			name: "retry unsupported",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec: {tasks: [{name: db, service: {}, resources: {walltime: 2h}, retry: {attempts: 1}, command: ["db"]}]}
`,
			code: "SERVICE_FIELD_UNSUPPORTED",
		},
		{
			name: "when unsupported",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec: {tasks: [{name: db, service: {}, resources: {walltime: 2h}, when: "true", command: ["db"]}]}
`,
			code: "SERVICE_FIELD_UNSUPPORTED",
		},
		{
			name: "outputs unsupported",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec: {tasks: [{name: db, service: {}, resources: {walltime: 2h}, outputs: {}, command: ["db"]}]}
`,
			code: "SERVICE_FIELD_UNSUPPORTED",
		},
		{
			name: "condition unsupported",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec: {tasks: [{name: gate, type: condition, service: {}, when: "true"}]}
`,
			code: "SERVICE_FIELD_UNSUPPORTED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errs := validate.Static(wf(t, tc.yaml)); !hasCode(errs, tc.code) {
				t.Fatalf("errors = %+v, want %s", errs, tc.code)
			}
		})
	}

	collision := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec:
  tasks:
    - {name: db-api, service: {}, resources: {walltime: 1h}, command: ["db"]}
    - {name: db_api, service: {}, resources: {walltime: 1h}, command: ["db"]}
`)
	if !hasCode(validate.Static(collision), "SERVICE_NAME_COLLISION") {
		t.Fatal("normalized service-name collision was not rejected")
	}
	reserved := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec:
  defaults:
    env: {CUSTOS_SERVICE_DB_HOST: custom}
  tasks:
    - {name: db, service: {}, resources: {walltime: 1h}, command: ["db"]}
    - {name: client, dependsOn: [db], resources: {walltime: 1h}, command: ["./client"], env: {CUSTOS_SERVICE_DB_JOBID: "123"}}
`)
	reservedErrors := 0
	for _, fieldErr := range validate.Static(reserved) {
		if fieldErr.Code == "SECRET_ENV_CONTROLLED" {
			reservedErrors++
		}
	}
	if reservedErrors != 2 {
		t.Fatalf("service runtime names were not reserved in defaults and task env: %+v",
			validate.Static(reserved))
	}
}

func TestServiceClusterMismatchStaticValidation(t *testing.T) {
	w := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: services}
spec:
  placement: {cluster: compute-a}
  tasks:
    - name: db
      service: {}
      resources: {walltime: 2h}
      command: ["db"]
    - name: client
      placement: {cluster: compute-b}
      dependsOn: [db]
      resources: {walltime: 30m}
      command: ["./client"]
`)
	found := false
	for _, fieldErr := range validate.Static(w) {
		if fieldErr.Code == "SERVICE_CLUSTER_MISMATCH" &&
			fieldErr.Path == "spec.tasks[1].dependsOn[0]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cross-cluster service dependency error missing: %+v", validate.Static(w))
	}

	w.Spec.Tasks[1].Placement.Cluster = "compute-a"
	if errs := validate.Static(w); len(errs) != 0 {
		t.Fatalf("same-cluster service dependency rejected: %+v", errs)
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
		{"memory per cpu conflicts with memory", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["true"], resources: {memory: 1GiB, memoryPerCpu: 512MiB}}] }`,
			"MEMORY_CONFLICT"},
		{"memory per cpu conflicts with memory per node", `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["true"], resources: {memoryPerNode: 1GiB, memoryPerCpu: 512MiB}}] }`,
			"MEMORY_CONFLICT"},
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

func TestMemoryPerCPUConflictPath(t *testing.T) {
	errs := validate.Static(wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: { name: x }
spec: { tasks: [{name: a, command: ["true"], resources: {memory: 1GiB, memoryPerCpu: 512MiB}}] }
`))
	for _, err := range errs {
		if err.Code == "MEMORY_CONFLICT" {
			if err.Path != "spec.tasks[0].resources.memoryPerCpu" {
				t.Fatalf("memory conflict path = %q", err.Path)
			}
			return
		}
	}
	t.Fatalf("MEMORY_CONFLICT not found in %+v", errs)
}

func TestImagePullSecretStaticValidation(t *testing.T) {
	valid := wf(t, `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: pull}
spec:
  secrets:
    user: {ref: registry-user, use: image_pull}
    token: {ref: registry-token, use: image_pull}
  tasks:
    - name: run
      image:
        uri: docker://registry.example.com/team/app:1.2
        pullSecret: {usernameSecret: user, passwordSecret: token}
      command: ["true"]
    - name: array-run
      image:
        uri: docker://registry.example.com/team/app:1.2
        pullSecret: {username: "robot$ci", passwordSecret: token}
      array: {start: 0, end: 2}
      command: ["true"]
`)
	if errs := validate.Static(valid); len(errs) != 0 {
		t.Fatalf("valid image pull secret/array spec rejected: %+v", errs)
	}
	for _, username := range []string{strings.Repeat("x", 257), "robot\nci"} {
		invalid := workflowspec.Workflow{
			APIVersion: workflowspec.APIVersionV1Alpha1,
			Kind:       workflowspec.KindWorkflow,
			Metadata:   workflowspec.Metadata{Name: "pull"},
			Spec: workflowspec.Spec{
				Secrets: map[string]workflowspec.SecretUse{
					"token": {Ref: "registry-token", Use: "image_pull"},
				},
				Tasks: []workflowspec.Task{{
					Name:    "run",
					Command: []string{"true"},
					Image: &workflowspec.Image{
						URI: "docker://registry.example.com/team/app:1.2",
						PullSecret: &workflowspec.ImagePullSecret{
							Username: username, PasswordSecret: "token",
						},
					},
				}},
			},
		}
		if errs := validate.Static(invalid); !hasCode(errs, "PULL_SECRET_INVALID") {
			t.Fatalf("invalid literal username %q accepted: %+v", username, errs)
		}
	}

	tests := []struct {
		name string
		yaml string
		code string
	}{
		{
			name: "bad literal username",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: pull}
spec:
  secrets: {token: {ref: registry-token, use: image_pull}}
  tasks: [{name: run, image: {uri: "docker://registry.example.com/team/app:1.2", pullSecret: {username: "bad user", passwordSecret: token}}, command: ["true"]}]
`,
			code: "PULL_SECRET_INVALID",
		},
		{
			name: "both username forms",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: pull}
spec:
  secrets: {token: {ref: registry-token, use: image_pull}, user: {ref: registry-user, use: image_pull}}
  tasks: [{name: run, image: {uri: "docker://registry.example.com/team/app:1.2", pullSecret: {username: robot, usernameSecret: user, passwordSecret: token}}, command: ["true"]}]
`,
			code: "PULL_SECRET_INVALID",
		},
		{
			name: "missing password secret",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: pull}
spec:
  tasks: [{name: run, image: {uri: "docker://registry.example.com/team/app:1.2", pullSecret: {username: robot}}, command: ["true"]}]
`,
			code: "PULL_SECRET_INVALID",
		},
		{
			name: "pull secret without image URI",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: pull}
spec:
  secrets: {token: {ref: registry-token, use: image_pull}}
  tasks: [{name: run, image: {uri: "", pullSecret: {username: robot, passwordSecret: token}}, command: ["true"]}]
`,
			code: "PULL_SECRET_REQUIRES_IMAGE",
		},
		{
			name: "absolute image path",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: pull}
spec:
  secrets: {token: {ref: registry-token, use: image_pull}}
  tasks: [{name: run, image: {uri: "/images/app.sif", pullSecret: {username: robot, passwordSecret: token}}, command: ["true"]}]
`,
			code: "PULL_SECRET_INVALID",
		},
		{
			name: "password secret uses wrong mode",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: pull}
spec:
  secrets: {token: {ref: registry-token, use: env, envName: TOKEN}}
  tasks: [{name: run, image: {uri: "docker://registry.example.com/team/app:1.2", pullSecret: {username: robot, passwordSecret: token}}, command: ["true"]}]
`,
			code: "PULL_SECRET_INVALID",
		},
		{
			name: "multinode",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: pull}
spec:
  secrets: {token: {ref: registry-token, use: image_pull}}
  tasks: [{name: run, image: {uri: "docker://registry.example.com/team/app:1.2", pullSecret: {username: robot, passwordSecret: token}}, multinode: {nodes: 2, implementation: generic}, resources: {cpu: 2, walltime: 5m}, command: ["true"]}]
`,
			code: "PULL_SECRET_MULTINODE_UNSUPPORTED",
		},
		{
			name: "resources nodes greater than one",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: pull}
spec:
  secrets: {token: {ref: registry-token, use: image_pull}}
  tasks: [{name: run, image: {uri: "docker://registry.example.com/team/app:1.2", pullSecret: {username: robot, passwordSecret: token}}, resources: {nodes: 2, walltime: 5m}, command: ["true"]}]
`,
			code: "PULL_SECRET_MULTINODE_UNSUPPORTED",
		},
		{
			name: "unused image pull secret",
			yaml: `
apiVersion: custos.io/v1alpha1
kind: Workflow
metadata: {name: pull}
spec:
  secrets: {token: {ref: registry-token, use: image_pull}}
  tasks: [{name: run, command: ["true"]}]
`,
			code: "SECRET_USE_UNUSED",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if errs := validate.Static(wf(t, tc.yaml)); !hasCode(errs, tc.code) {
				t.Fatalf("want %s: %+v", tc.code, errs)
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
