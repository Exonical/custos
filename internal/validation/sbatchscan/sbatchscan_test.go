package sbatchscan_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/sbatchscan"
	"github.com/Exonical/custos/internal/workflowspec"
)

// TestOptionTableCoversFixture asserts the option table covers every
// long option documented for sbatch 26.05 and nothing else.
func TestOptionTableCoversFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/sbatch-26.05-options.txt")
	if err != nil {
		t.Fatal(err)
	}
	inTable := map[string]bool{}
	for _, o := range sbatchscan.Options {
		inTable[o.Long] = true
	}
	fixture := map[string]bool{}
	for _, ln := range strings.Split(string(raw), "\n") {
		name := strings.TrimPrefix(strings.TrimSpace(ln), "--")
		if name == "" {
			continue
		}
		fixture[name] = true
		if !inTable[name] {
			t.Errorf("fixture option --%s missing from table", name)
		}
	}
	for name := range inTable {
		if !fixture[name] {
			t.Errorf("table option --%s absent from fixture", name)
		}
	}
}

var expectRe = regexp.MustCompile(`^# expect: field=(\S+) code=(\S+)`)

// TestBypassCorpus runs every file in testdata/bypass through
// Scan+Judge and asserts the expected dominant diagnostic.
func TestBypassCorpus(t *testing.T) {
	files, err := filepath.Glob("testdata/bypass/*.sh")
	if err != nil || len(files) == 0 {
		t.Fatalf("corpus missing: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			m := expectRe.FindStringSubmatch(string(raw))
			if m == nil {
				t.Fatal("missing '# expect:' header")
			}
			wantField, wantCode := m[1], m[2]
			res, err := sbatchscan.Scan(raw, workflowspec.LanguageBash)
			if err != nil {
				t.Fatal(err)
			}
			diags, err := sbatchscan.Judge(res, sbatchscan.ModeReject)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range diags {
				if d.Code != wantCode {
					continue
				}
				if wantField == "none" || d.Field == wantField ||
					(wantField == "none" && d.Field == "") {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %s/%s; got %+v", wantField, wantCode, diags)
			}
			if wantField == "none" {
				for _, d := range diags {
					if d.Severity == validation.SeveritySecurity {
						t.Fatalf("unexpected SECURITY_VIOLATION: %+v", d)
					}
				}
			}
		})
	}
}

// TestCanonicalization: every aliased option resolves to the same
// field/value across its accepted forms.
func TestCanonicalization(t *testing.T) {
	cases := []struct {
		forms []string
		field sbatchscan.Field
		value string
	}{
		{[]string{"--account=x", "--account x", "-A x", "-Ax"}, sbatchscan.FieldAccount, "x"},
		{[]string{"--partition=p", "--partition p", "-p p", "-pp"}, sbatchscan.FieldPartition, "p"},
		{[]string{"--qos=q", "--qos q", "-q q", "-qq"}, sbatchscan.FieldQoS, "q"},
		{[]string{"--nodes=4", "--nodes 4", "-N 4", "-N4"}, sbatchscan.FieldNodes, "4"},
		{[]string{"--ntasks=8", "--ntasks 8", "-n 8", "-n8"}, sbatchscan.FieldTasks, "8"},
		{[]string{"--time=1:00", "--time 1:00", "-t 1:00", "-t1:00"}, sbatchscan.FieldWalltime, "1:00"},
		{[]string{"--gpus=2", "--gpus 2", "-G 2", "-G2"}, sbatchscan.FieldGRES, "2"},
		{[]string{"--licenses=l", "--licenses l", "-L l", "-Ll"}, sbatchscan.FieldLicenses, "l"},
		{[]string{"--array=0-3", "--array 0-3", "-a 0-3", "-a0-3"}, sbatchscan.FieldArray, "0-3"},
		{[]string{"--dependency=d", "--dependency d", "-d d", "-dd"}, sbatchscan.FieldDependencies, "d"},
		{[]string{"--chdir=/w", "--chdir /w", "-D /w", "-D/w"}, sbatchscan.FieldIOPaths, "/w"},
		{[]string{"--clusters=c", "--clusters c", "-M c", "-Mc"}, sbatchscan.FieldCluster, "c"},
		{[]string{"--constraint=c", "--constraint c", "-C c", "-Cc"}, sbatchscan.FieldConstraints, "c"},
	}
	for _, c := range cases {
		for _, form := range c.forms {
			res, err := sbatchscan.Scan(
				[]byte("#!/bin/bash\n#SBATCH "+form+"\necho hi\n"),
				workflowspec.LanguageBash)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Directives) != 1 || len(res.Directives[0].Options) != 1 {
				t.Fatalf("%s: got %+v", form, res.Directives)
			}
			po := res.Directives[0].Options[0]
			if po.Field != c.field || po.Value != c.value {
				t.Fatalf("%s: want %s=%q got %s=%q", form, c.field, c.value, po.Field, po.Value)
			}
		}
	}
}

func TestUnhonoredDirective(t *testing.T) {
	res, err := sbatchscan.Scan(
		[]byte("#!/bin/bash\necho x\n#SBATCH --qos=admin\n"), workflowspec.LanguageBash)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Directives) != 1 || res.Directives[0].Honored {
		t.Fatalf("expected unhonored directive: %+v", res.Directives)
	}
	diags, _ := sbatchscan.Judge(res, sbatchscan.ModeReject)
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "not honored") {
		t.Fatalf("unhonored note missing: %+v", diags)
	}
}

func TestManyDirectivesPerf(t *testing.T) {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	for i := 0; i < 10_000; i++ {
		b.WriteString("#SBATCH --qos=q\n")
	}
	start := time.Now()
	res, err := sbatchscan.Scan([]byte(b.String()), workflowspec.LanguageBash)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Directives) != 10_000 {
		t.Fatalf("got %d directives", len(res.Directives))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("scan too slow: %v", d)
	}
}

func FuzzScan(f *testing.F) {
	f.Add("#SBATCH --qos=admin\necho hi\n")
	f.Add("#SBATCH --gres=gpu:8 -N4\n")
	f.Fuzz(func(t *testing.T, s string) {
		res, err := sbatchscan.Scan([]byte(s), workflowspec.LanguageBash)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sbatchscan.Judge(res, sbatchscan.ModeReject); err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzTokenizeDirective(f *testing.F) {
	f.Add("--qos=admin -N 4 'x y' \"a b\"")
	f.Fuzz(func(_ *testing.T, s string) {
		// Must never panic.
		_, _ = sbatchscan.Scan([]byte("#SBATCH "+s+"\n"), workflowspec.LanguageBash)
	})
}
