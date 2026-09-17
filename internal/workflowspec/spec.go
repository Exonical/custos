// Package workflowspec holds the immutable task/workflow definition
// types shared by validation and admission. Storage and workflow
// versioning land in M5; this package is types-only by design.
package workflowspec

import (
	"encoding/json"
	"fmt"
	"time"
)

// Language identifies the payload language of a script.
type Language string

// Supported payload languages.
const (
	LanguageBash   Language = "bash"
	LanguageSh     Language = "sh"
	LanguagePython Language = "python"
	LanguageYAML   Language = "yaml"
	LanguageJSON   Language = "json"
)

// Duration serializes a time.Duration as a Go duration string
// ("4h30m"); on input a bare number is accepted as seconds.
type Duration time.Duration

// String renders the duration in Go short form.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON encodes the duration as a string ("4h30m").
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts "4h30m" strings or a number of seconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var secs float64
		if err2 := json.Unmarshal(b, &secs); err2 != nil {
			return fmt.Errorf("duration: %w", err)
		}
		*d = Duration(time.Duration(secs * float64(time.Second)))
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// GPURequest is a typed GPU requirement.
type GPURequest struct {
	Type  string `json:"type,omitempty"`
	Count int    `json:"count"`
}

// ArraySpec describes a job-array index range.
type ArraySpec struct {
	Start         int `json:"start"`
	End           int `json:"end"`
	Step          int `json:"step,omitempty"`
	MaxConcurrent int `json:"max_concurrent,omitempty"`
}

// Resources is the structured resource request that replaces every
// resource-bearing #SBATCH directive.
type Resources struct {
	Nodes            int         `json:"nodes,omitempty"`
	Tasks            int         `json:"tasks,omitempty"`
	TasksPerNode     int         `json:"tasks_per_node,omitempty"`
	CPUsPerTask      int         `json:"cpus_per_task,omitempty"`
	MemoryPerNodeMiB int64       `json:"memory_per_node_mib,omitempty"`
	MemoryPerCPUMiB  int64       `json:"memory_per_cpu_mib,omitempty"`
	GPU              *GPURequest `json:"gpu,omitempty"`
	Walltime         Duration    `json:"walltime"`
	Licenses         []string    `json:"licenses,omitempty"`
	Constraints      string      `json:"constraints,omitempty"`
	Exclusive        bool        `json:"exclusive,omitempty"`
	Array            *ArraySpec  `json:"array,omitempty"`
}

// Validate checks the request is internally sane (non-negative,
// walltime positive, gpu count >= 1, array bounds ordered).
func (r Resources) Validate() error {
	if r.Nodes < 0 || r.Tasks < 0 || r.TasksPerNode < 0 || r.CPUsPerTask < 0 {
		return fmt.Errorf("resources: negative count")
	}
	if r.MemoryPerNodeMiB < 0 || r.MemoryPerCPUMiB < 0 {
		return fmt.Errorf("resources: negative memory")
	}
	if r.Walltime <= 0 {
		return fmt.Errorf("resources: walltime must be > 0")
	}
	if r.GPU != nil && r.GPU.Count < 1 {
		return fmt.Errorf("resources: gpu.count must be >= 1")
	}
	if a := r.Array; a != nil {
		if a.Start < 0 || a.End < a.Start {
			return fmt.Errorf("resources: invalid array range %d-%d", a.Start, a.End)
		}
		if a.Step < 0 || a.MaxConcurrent < 0 {
			return fmt.Errorf("resources: negative array step/concurrency")
		}
	}
	return nil
}

// SoftwareRequirement is a structured software dependency resolved
// against the cluster software catalog.
type SoftwareRequirement struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ScriptRef points at a stored script by digest; the spec hash covers
// every script digest so payload bytes never reach scheduler fields.
type ScriptRef struct {
	Digest   string   `json:"ref"` // "sha256:<hex>"
	Language Language `json:"language"`
}
