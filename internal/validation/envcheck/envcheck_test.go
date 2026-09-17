package envcheck_test

import (
	"testing"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/envcheck"
)

func TestClassify(t *testing.T) {
	cases := map[string]envcheck.Class{
		"SLURM_NTASKS":         envcheck.ClassControlled,
		"SBATCH_FOO":           envcheck.ClassControlled,
		"SRUN_X":               envcheck.ClassControlled,
		"CUSTOS_EXECUTION_ID":  envcheck.ClassControlled,
		"PATH":                 envcheck.ClassControlled,
		"HOME":                 envcheck.ClassControlled,
		"LD_PRELOAD":           envcheck.ClassFiltered,
		"LD_DEBUG_STATISTICS":  envcheck.ClassFiltered,
		"http_proxy":           envcheck.ClassFiltered,
		"HTTPS_PROXY":          envcheck.ClassFiltered,
		"OMPI_MCA_btl":         envcheck.ClassFiltered,
		"PMIX_RANK":            envcheck.ClassFiltered,
		"NCCL_DEBUG":           envcheck.ClassFiltered,
		"CUDA_VISIBLE_DEVICES": envcheck.ClassFiltered,
		"BASH_ENV":             envcheck.ClassFiltered,
		"PYTHONSTARTUP":        envcheck.ClassFiltered,
		"MY_VAR":               envcheck.ClassUser,
		"OMP_NUM_THREADS":      envcheck.ClassUser, // not OMPI_
		"MOD1":                 envcheck.ClassGenerated,
	}
	for name, want := range cases {
		got := envcheck.Classify(name, []string{"MOD1"})
		if got != want {
			t.Errorf("%s: want %s got %s", name, want, got)
		}
	}
}

func codes(ds []validation.Diagnostic) map[string]validation.Severity {
	m := map[string]validation.Severity{}
	for _, d := range ds {
		m[d.Code] = d.Severity
	}
	return m
}

func TestValidate(t *testing.T) {
	env := map[string]string{
		"SLURM_NTASKS": "4",     // 301
		"MOD1":         "x",     // 302
		"LD_PRELOAD":   "/x.so", // 303
		"ld_preload":   "/y.so", // 305 warning (case variant)
		"BAD-NAME":     "v",     // 304
		"WITH\nLF":     "v",     // 304
		"OK_VAR":       "fine",
		"NULVAL":       "a\x00b", // 304
	}
	ds := envcheck.Validate(env, envcheck.EnvPolicy{GeneratedNames: []string{"MOD1"}})
	c := codes(ds)
	for code, want := range map[string]validation.Severity{
		"CUSTOS301": validation.SeverityError,
		"CUSTOS302": validation.SeverityError,
		"CUSTOS303": validation.SeverityError,
		"CUSTOS304": validation.SeverityError,
		"CUSTOS305": validation.SeverityWarning,
	} {
		if c[code] != want {
			t.Errorf("missing %s/%s in %+v", code, want, ds)
		}
	}
}

func TestAllowFiltered(t *testing.T) {
	ds := envcheck.Validate(map[string]string{"LD_PRELOAD": "/x.so"},
		envcheck.EnvPolicy{AllowFiltered: []string{"LD_PRELOAD"}})
	if len(ds) != 0 {
		t.Fatalf("allowed filtered var rejected: %+v", ds)
	}
}

func FuzzEnvName(f *testing.F) {
	f.Add("SLURM_NTASKS")
	f.Add("ld_preload")
	f.Fuzz(func(_ *testing.T, s string) {
		_ = envcheck.Classify(s, nil)
		_ = envcheck.Validate(map[string]string{s: "v"}, envcheck.EnvPolicy{})
	})
}
