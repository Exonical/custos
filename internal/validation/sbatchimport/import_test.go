package sbatchimport

import (
	"bytes"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/units"
)

func TestDocExample(t *testing.T) {
	script := "#!/bin/bash\n" +
		"#SBATCH --nodes=4\n" +
		"#SBATCH --gres=gpu:h100:4\n" +
		"#SBATCH --time=04:00:00\n" +
		"echo hello\n"
	p, err := Import([]byte(script), workflowspec.LanguageBash)
	if err != nil {
		t.Fatal(err)
	}
	if p.Resources.Nodes != 4 {
		t.Errorf("nodes: %d", p.Resources.Nodes)
	}
	if p.Resources.GPU == nil || p.Resources.GPU.Type != "h100" ||
		p.Resources.GPU.Count != 4 {
		t.Errorf("gpu: %+v", p.Resources.GPU)
	}
	if time.Duration(p.Resources.Walltime) != 4*time.Hour {
		t.Errorf("walltime: %v", p.Resources.Walltime)
	}
	if len(p.Diagnostics) != 0 {
		t.Errorf("unexpected diagnostics: %+v", p.Diagnostics)
	}
	for _, f := range []string{"nodes", "gres", "walltime"} {
		found := false
		for _, i := range p.Imported {
			if i == f {
				found = true
			}
		}
		if !found {
			t.Errorf("missing imported field %s: %v", f, p.Imported)
		}
	}
	if !bytes.Contains(p.Rewritten,
		[]byte("# [custos] imported: #SBATCH --nodes=4")) {
		t.Errorf("rewrite: %q", p.Rewritten)
	}
	if !bytes.Contains(p.Rewritten, []byte("echo hello")) {
		t.Error("non-directive lines must pass through")
	}
}

func TestTimeFormats(t *testing.T) {
	cases := map[string]time.Duration{
		"30":         30 * time.Minute,
		"1:30":       time.Minute + 30*time.Second,
		"04:00:00":   4 * time.Hour,
		"7-0":        7 * 24 * time.Hour,
		"1-12":       36 * time.Hour,
		"2-06:30":    2*24*time.Hour + 6*time.Hour + 30*time.Minute,
		"1-02:03:04": 24*time.Hour + 2*time.Hour + 3*time.Minute + 4*time.Second,
	}
	for v, want := range cases {
		d, ok := units.ParseSlurmTime(v)
		if !ok || d != want {
			t.Errorf("%s: got %v (ok=%v), want %v", v, d, ok, want)
		}
	}
	for _, bad := range []string{"", "x", "1:90", "1-99:99"} {
		if _, ok := units.ParseSlurmTime(bad); ok {
			t.Errorf("%s should not parse", bad)
		}
	}
}

func TestMemoryFormats(t *testing.T) {
	cases := map[string]int64{
		"512": 512, "512M": 512, "4G": 4096, "1T": 1048576, "2048K": 2,
	}
	for v, want := range cases {
		got, err := memMiB(v)
		if err != nil || got != want {
			t.Errorf("%s: got %d err %v, want %d", v, got, err, want)
		}
	}
}

func TestUnmappableDirectives(t *testing.T) {
	script := "#SBATCH --uid=root\n" +
		"#SBATCH --export=ALL\n" +
		"#SBATCH --account=other\n" +
		"#SBATCH --mail-type=ALL\n" +
		"#SBATCH --qos=admin\n"
	p, err := Import([]byte(script), workflowspec.LanguageBash)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, d := range p.Diagnostics {
		codes = append(codes, d.Code)
	}
	if len(codes) != 4 {
		t.Fatalf("want 4 CUSTOS201, got %v", codes)
	}
	for _, c := range codes {
		if c != "CUSTOS201" {
			t.Fatalf("code %s", c)
		}
	}
	// qos is mappable; the other four are not.
	if p.QoS != "admin" {
		t.Errorf("qos: %q", p.QoS)
	}
}

func TestArrayAndOutputPatterns(t *testing.T) {
	script := "#SBATCH --array=0-99%20\n" +
		"#SBATCH --output=out-%A_%a.log\n" +
		"#SBATCH --chdir=/scratch/x\n"
	p, err := Import([]byte(script), workflowspec.LanguageBash)
	if err != nil {
		t.Fatal(err)
	}
	if p.Resources.Array == nil || p.Resources.Array.Start != 0 ||
		p.Resources.Array.End != 99 || p.Resources.Array.MaxConcurrent != 20 {
		t.Errorf("array: %+v", p.Resources.Array)
	}
	if p.Stdout != "out-%A_%a.log" {
		t.Errorf("stdout pattern must pass through: %q", p.Stdout)
	}
	if p.WorkingDir != "/scratch/x" {
		t.Errorf("chdir: %q", p.WorkingDir)
	}
}

func TestLineCountAndCRLFPreserved(t *testing.T) {
	script := "#!/bin/bash\r\n#SBATCH --nodes=2\r\n\r\nsrun x\r\n"
	p, err := Import([]byte(script), workflowspec.LanguageBash)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(p.Rewritten, []byte("\n")) != bytes.Count([]byte(script), []byte("\n")) {
		t.Error("line count changed")
	}
	if !bytes.Contains(p.Rewritten,
		[]byte("# [custos] imported: #SBATCH --nodes=2\r")) {
		t.Errorf("CRLF not preserved: %q", p.Rewritten)
	}
}

func TestHeredocUntouched(t *testing.T) {
	script := "cat <<EOF\n#SBATCH --nodes=9\nEOF\necho done\n"
	p, err := Import([]byte(script), workflowspec.LanguageBash)
	if err != nil {
		t.Fatal(err)
	}
	if p.Resources.Nodes != 0 {
		t.Error("heredoc body is not a directive")
	}
	if !bytes.Equal(p.Rewritten, []byte(script)) {
		t.Error("heredoc text must not be rewritten")
	}
}

func TestGPUSForms(t *testing.T) {
	for _, tc := range []struct {
		line     string
		typ      string
		count    int
		wantDiag int
	}{
		{"#SBATCH --gpus=8", "", 8, 0},
		{"#SBATCH --gpus-per-node=gpu:a100:2", "a100", 2, 0},
		{"#SBATCH --gres=gpu:4", "", 4, 0},
		{"#SBATCH --gres=license:foo:1", "", 0, 1},
	} {
		p, err := Import([]byte(tc.line+"\necho x\n"), workflowspec.LanguageBash)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Diagnostics) != tc.wantDiag {
			t.Errorf("%s: diags %v", tc.line, p.Diagnostics)
			continue
		}
		if tc.wantDiag == 0 {
			if p.Resources.GPU == nil || p.Resources.GPU.Count != tc.count ||
				p.Resources.GPU.Type != tc.typ {
				t.Errorf("%s: gpu %+v", tc.line, p.Resources.GPU)
			}
		}
	}
}
