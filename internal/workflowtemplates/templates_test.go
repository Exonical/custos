package workflowtemplates_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowtemplates"
)

func TestCatalogLoadsAndValidates(t *testing.T) {
	all, err := workflowtemplates.All()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"openmp", "multicore", "mpi-tasks", "mpi-nodes",
		"hybrid-mpi-openmp", "per-core", "gpu", "long-running"}
	if len(all) != len(want) {
		t.Fatalf("got %d templates, want %d", len(all), len(want))
	}
	for i, tpl := range all {
		if tpl.ID != want[i] {
			t.Errorf("template %d is %q, want %q (catalog order)", i, tpl.ID, want[i])
		}
		for _, task := range tpl.Spec.Spec.Tasks {
			res, errs := task.Resources.Resolve("")
			if len(errs) > 0 {
				t.Errorf("%s/%s: %v", tpl.ID, task.Name, errs)
				continue
			}
			if err := res.Validate(); err != nil {
				t.Errorf("%s/%s: %v", tpl.ID, task.Name, err)
			}
		}
		if strings.Contains(tpl.YAML, "#SBATCH") {
			t.Errorf("%s: scheduler directives belong in resources, not the document", tpl.ID)
		}
		if _, err := workflowspec.Decode([]byte(tpl.YAML), "application/yaml"); err != nil {
			t.Errorf("%s: YAML view does not round-trip: %v", tpl.ID, err)
		}
	}
}

func TestTemplatesMatchTheirSbatchOrigins(t *testing.T) {
	get := func(id string) workflowspec.Resources {
		t.Helper()
		tpl, ok, err := workflowtemplates.Get(id)
		if err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", id, ok, err)
		}
		res, errs := tpl.Spec.Spec.Tasks[0].Resources.Resolve("")
		if len(errs) > 0 {
			t.Fatalf("%s: %v", id, errs)
		}
		return res
	}
	if r := get("hybrid-mpi-openmp"); r.Nodes != 2 || r.TasksPerNode != 4 || r.Tasks != 8 || r.CPUsPerTask != 5 {
		t.Errorf("hybrid resources drifted: %+v", r)
	}
	if r := get("gpu"); r.GPU == nil || r.GPU.Count != 1 || r.CPUsPerTask != 8 {
		t.Errorf("gpu resources drifted: %+v", r)
	}
	if r := get("long-running"); time.Duration(r.Walltime) != 7*24*time.Hour {
		t.Errorf("long-running walltime = %v", time.Duration(r.Walltime))
	}
	tpl, _, _ := workflowtemplates.Get("mpi-tasks")
	sw := tpl.Spec.Spec.Tasks[0].Software
	if len(sw) != 2 || sw[0].Name != "gcc" || sw[1].Name != "openmpi" {
		t.Errorf("mpi-tasks software = %+v", sw)
	}
	if _, ok, _ := workflowtemplates.Get("nope"); ok {
		t.Error("unknown id resolved")
	}
}

func TestTemplateTagsAreNeverNil(t *testing.T) {
	all, _ := workflowtemplates.All()
	for _, tpl := range all {
		if tpl.Tags == nil {
			t.Errorf("%s: tags must be an empty map, not nil", tpl.ID)
		}
	}
}
