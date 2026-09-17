package policy

import (
	"testing"

	"github.com/Exonical/custos/internal/validation"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		p    Policy
		bad  bool
	}{
		{"default", Default(), false},
		{"blockAt INFO", Policy{BlockAt: validation.SeverityInfo}, false},
		{"blockAt SECURITY", Policy{BlockAt: validation.SeveritySecurity}, true},
		{"override to SECURITY", Policy{BlockAt: validation.SeverityError,
			Overrides: []validation.Override{
				{Source: "shellcheck", Code: "SC2086", Severity: validation.SeveritySecurity},
			}}, true},
		{"disable CUSTOS code", Policy{BlockAt: validation.SeverityError,
			DisabledCodes: []string{"CUSTOS101"}}, true},
		{"disable SC code", Policy{BlockAt: validation.SeverityError,
			DisabledCodes: []string{"SC2034"}}, false},
		{"bad shell", Policy{BlockAt: validation.SeverityError,
			ShellcheckShell: "zsh"}, true},
		{"sh shell", Policy{BlockAt: validation.SeverityError,
			ShellcheckShell: "sh"}, false},
	}
	for _, c := range cases {
		err := c.p.Validate()
		if (err != nil) != c.bad {
			t.Errorf("%s: err=%v", c.name, err)
		}
	}
}

func TestMerge(t *testing.T) {
	cluster := Policy{
		BlockAt:           validation.SeverityError,
		DisabledCodes:     []string{"SC2034", "SC2086"},
		AllowShellTasks:   true,
		ForbiddenCommands: []string{"sbatch"},
		ShellcheckShell:   "bash",
		FilteredEnvAllow:  []string{"LD_PRELOAD"},
		Overrides: []validation.Override{
			{Source: "shellcheck", Code: "SC2046", Severity: validation.SeverityError},
		},
	}
	tenant := Policy{
		BlockAt:                   validation.SeverityWarning,
		DisabledCodes:             []string{"SC2086"},
		AllowShellTasks:           false,
		ForbiddenCommands:         []string{"sbatch", "salloc"},
		ForbiddenCommandsSeverity: validation.SeverityError,
		FilteredEnvAllow:          []string{"LD_PRELOAD", "NCCL_DEBUG"},
		Overrides: []validation.Override{
			{Source: "shellcheck", Code: "SC2046", Severity: validation.SeverityInfo},
		},
	}
	m := Merge(cluster, tenant)
	if m.BlockAt != validation.SeverityWarning {
		t.Errorf("BlockAt: %v", m.BlockAt)
	}
	if len(m.DisabledCodes) != 1 || m.DisabledCodes[0] != "SC2086" {
		t.Errorf("DisabledCodes: %v", m.DisabledCodes)
	}
	if m.AllowShellTasks {
		t.Error("AllowShellTasks must AND")
	}
	if len(m.ForbiddenCommands) != 2 {
		t.Errorf("ForbiddenCommands union: %v", m.ForbiddenCommands)
	}
	if m.ForbiddenCommandsSeverity != validation.SeverityError {
		t.Errorf("ForbiddenCommandsSeverity: %v", m.ForbiddenCommandsSeverity)
	}
	if len(m.FilteredEnvAllow) != 1 || m.FilteredEnvAllow[0] != "LD_PRELOAD" {
		t.Errorf("FilteredEnvAllow intersect: %v", m.FilteredEnvAllow)
	}
	if m.Overrides[0].Severity != validation.SeverityError {
		t.Errorf("override takes higher severity: %+v", m.Overrides)
	}
	if m.ShellcheckShell != "bash" {
		t.Errorf("ShellcheckShell: %v", m.ShellcheckShell)
	}
}

func TestMergeEqualReturnsTenant(t *testing.T) {
	// The stricter-than-cluster write rule relies on
	// Merge(cluster, tenant) == tenant when tenant is stricter.
	tenant := Policy{BlockAt: validation.SeverityWarning,
		ForbiddenCommands: []string{"sbatch", "salloc"},
		AllowShellTasks:   false}
	m := Merge(Default(), tenant)
	if m.BlockAt != tenant.BlockAt ||
		len(m.ForbiddenCommands) != len(tenant.ForbiddenCommands) ||
		m.AllowShellTasks != tenant.AllowShellTasks {
		t.Fatalf("merge(default, tenant) lost restrictiveness: %+v", m)
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	a := Policy{BlockAt: validation.SeverityError,
		DisabledCodes: []string{"SC1", "SC2"}}
	b := Policy{BlockAt: validation.SeverityError,
		DisabledCodes: []string{"SC2", "SC1"}}
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("fingerprint must be order-insensitive for lists")
	}
	c := Policy{BlockAt: validation.SeverityWarning}
	if Fingerprint(a) == Fingerprint(c) {
		t.Fatal("fingerprint must differ across policies")
	}
}

func TestNormalizeNotStricter(t *testing.T) {
	cluster := Policy{
		BlockAt:         validation.SeverityError,
		DisabledCodes:   []string{"SC2034", "SC2086"},
		ShellcheckShell: "bash",
		Overrides: []validation.Override{
			{Source: "shellcheck", Code: "SC2046", Severity: validation.SeverityError},
			{Source: "shellcheck", Code: "SC2086", Severity: validation.SeverityWarning},
		},
	}
	// Same effective policy written with reversed ordering and
	// duplicates (including a weaker duplicate that Normalize keeps the
	// stronger of) must be accepted as not looser.
	tenant := Policy{
		BlockAt:         validation.SeverityError,
		ShellcheckShell: "bash",
		DisabledCodes:   []string{"SC2086", "SC2034", "SC2086"},
		Overrides: []validation.Override{
			{Source: "shellcheck", Code: "SC2086", Severity: validation.SeverityWarning},
			{Source: "shellcheck", Code: "SC2046", Severity: validation.SeverityInfo},
			{Source: "shellcheck", Code: "SC2046", Severity: validation.SeverityError},
			{Source: "shellcheck", Code: "SC2086", Severity: validation.SeverityWarning},
		},
	}
	if dim := notStricter(cluster, tenant); dim != "" {
		t.Fatalf("equivalent policy rejected as looser: %s", dim)
	}
	// A genuinely weaker override is still caught.
	loose := tenant.Normalize()
	for i := range loose.Overrides {
		if loose.Overrides[i].Code == "SC2046" {
			loose.Overrides[i].Severity = validation.SeverityWarning
		}
	}
	if dim := notStricter(cluster, loose); dim != "overrides" {
		t.Fatalf("weaker override accepted: %q", dim)
	}
	// Normalize itself: dedupe keeps the higher severity.
	n := tenant.Normalize()
	if len(n.Overrides) != 2 || len(n.DisabledCodes) != 2 {
		t.Fatalf("normalize: %+v", n)
	}
	for _, o := range n.Overrides {
		if o.Code == "SC2046" && o.Severity != validation.SeverityError {
			t.Fatalf("dedupe kept weaker severity: %+v", o)
		}
	}
}
