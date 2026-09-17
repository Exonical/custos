package shsyntax_test

import (
	"context"
	"testing"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/shsyntax"
	"github.com/Exonical/custos/internal/workflowspec"
)

func validate(t *testing.T, script string) []validation.Diagnostic {
	t.Helper()
	res, err := shsyntax.Validator{}.Validate(context.Background(), validation.Input{
		Language: workflowspec.LanguageBash,
		Script:   []byte(script),
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.Diagnostics
}

func has(t *testing.T, ds []validation.Diagnostic, code string) bool {
	t.Helper()
	for _, d := range ds {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestSyntaxError(t *testing.T) {
	ds := validate(t, "#!/bin/bash\nif [ ; then\n")
	if !has(t, ds, "CUSTOS001") {
		t.Fatalf("no CUSTOS001 in %+v", ds)
	}
}

func TestCRLF(t *testing.T) {
	ds := validate(t, "#!/bin/bash\r\necho hi\r\n")
	if !has(t, ds, "CUSTOS010") {
		t.Fatalf("no CUSTOS010 in %+v", ds)
	}
}

func TestForeignDirective(t *testing.T) {
	for _, p := range []string{"#PBS -l nodes=2", "#$ -cwd", "#BSUB -q x", "#COBALT -t 10"} {
		ds := validate(t, "#!/bin/bash\n"+p+"\necho hi\n")
		if !has(t, ds, "CUSTOS012") {
			t.Fatalf("no CUSTOS012 for %q in %+v", p, ds)
		}
	}
}

func TestClean(t *testing.T) {
	if ds := validate(t, "#!/bin/bash\nset -e\necho hi\n"); len(ds) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", ds)
	}
}
