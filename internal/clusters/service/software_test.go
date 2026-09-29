package service

import (
	"testing"

	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/validation"
)

func TestNormalizeContainerRuntime(t *testing.T) {
	nilRuntime, err := normalizeContainerRuntime(nil)
	if err != nil || nilRuntime != nil {
		t.Fatalf("nil runtime: got %#v, %v", nilRuntime, err)
	}
	prefixes := []string{"oras://docker.io/anderbubble/"}
	got, err := normalizeContainerRuntime(&validation.ContainerRuntime{
		Type: "apptainer", AllowedImagePrefixes: prefixes,
	})
	if err != nil || got == nil || got.Binary != "apptainer" || got.MPIPlugin != "pmix" {
		t.Fatalf("defaults not applied: %#v, %v", got, err)
	}
	got.AllowedImagePrefixes[0] = "mutated"
	if prefixes[0] != "oras://docker.io/anderbubble/" {
		t.Fatal("normalized runtime aliases the input prefixes")
	}
	pyxis, err := normalizeContainerRuntime(&validation.ContainerRuntime{Type: "pyxis"})
	if err != nil || pyxis == nil || pyxis.MPIPlugin != "pmix" {
		t.Fatalf("pyxis runtime rejected: %#v, %v", pyxis, err)
	}
	bad := []validation.ContainerRuntime{
		{Type: "unknown"},
		{Type: "apptainer", Binary: "apptainer; id"},
		{Type: "pyxis", Binary: "apptainer"},
		{Type: "apptainer", MPIPlugin: "pmix;id"},
		{Type: "apptainer", AllowedImagePrefixes: []string{"oras://registry/../secret"}},
		{Type: "apptainer", AllowedImagePrefixes: []string{"docker://registry;id"}},
	}
	for i, runtime := range bad {
		if _, err := normalizeContainerRuntime(&runtime); !apperr.Is(err, apperr.Validation) {
			t.Errorf("case %d: want validation error, got %v", i, err)
		}
	}
}

func TestClusterSummaryExposesOnlyContainerRuntimeType(t *testing.T) {
	c := clusters.Cluster{
		ContainerRuntime: &validation.ContainerRuntime{Type: "apptainer", Binary: "/site/apptainer", AllowedImagePrefixes: []string{"oras://private.registry/"}},
	}
	summary := summarize(c, clusters.Assignment{})
	if summary.ContainerRuntime == nil || summary.ContainerRuntime.Type != "apptainer" {
		t.Fatalf("container runtime summary = %+v", summary.ContainerRuntime)
	}
}

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
