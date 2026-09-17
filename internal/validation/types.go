// Package validation defines the ScriptValidator port, diagnostics,
// policy application and shared limits per docs/script-validation.md.
package validation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Exonical/custos/internal/workflowspec"
)

// Language is re-exported so validator implementations need only this
// package for the port types.
type Language = workflowspec.Language

// Language constants, re-exported.
const (
	LanguageBash   = workflowspec.LanguageBash
	LanguageSh     = workflowspec.LanguageSh
	LanguagePython = workflowspec.LanguagePython
	LanguageYAML   = workflowspec.LanguageYAML
	LanguageJSON   = workflowspec.LanguageJSON
)

// Severity of a diagnostic. POLICY_VIOLATION and SECURITY_VIOLATION are
// a hard floor: they always block and can never be overridden.
type Severity string

// Severity levels, ordered INFO < WARNING < ERROR < POLICY < SECURITY.
const (
	SeverityInfo     Severity = "INFO"
	SeverityWarning  Severity = "WARNING"
	SeverityError    Severity = "ERROR"
	SeverityPolicy   Severity = "POLICY_VIOLATION"
	SeveritySecurity Severity = "SECURITY_VIOLATION"
)

// rank orders severities for threshold comparison.
func rank(s Severity) int {
	switch s {
	case SeverityInfo:
		return 0
	case SeverityWarning:
		return 1
	case SeverityError:
		return 2
	case SeverityPolicy:
		return 3
	case SeveritySecurity:
		return 4
	}
	return -1
}

// AtLeast reports whether s is at or above the threshold.
func (s Severity) AtLeast(threshold Severity) bool {
	return rank(s) >= rank(threshold)
}

// Diagnostic is a single validation finding.
type Diagnostic struct {
	Source    string   `json:"source"`
	Code      string   `json:"code"`
	Severity  Severity `json:"severity"`
	Line      int      `json:"line"`
	Column    int      `json:"column"`
	EndLine   int      `json:"endLine"`
	EndColumn int      `json:"endColumn"`
	Message   string   `json:"message"`
	Field     string   `json:"field,omitempty"`
	Fix       string   `json:"fix,omitempty"`
}

// ClusterSnapshot is the validator-facing view of cluster capabilities.
type ClusterSnapshot struct {
	Partitions  []string
	GRESTypes   []string
	QoS         []string
	MaxWalltime map[string]time.Duration
}

// Override remaps the severity of one (source, code) pair.
type Override struct {
	Source   string
	Code     string
	Severity Severity
}

// EffectivePolicy is the resolved tenant ∩ cluster validation policy.
type EffectivePolicy struct {
	BlockAt                   Severity
	Overrides                 []Override
	DisabledCodes             []string
	AllowShellTasks           bool
	AllowLegacySbatchImport   bool
	ForbiddenCommands         []string
	ForbiddenCommandsSeverity Severity
}

// Input is everything a validator sees; Script is bounded by CheckLimits
// before it reaches validators.
type Input struct {
	Language    workflowspec.Language
	Script      []byte
	Digest      Digest
	Resources   workflowspec.Resources
	Environment map[string]string
	Software    []workflowspec.SoftwareRequirement
	Cluster     *ClusterSnapshot
	Policy      EffectivePolicy
}

// Result is one validator's output.
type Result struct {
	Diagnostics []Diagnostic
	Tool        ToolVersion
}

// ToolVersion records the tool that produced a result for
// reproducibility and validation freshness.
type ToolVersion struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ScriptValidator is the port implemented by every validator. A
// non-nil error means the validator failed (mapped to CUSTOS900 by the
// pipeline), not that the script is invalid.
type ScriptValidator interface {
	Name() string
	Languages() []workflowspec.Language
	Validate(ctx context.Context, in Input) (Result, error)
}

// Digest is a sha256 over exact stored bytes.
type Digest [32]byte

// String renders "sha256:<hex>".
func (d Digest) String() string { return "sha256:" + hex.EncodeToString(d[:]) }

// DigestOf hashes b.
func DigestOf(b []byte) Digest { return Digest(sha256.Sum256(b)) }

// ParseDigest parses "sha256:<hex>".
func ParseDigest(s string) (Digest, error) {
	var d Digest
	hexpart, ok := strings.CutPrefix(s, "sha256:")
	if !ok {
		return d, fmt.Errorf("digest %q: missing sha256: prefix", s)
	}
	raw, err := hex.DecodeString(hexpart)
	if err != nil || len(raw) != 32 {
		return d, fmt.Errorf("digest %q: invalid hex", s)
	}
	copy(d[:], raw)
	return d, nil
}

// SortDiagnostics orders by (line, column, source, code) so persisted
// results are reproducible and diffable.
func SortDiagnostics(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Code < b.Code
	})
}
