package executions_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/executions"
)

// mermaidEdges extracts the stateDiagram edge set from docs/workflows.md.
// Blocks are matched in document order: the first is the
// WorkflowExecution diagram, the second the TaskExecution diagram.
func mermaidEdges(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile("../../docs/workflows.md")
	if err != nil {
		t.Skip("docs/workflows.md unavailable")
	}
	blocks := regexp.MustCompile(
		"(?s)```mermaid\\s*\nstateDiagram-v2\\s*\n(.*?)```").
		FindAllStringSubmatch(string(b), -1)
	if len(blocks) < 2 {
		t.Fatalf("expected ≥2 stateDiagram blocks, found %d", len(blocks))
	}
	edgeRe := regexp.MustCompile(`^\s*(\w+)\s*-->\s*(\w+)`)
	var out [][]string
	for _, blk := range blocks {
		var edges []string
		for _, line := range strings.Split(blk[1], "\n") {
			m := edgeRe.FindStringSubmatch(line)
			if m == nil || m[1] == "[*]" || m[2] == "[*]" {
				continue
			}
			edges = append(edges, m[1]+"->"+m[2])
		}
		out = append(out, edges)
	}
	return out
}

func edgeSet[S ~string](m map[S][]S) map[string]bool {
	out := map[string]bool{}
	for from, tos := range m {
		for _, to := range tos {
			out[string(from)+"->"+string(to)] = true
		}
	}
	return out
}

func docEdgeSet(edges []string) map[string]bool {
	out := map[string]bool{}
	for _, e := range edges {
		out[e] = true
	}
	return out
}

// TestExecutionTransitionsMatchDoc asserts the WorkflowExecution edge
// table equals the mermaid diagram in docs/workflows.md.
func TestExecutionTransitionsMatchDoc(t *testing.T) {
	doc := docEdgeSet(mermaidEdges(t)[0])
	code := edgeSet(executions.ExecutionTransitions)
	for e := range doc {
		if !code[e] {
			t.Errorf("doc edge %s missing from ExecutionTransitions", e)
		}
	}
	for e := range code {
		if !doc[e] {
			t.Errorf("ExecutionTransitions edge %s not in the doc", e)
		}
	}
}

// TestTaskTransitionsMatchDoc asserts the TaskExecution edge table
// equals the mermaid diagram in docs/workflows.md.
func TestTaskTransitionsMatchDoc(t *testing.T) {
	doc := docEdgeSet(mermaidEdges(t)[1])
	code := edgeSet(executions.TaskTransitions)
	for e := range doc {
		if !code[e] {
			t.Errorf("doc edge %s missing from TaskTransitions", e)
		}
	}
	for e := range code {
		if !doc[e] {
			t.Errorf("TaskTransitions edge %s not in the doc", e)
		}
	}
}
