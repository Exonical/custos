package sbatchexport_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/sbatchexport"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/sbatchimport"
	"github.com/Exonical/custos/internal/validation/sbatchscan"
	"github.com/Exonical/custos/internal/validation/shsyntax"
	"github.com/Exonical/custos/internal/workflowspec"
)

func validateBash(t *testing.T, script string) {
	t.Helper()
	result, err := shsyntax.Validator{}.Validate(t.Context(), validation.Input{
		Language: workflowspec.LanguageBash, Script: []byte(script),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Severity.AtLeast(validation.SeverityError) {
			t.Fatalf("exported script has %s: %s\n%s", diagnostic.Code, diagnostic.Message, script)
		}
	}
}

func TestRenderHybridAndRoundTripSbatchDirectives(t *testing.T) {
	workflow := workflowspec.Workflow{
		APIVersion: workflowspec.APIVersionV1Alpha1,
		Kind:       workflowspec.KindWorkflow,
		Metadata:   workflowspec.Metadata{Name: "hybrid"},
		Spec:       workflowspec.Spec{Defaults: &workflowspec.Defaults{Partition: "compute", QoS: "normal"}},
	}
	task := workflowspec.Task{
		Name: "hybrid-run", Launch: workflowspec.LaunchSrun,
		Resources: workflowspec.TaskResources{
			Nodes: 2, TasksPerNode: 4, CPUsPerTask: 5,
			Memory: "4096Mi", Walltime: "1-02:03:04",
			GPU:      &workflowspec.GPURequest{Type: "a100", Count: 2},
			Licenses: []string{"solver:1"}, Constraints: "avx2", Exclusive: true,
		},
		Array:  &workflowspec.ArraySpecYAML{Start: 1, End: 9, Step: 2, MaxConcurrent: 3},
		Stdout: "out-%A_%a.log", Stderr: "err-%j.log",
		WorkingDirectory: "/scratch/hybrid",
		Env:              map[string]string{"OMP_NUM_THREADS": "{{ task.resources.cpusPerTask }}"},
		Software:         []workflowspec.SoftwareRequirement{{Name: "gcc", Version: "default"}, {Name: "openmpi", Version: "default"}},
		Command:          []string{"./hybrid"},
	}
	exported, err := sbatchexport.Render(workflow, task, nil)
	if err != nil {
		t.Fatal(err)
	}
	validateBash(t, exported)
	if !strings.Contains(exported, "#!/bin/bash\n") || !strings.Contains(exported, "exec srun './hybrid'") {
		t.Fatalf("hybrid launch/shebang missing:\n%s", exported)
	}
	if strings.Contains(exported, "--ntasks=") {
		t.Fatalf("srun should inherit nodes x tasksPerNode when tasks is zero:\n%s", exported)
	}
	if !strings.Contains(exported, "#SBATCH --nodes=2") || !strings.Contains(exported, "#SBATCH --ntasks-per-node=4") ||
		!strings.Contains(exported, "#SBATCH --cpus-per-task=5") || !strings.Contains(exported, "#SBATCH --mem=4096M") ||
		!strings.Contains(exported, "#SBATCH --array=1-9:2%3") || !strings.Contains(exported, "#SBATCH --partition=compute") ||
		!strings.Contains(exported, "#SBATCH --qos=normal") {
		t.Fatalf("missing Slurm directives:\n%s", exported)
	}
	firstCommand := strings.Index(exported, "export OMP_NUM_THREADS")
	lastDirective := strings.LastIndex(exported, "#SBATCH ")
	if firstCommand < 0 || lastDirective > firstCommand {
		t.Fatalf("SBATCH directives must precede command lines:\n%s", exported)
	}
	scan, err := sbatchscan.Scan([]byte(exported), workflowspec.LanguageBash)
	if err != nil || len(scan.Directives) < 10 {
		t.Fatalf("scanner did not accept exported directives: directives=%d err=%v", len(scan.Directives), err)
	}
	for _, directive := range scan.Directives {
		if !directive.Honored {
			t.Errorf("directive line %d was not honored", directive.Line)
		}
	}
	proposal, err := sbatchimport.Import([]byte(exported), workflowspec.LanguageBash)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Diagnostics) != 0 {
		t.Fatalf("round-trip diagnostics: %+v", proposal.Diagnostics)
	}
	want, resourceErrors := task.ResolveResources("resources")
	if len(resourceErrors) != 0 {
		t.Fatalf("test resource request invalid: %+v", resourceErrors)
	}
	want.Array = task.Array.Normalized()
	if !reflect.DeepEqual(proposal.Resources, want) {
		t.Fatalf("round-trip resources = %+v, want %+v", proposal.Resources, want)
	}
	if proposal.Partition != "compute" || proposal.QoS != "normal" || proposal.Stdout != task.Stdout ||
		proposal.Stderr != task.Stderr || proposal.WorkingDir != task.WorkingDirectory {
		t.Fatalf("round-trip placement/io differs: %+v", proposal)
	}
	if time.Duration(proposal.Resources.Walltime) != 26*time.Hour+3*time.Minute+4*time.Second {
		t.Fatalf("walltime round-trip = %s", time.Duration(proposal.Resources.Walltime))
	}
}

func TestRenderMemoryPerCPU(t *testing.T) {
	workflow := workflowspec.Workflow{Metadata: workflowspec.Metadata{Name: "mem-per-cpu"}}
	task := workflowspec.Task{
		Name:      "run",
		Resources: workflowspec.TaskResources{MemoryPerCPU: "512Mi", Walltime: "5m"},
		Command:   []string{"./run"},
	}
	exported, err := sbatchexport.Render(workflow, task, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exported, "#SBATCH --mem-per-cpu=512M") {
		t.Fatalf("memory-per-cpu directive missing:\n%s", exported)
	}
	validateBash(t, exported)
	proposal, err := sbatchimport.Import([]byte(exported), workflowspec.LanguageBash)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Resources.MemoryPerCPUMiB != 512 {
		t.Fatalf("round-trip memory per cpu = %d MiB, want 512", proposal.Resources.MemoryPerCPUMiB)
	}
}

func TestRenderServiceTaskAndServiceDependent(t *testing.T) {
	workflow := workflowspec.Workflow{Metadata: workflowspec.Metadata{Name: "services"}}
	service := workflowspec.Task{
		Name: "db", Service: &workflowspec.ServiceTask{},
		Resources: workflowspec.TaskResources{Walltime: "2h"},
		Command:   []string{"postgres", "-D", "/var/lib/pg"},
	}
	exported, err := sbatchexport.Render(workflow, service, nil)
	if err != nil {
		t.Fatalf("service export: %v", err)
	}
	if !strings.Contains(exported, "# Custos service lifecycle and autoStop behavior are not represented") {
		t.Fatalf("service comment missing:\n%s", exported)
	}
	dependent := workflowspec.Task{
		Name: "client", DependsOn: []string{"db"},
		Resources: workflowspec.TaskResources{Walltime: "30m"},
		Command:   []string{"./client"},
	}
	exported, err = sbatchexport.Render(workflow, dependent, nil)
	if err != nil {
		t.Fatalf("service-dependent task export: %v", err)
	}
	if !strings.Contains(exported, "# Custos dependencies are not represented: db") {
		t.Fatalf("service dependency comment missing:\n%s", exported)
	}
}

func TestRenderQuotesLiteralsAndParameterDefaults(t *testing.T) {
	workflow := workflowspec.Workflow{
		APIVersion: workflowspec.APIVersionV1Alpha1,
		Kind:       workflowspec.KindWorkflow,
		Metadata:   workflowspec.Metadata{Name: "quote-test"},
		Spec: workflowspec.Spec{
			Parameters: map[string]workflowspec.Parameter{
				"input":    {Type: "string", Required: true},
				"fallback": {Type: "string", Default: "a b"},
			},
		},
	}
	task := workflowspec.Task{
		Name: "run", Resources: workflowspec.TaskResources{CPU: 2, Walltime: "5m"},
		Command: []string{"./run", "{{ parameters.input }}", `x'; rm -rf / #`, "{{ task.resources.cpu }}"},
	}
	exported, err := sbatchexport.Render(workflow, task, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exported, `input="${input:?required parameter input}"`) {
		t.Fatalf("required parameter guard missing:\n%s", exported)
	}
	if !strings.Contains(exported, `fallback="${fallback:-a b}"`) {
		t.Fatalf("parameter default was not safely quoted:\n%s", exported)
	}
	if !strings.Contains(exported, `'x'\''; rm -rf / #'`) || !strings.Contains(exported, `"${input}"`) || !strings.Contains(exported, " 2") {
		t.Fatalf("literal or template rendering missing:\n%s", exported)
	}
	validateBash(t, exported)
}

func TestRenderScriptSrunUsesCollisionFreeTempBody(t *testing.T) {
	workflow := workflowspec.Workflow{Metadata: workflowspec.Metadata{Name: "script"}}
	task := workflowspec.Task{
		Name: "solve", Launch: workflowspec.LaunchSrun,
		Resources: workflowspec.TaskResources{Nodes: 2, TasksPerNode: 4, Walltime: "30m"},
		Script:    &workflowspec.ScriptRef{Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Language: workflowspec.LanguageBash},
	}
	body := []byte("#!/bin/bash\necho CUSTOS_EXPORTED_SCRIPT\n")
	exported, err := sbatchexport.Render(workflow, task, body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exported, `cat > "$f" <<'CUSTOS_EXPORTED_SCRIPT_1'`) ||
		!strings.Contains(exported, "\nsrun bash \"$f\"") || strings.Contains(exported, "exec srun") ||
		strings.Contains(exported, "--ntasks=") ||
		strings.Contains(strings.TrimPrefix(exported, "#!/bin/bash\n"), "#!/bin/bash") {
		t.Fatalf("srun script export did not safely stage the payload:\n%s", exported)
	}
	validateBash(t, exported)
}

func TestRenderPythonShebangAndScript(t *testing.T) {
	workflow := workflowspec.Workflow{Metadata: workflowspec.Metadata{Name: "python"}}
	task := workflowspec.Task{
		Name: "script", Script: &workflowspec.ScriptRef{Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Language: workflowspec.LanguagePython},
	}
	exported, err := sbatchexport.Render(workflow, task, []byte("#!/usr/bin/env python3\nprint('ok')\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(exported, "#!/usr/bin/env python3\n") || !strings.Contains(exported, "print('ok')") {
		t.Fatalf("Python export missing language shebang or body:\n%s", exported)
	}
}

func TestRenderVersionRequiresContext(t *testing.T) {
	workflow := workflowspec.Workflow{Metadata: workflowspec.Metadata{Name: "versioned"}}
	task := workflowspec.Task{Name: "run", Command: []string{"./run", "{{ run.version }}"}}
	if _, err := sbatchexport.Render(workflow, task, nil); err == nil {
		t.Fatal("run.version without context must be rejected")
	}
	version := 7
	rendered, err := sbatchexport.RenderWithOptions(workflow, task, nil,
		sbatchexport.RenderOptions{WorkflowVersion: &version})
	if err != nil || !strings.Contains(rendered, " 7") {
		t.Fatalf("run.version = %s, err=%v", rendered, err)
	}
	validateBash(t, rendered)
}

func TestRenderInlineScriptAffinityAndUnsupportedExecutionContext(t *testing.T) {
	workflow := workflowspec.Workflow{Metadata: workflowspec.Metadata{Name: "inline"}}
	task := workflowspec.Task{
		Name: "run", Launch: workflowspec.LaunchSrun,
		Resources: workflowspec.TaskResources{CPUAffinity: "numa", Walltime: "5m"},
		Script:    &workflowspec.ScriptRef{Inline: "#!/bin/sh\necho inline\n"},
	}
	exported, err := sbatchexport.Render(workflow, task, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(exported, "#!/bin/sh\n") || !strings.Contains(exported, "echo inline") ||
		!strings.Contains(exported, "srun --cpu-bind=ldoms") {
		t.Fatalf("inline script or CPU affinity export missing:\n%s", exported)
	}
	validateBash(t, exported)

	for _, unsupportedTask := range []workflowspec.Task{
		{Name: "image", Image: &workflowspec.Image{URI: "oras://registry/image.sif"}, Command: []string{"true"}},
		{Name: "image-pull-secret", Image: &workflowspec.Image{
			URI: "docker://registry.example.com/team/app:1.2",
			PullSecret: &workflowspec.ImagePullSecret{
				Username: "robot$ci", PasswordSecret: "registry-token",
			},
		}, Command: []string{"true"}},
		{Name: "multi", Multinode: &workflowspec.Multinode{Nodes: 2, Implementation: "generic"}, Command: []string{"true"}},
	} {
		_, err := sbatchexport.Render(workflow, unsupportedTask, nil)
		var unsupported *sbatchexport.UnsupportedError
		if !errors.As(err, &unsupported) || unsupported.Field == "" {
			t.Errorf("%s: export error = %v, want field-scoped EXPORT_UNSUPPORTED", unsupportedTask.Name, err)
		}
	}
}

func TestRenderRejectsUnrepresentableReferences(t *testing.T) {
	workflow := workflowspec.Workflow{Metadata: workflowspec.Metadata{Name: "unsupported"}}
	for _, value := range []string{"{{ tasks.other.state }}", "{{ parameters.n + 1 }}", "{{ secrets.token }}"} {
		task := workflowspec.Task{Name: "run", Command: []string{"./run", value}}
		_, err := sbatchexport.Render(workflow, task, nil)
		var unsupported *sbatchexport.UnsupportedError
		if !errors.As(err, &unsupported) || unsupported.Field != "spec.tasks.run.command[1]" {
			t.Errorf("%s: error=%v, want EXPORT_UNSUPPORTED field", value, err)
		}
	}
	_, err := sbatchexport.Render(workflow, workflowspec.Task{Name: "when", Type: "condition"}, nil)
	var unsupported *sbatchexport.UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("condition export error = %v, want EXPORT_UNSUPPORTED", err)
	}
}
