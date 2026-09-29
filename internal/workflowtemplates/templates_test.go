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
		"hybrid-mpi-openmp", "per-core", "gpu", "long-running",
		"mpi-hello-openmpi", "mpi-hello-mpich", "generic-multinode"}
	if len(all) != len(want) {
		t.Fatalf("got %d templates, want %d", len(all), len(want))
	}
	for i, tpl := range all {
		if tpl.ID != want[i] {
			t.Errorf("template %d is %q, want %q (catalog order)", i, tpl.ID, want[i])
		}
		for _, task := range tpl.Spec.Spec.Tasks {
			if task.Type != "" {
				t.Errorf("%s/%s: new template should omit deprecated task type %q", tpl.ID, task.Name, task.Type)
			}
			res, errs := task.ResolveResources("")
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
		res, errs := tpl.Spec.Spec.Tasks[0].ResolveResources("")
		if len(errs) > 0 {
			t.Fatalf("%s: %v", id, errs)
		}
		return res
	}
	if r := get("hybrid-mpi-openmp"); r.Nodes != 2 || r.TasksPerNode != 4 || r.Tasks != 0 || r.CPUsPerTask != 5 {
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

func TestTemplateLaunchModel(t *testing.T) {
	for _, tt := range []struct {
		id     string
		launch string
		tasks  int
	}{
		{"mpi-tasks", workflowspec.LaunchSrun, 40},
		{"mpi-nodes", workflowspec.LaunchSrun, 0},
		{"hybrid-mpi-openmp", workflowspec.LaunchSrun, 0},
		{"per-core", workflowspec.LaunchSbatch, 4},
		{"multicore", workflowspec.LaunchSbatch, 0},
		{"mpi-hello-openmpi", workflowspec.LaunchSrun, 0},
		{"mpi-hello-mpich", workflowspec.LaunchSrun, 0},
		{"generic-multinode", workflowspec.LaunchSbatch, 0},
	} {
		tpl, ok, err := workflowtemplates.Get(tt.id)
		if err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", tt.id, ok, err)
		}
		task := tpl.Spec.Spec.Tasks[0]
		if got := task.EffectiveLaunch(); got != tt.launch {
			t.Errorf("%s launch = %q, want %q", tt.id, got, tt.launch)
		}
		if task.Resources.Tasks != tt.tasks {
			t.Errorf("%s tasks = %d, want %d", tt.id, task.Resources.Tasks, tt.tasks)
		}
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
