// Package validate implements the backend validation pipeline of
// docs/workflows.md §Validation. Static covers steps 1-4 (schema,
// names, graph, expressions); Contextual covers steps 5-8 against a
// caller-supplied Context (policy, cluster bindings, secret
// availability). Every check returns all errors, path-addressed.
package validate

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/expr"
)

// FieldError locates one spec problem; Path is dot/bracket addressed
// ("spec.tasks[2].resources.walltime").
type FieldError = workflowspec.FieldError

// fe builds a FieldError without tripping the unkeyed-composite vet check.
func fe(path, code, msg string) FieldError {
	return FieldError{Path: path, Code: code, Message: msg}
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var envNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
var imageURIRe = regexp.MustCompile(`^(?:(?:` +
	regexp.QuoteMeta(workflowspec.ImageSchemeDocker) + `|` + regexp.QuoteMeta(workflowspec.ImageSchemeORAS) +
	`)[A-Za-z0-9._/:@+-]+|/[A-Za-z0-9._/+-]+)$`)
var imageDigestRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
var multinodeDollarRefRe = regexp.MustCompile(`\$\{?(MULTINODE_HOSTLIST|MULTINODE_HOSTLIST_NOSLOTS|MULTINODE_TOTAL_SLOTS|MULTINODE_NODE_IP|MULTINODE_SSH_WRAPPER|MULTINODE_RSH_WRAPPER)\}?`)

const maxMultinodeNodes = 10000

func validImageURI(uri string) bool {
	if len(uri) == 0 || len(uri) > 512 || !imageURIRe.MatchString(uri) {
		return false
	}
	if strings.Contains(uri, "@") && (strings.Count(uri, "@") != 1 || !imageDigestRe.MatchString(uri)) {
		return false
	}
	for _, segment := range strings.Split(uri, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

func validPullUsername(username string) bool {
	if utf8.RuneCountInString(username) > 256 {
		return false
	}
	for _, r := range username {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return username != ""
}

func checkImagePullSecret(task workflowspec.Task, base string,
	secrets map[string]workflowspec.SecretUse, used map[string]bool,
) []FieldError {
	if task.Image == nil || task.Image.PullSecret == nil {
		return nil
	}
	pull := task.Image.PullSecret
	path := base + ".image.pullSecret"
	var errs []FieldError
	if task.Image.URI == "" {
		errs = append(errs, fe(path, "PULL_SECRET_REQUIRES_IMAGE",
			"pullSecret requires an image URI"))
	} else if !workflowspec.HasRegistryScheme(task.Image.URI) {
		errs = append(errs, fe(path, "PULL_SECRET_INVALID",
			"pullSecret requires a docker:// or oras:// image URI"))
	}
	if (pull.Username == "") == (pull.UsernameSecret == "") {
		errs = append(errs, fe(path, "PULL_SECRET_INVALID",
			"exactly one of username or usernameSecret is required"))
	}
	if pull.Username != "" && !validPullUsername(pull.Username) {
		errs = append(errs, fe(path+".username", "PULL_SECRET_INVALID",
			"literal username must be at most 256 characters with no whitespace or control characters"))
	}
	if pull.PasswordSecret == "" {
		errs = append(errs, fe(path+".passwordSecret", "PULL_SECRET_INVALID",
			"passwordSecret is required"))
	}
	for _, ref := range []struct{ field, handle string }{
		{"usernameSecret", pull.UsernameSecret},
		{"passwordSecret", pull.PasswordSecret},
	} {
		field, handle := ref.field, ref.handle
		if handle == "" {
			continue
		}
		used[handle] = true
		use, ok := secrets[handle]
		if !ok || use.Use != "image_pull" {
			errs = append(errs, fe(path+"."+field, "PULL_SECRET_INVALID",
				"secret handle must be declared with use: image_pull"))
		}
	}
	multiNode := task.Multinode != nil || task.Resources.Nodes > 1
	if !multiNode {
		if resolved, resourceErrs := task.ResolveResources(base + ".resources"); len(resourceErrs) == 0 {
			multiNode = resolved.Nodes > 1
		}
	}
	if multiNode {
		errs = append(errs, fe(path, "PULL_SECRET_MULTINODE_UNSUPPORTED",
			"pullSecret is only supported for single-node tasks"))
	}
	return errs
}

func multinodeImplementation(task workflowspec.Task) string {
	if task.Multinode == nil {
		return ""
	}
	return task.Multinode.EffectiveImplementation()
}

func genericMultinode(task workflowspec.Task) bool {
	return task.Multinode != nil && multinodeImplementation(task) == workflowspec.MultinodeGeneric
}

// Task types of docs/workflows.md §Task types.
const (
	TypeBatch       = "batch"
	TypeMPI         = "mpi"
	TypeGPU         = "gpu"
	TypeArray       = "array"
	TypeShell       = "shell"
	TypeStageIn     = "stageIn"
	TypeStageOut    = "stageOut"
	TypeInteractive = "interactive"
	TypeCondition   = "condition"
)

var taskTypes = map[string]bool{
	TypeBatch: true, TypeMPI: true, TypeGPU: true, TypeArray: true,
	TypeShell: true, TypeStageIn: true, TypeStageOut: true,
	TypeInteractive: true, TypeCondition: true,
}

var reservedTypes = map[string]bool{
	TypeStageIn: true, TypeStageOut: true, TypeInteractive: true,
}

var retryStates = map[string]bool{
	"FAILED": true, "NODE_FAIL": true, "TIMEOUT": true,
}

// emptyResources reports whether a task declares no resource fields.
func emptyResources(r workflowspec.TaskResources) bool {
	return r.Empty()
}

// Static runs steps 1-4: document shape, names, graph, and
// expression/reference resolution.
func Static(w workflowspec.Workflow) []FieldError {
	var errs []FieldError
	errs = append(errs, checkDocument(w)...)
	errs = append(errs, checkNames(w)...)
	cycle, depErrs := checkGraph(w)
	errs = append(errs, depErrs...)
	errs = append(errs, checkServiceClusterPlacements(w)...)
	errs = append(errs, checkShapes(w)...)
	errs = append(errs, checkExprs(w, cycle)...)
	return errs
}

func explicitTaskCluster(w workflowspec.Workflow, task workflowspec.Task) string {
	if task.Placement != nil && task.Placement.Cluster != "" {
		return task.Placement.Cluster
	}
	if w.Spec.Placement != nil {
		return w.Spec.Placement.Cluster
	}
	return ""
}

func checkServiceClusterPlacements(w workflowspec.Workflow) []FieldError {
	byName := make(map[string]workflowspec.Task, len(w.Spec.Tasks))
	for _, task := range w.Spec.Tasks {
		byName[task.Name] = task
	}
	var errs []FieldError
	for i, dependent := range w.Spec.Tasks {
		for k, dependencyName := range dependent.DependsOn {
			service := byName[dependencyName]
			if service.Service == nil {
				continue
			}
			serviceCluster := explicitTaskCluster(w, service)
			dependentCluster := explicitTaskCluster(w, dependent)
			if serviceCluster != "" && dependentCluster != "" &&
				serviceCluster != dependentCluster {
				errs = append(errs, fe(
					fmt.Sprintf("spec.tasks[%d].dependsOn[%d]", i, k),
					"SERVICE_CLUSTER_MISMATCH",
					"service and dependent tasks must use the same Slurm cluster"))
			}
		}
	}
	return errs
}

// --- step 1: document ------------------------------------------------------

func checkDocument(w workflowspec.Workflow) []FieldError {
	var errs []FieldError
	if w.APIVersion != workflowspec.APIVersionV1Alpha1 {
		errs = append(errs, fe("apiVersion", "API_VERSION",
			"apiVersion must be "+workflowspec.APIVersionV1Alpha1))
	}
	if w.Kind != workflowspec.KindWorkflow {
		errs = append(errs, fe("kind", "KIND",
			"kind must be Workflow"))
	}
	if w.Metadata.Name == "" {
		errs = append(errs, fe("metadata.name", "NAME_REQUIRED",
			"metadata.name is required"))
	}
	if len(w.Spec.Tasks) == 0 {
		errs = append(errs, fe("spec.tasks", "TASKS_REQUIRED",
			"at least one task is required"))
	}
	if e := w.Spec.Execution; e != nil {
		if !slices.Contains([]string{"auto", "engine", "native"},
			e.Strategy) && e.Strategy != "" {
			errs = append(errs, fe("spec.execution.strategy",
				"EXECUTION_STRATEGY", "strategy must be auto|engine|native"))
		}
		if e.FailurePolicy != "" && e.FailurePolicy != "fail" &&
			e.FailurePolicy != "continue" {
			errs = append(errs, fe("spec.execution.failurePolicy",
				"EXECUTION_FAILURE_POLICY",
				"failurePolicy must be fail|continue"))
		}
	}
	if p := w.Spec.Placement; p != nil && len(p.Requirements) > 0 {
		errs = append(errs, fe("spec.placement.requirements",
			"PLACEMENT_REQUIREMENTS_UNSUPPORTED",
			"requirement-based placement is reserved for a later version"))
	}
	envNames := map[string]string{}
	serviceNames := map[string]string{}
	for i, task := range w.Spec.Tasks {
		if task.Service != nil {
			suffix := workflowspec.ServiceEnvSuffix(task.Name)
			if previous, ok := serviceNames[suffix]; ok {
				errs = append(errs, fe(fmt.Sprintf("spec.tasks[%d].name", i),
					"SERVICE_NAME_COLLISION",
					fmt.Sprintf("service name collides with task %q after environment-name normalization", previous)))
			} else {
				serviceNames[suffix] = task.Name
			}
		}
		for name := range task.Env {
			if strings.HasPrefix(name, "CUSTOS_SECRET_") ||
				strings.HasPrefix(name, "CUSTOS_SERVICE_") ||
				name == "CUSTOS_BAO_ADDR" ||
				name == "CUSTOS_BAO_NAMESPACE" ||
				name == admission.ImagePullUsernameEnvName ||
				name == admission.ImagePullPasswordEnvName {
				errs = append(errs, fe(fmt.Sprintf("spec.tasks[%d].env.%s", i, name),
					"SECRET_ENV_CONTROLLED", "environment name is reserved for secret delivery"))
			}
		}
	}
	if w.Spec.Defaults != nil {
		for name := range w.Spec.Defaults.Env {
			if strings.HasPrefix(name, "CUSTOS_SERVICE_") ||
				name == admission.ImagePullUsernameEnvName ||
				name == admission.ImagePullPasswordEnvName {
				errs = append(errs, fe("spec.defaults.env."+name,
					"SECRET_ENV_CONTROLLED", "environment name is reserved for image pull secret delivery"))
			}
		}
	}
	usedImagePullSecrets := map[string]bool{}
	for i, task := range w.Spec.Tasks {
		errs = append(errs, checkImagePullSecret(task,
			fmt.Sprintf("spec.tasks[%d]", i), w.Spec.Secrets,
			usedImagePullSecrets)...)
	}
	for handle, use := range w.Spec.Secrets {
		base := "spec.secrets." + handle
		if use.Ref == "" {
			errs = append(errs, fe(base+".ref", "SECRET_REFERENCE_REQUIRED",
				"secret reference name is required"))
		}
		if use.Use != "env" && use.Use != "wrapped_token" && use.Use != "image_pull" {
			errs = append(errs, fe(base+".use", "SECRET_USE_INVALID",
				"secret use must be env, wrapped_token, or image_pull"))
			continue
		}
		if use.Use != "env" && use.EnvName != "" {
			errs = append(errs, fe(base+".envName", "SECRET_ENV_NAME_UNUSED",
				"envName is only valid with use: env"))
			continue
		}
		if use.Use == "image_pull" && !usedImagePullSecrets[handle] {
			errs = append(errs, fe(base+".use", "SECRET_USE_UNUSED",
				"image_pull secret handles must be referenced by an image.pullSecret"))
		}
		if use.Use != "env" {
			continue
		}
		name := workflowspec.SecretEnvName(handle, use)
		if !envNameRe.MatchString(name) {
			errs = append(errs, fe(base+".envName", "SECRET_ENV_NAME_INVALID",
				"secret envName must match ^[A-Z_][A-Z0-9_]*$"))
		}
		if strings.HasPrefix(name, "CUSTOS_") {
			errs = append(errs, fe(base+".envName", "SECRET_ENV_CONTROLLED",
				"secret envName collides with a Custos-controlled variable"))
		}
		if previous, ok := envNames[name]; ok {
			errs = append(errs, fe(base+".envName", "SECRET_ENV_COLLISION",
				"secret envName also belongs to "+previous))
		} else {
			envNames[name] = handle
		}
	}
	isHandle := func(value, handle string) bool {
		tpl, err := expr.ParseTemplate(value)
		if err != nil {
			return false
		}
		e, ok := tpl.SoleExpr()
		if !ok {
			return false
		}
		p, ok := e.SoleRef()
		return ok && len(p) == 2 && p[0] == "secrets" && p[1] == handle
	}
	for name, handle := range envNames {
		for i, task := range w.Spec.Tasks {
			if value, exists := task.Env[name]; exists && !isHandle(value, handle) {
				errs = append(errs, fe(fmt.Sprintf("spec.tasks[%d].env.%s", i, name),
					"SECRET_ENV_COLLISION", "environment name collides with secret "+handle))
			}
		}
		if w.Spec.Defaults != nil {
			if value, exists := w.Spec.Defaults.Env[name]; exists && !isHandle(value, handle) {
				errs = append(errs, fe("spec.defaults.env."+name,
					"SECRET_ENV_COLLISION", "environment name collides with secret "+handle))
			}
		}
	}
	return errs
}

// --- step 2: names ----------------------------------------------------------

func checkNames(w workflowspec.Workflow) []FieldError {
	var errs []FieldError
	seen := map[string]string{}
	check := func(name, path, what string) {
		if name == "" {
			errs = append(errs, fe(path, "NAME_REQUIRED",
				what+" name is required"))
			return
		}
		if len(name) > 63 || !nameRe.MatchString(name) {
			errs = append(errs, fe(path, "NAME_INVALID",
				what+" name must be DNS-label-like and <=63 chars"))
			return
		}
		if prev, dup := seen[name]; dup {
			errs = append(errs, fe(path, "NAME_DUPLICATE",
				fmt.Sprintf("name %q already used by %s", name, prev)))
			return
		}
		seen[name] = what + " " + path
	}
	for name := range w.Spec.Parameters {
		check(name, "spec.parameters."+name, "parameter")
	}
	for i, t := range w.Spec.Tasks {
		check(t.Name, fmt.Sprintf("spec.tasks[%d].name", i), "task")
	}
	for h := range w.Spec.Secrets {
		if !nameRe.MatchString(h) || len(h) > 63 {
			errs = append(errs, fe("spec.secrets."+h,
				"NAME_INVALID", "secret handle must be DNS-label-like"))
		}
	}
	return errs
}

// --- step 3: graph ----------------------------------------------------------

// checkGraph validates dependsOn and returns the set of tasks involved
// in a cycle (used to suppress noisy expression errors).
func checkGraph(w workflowspec.Workflow) (map[string]bool, []FieldError) {
	var errs []FieldError
	names := map[string]bool{}
	for _, t := range w.Spec.Tasks {
		names[t.Name] = true
	}
	deps := map[string][]string{}
	indeg := map[string]int{}
	for i, t := range w.Spec.Tasks {
		path := fmt.Sprintf("spec.tasks[%d].dependsOn", i)
		for _, d := range t.DependsOn {
			if d == t.Name {
				errs = append(errs, fe(path, "GRAPH_SELF_DEP",
					"task cannot depend on itself"))
				continue
			}
			if !names[d] {
				errs = append(errs, fe(path, "GRAPH_UNKNOWN_DEP",
					"dependsOn target "+d+" does not exist"))
				continue
			}
			deps[d] = append(deps[d], t.Name)
			indeg[t.Name]++
		}
	}
	// Kahn's algorithm; anything left is on/depends on a cycle.
	var queue []string
	for _, t := range w.Spec.Tasks {
		if indeg[t.Name] == 0 {
			queue = append(queue, t.Name)
		}
	}
	done := map[string]bool{}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		done[n] = true
		for _, m := range deps[n] {
			indeg[m]--
			if indeg[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	cyclic := map[string]bool{}
	var cyc []string
	for _, t := range w.Spec.Tasks {
		if !done[t.Name] {
			cyclic[t.Name] = true
			cyc = append(cyc, t.Name)
		}
	}
	if len(cyc) > 0 {
		slices.Sort(cyc)
		errs = append(errs, fe("spec.tasks", "GRAPH_CYCLE",
			"dependency cycle involving: "+strings.Join(cyc, ", ")))
	}
	return cyclic, errs
}

// --- task shapes ------------------------------------------------------------

func slurmBacked(t workflowspec.Task) bool {
	ty := t.Type
	return ty == "" || ty == TypeBatch || ty == TypeMPI || ty == TypeGPU ||
		ty == TypeArray || ty == TypeShell
}

func checkTaskFeatures(t workflowspec.Task, base string, ty string) []FieldError {
	var errs []FieldError
	if t.Script != nil {
		hasRef := t.Script.Digest != ""
		hasInline := t.Script.HasInline()
		if hasRef == hasInline {
			errs = append(errs, fe(base+".script", "SCRIPT_SOURCE", "script must specify exactly one of ref or inline"))
		}
		if hasRef {
			if _, err := validation.ParseDigest(t.Script.Digest); err != nil {
				errs = append(errs, fe(base+".script.ref", "SCRIPT_REF", "script ref must be sha256:<64 lowercase hex>"))
			}
			if t.Script.Language == "" {
				errs = append(errs, fe(base+".script.language", "SCRIPT_LANGUAGE_INVALID", "referenced scripts require a language"))
			}
		}
		if hasInline {
			if t.Script.Inline == "" {
				errs = append(errs, fe(base+".script", "SCRIPT_SOURCE", "inline script cannot be empty"))
			}
			if len([]byte(t.Script.Inline)) > validation.DefaultLimits.MaxScriptBytes {
				errs = append(errs, fe(base+".script.inline", "SCRIPT_INLINE_TOO_LARGE", "inline script exceeds the stored script size limit"))
			}
			language := t.Script.EffectiveLanguage()
			if language != workflowspec.LanguageBash && language != workflowspec.LanguageSh && language != workflowspec.LanguagePython {
				errs = append(errs, fe(base+".script.language", "SCRIPT_LANGUAGE_INVALID", "inline scripts support bash, sh, or python"))
			}
		}
	}
	if t.Image != nil {
		if !slurmBacked(t) || ty == TypeCondition {
			errs = append(errs, fe(base+".image", "IMAGE_TASK_KIND", "image is only valid on Slurm-backed tasks"))
		}
		if !validImageURI(t.Image.URI) {
			errs = append(errs, fe(base+".image.uri", "IMAGE_URI_INVALID", "image URI must be a safe docker://, oras://, or absolute path reference"))
		}
	}
	if t.Multinode != nil {
		m := t.Multinode
		if !slurmBacked(t) || ty == TypeCondition {
			errs = append(errs, fe(base+".multinode", "MULTINODE_TASK_KIND", "multinode is only valid on Slurm-backed tasks"))
		}
		if m.Nodes < 1 || m.Nodes > maxMultinodeNodes {
			errs = append(errs, fe(base+".multinode.nodes", "MULTINODE_INVALID", "nodes must be between 1 and 10000"))
		}
		implementation := m.EffectiveImplementation()
		if m.Implementation != workflowspec.MultinodeOpenMPI && m.Implementation != workflowspec.MultinodeMPICH &&
			m.Implementation != workflowspec.MultinodeGeneric {
			errs = append(errs, fe(base+".multinode.implementation", "MULTINODE_INVALID", "implementation must be openmpi, mpich, or generic"))
		}
		if m.ProcsPerNode < 0 || (m.HasProcsPerNode() && m.ProcsPerNode < 1) {
			errs = append(errs, fe(base+".multinode.procsPerNode", "MULTINODE_INVALID", "procsPerNode must be >= 1 when set"))
		}
		if t.Array != nil || ty == TypeArray || t.Resources.Nodes != 0 ||
			t.Resources.Tasks != 0 || t.Resources.TasksPerNode != 0 ||
			t.Resources.CPUsPerTask != 0 {
			errs = append(errs, fe(base+".multinode", "MULTINODE_CONFLICT", "multinode conflicts with array or explicit nodes/tasks/tasksPerNode/cpusPerTask resources"))
		}
		wantLaunch := workflowspec.LaunchSbatch
		if implementation == workflowspec.MultinodeOpenMPI || implementation == workflowspec.MultinodeMPICH {
			wantLaunch = workflowspec.LaunchSrun
		}
		if t.Launch != "" && (t.Launch == workflowspec.LaunchSbatch || t.Launch == workflowspec.LaunchSrun) && t.Launch != wantLaunch {
			errs = append(errs, fe(base+".launch", "MULTINODE_LAUNCH", "explicit launch conflicts with multinode implementation"))
		}
		cores := t.Resources.CPU
		if cores <= 0 {
			cores = 1
		}
		procs := m.ProcsPerNode
		if procs <= 0 {
			procs = cores
		}
		if (implementation == workflowspec.MultinodeOpenMPI || implementation == workflowspec.MultinodeMPICH) &&
			(procs > cores || cores%procs != 0) {
			errs = append(errs, fe(base+".multinode.procsPerNode", "MULTINODE_CPU_DIVISIBLE", "CPU cores per node must divide evenly by procsPerNode"))
		}
		maxInt := int(^uint(0) >> 1)
		if procs > 0 && m.Nodes > maxInt/procs {
			errs = append(errs, fe(base+".multinode", "MULTINODE_INVALID", "total multinode slot count overflows"))
		}
	}
	if t.Service != nil {
		errs = append(errs, checkServiceTask(t, base, ty)...)
	}
	if affinity := t.Resources.CPUAffinity; affinity != "" {
		if affinity != "none" && affinity != "core" && affinity != "socket" && affinity != "numa" {
			errs = append(errs, fe(base+".resources.cpuAffinity", "CPU_AFFINITY_INVALID", "cpuAffinity must be none, core, socket, or numa"))
		} else if !slurmBacked(t) {
			errs = append(errs, fe(base+".resources.cpuAffinity", "CPU_AFFINITY_TASK_KIND", "cpuAffinity is only valid on Slurm-backed tasks"))
		}
	}
	return errs
}

func checkServiceTask(t workflowspec.Task, base, ty string) []FieldError {
	var errs []FieldError
	if !slurmBacked(t) || ty == TypeCondition || ty == TypeArray || ty == "approval" {
		errs = append(errs, fe(base+".service", "SERVICE_FIELD_UNSUPPORTED",
			"service tasks require a Slurm-backed task kind"))
	}
	if strings.TrimSpace(t.Resources.Walltime) == "" {
		errs = append(errs, fe(base+".resources.walltime",
			"SERVICE_WALLTIME_REQUIRED", "service tasks require resources.walltime"))
	}
	for _, field := range []struct {
		name string
		set  bool
	}{
		{"array", t.Array != nil},
		{"fanOut", t.FanOut != nil},
		{"retry", t.Retry != nil},
		{"when", t.When != ""},
		{"outputs", t.Outputs != nil},
	} {
		if field.set {
			errs = append(errs, fe(base+"."+field.name,
				"SERVICE_FIELD_UNSUPPORTED",
				field.name+" is not supported on service tasks"))
		}
	}
	return errs
}

func checkShapes(w workflowspec.Workflow) []FieldError {
	var errs []FieldError
	for i, t := range w.Spec.Tasks {
		base := fmt.Sprintf("spec.tasks[%d]", i)
		if t.Resources.MemoryPerCPU != "" &&
			(t.Resources.Memory != "" || t.Resources.MemoryPerNode != "") {
			errs = append(errs, fe(base+".resources.memoryPerCpu",
				"MEMORY_CONFLICT",
				"memoryPerCpu is mutually exclusive with memory and memoryPerNode"))
		}
		ty := t.Type
		if ty == "" {
			ty = TypeBatch
		}
		if t.Launch != "" && t.Launch != workflowspec.LaunchSbatch && t.Launch != workflowspec.LaunchSrun {
			errs = append(errs, fe(base+".launch", "LAUNCH_INVALID",
				"launch must be sbatch or srun"))
		} else if ty == TypeCondition && t.Launch != "" {
			errs = append(errs, fe(base+".launch", "LAUNCH_INVALID",
				"condition tasks cannot select a Slurm launch mode"))
		}
		errs = append(errs, checkTaskFeatures(t, base, ty)...)
		switch {
		case !taskTypes[ty]:
			errs = append(errs, fe(base+".type",
				"TASK_TYPE_UNKNOWN", "unknown task type "+t.Type))
			continue
		case reservedTypes[ty]:
			errs = append(errs, fe(base+".type",
				"TASK_TYPE_RESERVED",
				"task type "+ty+" is reserved for a later milestone"))
			continue
		}
		switch ty {
		case TypeCondition:
			if t.When == "" {
				errs = append(errs, fe(base+".when",
					"CONDITION_WHEN_REQUIRED",
					"condition tasks require a when expression"))
			}
			if len(t.Command) > 0 || t.Script != nil || t.Image != nil ||
				t.Multinode != nil || !emptyResources(t.Resources) {
				errs = append(errs, fe(base,
					"CONDITION_HAS_WORKLOAD",
					"condition tasks run no Slurm job: no command, script or resources"))
			}
		case TypeShell:
			if len(t.Command) > 0 || t.Script == nil {
				errs = append(errs, fe(base+".script",
					"SHELL_SCRIPT_REQUIRED",
					"shell tasks carry a script, never a command"))
			}
		default: // batch, mpi, gpu, array
			if (len(t.Command) > 0) == (t.Script != nil) {
				errs = append(errs, fe(base,
					"WORKLOAD_XOR",
					"exactly one of command or script is required"))
			}
		}
		if t.OnDependencyFailure != "" &&
			!slices.Contains([]string{"fail", "run"}, t.OnDependencyFailure) {
			errs = append(errs, fe(base+".onDependencyFailure",
				"ON_DEP_FAILURE", "must be fail|run"))
		}
		if t.FanOut != nil && t.FanOut.From != "" {
			errs = append(errs, fe(base+".fanOut.from",
				"FANOUT_FROM_UNSUPPORTED",
				"dynamic fan-out from task outputs lands in a later milestone"))
		}
		if t.Array != nil && !slurmBacked(t) {
			errs = append(errs, fe(base+".array",
				"ARRAY_ONLY_ON_SLURM_TASK",
				"array spec is only valid on Slurm-backed tasks"))
		}
		if ty == TypeArray && t.Array == nil {
			errs = append(errs, fe(base+".array",
				"ARRAY_REQUIRED", "type array requires an array spec"))
		}
		if r := t.Retry; r != nil {
			for _, s := range r.On {
				if !retryStates[s] {
					errs = append(errs, fe(base+".retry.on",
						"RETRY_STATE", "retry.on entries must be FAILED|NODE_FAIL|TIMEOUT"))
				}
			}
		}
		if t.Placement != nil && len(t.Placement.Requirements) > 0 {
			errs = append(errs, fe(base+".placement.requirements",
				"PLACEMENT_REQUIREMENTS_UNSUPPORTED",
				"requirement-based placement is reserved for a later version"))
		}
	}
	return errs
}

// --- step 4: expressions ----------------------------------------------------

// taskScope describes where a template/expression lives, which bounds
// the namespaces its refs may use.
type taskScope struct {
	task           workflowspec.Task
	declared       map[string]bool // task names
	params         map[string]bool // parameter names
	depClosure     map[string]bool // transitive deps of this task
	secrets        map[string]workflowspec.SecretUse
	inEnv          bool // secret refs only inside env values
	allowMultinode bool
}

func checkExprs(w workflowspec.Workflow, _ map[string]bool) []FieldError {
	var errs []FieldError
	declared := map[string]bool{}
	for _, t := range w.Spec.Tasks {
		declared[t.Name] = true
	}
	// Transitive dependency closure per task (best effort even with
	// cycles: edges pointing into the cycle are still recorded).
	trans := map[string]map[string]bool{}
	var reach func(n string, acc map[string]bool, seen map[string]bool)
	reach = func(n string, acc, seen map[string]bool) {
		if seen[n] {
			return
		}
		seen[n] = true
		for _, t := range w.Spec.Tasks {
			if t.Name != n {
				continue
			}
			for _, d := range t.DependsOn {
				if !acc[d] {
					acc[d] = true
					reach(d, acc, seen)
				}
			}
		}
	}
	for _, t := range w.Spec.Tasks {
		acc := map[string]bool{}
		reach(t.Name, acc, map[string]bool{})
		trans[t.Name] = acc
	}

	checkRefs := func(refs []expr.Path, sc taskScope, path string) {
		for _, r := range refs {
			errs = append(errs, checkRef(r, sc, path)...)
		}
	}

	params := map[string]bool{}
	for name := range w.Spec.Parameters {
		params[name] = true
	}
	for i, t := range w.Spec.Tasks {
		base := fmt.Sprintf("spec.tasks[%d]", i)
		sc := taskScope{task: t, declared: declared, params: params,
			depClosure: trans[t.Name], secrets: w.Spec.Secrets}
		// arrayRuntimeOK reports whether a template's only array.taskId
		// use is a sole `{{ array.taskId }}` interpolation — a runtime
		// reference must be the whole argv element or env value.
		hasArrayTaskID := func(refs []expr.Path) bool {
			for _, r := range refs {
				if r.String() == "array.taskId" {
					return true
				}
			}
			return false
		}
		arrayRuntimeOK := func(tpl *expr.Template, refs []expr.Path) bool {
			if !hasArrayTaskID(refs) {
				return true
			}
			e, ok := tpl.SoleExpr()
			if !ok {
				return false
			}
			p, ok := e.SoleRef()
			return ok && p.String() == "array.taskId"
		}
		hasMultinodeRef := func(refs []expr.Path) bool {
			for _, ref := range refs {
				if len(ref) > 0 && ref[0] == "multinode" {
					return true
				}
			}
			return false
		}
		wholeMultinodeRef := func(tpl *expr.Template) bool {
			e, ok := tpl.SoleExpr()
			if !ok {
				return false
			}
			p, ok := e.SoleRef()
			return ok && len(p) == 2 && p[0] == "multinode"
		}
		parse := func(src, path string, bare, allowRuntime bool) {
			if bare {
				e, err := expr.BareExpr(src)
				if err != nil {
					errs = append(errs, fe(path, "EXPR_PARSE",
						err.Error()))
					return
				}
				refs := e.Refs()
				for _, r := range refs {
					if r.String() == "array.taskId" {
						errs = append(errs, fe(path,
							"REF_ARRAY_RUNTIME_WHOLE",
							"array.taskId renders a runtime value and is only valid as a whole argv element or env value"))
					}
				}
				checkRefs(refs, sc, path)
				return
			}
			tpl, err := expr.ParseTemplate(src)
			if err != nil {
				errs = append(errs, fe(path, "EXPR_PARSE",
					err.Error()))
				return
			}
			refs := tpl.Refs()
			if hasMultinodeRef(refs) && allowRuntime && genericMultinode(t) && !wholeMultinodeRef(tpl) {
				errs = append(errs, fe(path, "MULTINODE_REF", "multinode runtime references must be the whole command or args element"))
			}
			if !allowRuntime && hasArrayTaskID(refs) ||
				!arrayRuntimeOK(tpl, refs) {
				errs = append(errs, fe(path,
					"REF_ARRAY_RUNTIME_WHOLE",
					"array.taskId renders a runtime value and is only valid as a whole argv element or env value"))
			}
			if !secretWholeOK(tpl, refs) {
				errs = append(errs, fe(path, "REF_SECRET_WHOLE",
					"secrets.<handle> must be the whole env value"))
			}
			scope := sc
			scope.allowMultinode = allowRuntime && genericMultinode(t)
			checkRefs(refs, scope, path)
		}
		for j, c := range t.Command {
			parse(c, fmt.Sprintf("%s.command[%d]", base, j), false, true)
		}
		for j, a := range t.Args {
			parse(a, fmt.Sprintf("%s.args[%d]", base, j), false, true)
		}
		envScope := sc
		envScope.inEnv = true
		for k, v := range t.Env {
			sc2 := envScope
			tpl, err := expr.ParseTemplate(v)
			if err != nil {
				errs = append(errs, fe(
					fmt.Sprintf("%s.env.%s", base, k), "EXPR_PARSE",
					err.Error()))
				continue
			}
			refs := tpl.Refs()
			if !arrayRuntimeOK(tpl, refs) {
				errs = append(errs, fe(
					fmt.Sprintf("%s.env.%s", base, k),
					"REF_ARRAY_RUNTIME_WHOLE",
					"array.taskId renders a runtime value and is only valid as a whole env value"))
			}
			if !secretWholeOK(tpl, refs) {
				errs = append(errs, fe(fmt.Sprintf("%s.env.%s", base, k),
					"REF_SECRET_WHOLE", "secrets.<handle> must be the whole env value"))
			}
			checkRefs(refs, sc2, fmt.Sprintf("%s.env.%s", base, k))
		}
		if t.When != "" {
			parse(t.When, base+".when", true, false)
		}
		if t.FanOut != nil {
			switch c := t.FanOut.Count.(type) {
			case string:
				parse(c, base+".fanOut.count", true, false)
			case float64, int64, int, nil:
			default:
				errs = append(errs, fe(base+".fanOut.count",
					"FANOUT_COUNT", "count must be an integer or expression"))
			}
		}
		if t.WorkingDirectory != "" {
			parse(t.WorkingDirectory, base+".workingDirectory", false, false)
		}
		if t.Stdout != "" {
			parse(t.Stdout, base+".stdout", false, false)
		}
		if t.Stderr != "" {
			parse(t.Stderr, base+".stderr", false, false)
		}
		if !genericMultinode(t) {
			for j, value := range t.Command {
				if multinodeDollarRefRe.MatchString(value) {
					errs = append(errs, fe(fmt.Sprintf("%s.command[%d]", base, j), "MULTINODE_REF", "multinode runtime variables are only valid on generic multinode tasks"))
				}
			}
			for j, value := range t.Args {
				if multinodeDollarRefRe.MatchString(value) {
					errs = append(errs, fe(fmt.Sprintf("%s.args[%d]", base, j), "MULTINODE_REF", "multinode runtime variables are only valid on generic multinode tasks"))
				}
			}
			for name, value := range t.Env {
				if multinodeDollarRefRe.MatchString(value) {
					errs = append(errs, fe(base+".env."+name, "MULTINODE_REF", "multinode runtime variables are only valid on generic multinode tasks"))
				}
			}
			for _, field := range []struct{ name, value string }{
				{"workingDirectory", t.WorkingDirectory}, {"stdout", t.Stdout},
				{"stderr", t.Stderr}, {"when", t.When},
			} {
				if multinodeDollarRefRe.MatchString(field.value) {
					errs = append(errs, fe(base+"."+field.name, "MULTINODE_REF", "multinode runtime variables are only valid on generic multinode tasks"))
				}
			}
		}
	}
	if d := w.Spec.Defaults; d != nil {
		for k, v := range d.Env {
			if multinodeDollarRefRe.MatchString(v) {
				errs = append(errs, fe("spec.defaults.env."+k, "MULTINODE_REF", "multinode runtime variables require a generic multinode task"))
			}
			sc := taskScope{declared: declared, params: params, secrets: w.Spec.Secrets,
				inEnv: true}
			tpl, err := expr.ParseTemplate(v)
			if err != nil {
				errs = append(errs, fe(
					"spec.defaults.env."+k, "EXPR_PARSE", err.Error()))
				continue
			}
			refs := tpl.Refs()
			for _, r := range refs {
				errs = append(errs, checkRef(r, sc,
					"spec.defaults.env."+k)...)
			}
			if !secretWholeOK(tpl, refs) {
				errs = append(errs, fe("spec.defaults.env."+k,
					"REF_SECRET_WHOLE", "secrets.<handle> must be the whole env value"))
			}
		}
		if d.WorkingDirectory != "" {
			sc := taskScope{declared: declared, params: params,
				secrets: w.Spec.Secrets}
			tpl, err := expr.ParseTemplate(d.WorkingDirectory)
			if err != nil {
				errs = append(errs, fe("spec.defaults.workingDirectory",
					"EXPR_PARSE", err.Error()))
			} else {
				for _, r := range tpl.Refs() {
					errs = append(errs, checkRef(r, sc,
						"spec.defaults.workingDirectory")...)
				}
			}
		}
	}
	return errs
}

// checkRef validates one referenced path against the scope it appears
// in (step 4 resolution rules).
func checkRef(r expr.Path, sc taskScope, path string) []FieldError {
	if len(r) == 0 {
		return nil
	}
	switch r[0] {
	case "parameters":
		if len(r) < 2 {
			return []FieldError{fe(path, "REF_PARAMETERS",
				"parameters reference needs a name")}
		}
		// declared-ness is checked by the caller passing parameter
		// names through sc.declared via the "params" key set.
		if !sc.params[r[1]] {
			return []FieldError{fe(path, "REF_UNKNOWN_PARAMETER",
				"parameter "+r[1]+" is not declared")}
		}
	case "tasks":
		if len(r) < 3 {
			return []FieldError{fe(path, "REF_TASKS",
				"tasks.<name>.<field> expected")}
		}
		if !sc.declared[r[1]] {
			return []FieldError{fe(path, "REF_UNKNOWN_TASK",
				"task "+r[1]+" does not exist")}
		}
		if !sc.depClosure[r[1]] {
			return []FieldError{fe(path, "REF_NOT_ANCESTOR",
				"tasks."+r[1]+" is only reachable from a (transitive) dependency")}
		}
		switch r[2] {
		case "succeededCount", "failedCount", "state":
		default:
			return []FieldError{fe(path, "REF_TASK_FIELD",
				"tasks.<name> exposes succeededCount|failedCount|state")}
		}
	case "item":
		if r[1] != "index" || len(r) != 2 {
			return []FieldError{fe(path, "REF_ITEM", "only item.index exists")}
		}
		if sc.task.FanOut == nil {
			return []FieldError{fe(path, "REF_ITEM_SCOPE",
				"item.* is only valid under fanOut")}
		}
	case "array":
		if r[1] != "taskId" || len(r) != 2 {
			return []FieldError{fe(path, "REF_ARRAY", "only array.taskId exists")}
		}
		if sc.task.Array == nil {
			return []FieldError{fe(path, "REF_ARRAY_SCOPE",
				"array.* is only valid on tasks with an array spec")}
		}
	case "multinode":
		fields := map[string]bool{
			"hostlist": true, "hostlistNoSlots": true, "totalSlots": true,
			"nodeIp": true, "sshWrapper": true, "rshWrapper": true,
		}
		if len(r) != 2 || !fields[r[1]] {
			return []FieldError{fe(path, "MULTINODE_REF", "unsupported multinode runtime reference")}
		}
		if !sc.allowMultinode || !genericMultinode(sc.task) {
			return []FieldError{fe(path, "MULTINODE_REF", "multinode runtime references are only valid in command or args of generic multinode tasks")}
		}
	case "secrets":
		if len(r) != 2 {
			return []FieldError{fe(path, "REF_SECRETS",
				"secrets.<handle> expected")}
		}
		su, ok := sc.secrets[r[1]]
		if !ok || su.Use != "env" {
			return []FieldError{fe(path, "REF_UNKNOWN_SECRET",
				"secret handle "+r[1]+" is not declared with use: env")}
		}
		if !sc.inEnv {
			return []FieldError{fe(path, "REF_SECRET_SCOPE",
				"secrets.* is only valid inside env values")}
		}
	case "run", "task":
		// run.* (scratch, id) and task.* (the enclosing task's own
		// resolved fields) are engine-provided; any subpath is allowed.
	default:
		return []FieldError{fe(path, "REF_UNKNOWN_NAMESPACE",
			"unknown namespace "+r[0])}
	}
	return nil
}

// secretWholeOK reports whether tpl either references no secrets or is
// exactly one secrets.<handle> reference.
func secretWholeOK(tpl *expr.Template, refs []expr.Path) bool {
	var secret bool
	for _, r := range refs {
		secret = secret || len(r) > 0 && r[0] == "secrets"
	}
	if !secret {
		return true
	}
	e, ok := tpl.SoleExpr()
	if !ok {
		return false
	}
	p, ok := e.SoleRef()
	return ok && len(p) == 2 && p[0] == "secrets"
}
