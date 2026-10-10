package nodehooks_test

import (
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/nodehooks"
)

func TestBundleIsRootOnly(t *testing.T) {
	for _, mode := range []string{nodehooks.ModeNamespace, nodehooks.ModeTenantExclusive, nodehooks.ModeNodeExclusive} {
		in := renderInput(t)
		in.Config.IsolationMode = mode
		r, err := nodehooks.Render(in)
		if err != nil {
			t.Fatal(err)
		}
		dirs := map[string]bool{}
		for _, f := range r.Files {
			if f.Name == "etc/" || f.Name == "etc/custos/" || f.Name == "etc" || f.Name == "etc/custos" {
				t.Fatalf("%s: bundle must not carry an entry for %s (extraction would chmod it)", mode, f.Name)
			}
			if f.Dir {
				if !strings.HasSuffix(f.Name, "/") || len(f.Data) != 0 {
					t.Fatalf("%s: bad directory entry %+v", mode, f)
				}
				if f.Mode != 0o700 {
					t.Fatalf("%s: dir %s mode %o, want 700", mode, f.Name, f.Mode)
				}
				dirs[f.Name] = true
			}
			if strings.HasPrefix(f.Name, "etc/custos/node/") && f.Mode&0o077 != 0 {
				t.Fatalf("%s: %s has group/other bits: %o", mode, f.Name, f.Mode)
			}
			if strings.HasPrefix(f.Name, "etc/systemd/") || f.Name == "README.md" {
				if f.Mode != 0o644 {
					t.Fatalf("%s: %s mode %o, want 644", mode, f.Name, f.Mode)
				}
			}
		}
		want := []string{"etc/custos/node/", "etc/custos/node/lib/", "etc/custos/node/prolog.d/", "etc/custos/node/epilog.d/"}
		if len(dirs) != len(want) {
			t.Fatalf("%s: directory entries = %v, want exactly %v", mode, dirs, want)
		}
		for _, d := range want {
			if !dirs[d] {
				t.Fatalf("%s: missing directory entry %s", mode, d)
			}
		}
	}
}

func TestBundleDataFilesAreNotExecutable(t *testing.T) {
	r, err := nodehooks.Render(renderInput(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Files {
		switch {
		case f.Name == "etc/custos/node/mounts.tsv", f.Name == "etc/custos/node/lib/common.sh":
			if f.Mode != 0o600 {
				t.Fatalf("%s mode %o, want 600", f.Name, f.Mode)
			}
		case strings.Contains(f.Name, "etc/custos/node/") && !f.Dir:
			if f.Mode != 0o700 {
				t.Fatalf("%s mode %o, want 700", f.Name, f.Mode)
			}
		}
	}
}
