package admission_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

func req() workflowspec.Resources {
	return workflowspec.Resources{Nodes: 2, Walltime: workflowspec.Duration(time.Hour)}
}

func TestCheckResourcePolicy(t *testing.T) {
	pol := admission.ResourcePolicy{MaxNodes: 4, MaxGPUsPerJob: 4, AllowExclusive: false}
	if d := admission.CheckResourcePolicy(req(), pol); d != nil {
		t.Fatalf("unexpected denial: %v", d)
	}
	r := req()
	r.Nodes = 8
	if d := admission.CheckResourcePolicy(r, pol); d == nil || d.Code != "RESOURCE_LIMIT" {
		t.Fatalf("want RESOURCE_LIMIT, got %v", d)
	}
	r = req()
	r.GPU = &workflowspec.GPURequest{Type: "h100", Count: 8}
	if d := admission.CheckResourcePolicy(r, pol); d == nil {
		t.Fatal("gpu limit not enforced")
	}
	pol.AllowedGPUTypes = []string{"a100"}
	r.GPU.Count = 1
	if d := admission.CheckResourcePolicy(r, pol); d == nil || d.Code != "GPU_TYPE_DENIED" {
		t.Fatalf("want GPU_TYPE_DENIED, got %v", d)
	}
	r = req()
	r.Exclusive = true
	if d := admission.CheckResourcePolicy(r, pol); d == nil || d.Code != "EXCLUSIVE_DENIED" {
		t.Fatalf("want EXCLUSIVE_DENIED, got %v", d)
	}
}

func TestCheckEntitlement(t *testing.T) {
	b := admission.Binding{Account: "proj", DefaultPartition: "main",
		AllowedPartitions: []string{"main", "gpu"}, AllowedQoS: []string{"normal"}}
	acct, part, qos, d := admission.CheckEntitlement(b, "", "normal")
	if d != nil || acct != "proj" || part != "main" || qos != "normal" {
		t.Fatalf("got %s %s %s %v", acct, part, qos, d)
	}
	if _, _, _, d := admission.CheckEntitlement(b, "other", ""); d == nil || d.Code != "PARTITION_DENIED" {
		t.Fatalf("want PARTITION_DENIED, got %v", d)
	}
}

func TestCheckAdmission(t *testing.T) {
	caps := validation.ClusterSnapshot{Partitions: []string{"main"},
		GRESTypes: []string{"gpu:h100"}, MaxWalltime: map[string]time.Duration{"main": 2 * time.Hour}}
	if d := admission.CheckAdmission(req(), "main", caps); d != nil {
		t.Fatalf("unexpected denial: %v", d)
	}
	if d := admission.CheckAdmission(req(), "nope", caps); d == nil || d.Code != "NO_PARTITION" {
		t.Fatalf("want NO_PARTITION, got %v", d)
	}
	r := req()
	r.GPU = &workflowspec.GPURequest{Type: "gpu:v999", Count: 1}
	if d := admission.CheckAdmission(r, "main", caps); d == nil || d.Code != "NO_GRES" {
		t.Fatalf("want NO_GRES, got %v", d)
	}
	r = req()
	r.Walltime = workflowspec.Duration(3 * time.Hour)
	if d := admission.CheckAdmission(r, "main", caps); d == nil || d.Code != "WALLTIME_EXCEEDS_PARTITION" {
		t.Fatalf("want WALLTIME_EXCEEDS_PARTITION, got %v", d)
	}
}

func TestBuildFreezes(t *testing.T) {
	spec := admission.ExecutionSpec{
		ID: uuid.New(), TenantID: uuid.New(), TaskName: "t1",
		Payload: admission.PayloadRef{
			ScriptID: uuid.New(), Digest: validation.DigestOf([]byte("x")),
			Language: workflowspec.LanguageBash, Interpreter: admission.InterpreterBash,
		},
	}
	out, d := admission.Build(admission.BuildInput{
		Spec: spec, Request: req(),
		Policy:  admission.ResourcePolicy{MaxNodes: 4},
		Binding: admission.Binding{Account: "a", DefaultPartition: "main"},
		Cluster: validation.ClusterSnapshot{Partitions: []string{"main"}},
	})
	if d != nil {
		t.Fatalf("denied: %v", d)
	}
	if out.Digest == (validation.Digest{}) || out.SchemaVersion != 1 || out.Partition != "main" {
		t.Fatalf("bad spec: %+v", out)
	}
	if out.Resources.WalltimeSeconds != 3600 {
		t.Fatalf("walltime not normalized: %+v", out.Resources)
	}
	// Canonical is deterministic and excludes Digest/AdmittedAt.
	out.AdmittedAt = time.Now()
	a, _ := out.Canonical()
	b, _ := out.Canonical()
	if string(a) != string(b) {
		t.Fatal("canonical form not deterministic")
	}
}
