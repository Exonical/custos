package workflowspec

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/Exonical/custos/internal/workflowspec/units"
)

// The custos.io/v1alpha1 workflow document (docs/workflows.md
// §Specification). These types decode strictly: unknown fields are
// rejected at the API boundary.

// APIVersion and Kind are the only accepted document identifiers.
const (
	APIVersionV1Alpha1 = "custos.io/v1alpha1"
	KindWorkflow       = "Workflow"
	LaunchSbatch       = "sbatch"
	LaunchSrun         = "srun"
)

// Workflow is the top-level document.
type Workflow struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}

// Metadata carries identity and labels.
type Metadata struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

// Spec is the workflow body.
type Spec struct {
	Parameters map[string]Parameter `json:"parameters,omitempty"`
	Placement  *Placement           `json:"placement,omitempty"`
	Defaults   *Defaults            `json:"defaults,omitempty"`
	Secrets    map[string]SecretUse `json:"secrets,omitempty"`
	Execution  *Execution           `json:"execution,omitempty"`
	Tasks      []Task               `json:"tasks"`
}

// Parameter declares a run-time input.
type Parameter struct {
	Type     string `json:"type"` // string | integer | boolean
	Required bool   `json:"required,omitempty"`
	Default  any    `json:"default,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Minimum  *int64 `json:"minimum,omitempty"`
	Maximum  *int64 `json:"maximum,omitempty"`
	Enum     []any  `json:"enum,omitempty"`
}

// Placement selects a cluster by name; requirements are reserved for
// v2 (rejected with PLACEMENT_REQUIREMENTS_UNSUPPORTED).
type Placement struct {
	Cluster      string         `json:"cluster,omitempty"`
	Requirements map[string]any `json:"requirements,omitempty"`
}

// Defaults are task-level fallbacks.
type Defaults struct {
	Account          string            `json:"account,omitempty"`
	Partition        string            `json:"partition,omitempty"`
	QoS              string            `json:"qos,omitempty"`
	WorkingDirectory string            `json:"workingDirectory,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler and decodes env maps or lists.
func (d *Defaults) UnmarshalJSON(data []byte) error {
	type defaultsAlias Defaults
	var decoded defaultsAlias
	env, err := decodeObjectWithEnv(data, "env", &decoded)
	if err != nil {
		return err
	}
	decoded.Env, err = decodeEnv(env)
	if err != nil {
		return err
	}
	*d = Defaults(decoded)
	return nil
}

func decodeObjectWithEnv(data []byte, envKey string, target any) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("expected object")
	}
	env := fields[envKey]
	delete(fields, envKey)
	rest, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(rest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return nil, err
	}
	return env, nil
}

func decodeEnv(raw json.RawMessage) (map[string]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	switch trimmed[0] {
	case '{':
		var env map[string]string
		if err := json.Unmarshal(trimmed, &env); err != nil {
			return nil, fmt.Errorf("env: %w", err)
		}
		return env, nil
	case '[':
		var entries []string
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, fmt.Errorf("env: %w", err)
		}
		env := make(map[string]string, len(entries))
		for i, entry := range entries {
			name, value, ok := strings.Cut(entry, "=")
			if !ok || !validEnvName(name) {
				return nil, fmt.Errorf("env[%d] must be a valid NAME=VALUE pair", i)
			}
			if _, exists := env[name]; exists {
				return nil, fmt.Errorf("env[%d] duplicates %s", i, name)
			}
			env[name] = value
		}
		return env, nil
	default:
		return nil, fmt.Errorf("env must be an object or a list of NAME=VALUE strings")
	}
}

func validEnvName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	validFirst := func(b byte) bool {
		return b == '_' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
	}
	validRest := func(b byte) bool {
		return validFirst(b) || b >= '0' && b <= '9'
	}
	if !validFirst(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !validRest(name[i]) {
			return false
		}
	}
	return true
}

// SecretUse binds a SecretReference to a consumption mode.
type SecretUse struct {
	Ref     string `json:"ref"`
	Use     string `json:"use"` // env | wrapped_token | image_pull
	EnvName string `json:"envName,omitempty"`
}

// SecretEnvName returns the explicit envName or the handle converted to
// upper snake case.
func SecretEnvName(handle string, use SecretUse) string {
	if use.EnvName != "" {
		return use.EnvName
	}
	var b strings.Builder
	for _, r := range handle {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Execution selects the engine strategy (debugging aid; v1 ships
// engine-driven regardless) and the failure policy.
type Execution struct {
	Strategy      string `json:"strategy,omitempty"`      // auto | engine | native
	FailurePolicy string `json:"failurePolicy,omitempty"` // fail (default) | continue
}

// FanOut replicates a task. Count is an integer or expression string;
// From (dynamic fan-out from an upstream output) is unsupported in v1.
type FanOut struct {
	Count any    `json:"count,omitempty"`
	From  string `json:"from,omitempty"`
}

// Retry controls per-attempt resubmission.
type Retry struct {
	Attempts int      `json:"attempts"`
	On       []string `json:"on,omitempty"`
}

// Output declares a task artifact.
type Output struct {
	Type string `json:"type"` // file
	Path string `json:"path"`
}

// Image identifies an optional task container image.
type Image struct {
	URI        string           `json:"uri"`
	PullSecret *ImagePullSecret `json:"pullSecret,omitempty"`
}

// ImagePullSecret selects workflow SecretReferences for a one-time image pull.
type ImagePullSecret struct {
	Username       string `json:"username,omitempty"`
	UsernameSecret string `json:"usernameSecret,omitempty"`
	PasswordSecret string `json:"passwordSecret"`
}

// Multinode selects a distributed task launcher and node shape.
type Multinode struct {
	Nodes           int    `json:"nodes"`
	Implementation  string `json:"implementation"`
	ProcsPerNode    int    `json:"procsPerNode,omitempty"`
	procsPerNodeSet bool
}

// UnmarshalJSON implements json.Unmarshaler and records optional slot presence.
func (m *Multinode) UnmarshalJSON(data []byte) error {
	type multinodeAlias Multinode
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var decoded multinodeAlias
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, set := fields["procsPerNode"]
	*m = Multinode(decoded)
	m.procsPerNodeSet = set
	return nil
}

// HasProcsPerNode reports whether the optional field was present in input.
func (m Multinode) HasProcsPerNode() bool { return m.procsPerNodeSet }

// EffectiveImplementation returns the native multinode implementation.
func (m Multinode) EffectiveImplementation() string { return m.Implementation }

// Task is one DAG node.
type Task struct {
	Name                string                `json:"name"`
	Type                string                `json:"type,omitempty"`
	Launch              string                `json:"launch,omitempty"`
	DependsOn           []string              `json:"dependsOn,omitempty"`
	OnDependencyFailure string                `json:"onDependencyFailure,omitempty"` // fail | run
	When                string                `json:"when,omitempty"`
	FanOut              *FanOut               `json:"fanOut,omitempty"`
	Resources           TaskResources         `json:"resources,omitempty"`
	Placement           *Placement            `json:"placement,omitempty"`
	Image               *Image                `json:"image,omitempty"`
	Multinode           *Multinode            `json:"multinode,omitempty"`
	Command             []string              `json:"command,omitempty"`
	Script              *ScriptRef            `json:"script,omitempty"`
	Args                []string              `json:"args,omitempty"`
	Env                 map[string]string     `json:"env,omitempty"`
	Software            []SoftwareRequirement `json:"software,omitempty"`
	Outputs             map[string]Output     `json:"outputs,omitempty"`
	Retry               *Retry                `json:"retry,omitempty"`
	Array               *ArraySpecYAML        `json:"array,omitempty"`
	Partition           string                `json:"partition,omitempty"`
	QoS                 string                `json:"qos,omitempty"`
	WorkingDirectory    string                `json:"workingDirectory,omitempty"`
	Stdout              string                `json:"stdout,omitempty"`
	Stderr              string                `json:"stderr,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler and decodes env maps or lists.
func (t *Task) UnmarshalJSON(data []byte) error {
	type taskAlias Task
	var decoded taskAlias
	env, err := decodeObjectWithEnv(data, "env", &decoded)
	if err != nil {
		return err
	}
	decoded.Env, err = decodeEnv(env)
	if err != nil {
		return err
	}
	*t = Task(decoded)
	return nil
}

// EffectiveLaunch chooses the explicit launch mode, defaulting legacy mpi
// tasks to srun and all other tasks to sbatch.
func (t Task) EffectiveLaunch() string {
	if t.Launch != "" {
		return t.Launch
	}
	if t.Multinode != nil {
		switch t.Multinode.EffectiveImplementation() {
		case "openmpi", "mpich":
			return LaunchSrun
		case "generic":
			return LaunchSbatch
		}
	}
	if t.Type == "mpi" {
		return LaunchSrun
	}
	return LaunchSbatch
}

// EffectiveProcsPerNode returns the multinode slot count for one node.
func (t Task) EffectiveProcsPerNode() int {
	if t.Multinode != nil && t.Multinode.ProcsPerNode > 0 {
		return t.Multinode.ProcsPerNode
	}
	if t.Resources.CPU > 0 {
		return t.Resources.CPU
	}
	return 1
}

// TaskResources is the spec-facing resource block; Resolve converts it
// into the normalized Resources used by admission.
type TaskResources struct {
	CPU           int         `json:"cpu,omitempty"`
	CPUAffinity   string      `json:"cpuAffinity,omitempty"`
	Memory        string      `json:"memory,omitempty"`
	Walltime      string      `json:"walltime,omitempty"`
	GPU           *GPURequest `json:"gpu,omitempty"`
	Nodes         int         `json:"nodes,omitempty"`
	Tasks         int         `json:"tasks,omitempty"`
	TasksPerNode  int         `json:"tasksPerNode,omitempty"`
	CPUsPerTask   int         `json:"cpusPerTask,omitempty"`
	MemoryPerCPU  string      `json:"memoryPerCpu,omitempty"`
	MemoryPerNode string      `json:"memoryPerNode,omitempty"`
	Licenses      []string    `json:"licenses,omitempty"`
	Constraints   string      `json:"constraints,omitempty"`
	Exclusive     bool        `json:"exclusive,omitempty"`
}

// FieldError locates a spec problem (path-addressed like
// "spec.tasks[2].resources.walltime"); the validate package defines the
// canonical codes.
type FieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Empty reports whether the task declares no resource fields.
func (t TaskResources) Empty() bool {
	return t.CPU == 0 && t.CPUAffinity == "" && t.Memory == "" && t.Walltime == "" &&
		t.GPU == nil && t.Nodes == 0 && t.Tasks == 0 &&
		t.TasksPerNode == 0 && t.CPUsPerTask == 0 &&
		t.MemoryPerCPU == "" && t.MemoryPerNode == "" && len(t.Licenses) == 0 &&
		t.Constraints == "" && !t.Exclusive
}

// Resolve converts TaskResources to the normalized Resources. CPU maps
// to CPUsPerTask (per-task cpu count); memory and memoryPerNode map to
// MemoryPerNodeMiB, while memoryPerCpu maps to MemoryPerCPUMiB. Errors
// are path-relative to the resources block.
func (t TaskResources) Resolve(path string) (Resources, []FieldError) {
	var errs []FieldError
	r := Resources{
		Nodes: t.Nodes, Tasks: t.Tasks, TasksPerNode: t.TasksPerNode,
		CPUsPerTask: t.CPUsPerTask, GPU: t.GPU,
		Licenses: t.Licenses, Constraints: t.Constraints,
		Exclusive: t.Exclusive,
	}
	if t.CPU > 0 && t.CPUsPerTask == 0 {
		r.CPUsPerTask = t.CPU
	}
	if t.Memory != "" {
		mib, err := units.ParseMemoryMiB(t.Memory)
		if err != nil {
			errs = append(errs, FieldError{Path: path + ".memory",
				Code: "RESOURCE_UNIT", Message: err.Error()})
		} else {
			r.MemoryPerNodeMiB = mib
		}
	}
	if t.MemoryPerCPU != "" {
		mib, err := units.ParseMemoryMiB(t.MemoryPerCPU)
		if err != nil {
			errs = append(errs, FieldError{Path: path + ".memoryPerCpu",
				Code: "RESOURCE_UNIT", Message: err.Error()})
		} else {
			r.MemoryPerCPUMiB = mib
		}
	}
	if t.MemoryPerNode != "" {
		mib, err := units.ParseMemoryMiB(t.MemoryPerNode)
		if err != nil {
			errs = append(errs, FieldError{Path: path + ".memoryPerNode",
				Code: "RESOURCE_UNIT", Message: err.Error()})
		} else {
			r.MemoryPerNodeMiB = mib
		}
	}
	if t.Walltime != "" {
		d, err := units.ParseWalltime(t.Walltime)
		if err != nil {
			errs = append(errs, FieldError{Path: path + ".walltime",
				Code: "RESOURCE_UNIT", Message: err.Error()})
		} else {
			r.Walltime = Duration(d)
		}
	}
	if err := r.Validate(); err != nil && len(errs) == 0 {
		errs = append(errs, FieldError{Path: path,
			Code: "RESOURCE_INVALID", Message: err.Error()})
	}
	return r, errs
}

// ResolveResources merges the task's scheduler resources with its
// multinode shape, when present. Multinode memory remains per node.
func (t Task) ResolveResources(path string) (Resources, []FieldError) {
	if t.Multinode == nil {
		r, errs := t.Resources.Resolve(path)
		if t.Array != nil {
			r.Array = t.Array.Normalized()
		}
		return r, errs
	}
	m := *t.Multinode
	if m.Nodes < 1 || m.Nodes > 10000 {
		return Resources{}, []FieldError{{Path: path + ".multinode.nodes",
			Code: "MULTINODE_INVALID", Message: "multinode.nodes must be between 1 and 10000"}}
	}
	if t.Array != nil || t.Type == "array" || t.Resources.Nodes != 0 ||
		t.Resources.Tasks != 0 || t.Resources.TasksPerNode != 0 ||
		t.Resources.CPUsPerTask != 0 {
		return Resources{}, []FieldError{{Path: path + ".multinode",
			Code: "MULTINODE_CONFLICT", Message: "multinode conflicts with array or explicit nodes/tasks/tasksPerNode/cpusPerTask resources"}}
	}
	implementation := m.EffectiveImplementation()
	if implementation != "openmpi" && implementation != "mpich" && implementation != "generic" {
		return Resources{}, []FieldError{{Path: path + ".multinode.implementation",
			Code: "MULTINODE_INVALID", Message: "implementation must be openmpi, mpich, or generic"}}
	}
	if m.HasProcsPerNode() && m.ProcsPerNode < 1 {
		return Resources{}, []FieldError{{Path: path + ".multinode.procsPerNode",
			Code: "MULTINODE_INVALID", Message: "procsPerNode must be >= 1 when set"}}
	}
	var r Resources
	if !t.Resources.Empty() {
		var errs []FieldError
		r, errs = t.Resources.Resolve(path)
		if len(errs) != 0 {
			return Resources{}, errs
		}
	}
	cores := t.Resources.CPU
	if cores <= 0 {
		cores = 1
	}
	procs := t.EffectiveProcsPerNode()
	if procs < 1 {
		return Resources{}, []FieldError{{Path: path + ".multinode.procsPerNode",
			Code: "MULTINODE_INVALID", Message: "procsPerNode must be positive"}}
	}
	maxInt := int(^uint(0) >> 1)
	if m.Nodes > maxInt/procs {
		return Resources{}, []FieldError{{Path: path + ".multinode",
			Code: "MULTINODE_INVALID", Message: "multinode total slot count overflows"}}
	}
	if implementation == "generic" {
		r.Nodes, r.Tasks, r.TasksPerNode, r.CPUsPerTask = m.Nodes, m.Nodes, 1, cores
	} else {
		if procs > cores || cores%procs != 0 {
			return Resources{}, []FieldError{{Path: path + ".multinode.procsPerNode",
				Code: "MULTINODE_CPU_DIVISIBLE", Message: "multinode cpu cores must divide evenly by procsPerNode"}}
		}
		r.Nodes, r.Tasks, r.TasksPerNode, r.CPUsPerTask = m.Nodes, m.Nodes*procs, procs, cores/procs
	}
	return r, nil
}

// Decode parses a workflow document from JSON or YAML into the strict
// Workflow struct; unknown fields are rejected with their path.
func Decode(body []byte, contentType string) (Workflow, error) {
	var w Workflow
	var raw []byte
	switch contentType {
	case "application/json", "":
		raw = body
	case "application/yaml", "application/x-yaml", "text/yaml":
		var v any
		if err := yaml.Unmarshal(body, &v); err != nil {
			return w, fmt.Errorf("yaml: %w", err)
		}
		j, err := json.Marshal(v)
		if err != nil {
			return w, fmt.Errorf("yaml->json: %w", err)
		}
		raw = j
	default:
		return w, fmt.Errorf("unsupported content type %q", contentType)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return w, fmt.Errorf("spec: %w", err)
	}
	return w, nil
}

// Canonical returns the deterministic JSON form (struct field order is
// fixed, map keys sorted, empties omitted) used for SpecHash and
// storage.
func Canonical(w Workflow) ([]byte, error) {
	return json.Marshal(w)
}

// SpecHash digests the canonical spec; the version record pins it.
func SpecHash(w Workflow) ([32]byte, error) {
	b, err := Canonical(w)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(b), nil
}

// ArraySpecYAML is the spec-facing array block (camelCase); it
// resolves into the normalized ArraySpec for admission.
type ArraySpecYAML struct {
	Start         int `json:"start"`
	End           int `json:"end"`
	Step          int `json:"step,omitempty"`
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
}

// Normalized converts the spec form to the admission ArraySpec.
func (a *ArraySpecYAML) Normalized() *ArraySpec {
	if a == nil {
		return nil
	}
	return &ArraySpec{Start: a.Start, End: a.End, Step: a.Step,
		MaxConcurrent: a.MaxConcurrent}
}
