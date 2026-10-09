package nodehooks_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/nodehooks"
)

func TestMechanismDefaultAndValidation(t *testing.T) {
	n := nodehooks.Normalize(nodehooks.Config{})
	if n.TenantExclusiveMechanism != nodehooks.MechanismMCSLabel {
		t.Fatalf("default mechanism = %q", n.TenantExclusiveMechanism)
	}
	if errs := nodehooks.Validate(n); len(errs) != 0 {
		t.Fatalf("default config invalid: %+v", errs)
	}
	n.TenantExclusiveMechanism = nodehooks.MechanismUser
	if errs := nodehooks.Validate(n); len(errs) != 0 {
		t.Fatalf("user mechanism invalid: %+v", errs)
	}
	n.TenantExclusiveMechanism = "cgroup"
	errs := nodehooks.Validate(n)
	if len(errs) != 1 || errs[0].Path != "tenant_exclusive_mechanism" || errs[0].Code != nodehooks.CodeConfigInvalid {
		t.Fatalf("errs = %+v", errs)
	}
}

func TestReadmeMechanismSections(t *testing.T) {
	readme := func(mode, mech string) string {
		in := renderInput(t)
		in.Config.IsolationMode = mode
		in.Config.TenantExclusiveMechanism = mech
		r, err := nodehooks.Render(in)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range r.Files {
			if f.Name == "README.md" {
				return string(f.Data)
			}
		}
		t.Fatal("no README")
		return ""
	}
	mcs := readme("tenant_exclusive", "mcs_label")
	for _, want := range []string{"MCSPlugin=mcs/label", "MCSParameters=ondemand,select", "job_submit", "--exclusive=mcs"} {
		if !strings.Contains(mcs, want) {
			t.Errorf("mcs README lacks %q", want)
		}
	}
	user := readme("tenant_exclusive", "user")
	if !strings.Contains(user, "--exclusive=user") || strings.Contains(user, "MCSPlugin") ||
		!strings.Contains(user, "own Slurm service user") {
		t.Errorf("user README wrong:\n%s", user)
	}
	if ns := readme("namespace", "mcs_label"); strings.Contains(ns, "MCSPlugin") || strings.Contains(ns, "--exclusive") {
		t.Error("namespace README must not mention exclusivity")
	}
	if ne := readme("node_exclusive", "mcs_label"); !strings.Contains(ne, "--exclusive") || strings.Contains(ne, "MCSPlugin") {
		t.Error("node_exclusive README wrong")
	}
}

func TestIsolationConverter(t *testing.T) {
	cfg := goodConfig()
	cfg.IsolationMode = nodehooks.ModeTenantExclusive
	cfg.TenantMounts = append(cfg.TenantMounts,
		nodehooks.TenantMount{Tenant: tenantA, Name: "ro-ref", FSType: "nfs4",
			Source: "nfs2.example.org:/tenantA/ref", Target: "/data/ref", Options: []string{"ro"}},
		nodehooks.TenantMount{Tenant: tenantA, Name: "scratch", FSType: "nfs4",
			Source: "nfs2.example.org:/tenantA/scratch", Target: "/scratch"})
	cfg.SharedMounts = append(cfg.SharedMounts,
		nodehooks.Mount{Name: "z", FSType: "nfs", Source: "h:/z", Target: "/zzz", Options: []string{"rw"}})

	a := nodehooks.Isolation(cfg, uuid.MustParse(tenantA), "tenant-a")
	if a.Mode != "tenant_exclusive" || a.Mechanism != "mcs_label" || a.TenantSlug != "tenant-a" {
		t.Fatalf("a = %+v", a)
	}
	var got []string
	for _, m := range a.Mounts {
		flags := ""
		if m.ReadOnly {
			flags += "ro"
		}
		if m.Shared {
			flags += "S"
		}
		got = append(got, m.Target+":"+flags)
	}
	want := "/apps:roS /data/ref:ro /mnt/data: /scratch: /zzz:S"
	if strings.Join(got, " ") != want {
		t.Fatalf("mounts = %v, want %s", got, want)
	}

	b := nodehooks.Isolation(cfg, uuid.MustParse(tenantB), "tenant-b")
	for _, m := range b.Mounts {
		if m.Target == "/data/ref" || m.Target == "/scratch" {
			t.Fatalf("tenant B sees tenant A mount %s", m.Target)
		}
	}
	other := nodehooks.Isolation(cfg, uuid.New(), "x")
	if len(other.Mounts) != 2 || !other.Mounts[0].Shared || !other.Mounts[1].Shared {
		t.Fatalf("unbound tenant must see only shared mounts: %+v", other.Mounts)
	}
}
