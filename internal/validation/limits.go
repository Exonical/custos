package validation

import (
	"bytes"
	"unicode/utf8"
)

// Limits bound a script before storage or validation (defaults per
// docs/script-validation.md).
type Limits struct {
	MaxScriptBytes int
	MaxLines       int
	MaxLineBytes   int
}

// DefaultLimits are the documented defaults.
var DefaultLimits = Limits{
	MaxScriptBytes: 256 * 1024,
	MaxLines:       10_000,
	MaxLineBytes:   16 * 1024,
}

// CheckLimits enforces the limits and input hygiene: size, line count,
// line length, NUL bytes, invalid UTF-8 (all ERROR CUSTOS901) and a
// leading BOM (ERROR CUSTOS013 — bash would execute it as a command).
func CheckLimits(script []byte, lim Limits) []Diagnostic {
	var out []Diagnostic
	add := func(code string, sev Severity, line int, msg string) {
		out = append(out, Diagnostic{
			Source: "pipeline", Code: code, Severity: sev,
			Line: line, Column: 1, Message: msg,
		})
	}
	if len(script) > lim.MaxScriptBytes {
		add("CUSTOS901", SeverityError, 0, "script exceeds maximum size")
	}
	if len(script) > 0 && !utf8.Valid(script) {
		add("CUSTOS901", SeverityError, 0, "script is not valid UTF-8")
	}
	if bytes.IndexByte(script, 0) >= 0 {
		add("CUSTOS901", SeverityError, 0, "script contains NUL bytes")
	}
	if bytes.HasPrefix(script, []byte{0xEF, 0xBB, 0xBF}) {
		add("CUSTOS013", SeverityError, 1,
			"byte-order mark at start of script would be executed as a command")
	}
	lines := bytes.Split(script, []byte("\n"))
	if n := len(lines); script[len(script)-1] == '\n' || len(script) == 0 {
		n--
		if n > lim.MaxLines {
			add("CUSTOS901", SeverityError, 0, "script exceeds maximum line count")
		}
	} else if n > lim.MaxLines {
		add("CUSTOS901", SeverityError, 0, "script exceeds maximum line count")
	}
	for i, ln := range lines {
		if len(ln) > lim.MaxLineBytes {
			add("CUSTOS901", SeverityError, i+1, "line exceeds maximum length")
			break
		}
	}
	return out
}

// ApplyPolicy applies severity overrides and disabled codes, then
// decides validity. Overrides/disables apply only to INFO/WARNING/ERROR
// diagnostics; POLICY_VIOLATION and SECURITY_VIOLATION are the hard
// floor and are never changed. Valid means no diagnostic at or above
// BlockAt and no POLICY/SECURITY diagnostics.
func ApplyPolicy(diags []Diagnostic, pol EffectivePolicy) ([]Diagnostic, bool) {
	disabled := make(map[string]bool, len(pol.DisabledCodes))
	for _, c := range pol.DisabledCodes {
		disabled[c] = true
	}
	overrides := make(map[[2]string]Severity, len(pol.Overrides))
	for _, o := range pol.Overrides {
		overrides[[2]string{o.Source, o.Code}] = o.Severity
	}
	out := diags[:0:0]
	for _, d := range diags {
		if rank(d.Severity) <= rank(SeverityError) {
			if disabled[d.Code] {
				continue
			}
			if sev, ok := overrides[[2]string{d.Source, d.Code}]; ok {
				d.Severity = sev
			}
		}
		out = append(out, d)
	}
	valid := true
	for _, d := range out {
		if rank(d.Severity) >= rank(SeverityPolicy) || d.Severity.AtLeast(pol.BlockAt) {
			valid = false
			break
		}
	}
	return out, valid
}
