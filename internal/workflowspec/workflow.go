package workflowspec

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"

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

// SecretUse binds a SecretReference to a consumption mode.
type SecretUse struct {
	Ref     string `json:"ref"`
	Use     string `json:"use"` // "env"
	EnvName string `json:"envName,omitempty"`
}

// Execution selects the engine strategy (debugging aid; v1 ships
// engine-driven).
type Execution struct {
	Strategy string `json:"strategy,omitempty"` // auto | engine | native
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

// Task is one DAG node.
type Task struct {
	Name                string                `json:"name"`
	Type                string                `json:"type,omitempty"`
	DependsOn           []string              `json:"dependsOn,omitempty"`
	OnDependencyFailure string                `json:"onDependencyFailure,omitempty"` // fail | run
	When                string                `json:"when,omitempty"`
	FanOut              *FanOut               `json:"fanOut,omitempty"`
	Resources           TaskResources         `json:"resources,omitempty"`
	Placement           *Placement            `json:"placement,omitempty"`
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

// TaskResources is the spec-facing resource block; Resolve converts it
// into the normalized Resources used by admission.
type TaskResources struct {
	CPU           int         `json:"cpu,omitempty"`
	Memory        string      `json:"memory,omitempty"`
	Walltime      string      `json:"walltime,omitempty"`
	GPU           *GPURequest `json:"gpu,omitempty"`
	Nodes         int         `json:"nodes,omitempty"`
	Tasks         int         `json:"tasks,omitempty"`
	TasksPerNode  int         `json:"tasksPerNode,omitempty"`
	CPUsPerTask   int         `json:"cpusPerTask,omitempty"`
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
	return t.CPU == 0 && t.Memory == "" && t.Walltime == "" &&
		t.GPU == nil && t.Nodes == 0 && t.Tasks == 0 &&
		t.TasksPerNode == 0 && t.CPUsPerTask == 0 &&
		t.MemoryPerNode == "" && len(t.Licenses) == 0 &&
		t.Constraints == "" && !t.Exclusive
}

// Resolve converts TaskResources to the normalized Resources. CPU maps
// to CPUsPerTask (per-task cpu count); memory maps to
// MemoryPerNodeMiB. Errors are path-relative to the resources block.
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
