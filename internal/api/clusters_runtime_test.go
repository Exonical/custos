package api

import (
	"encoding/json"
	"testing"
)

func TestPatchClusterRuntimeDistinguishesOmittedFromNull(t *testing.T) {
	var omitted updateClusterDTO
	if err := json.Unmarshal([]byte(`{"version":1}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.ContainerRuntime.Present {
		t.Fatal("omitted container_runtime must leave the value unchanged")
	}

	var clearPatch updateClusterDTO
	if err := json.Unmarshal([]byte(`{"version":1,"container_runtime":null}`), &clearPatch); err != nil {
		t.Fatal(err)
	}
	if !clearPatch.ContainerRuntime.Present || clearPatch.ContainerRuntime.Value != nil {
		t.Fatalf("null container_runtime = %+v, want explicit clear", clearPatch.ContainerRuntime)
	}

	var set updateClusterDTO
	if err := json.Unmarshal([]byte(`{"version":1,"container_runtime":{"type":"apptainer"}}`), &set); err != nil {
		t.Fatal(err)
	}
	if !set.ContainerRuntime.Present || set.ContainerRuntime.Value == nil ||
		set.ContainerRuntime.Value.Type != "apptainer" {
		t.Fatalf("container_runtime object = %+v", set.ContainerRuntime)
	}

	var unknown updateClusterDTO
	if err := json.Unmarshal([]byte(`{"version":1,"container_runtime":{"type":"apptainer","unexpected":true}}`), &unknown); err == nil {
		t.Fatal("unknown container_runtime field was accepted")
	}
}
