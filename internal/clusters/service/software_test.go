package service

import (
	"testing"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/validation"
)

func TestNormalizeSoftwareModules(t *testing.T) {
	ok := []validation.SoftwareModule{
		{Name: "gcc", Modules: []string{"gcc"}},
		{Name: "gcc", Version: "13.2", Modules: []string{"gcc/13.2.0"}},
		{Name: "openmpi", Version: "5.0", Modules: []string{"openmpi/5.0.3-gcc@13"}},
	}
	got, err := normalizeSoftwareModules(ok)
	if err != nil || len(got) != 3 {
		t.Fatalf("valid catalog rejected: %v", err)
	}
	got[0].Modules[0] = "mutated"
	if ok[0].Modules[0] != "gcc" {
		t.Fatal("normalized catalog aliases the input")
	}
	if got, err := normalizeSoftwareModules(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("nil catalog: got %v, %v", got, err)
	}
	bad := map[string][]validation.SoftwareModule{
		"empty name":      {{Modules: []string{"gcc"}}},
		"no modules":      {{Name: "gcc"}},
		"shell in module": {{Name: "gcc", Modules: []string{"gcc; rm -rf /"}}},
		"quote in module": {{Name: "gcc", Modules: []string{"gcc'"}}},
		"bad version":     {{Name: "gcc", Version: "1 2", Modules: []string{"gcc"}}},
		"duplicate": {
			{Name: "gcc", Modules: []string{"gcc"}},
			{Name: "gcc", Modules: []string{"gcc/12"}},
		},
	}
	for name, in := range bad {
		if _, err := normalizeSoftwareModules(in); !apperr.Is(err, apperr.Validation) {
			t.Errorf("%s: want validation error, got %v", name, err)
		}
	}
}
