package validation_test

import (
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/validation"
)

func TestCheckLimits(t *testing.T) {
	lim := validation.DefaultLimits
	tests := []struct {
		name     string
		script   []byte
		wantCode string
		wantSev  validation.Severity
	}{
		{"ok", []byte("#!/bin/bash\necho hi\n"), "", ""},
		{"nul", []byte("echo \x00"), "CUSTOS901", validation.SeverityError},
		{"bad-utf8", []byte{0xff, 0xfe}, "CUSTOS901", validation.SeverityError},
		{"bom", []byte{0xEF, 0xBB, 0xBF, '#'}, "CUSTOS013", validation.SeverityError},
		{"too-big", make([]byte, lim.MaxScriptBytes+1), "CUSTOS901", validation.SeverityError},
		{"long-line", append([]byte("x"), make([]byte, lim.MaxLineBytes)...), "CUSTOS901", validation.SeverityError},
		{"too-many-lines", []byte(strings.Repeat("x\n", lim.MaxLines+1)), "CUSTOS901", validation.SeverityError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := validation.CheckLimits(tt.script, lim)
			if tt.wantCode == "" {
				if len(ds) != 0 {
					t.Fatalf("unexpected diagnostics: %+v", ds)
				}
				return
			}
			found := false
			for _, d := range ds {
				if d.Code == tt.wantCode && d.Severity == tt.wantSev {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s/%s in %+v", tt.wantCode, tt.wantSev, ds)
			}
		})
	}
}

func TestApplyPolicy(t *testing.T) {
	mk := func(code string, sev validation.Severity) validation.Diagnostic {
		return validation.Diagnostic{Source: "shellcheck", Code: code, Severity: sev}
	}
	pol := validation.EffectivePolicy{
		BlockAt:       validation.SeverityError,
		Overrides:     []validation.Override{{Source: "shellcheck", Code: "SC2086", Severity: validation.SeverityWarning}},
		DisabledCodes: []string{"SC2034"},
	}
	diags := []validation.Diagnostic{
		mk("SC2086", validation.SeverityError),   // downgraded to WARNING
		mk("SC2034", validation.SeverityWarning), // disabled
		mk("CUSTOS101", validation.SeveritySecurity),
	}
	out, valid := validation.ApplyPolicy(diags, pol)
	if len(out) != 2 {
		t.Fatalf("want 2 diagnostics, got %d", len(out))
	}
	if out[0].Severity != validation.SeverityWarning {
		t.Fatalf("override not applied: %v", out[0].Severity)
	}
	if out[1].Severity != validation.SeveritySecurity {
		t.Fatalf("security diagnostic modified: %v", out[1].Severity)
	}
	if valid {
		t.Fatal("security violation must make result invalid")
	}

	// Overrides cannot touch POLICY/SECURITY even when listed.
	pol.Overrides = append(pol.Overrides, validation.Override{
		Source: "shellcheck", Code: "CUSTOS101", Severity: validation.SeverityInfo})
	pol.DisabledCodes = append(pol.DisabledCodes, "CUSTOS101")
	out, valid = validation.ApplyPolicy(diags, pol)
	if len(out) != 2 || out[1].Severity != validation.SeveritySecurity || valid {
		t.Fatalf("hard floor violated: %+v valid=%v", out, valid)
	}
}

func TestDigest(t *testing.T) {
	d := validation.DigestOf([]byte("hello"))
	parsed, err := validation.ParseDigest(d.String())
	if err != nil || parsed != d {
		t.Fatalf("round-trip failed: %v %v", parsed, err)
	}
	if _, err := validation.ParseDigest("md5:abc"); err == nil {
		t.Fatal("bad prefix accepted")
	}
}
