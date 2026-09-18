package workflowspec_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/schema"
)

// The hand-written schema must name every exported spec field; this
// test walks the Go structs' JSON tags against the schema's
// properties maps.
func TestSchemaCoversSpec(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(schema.V1Alpha1, &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	defs, _ := doc["$defs"].(map[string]any)
	rootProps := doc["properties"].(map[string]any)

	// struct type -> schema object carrying its properties
	mustCover := func(st reflect.Type, sch map[string]any, path string) {
		t.Helper()
		props, _ := sch["properties"].(map[string]any)
		for i := 0; i < st.NumField(); i++ {
			f := st.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			name, _, _ := strings.Cut(tag, ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if _, ok := props[name]; !ok {
				t.Errorf("%s.%s (%q) missing from schema", path, f.Name, name)
			}
		}
	}
	mustCover(reflect.TypeOf(workflowspec.Workflow{}), doc, "Workflow")
	mustCover(reflect.TypeOf(workflowspec.Metadata{}),
		rootProps["metadata"].(map[string]any), "Metadata")
	specSch := rootProps["spec"].(map[string]any)
	mustCover(reflect.TypeOf(workflowspec.Spec{}), specSch, "Spec")
	for name, st := range map[string]reflect.Type{
		"parameter":           reflect.TypeOf(workflowspec.Parameter{}),
		"placement":           reflect.TypeOf(workflowspec.Placement{}),
		"defaults":            reflect.TypeOf(workflowspec.Defaults{}),
		"secretUse":           reflect.TypeOf(workflowspec.SecretUse{}),
		"execution":           reflect.TypeOf(workflowspec.Execution{}),
		"fanOut":              reflect.TypeOf(workflowspec.FanOut{}),
		"retry":               reflect.TypeOf(workflowspec.Retry{}),
		"output":              reflect.TypeOf(workflowspec.Output{}),
		"taskResources":       reflect.TypeOf(workflowspec.TaskResources{}),
		"gpuRequest":          reflect.TypeOf(workflowspec.GPURequest{}),
		"arraySpec":           reflect.TypeOf(workflowspec.ArraySpecYAML{}),
		"scriptRef":           reflect.TypeOf(workflowspec.ScriptRef{}),
		"softwareRequirement": reflect.TypeOf(workflowspec.SoftwareRequirement{}),
		"task":                reflect.TypeOf(workflowspec.Task{}),
	} {
		sch, ok := defs[name].(map[string]any)
		if !ok {
			t.Fatalf("$defs.%s missing", name)
		}
		mustCover(st, sch, name)
	}
}
