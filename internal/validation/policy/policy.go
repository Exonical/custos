// Package policy defines the persisted ValidationPolicy of
// docs/script-validation.md §Severity and ValidationPolicy, its merge
// (most restrictive) semantics, and the fingerprint that
// ScriptValidation.PolicyVersion stores.
package policy

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/validation"
)

// Scope kinds for a stored policy.
const (
	ScopeTenant  = "tenant"
	ScopeCluster = "cluster"
)

// Policy mirrors the YAML in docs/script-validation.md; JSON field
// names match the doc keys.
type Policy struct {
	BlockAt                   validation.Severity   `json:"blockAt"`
	Overrides                 []validation.Override `json:"overrides,omitempty"`
	DisabledCodes             []string              `json:"disabledCodes,omitempty"`
	ShellcheckShell           string                `json:"shellcheckShell,omitempty"`
	AllowShellTasks           bool                  `json:"allowShellTasks"`
	AllowLegacySbatchImport   bool                  `json:"allowLegacySbatchImport"`
	ForbiddenCommands         []string              `json:"forbiddenCommands,omitempty"`
	ForbiddenCommandsSeverity validation.Severity   `json:"forbiddenCommandsSeverity,omitempty"`
	// FilteredEnvAllow lists filtered env names this scope allows
	// (envcheck CUSTOS303 suppression).
	FilteredEnvAllow []string `json:"filteredEnvAllow,omitempty"`
}

// Default returns the built-in policy applied to scopes with no stored
// row.
func Default() Policy {
	return Policy{
		BlockAt:                   validation.SeverityError,
		ShellcheckShell:           "bash",
		ForbiddenCommands:         []string{"sbatch", "salloc"},
		ForbiddenCommandsSeverity: validation.SeverityWarning,
	}
}

// adjustable reports whether a severity is within the tunable band:
// POLICY_VIOLATION and SECURITY_VIOLATION are the hard floor and can
// never be set by policy.
func adjustable(s validation.Severity) bool {
	switch s {
	case validation.SeverityInfo, validation.SeverityWarning, validation.SeverityError:
		return true
	}
	return false
}

// Validate checks the policy is writable: BlockAt must be a tunable
// threshold, overrides can only set tunable severities, and
// DisabledCodes may not name any CUSTOS* code — Custos diagnostics are
// ours and are never disableable (a deliberate simplification: for
// shellcheck codes the origin severity is unknowable here).
func (p Policy) Validate() error {
	if !adjustable(p.BlockAt) {
		return fmt.Errorf("blockAt must be INFO, WARNING or ERROR")
	}
	for _, o := range p.Overrides {
		if !adjustable(o.Severity) {
			return fmt.Errorf("override %s/%s: severity must be INFO, WARNING or ERROR", o.Source, o.Code)
		}
	}
	for _, c := range p.DisabledCodes {
		if strings.HasPrefix(c, "CUSTOS") {
			return fmt.Errorf("disabledCodes: %s is a Custos diagnostic and cannot be disabled", c)
		}
	}
	switch p.ShellcheckShell {
	case "", "bash", "sh", "dash", "ksh":
	default:
		return fmt.Errorf("shellcheckShell must be bash, sh, dash or ksh")
	}
	if p.ForbiddenCommandsSeverity != "" && !adjustable(p.ForbiddenCommandsSeverity) {
		return fmt.Errorf("forbiddenCommandsSeverity must be INFO, WARNING or ERROR")
	}
	return nil
}

func rank(s validation.Severity) int {
	switch s {
	case validation.SeverityInfo:
		return 0
	case validation.SeverityWarning:
		return 1
	case validation.SeverityError:
		return 2
	case validation.SeverityPolicy:
		return 3
	case validation.SeveritySecurity:
		return 4
	}
	return -1
}

func higher(a, b validation.Severity) validation.Severity {
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

func intersect(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	set := make(map[string]bool, len(b))
	for _, s := range b {
		set[s] = true
	}
	var out []string
	for _, s := range a {
		if set[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func union(a, b []string) []string {
	set := make(map[string]bool, len(a)+len(b))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Merge returns the most restrictive combination of a cluster policy
// and a tenant policy: BlockAt is the lower rank, overrides take the
// higher severity per (source, code), disabled codes intersect, Allow*
// flags AND, forbidden commands union, forbidden severity takes the
// higher rank.
func Merge(cluster, tenant Policy) Policy {
	out := Policy{
		AllowShellTasks:           cluster.AllowShellTasks && tenant.AllowShellTasks,
		AllowLegacySbatchImport:   cluster.AllowLegacySbatchImport && tenant.AllowLegacySbatchImport,
		DisabledCodes:             intersect(cluster.DisabledCodes, tenant.DisabledCodes),
		ForbiddenCommands:         union(cluster.ForbiddenCommands, tenant.ForbiddenCommands),
		ForbiddenCommandsSeverity: higher(cluster.ForbiddenCommandsSeverity, tenant.ForbiddenCommandsSeverity),
		FilteredEnvAllow:          intersect(cluster.FilteredEnvAllow, tenant.FilteredEnvAllow),
	}
	if rank(cluster.BlockAt) <= rank(tenant.BlockAt) {
		out.BlockAt = cluster.BlockAt
	} else {
		out.BlockAt = tenant.BlockAt
	}
	switch {
	case cluster.ShellcheckShell != "":
		out.ShellcheckShell = cluster.ShellcheckShell
	default:
		out.ShellcheckShell = tenant.ShellcheckShell
	}
	out.Overrides = dedupeOverrides(append(append([]validation.Override{},
		cluster.Overrides...), tenant.Overrides...))
	return out
}

// dedupeOverrides collapses overrides keyed by (source, code) keeping
// the higher severity, sorted for canonical output.
func dedupeOverrides(in []validation.Override) []validation.Override {
	merged := map[[2]string]validation.Override{}
	for _, o := range in {
		k := [2]string{o.Source, o.Code}
		if prev, ok := merged[k]; !ok || rank(o.Severity) > rank(prev.Severity) {
			merged[k] = o
		}
	}
	out := make([]validation.Override, 0, len(merged))
	for _, o := range merged {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// Normalize returns the canonical form stored and compared: overrides
// deduplicated by (source, code) keeping the higher severity; the list
// fields sorted and deduplicated. Two policies equal up to ordering or
// duplicate entries are identical after Normalize.
func (p Policy) Normalize() Policy {
	p.Overrides = dedupeOverrides(p.Overrides)
	p.DisabledCodes = union(p.DisabledCodes, nil)
	p.ForbiddenCommands = union(p.ForbiddenCommands, nil)
	p.FilteredEnvAllow = union(p.FilteredEnvAllow, nil)
	return p
}

// Effective projects the policy into the validator-facing view.
func (p Policy) Effective() validation.EffectivePolicy {
	return validation.EffectivePolicy{
		BlockAt:                   p.BlockAt,
		Overrides:                 p.Overrides,
		DisabledCodes:             p.DisabledCodes,
		AllowShellTasks:           p.AllowShellTasks,
		AllowLegacySbatchImport:   p.AllowLegacySbatchImport,
		ForbiddenCommands:         p.ForbiddenCommands,
		ForbiddenCommandsSeverity: p.ForbiddenCommandsSeverity,
		FilteredEnvAllowed:        p.FilteredEnvAllow,
	}
}

// Fingerprint returns the FNV-64a hash of the canonical JSON encoding
// of p (struct field order + sorted map keys are deterministic). Stored
// as ScriptValidation.PolicyVersion; currency checks compare
// fingerprints.
func Fingerprint(p Policy) int64 {
	canonical := struct {
		BlockAt                   validation.Severity   `json:"blockAt"`
		DisabledCodes             []string              `json:"disabledCodes"`
		Overrides                 []validation.Override `json:"overrides"`
		ShellcheckShell           string                `json:"shellcheckShell"`
		AllowShellTasks           bool                  `json:"allowShellTasks"`
		AllowLegacySbatchImport   bool                  `json:"allowLegacySbatchImport"`
		ForbiddenCommands         []string              `json:"forbiddenCommands"`
		ForbiddenCommandsSeverity validation.Severity   `json:"forbiddenCommandsSeverity"`
		FilteredEnvAllow          []string              `json:"filteredEnvAllow"`
	}{
		BlockAt:                   p.BlockAt,
		DisabledCodes:             append([]string{}, p.DisabledCodes...),
		Overrides:                 append([]validation.Override{}, p.Overrides...),
		ShellcheckShell:           p.ShellcheckShell,
		AllowShellTasks:           p.AllowShellTasks,
		AllowLegacySbatchImport:   p.AllowLegacySbatchImport,
		ForbiddenCommands:         append([]string{}, p.ForbiddenCommands...),
		ForbiddenCommandsSeverity: p.ForbiddenCommandsSeverity,
		FilteredEnvAllow:          append([]string{}, p.FilteredEnvAllow...),
	}
	sort.Strings(canonical.DisabledCodes)
	sort.Strings(canonical.ForbiddenCommands)
	sort.Strings(canonical.FilteredEnvAllow)
	sort.Slice(canonical.Overrides, func(i, j int) bool {
		if canonical.Overrides[i].Source != canonical.Overrides[j].Source {
			return canonical.Overrides[i].Source < canonical.Overrides[j].Source
		}
		return canonical.Overrides[i].Code < canonical.Overrides[j].Code
	})
	b, _ := json.Marshal(canonical)
	h := fnv.New64a()
	_, _ = h.Write(b)
	return int64(h.Sum64()) // #nosec G115 -- a hash is a hash regardless of sign
}

// Scoped is a stored policy row.
type Scoped struct {
	ScopeKind string
	ScopeID   uuid.UUID
	Version   int64
	Body      Policy
	UpdatedBy uuid.UUID
	UpdatedAt time.Time
}

// notStricter reports which dimension of a tenant policy is looser than
// the merged floor, for the POLICY_NOT_STRICTER message. Both sides are
// normalized first so ordering and duplicate entries cannot cause a
// false rejection.
func notStricter(cluster, tenant Policy) string {
	m := Merge(cluster, tenant).Normalize()
	t := tenant.Normalize()
	if m.BlockAt != t.BlockAt {
		return "blockAt"
	}
	if m.AllowShellTasks != t.AllowShellTasks {
		return "allowShellTasks"
	}
	if m.AllowLegacySbatchImport != t.AllowLegacySbatchImport {
		return "allowLegacySbatchImport"
	}
	if !slices.Equal(m.DisabledCodes, t.DisabledCodes) {
		return "disabledCodes"
	}
	if !slices.Equal(m.ForbiddenCommands, t.ForbiddenCommands) {
		return "forbiddenCommands"
	}
	if !slices.Equal(m.FilteredEnvAllow, t.FilteredEnvAllow) {
		return "filteredEnvAllow"
	}
	if m.ForbiddenCommandsSeverity != t.ForbiddenCommandsSeverity {
		return "forbiddenCommandsSeverity"
	}
	if !slices.Equal(m.Overrides, t.Overrides) {
		return "overrides"
	}
	return ""
}

// ErrNotStricter builds the write-time rejection when a tenant policy
// is looser than any assigned cluster's policy.
func ErrNotStricter(clusterName, dim string) error {
	return apperr.New(apperr.Validation, "POLICY_NOT_STRICTER",
		fmt.Sprintf("tenant policy is less restrictive than cluster %q (%s)",
			clusterName, dim))
}
