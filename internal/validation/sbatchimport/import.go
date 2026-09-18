// Package sbatchimport implements the legacy #SBATCH import mode of
// docs/script-validation.md §Legacy import mode: mappable directives
// become a structured resource/placement proposal; every directive
// line is rewritten to "# [custos] imported: <original>" (same line
// count, CRLF preserved); unmappable directives yield CUSTOS201
// diagnostics for the author to resolve.
package sbatchimport

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/sbatchscan"
	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/units"
)

// Proposal is the import result: nothing is saved; the caller decides.
type Proposal struct {
	Resources   workflowspec.Resources
	Partition   string `json:"partition,omitempty"`
	QoS         string `json:"qos,omitempty"`
	Name        string `json:"name,omitempty"`
	Stdout      string `json:"stdout,omitempty"`
	Stderr      string `json:"stderr,omitempty"`
	WorkingDir  string `json:"working_dir,omitempty"`
	Rewritten   []byte // directive lines replaced; same line count
	Imported    []string
	Diagnostics []validation.Diagnostic
}

// mappable lists the sbatch long names that convert into the resource
// or placement proposal.
var mappable = map[string]bool{
	"nodes": true, "ntasks": true, "ntasks-per-node": true,
	"cpus-per-task": true, "mem": true, "mem-per-cpu": true,
	"gres": true, "gpus": true, "gpus-per-node": true,
	"time": true, "array": true, "partition": true, "qos": true,
	"exclusive": true, "constraint": true, "licenses": true,
	"job-name": true, "output": true, "error": true, "chdir": true,
}

// fieldOf returns the canonical field an imported option contributed to.
func fieldOf(opt *sbatchscan.Option) string {
	if opt == nil {
		return "unknown"
	}
	switch opt.Long {
	case "partition", "qos", "job-name", "output", "error", "chdir":
		return string(opt.Field)
	case "gres", "gpus", "gpus-per-node":
		return "gres"
	case "mem", "mem-per-cpu":
		return "memory"
	case "time":
		return "walltime"
	default:
		return string(opt.Field)
	}
}

var memRe = regexp.MustCompile(`^(\d+)([KMGTkmgt]?)$`)

// memMiB parses a Slurm memory value: bare numbers and M are MiB,
// K/G/T scale by powers of 1024.
func memMiB(v string) (int64, error) {
	m := memRe.FindStringSubmatch(v)
	if m == nil {
		return 0, fmt.Errorf("invalid memory value %q", v)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, err
	}
	switch strings.ToUpper(m[2]) {
	case "", "M":
	case "K":
		n /= 1024
	case "G":
		n *= 1024
	case "T":
		n *= 1024 * 1024
	}
	return n, nil
}

var arrayRe = regexp.MustCompile(`^(\d+)-(\d+)(?::(\d+))?(?:%(\d+))?$`)

func parseArray(v string) (*workflowspec.ArraySpec, bool) {
	m := arrayRe.FindStringSubmatch(v)
	if m == nil {
		return nil, false
	}
	a := &workflowspec.ArraySpec{}
	a.Start, _ = strconv.Atoi(m[1])
	a.End, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		a.Step, _ = strconv.Atoi(m[3])
	}
	if m[4] != "" {
		a.MaxConcurrent, _ = strconv.Atoi(m[4])
	}
	return a, true
}

var gresRe = regexp.MustCompile(`(?i)^gpu(?::([^:,]+))?(?::(\d+))?$`)

// gpuOf parses a gpu GRES token into Type/Count.
func gpuOf(v string) (workflowspec.GPURequest, bool) {
	v = strings.TrimSpace(v)
	m := gresRe.FindStringSubmatch(v)
	if m == nil {
		// bare count from --gpus=N
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return workflowspec.GPURequest{Count: n}, true
		}
		return workflowspec.GPURequest{}, false
	}
	g := workflowspec.GPURequest{Type: m[1], Count: 1}
	if n, err := strconv.Atoi(m[1]); err == nil {
		// gpu:N — a bare count carries no type.
		g.Type, g.Count = "", n
	}
	if m[2] != "" {
		g.Count, _ = strconv.Atoi(m[2])
	}
	return g, true
}

func unmappableDiag(line int, po sbatchscan.ParsedOption) validation.Diagnostic {
	field := "unknown"
	if po.Option != nil {
		field = fieldOf(po.Option)
	}
	return validation.Diagnostic{
		Source:   "sbatchimport",
		Code:     "CUSTOS201",
		Severity: validation.SeverityError,
		Line:     line,
		Field:    field,
		Message: fmt.Sprintf(
			"directive %s cannot be imported; remove it or configure the equivalent in the panel",
			po.Name),
	}
}

func apply(p *Proposal, line int, po sbatchscan.ParsedOption,
	imported map[string]bool) []validation.Diagnostic {
	if po.Option == nil || !mappable[po.Option.Long] {
		return []validation.Diagnostic{unmappableDiag(line, po)}
	}
	mark := func(f string) { imported[f] = true }
	switch po.Option.Long {
	case "nodes":
		n, err := strconv.Atoi(minPart(po.Value))
		if err != nil {
			return []validation.Diagnostic{unmappableDiag(line, po)}
		}
		p.Resources.Nodes = n
	case "ntasks":
		n, err := strconv.Atoi(po.Value)
		if err != nil {
			return []validation.Diagnostic{unmappableDiag(line, po)}
		}
		p.Resources.Tasks = n
	case "ntasks-per-node":
		n, err := strconv.Atoi(po.Value)
		if err != nil {
			return []validation.Diagnostic{unmappableDiag(line, po)}
		}
		p.Resources.TasksPerNode = n
	case "cpus-per-task":
		n, err := strconv.Atoi(po.Value)
		if err != nil {
			return []validation.Diagnostic{unmappableDiag(line, po)}
		}
		p.Resources.CPUsPerTask = n
	case "mem":
		n, err := memMiB(po.Value)
		if err != nil {
			return []validation.Diagnostic{unmappableDiag(line, po)}
		}
		p.Resources.MemoryPerNodeMiB = n
	case "mem-per-cpu":
		n, err := memMiB(po.Value)
		if err != nil {
			return []validation.Diagnostic{unmappableDiag(line, po)}
		}
		p.Resources.MemoryPerCPUMiB = n
	case "gres", "gpus", "gpus-per-node":
		// A gres list may carry several comma-separated TRES; only gpu
		// entries are mappable.
		for _, tok := range strings.Split(po.Value, ",") {
			g, ok := gpuOf(tok)
			if !ok {
				return []validation.Diagnostic{unmappableDiag(line, po)}
			}
			p.Resources.GPU = &g
		}
	case "time":
		d, ok := units.ParseSlurmTime(po.Value)
		if !ok {
			return []validation.Diagnostic{unmappableDiag(line, po)}
		}
		p.Resources.Walltime = workflowspec.Duration(d)
	case "array":
		a, ok := parseArray(po.Value)
		if !ok {
			return []validation.Diagnostic{unmappableDiag(line, po)}
		}
		p.Resources.Array = a
	case "partition":
		p.Partition = po.Value
	case "qos":
		p.QoS = po.Value
	case "exclusive":
		p.Resources.Exclusive = true
	case "constraint":
		p.Resources.Constraints = po.Value
	case "licenses":
		p.Resources.Licenses = strings.Split(po.Value, ",")
	case "job-name":
		p.Name = po.Value
	case "output":
		p.Stdout = po.Value
	case "error":
		p.Stderr = po.Value
	case "chdir":
		p.WorkingDir = po.Value
	}
	mark(fieldOf(po.Option))
	return nil
}

// minPart returns the lower bound of a Slurm range/count value
// ("4-8" -> "4").
func minPart(v string) string {
	if i := strings.IndexAny(v, "-,"); i >= 0 {
		return v[:i]
	}
	return v
}

// Import converts a legacy #SBATCH script into a Proposal. The
// directive scanner is reused; this package only interprets the
// canonicalized options and rewrites lines.
func Import(script []byte, lang workflowspec.Language) (Proposal, error) {
	res, err := sbatchscan.Scan(script, lang)
	if err != nil {
		return Proposal{}, err
	}
	p := Proposal{Rewritten: script}
	lines := bytes.Split(script, []byte("\n"))
	rewrite := make(map[int][]byte)
	imported := map[string]bool{}
	for _, d := range res.Directives {
		idx := d.Line - 1
		if idx < 0 || idx >= len(lines) {
			continue
		}
		orig := bytes.TrimSuffix(lines[idx], []byte("\r"))
		// Only whole-line directive comments are rewritten; inline
		// "#SBATCH" text in code is not a directive sbatch honors.
		trim := bytes.TrimLeft(orig, " \t")
		if len(trim) == 0 || trim[0] != '#' ||
			int(bytes.Index(orig, []byte("#")))+1 != d.Column {
			continue
		}
		for _, po := range d.Options {
			p.Diagnostics = append(p.Diagnostics, apply(&p, d.Line, po, imported)...)
		}
		for _, u := range d.Unknown {
			p.Diagnostics = append(p.Diagnostics, unmappableDiag(d.Line,
				sbatchscan.ParsedOption{Name: u}))
		}
		rewrite[idx] = append([]byte("# [custos] imported: "), lines[idx]...)
	}
	if len(rewrite) > 0 {
		out := make([][]byte, len(lines))
		copy(out, lines)
		for i, l := range rewrite {
			out[i] = l
		}
		p.Rewritten = bytes.Join(out, []byte("\n"))
	}
	for f := range imported {
		p.Imported = append(p.Imported, f)
	}
	sort.Strings(p.Imported)
	validation.SortDiagnostics(p.Diagnostics)
	return p, nil
}
