package nodehooks_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/nodehooks"
)

const (
	tenantA = "11111111-1111-4111-8111-111111111111"
	tenantB = "22222222-2222-4222-8222-222222222222"
)

func goodConfig() nodehooks.Config {
	return nodehooks.Config{
		IsolationMode:       "namespace",
		MountTimeoutSeconds: 30,
		SharedMounts: []nodehooks.Mount{{
			Name: "apps", FSType: "nfs4", Source: "nfs1.example.org:/hpc/apps",
			Target: "/apps", Options: []string{"ro", "nfsvers=4.2"}}},
		TenantMounts: []nodehooks.TenantMount{
			{Tenant: tenantA, Name: "flight-data", FSType: "nfs4",
				Source: "nfs2.example.org:/tenantA/flight_data", Target: "/mnt/data",
				Options: []string{"rw", "hard"}},
			{Tenant: tenantB, Name: "flight-data", FSType: "nfs4",
				Source: "nfs2.example.org:/tenantB/flight_data", Target: "/mnt/data"},
		},
		Hooks: []nodehooks.Hook{
			{Name: "metrics", Phase: "prolog", Order: 0, Script: "#!/bin/bash\necho start\n"},
			{Name: "cleanup", Phase: "epilog", Order: 5, Script: "#!/bin/sh\necho done"},
		},
	}
}

func renderInput(t *testing.T) nodehooks.RenderInput {
	t.Helper()
	cfg := nodehooks.Normalize(goodConfig())
	if errs := nodehooks.Validate(cfg); len(errs) != 0 {
		t.Fatalf("good config invalid: %+v", errs)
	}
	return nodehooks.RenderInput{
		Cluster: "e2e", Revision: 7, Config: cfg,
		TenantSlugs: map[string]string{tenantA: "tenant-a", tenantB: "tenant-b"},
		Bindings: []nodehooks.Binding{
			{TenantID: tenantA, Account: "acct-a2"},
			{TenantID: tenantA, Account: "acct-a1"},
			{TenantID: tenantB, Account: "acct-b"},
			{TenantID: tenantA, Account: "shared-acct"},
			{TenantID: tenantB, Account: "shared-acct"},
		},
	}
}

func TestNormalizeDefaults(t *testing.T) {
	n := nodehooks.Normalize(nodehooks.Config{
		SharedMounts: []nodehooks.Mount{{Name: "a", FSType: "nfs", Source: "h:/x", Target: "/a"}},
		TenantMounts: []nodehooks.TenantMount{{Tenant: strings.ToUpper(tenantA), Name: "b",
			FSType: "nfs", Source: "h:/y", Target: "/b", Options: []string{"hard"}}},
	})
	if n.IsolationMode != "namespace" || n.MountTimeoutSeconds != 30 {
		t.Fatalf("defaults: %+v", n)
	}
	if got := strings.Join(n.SharedMounts[0].Options, ","); got != "ro" {
		t.Fatalf("shared default options = %s", got)
	}
	if got := strings.Join(n.TenantMounts[0].Options, ","); got != "hard,nodev,nosuid,rw" {
		t.Fatalf("tenant options = %s", got)
	}
	if n.TenantMounts[0].Tenant != tenantA {
		t.Fatalf("tenant not lowercased: %s", n.TenantMounts[0].Tenant)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*nodehooks.Config)
		path   string
		code   string
	}{
		{"mode", func(c *nodehooks.Config) { c.IsolationMode = "bogus" }, "isolation_mode", nodehooks.CodeConfigInvalid},
		{"timeout low", func(c *nodehooks.Config) { c.MountTimeoutSeconds = 4 }, "mount_timeout_seconds", nodehooks.CodeConfigInvalid},
		{"timeout high", func(c *nodehooks.Config) { c.MountTimeoutSeconds = 301 }, "mount_timeout_seconds", nodehooks.CodeConfigInvalid},
		{"name", func(c *nodehooks.Config) { c.SharedMounts[0].Name = "Apps" }, "shared_mounts[0].name", nodehooks.CodeMountInvalid},
		{"fstype", func(c *nodehooks.Config) { c.SharedMounts[0].FSType = "cifs" }, "shared_mounts[0].fstype", nodehooks.CodeMountInvalid},
		{"source", func(c *nodehooks.Config) { c.SharedMounts[0].Source = "no-colon/path" }, "shared_mounts[0].source", nodehooks.CodeMountInvalid},
		{"source dotdot", func(c *nodehooks.Config) { c.SharedMounts[0].Source = "h:/a/../b" }, "shared_mounts[0].source", nodehooks.CodeMountInvalid},
		{"source semicolon", func(c *nodehooks.Config) { c.SharedMounts[0].Source = "h:/a;rm" }, "shared_mounts[0].source", nodehooks.CodeMountInvalid},
		{"target relative", func(c *nodehooks.Config) { c.SharedMounts[0].Target = "apps" }, "shared_mounts[0].target", nodehooks.CodeMountInvalid},
		{"target slash", func(c *nodehooks.Config) { c.SharedMounts[0].Target = "/" }, "shared_mounts[0].target", nodehooks.CodeMountInvalid},
		{"target trailing", func(c *nodehooks.Config) { c.SharedMounts[0].Target = "/apps/" }, "shared_mounts[0].target", nodehooks.CodeMountInvalid},
		{"target dotdot", func(c *nodehooks.Config) { c.SharedMounts[0].Target = "/a/../b" }, "shared_mounts[0].target", nodehooks.CodeMountInvalid},
		{"target reserved", func(c *nodehooks.Config) { c.SharedMounts[0].Target = "/etc/foo" }, "shared_mounts[0].target", nodehooks.CodeMountInvalid},
		{"target tmp", func(c *nodehooks.Config) { c.SharedMounts[0].Target = "/tmp" }, "shared_mounts[0].target", nodehooks.CodeMountInvalid},
		{"target charset", func(c *nodehooks.Config) { c.SharedMounts[0].Target = "/a b" }, "shared_mounts[0].target", nodehooks.CodeMountInvalid},
		{"option unknown", func(c *nodehooks.Config) { c.SharedMounts[0].Options = []string{"ro", "suid"} }, "shared_mounts[0].options[1]", nodehooks.CodeMountInvalid},
		{"option range", func(c *nodehooks.Config) { c.SharedMounts[0].Options = []string{"nconnect=99"} }, "shared_mounts[0].options[0]", nodehooks.CodeMountInvalid},
		{"option enum", func(c *nodehooks.Config) { c.SharedMounts[0].Options = []string{"nfsvers=5"} }, "shared_mounts[0].options[0]", nodehooks.CodeMountInvalid},
		{"ro and rw", func(c *nodehooks.Config) { c.SharedMounts[0].Options = []string{"ro", "rw"} }, "shared_mounts[0].options", nodehooks.CodeMountInvalid},
		{"hard and soft", func(c *nodehooks.Config) { c.SharedMounts[0].Options = []string{"ro", "hard", "soft"} }, "shared_mounts[0].options", nodehooks.CodeMountInvalid},
		{"duplicate shared name", func(c *nodehooks.Config) {
			c.SharedMounts = append(c.SharedMounts, nodehooks.Mount{Name: "apps", FSType: "nfs", Source: "h:/z", Target: "/z", Options: []string{"ro"}})
		}, "shared_mounts[1].name", nodehooks.CodeMountInvalid},
		{"duplicate shared target", func(c *nodehooks.Config) {
			c.SharedMounts = append(c.SharedMounts, nodehooks.Mount{Name: "other", FSType: "nfs", Source: "h:/z", Target: "/apps", Options: []string{"ro"}})
		}, "shared_mounts[1].target", nodehooks.CodeMountInvalid},
		{"tenant uuid", func(c *nodehooks.Config) { c.TenantMounts[0].Tenant = "acme" }, "tenant_mounts[0].tenant", nodehooks.CodeMountInvalid},
		{"tenant nests shared", func(c *nodehooks.Config) { c.TenantMounts[0].Target = "/apps/data" }, "tenant_mounts[0].target", nodehooks.CodeMountInvalid},
		{"tenant duplicate name", func(c *nodehooks.Config) {
			c.TenantMounts = append(c.TenantMounts, nodehooks.TenantMount{Tenant: tenantA, Name: "flight-data", FSType: "nfs4",
				Source: "h:/q", Target: "/mnt/other", Options: []string{"rw"}})
		}, "tenant_mounts[2].name", nodehooks.CodeMountInvalid},
		{"tenant nested targets", func(c *nodehooks.Config) {
			c.TenantMounts = append(c.TenantMounts, nodehooks.TenantMount{Tenant: tenantA, Name: "inner", FSType: "nfs4",
				Source: "h:/q", Target: "/mnt/data/inner", Options: []string{"rw"}})
		}, "tenant_mounts[2].target", nodehooks.CodeMountInvalid},
		{"hook name", func(c *nodehooks.Config) { c.Hooks[0].Name = "Bad_Name" }, "hooks[0].name", nodehooks.CodeHookInvalid},
		{"hook phase", func(c *nodehooks.Config) { c.Hooks[0].Phase = "boot" }, "hooks[0].phase", nodehooks.CodeHookInvalid},
		{"hook order", func(c *nodehooks.Config) { c.Hooks[0].Order = 100 }, "hooks[0].order", nodehooks.CodeHookInvalid},
		{"hook shebang", func(c *nodehooks.Config) { c.Hooks[0].Script = "echo hi\n" }, "hooks[0].script", nodehooks.CodeHookInvalid},
		{"hook python", func(c *nodehooks.Config) { c.Hooks[0].Script = "#!/usr/bin/python3\nprint(1)\n" }, "hooks[0].script", nodehooks.CodeHookInvalid},
		{"hook nul", func(c *nodehooks.Config) { c.Hooks[0].Script = "#!/bin/bash\n\x00" }, "hooks[0].script", nodehooks.CodeHookInvalid},
		{"hook crlf", func(c *nodehooks.Config) { c.Hooks[0].Script = "#!/bin/bash\r\necho\r\n" }, "hooks[0].script", nodehooks.CodeHookInvalid},
		{"hook syntax", func(c *nodehooks.Config) { c.Hooks[0].Script = "#!/bin/bash\nif then\n" }, "hooks[0].script", nodehooks.CodeHookInvalid},
		{"hook size", func(c *nodehooks.Config) {
			c.Hooks[0].Script = "#!/bin/bash\n" + strings.Repeat("#", nodehooks.MaxHookScriptBytes)
		}, "hooks[0].script", nodehooks.CodeHookInvalid},
		{"hook duplicate name", func(c *nodehooks.Config) { c.Hooks[1].Name = "metrics" }, "hooks[1].name", nodehooks.CodeHookInvalid},
		{"hook duplicate order", func(c *nodehooks.Config) { c.Hooks[1].Phase = "prolog"; c.Hooks[1].Order = 0 }, "hooks[1].order", nodehooks.CodeHookInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := nodehooks.Normalize(goodConfig())
			tc.mutate(&cfg)
			// Re-normalize options only where the mutation did not set them.
			errs := nodehooks.Validate(cfg)
			for _, e := range errs {
				if e.Path == tc.path && e.Code == tc.code {
					return
				}
			}
			t.Fatalf("want %s at %s, got %+v", tc.code, tc.path, errs)
		})
	}
}

func TestSameTargetAcrossTenantsAllowed(t *testing.T) {
	if errs := nodehooks.Validate(nodehooks.Normalize(goodConfig())); len(errs) != 0 {
		t.Fatalf("%+v", errs)
	}
}

func TestContentHashStableAndSensitive(t *testing.T) {
	a := nodehooks.ContentSHA256(goodConfig())
	b := nodehooks.ContentSHA256(nodehooks.Normalize(goodConfig()))
	if a != b {
		t.Fatal("hash must be computed over the normalized config")
	}
	c := goodConfig()
	c.SharedMounts[0].Target = "/apps2"
	if nodehooks.ContentSHA256(c) == a {
		t.Fatal("hash must change with content")
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", filepath.FromSlash(strings.ReplaceAll(name, "/", "__")))
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with UPDATE_GOLDEN=1): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from golden:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestRenderGolden(t *testing.T) {
	r, err := nodehooks.Render(renderInput(t))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]nodehooks.File{}
	for _, f := range r.Files {
		names = append(names, f.Name)
		byName[f.Name] = f
	}
	golden(t, "FILES.txt", []byte(strings.Join(names, "\n")+"\n"))
	for _, n := range []string{
		"etc/custos/node/mounts.tsv",
		"etc/custos/node/prolog.d/800-metrics",
		"etc/custos/node/epilog.d/795-cleanup",
		"README.md",
	} {
		f, ok := byName[n]
		if !ok {
			t.Fatalf("bundle lacks %s", n)
		}
		golden(t, n, f.Data)
	}
	if len(r.Skipped) != 0 {
		t.Fatalf("skipped: %v", r.Skipped)
	}
}

func TestMountsTSVConflictAndSkips(t *testing.T) {
	in := renderInput(t)
	in.TenantSlugs = map[string]string{tenantA: "tenant-a"}
	in.Bindings = append(in.Bindings, nodehooks.Binding{TenantID: tenantA, Account: "bad account"})
	r, err := nodehooks.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	var tsv string
	for _, f := range r.Files {
		if f.Name == "etc/custos/node/mounts.tsv" {
			tsv = string(f.Data)
		}
	}
	if !strings.Contains(tsv, "conflict\tshared-acct\n") {
		t.Fatalf("shared account must be a conflict:\n%s", tsv)
	}
	if strings.Contains(tsv, "account\tshared-acct") || strings.Contains(tsv, "tenant-b") {
		t.Fatalf("conflicting or unknown tenant leaked:\n%s", tsv)
	}
	joined := strings.Join(r.Skipped, ",")
	if !strings.Contains(joined, "tenant:"+tenantB) || !strings.Contains(joined, "account:bad account") {
		t.Fatalf("skipped = %v", r.Skipped)
	}
}

func TestNamespaceScriptsOnlyInNamespaceMode(t *testing.T) {
	in := renderInput(t)
	in.Config.IsolationMode = nodehooks.ModeTenantExclusive
	r, err := nodehooks.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Files {
		if strings.Contains(f.Name, "ns-") {
			t.Fatalf("unexpected %s in tenant_exclusive bundle", f.Name)
		}
	}
}

func extract(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
}

func TestArchiveDeterministic(t *testing.T) {
	r, err := nodehooks.Render(renderInput(t))
	if err != nil {
		t.Fatal(err)
	}
	a1, err := nodehooks.Archive(r.Files)
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := nodehooks.Render(renderInput(t))
	a2, _ := nodehooks.Archive(r2.Files)
	if !bytes.Equal(a1, a2) || nodehooks.BundleSHA256(a1) != nodehooks.BundleSHA256(a2) {
		t.Fatal("archive must be deterministic")
	}
	files := extract(t, a1)
	if len(files) != len(r.Files) {
		t.Fatalf("archive has %d files, want %d", len(files), len(r.Files))
	}
	for _, f := range r.Files {
		if !bytes.Equal(files[f.Name], f.Data) {
			t.Fatalf("%s differs after extraction", f.Name)
		}
	}
}

// writeBundle writes the rendered files under dir.
func writeBundle(t *testing.T, dir string, files []nodehooks.File) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, os.FileMode(f.Mode)); err != nil {
			t.Fatal(err)
		}
	}
}

func isShell(name string, data []byte) bool {
	return bytes.HasPrefix(data, []byte("#!/bin/bash")) || bytes.HasPrefix(data, []byte("#!/bin/sh")) ||
		strings.HasSuffix(name, "lib/common.sh")
}

func TestGeneratedScriptsBashSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH; syntax check skipped")
	}
	r, err := nodehooks.Render(renderInput(t))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range r.Files {
		if !isShell(f.Name, f.Data) {
			continue
		}
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = bytes.NewReader(f.Data)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bash -n %s: %v\n%s", f.Name, err, out)
		}
		n++
	}
	if n < 7 {
		t.Fatalf("checked only %d scripts", n)
	}
}

func TestGeneratedScriptsShellcheck(t *testing.T) {
	if os.Getenv("CUSTOS_TEST_SHELLCHECK") != "1" {
		t.Skip("set CUSTOS_TEST_SHELLCHECK=1 (needs podman) to run shellcheck")
	}
	r, err := nodehooks.Render(renderInput(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeBundle(t, dir, r.Files)
	args := []string{"run", "--rm", "--cgroups=disabled", "-v", dir + ":/mnt:ro", "-w", "/mnt/etc/custos/node",
		"docker.io/koalaman/shellcheck:v0.11.0", "-x", "-s", "bash", "-P", "/mnt/etc/custos/node",
		"prolog.d/900-custos-mounts", "epilog.d/100-custos-unmount", "ns-clone.sh", "ns-epilog.sh",
		"custos-node-sync", "lib/common.sh"}
	out, err := exec.Command("podman", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("shellcheck: %v\n%s", err, out)
	}
}
