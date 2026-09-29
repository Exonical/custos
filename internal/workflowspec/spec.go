// Package workflowspec holds the immutable task/workflow definition
// types shared by validation and admission. Storage and workflow
// versioning land in M5; this package is types-only by design.
package workflowspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"
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

// ScriptRef references a stored script or carries inline script text.
type ScriptRef struct {
	Digest    string   `json:"ref,omitempty"` // "sha256:<hex>"
	Language  Language `json:"language"`
	Inline    string   `json:"inline,omitempty"`
	inlineSet bool
}

// HasInline reports whether inline source was present in the input.
func (s ScriptRef) HasInline() bool { return s.inlineSet || s.Inline != "" }

// EffectiveLanguage returns the explicit language or infers an inline
// script's interpreter from its shebang without modifying the spec.
func (s ScriptRef) EffectiveLanguage() Language {
	if s.Language != "" {
		return s.Language
	}
	if !s.HasInline() {
		return LanguageBash
	}
	line, _, _ := strings.Cut(s.Inline, "\n")
	fields := strings.Fields(strings.TrimSuffix(line, "\r"))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "#!") {
		return LanguageBash
	}
	interpreter := strings.TrimPrefix(fields[0], "#!")
	name := path.Base(interpreter)
	if name == "env" {
		for _, field := range fields[1:] {
			if field == "-S" || strings.HasPrefix(field, "-") || strings.Contains(field, "=") {
				continue
			}
			name = path.Base(field)
			break
		}
	}
	switch {
	case name == "sh":
		return LanguageSh
	case name == "bash":
		return LanguageBash
	case strings.HasPrefix(name, "python"):
		return LanguagePython
	default:
		return LanguageBash
	}
}

// UnmarshalJSON accepts the compact inline string form and strictly
// decodes the object form without relying on the outer decoder's policy.
func (s *ScriptRef) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("script: empty value")
	}
	if trimmed[0] == '"' {
		var inline string
		if err := json.Unmarshal(trimmed, &inline); err != nil {
			return err
		}
		*s = ScriptRef{Inline: inline, inlineSet: true}
		return nil
	}
	type scriptRefWire struct {
		Ref      string          `json:"ref,omitempty"`
		Language Language        `json:"language"`
		Inline   json.RawMessage `json:"inline,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var wire scriptRefWire
	if err := dec.Decode(&wire); err != nil {
		return err
	}
	*s = ScriptRef{Digest: wire.Ref, Language: wire.Language}
	if wire.Inline != nil {
		if err := json.Unmarshal(wire.Inline, &s.Inline); err != nil {
			return fmt.Errorf("script.inline: %w", err)
		}
		s.inlineSet = true
	}
	return nil
}

// MarshalJSON preserves the legacy ref object bytes and writes inline-only
// source as an object without mutating the spec with an inferred language.
func (s ScriptRef) MarshalJSON() ([]byte, error) {
	if s.HasInline() {
		type inlineWire struct {
			Ref      string    `json:"ref,omitempty"`
			Inline   string    `json:"inline"`
			Language *Language `json:"language,omitempty"`
		}
		wire := inlineWire{Ref: s.Digest, Inline: s.Inline}
		if s.Language != "" {
			wire.Language = &s.Language
		}
		return json.Marshal(wire)
	}
	type refWire struct {
		Ref      string   `json:"ref,omitempty"`
		Language Language `json:"language"`
	}
	return json.Marshal(refWire{Ref: s.Digest, Language: s.Language})
}
