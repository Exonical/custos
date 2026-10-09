package admission_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

func isolationBase() admission.BuildInput {
	return admission.BuildInput{
		Spec: admission.ExecutionSpec{
			ID: uuid.New(), TenantID: uuid.New(), TaskName: "t",
			Payload: admission.PayloadRef{
				ScriptID: uuid.New(), Digest: validation.DigestOf([]byte("x")),
				Language: workflowspec.LanguageBash, Interpreter: admission.InterpreterBash,
			},
		},
		Request: req(), Policy: admission.ResourcePolicy{MaxNodes: 4},
		Binding: admission.Binding{Account: "a", DefaultPartition: "main"},
		Cluster: validation.ClusterSnapshot{Partitions: []string{"main"}},
	}
}

func TestIsolationMatrix(t *testing.T) {
	mounts := []admission.NodeMount{
		{Target: "/apps", ReadOnly: true, Shared: true},
		{Target: "/mnt/data"},
	}
	cases := []struct {
		name      string
		node      *admission.NodeIsolation
		exclusive bool
		allow     bool
		wantNil   bool
		shared    string
		label     string
		denied    string
	}{
		{name: "nil node", node: nil, wantNil: true, allow: true},
		{name: "namespace", node: &admission.NodeIsolation{Mode: "namespace"}, wantNil: true},
		{name: "namespace user exclusive", node: &admission.NodeIsolation{Mode: "namespace"},
			exclusive: true, allow: true, shared: "none"},
		{name: "namespace user exclusive denied", node: &admission.NodeIsolation{Mode: "namespace"},
			exclusive: true, denied: "EXCLUSIVE_DENIED"},
		{name: "namespace with mounts", node: &admission.NodeIsolation{Mode: "namespace", Mounts: mounts}},
		{name: "tenant mcs", node: &admission.NodeIsolation{Mode: "tenant_exclusive", Mechanism: "mcs_label", TenantSlug: "tenant-a"},
			shared: "mcs", label: "tenant-a"},
		{name: "tenant mcs user exclusive", node: &admission.NodeIsolation{Mode: "tenant_exclusive", Mechanism: "mcs_label", TenantSlug: "tenant-a"},
			exclusive: true, allow: true, shared: "none", label: "tenant-a"},
		{name: "tenant mcs user exclusive denied", node: &admission.NodeIsolation{Mode: "tenant_exclusive", Mechanism: "mcs_label", TenantSlug: "tenant-a"},
			exclusive: true, denied: "EXCLUSIVE_DENIED"},
		{name: "tenant user", node: &admission.NodeIsolation{Mode: "tenant_exclusive", Mechanism: "user", TenantSlug: "tenant-a"},
			shared: "user"},
		{name: "tenant user exclusive", node: &admission.NodeIsolation{Mode: "tenant_exclusive", Mechanism: "user", TenantSlug: "tenant-a"},
			exclusive: true, allow: true, shared: "none"},
		{name: "node exclusive", node: &admission.NodeIsolation{Mode: "node_exclusive", Mechanism: "mcs_label", TenantSlug: "tenant-a"},
			shared: "none"},
		{name: "node exclusive user exclusive", node: &admission.NodeIsolation{Mode: "node_exclusive"},
			exclusive: true, allow: true, shared: "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := isolationBase()
			in.Node = tc.node
			in.Request.Exclusive = tc.exclusive
			in.Policy.AllowExclusive = tc.allow
			out, d := admission.Build(in)
			if tc.denied != "" {
				if d == nil || d.Code != tc.denied {
					t.Fatalf("denial = %v, want %s", d, tc.denied)
				}
				return
			}
			if d != nil {
				t.Fatalf("denied: %v", d)
			}
			iso := out.Isolation
			if tc.wantNil {
				if iso != nil {
					t.Fatalf("Isolation = %+v, want nil", iso)
				}
				return
			}
			if iso == nil {
				t.Fatal("Isolation is nil")
			}
			if iso.Shared != tc.shared || iso.MCSLabel != tc.label || iso.Mode != tc.node.Mode {
				t.Fatalf("Isolation = %+v, want shared %q label %q", iso, tc.shared, tc.label)
			}
			if len(iso.Mounts) != len(tc.node.Mounts) {
				t.Fatalf("mounts = %+v", iso.Mounts)
			}
			if out.Resources.Exclusive != tc.exclusive {
				t.Fatalf("Resources.Exclusive = %v", out.Resources.Exclusive)
			}
		})
	}
}

func TestIsolationSnapshotKeepsOnlyGivenMounts(t *testing.T) {
	in := isolationBase()
	in.Node = &admission.NodeIsolation{Mode: "namespace", Mounts: []admission.NodeMount{
		{Target: "/apps", ReadOnly: true, Shared: true}, {Target: "/mnt/data"}}}
	out, d := admission.Build(in)
	if d != nil {
		t.Fatal(d)
	}
	b, _ := out.Canonical()
	for _, want := range []string{`"isolation":{"mode":"namespace"`,
		`{"target":"/apps","read_only":true,"shared":true}`, `{"target":"/mnt/data"}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("canonical lacks %s: %s", want, b)
		}
	}
	in.Node.Mounts[0].Target = "/mutated"
	if out.Isolation.Mounts[0].Target != "/apps" {
		t.Fatal("snapshot aliases the input slice")
	}
}

func TestSpecWithoutIsolationKeepsLegacyCanonicalBytes(t *testing.T) {
	spec := admission.ExecutionSpec{
		SchemaVersion: 1, TaskName: "t",
		Resources: admission.ResolvedResources{WalltimeSeconds: 60, Exclusive: true},
	}
	b, err := spec.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "isolation") {
		t.Fatalf("nil Isolation leaked into canonical form: %s", b)
	}
	const want = `{"schema_version":1,"id":"00000000-0000-0000-0000-000000000000","tenant_id":"00000000-0000-0000-0000-000000000000","project_id":"00000000-0000-0000-0000-000000000000","principal_id":"00000000-0000-0000-0000-000000000000","workflow_version_id":"00000000-0000-0000-0000-000000000000","task_name":"t","attempt":0,"cluster":{"id":"00000000-0000-0000-0000-000000000000","name":"","api_version":""},"resources":{"walltime_seconds":60,"exclusive":true},"placement":{"cluster":"","reason":""},"environment":{},"payload":{"script_id":"00000000-0000-0000-0000-000000000000","digest":[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],"language":"","interpreter":""},"security":{"slurm_user":"","impersonation_mode":"","shell_task":false},"admission":{},"digest":[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],"admitted_at":"0001-01-01T00:00:00Z","admitted_by":""}`
	if string(b) != want {
		t.Fatalf("canonical bytes changed:\n got %s\nwant %s", b, want)
	}
}
