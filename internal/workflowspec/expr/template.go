package expr

import (
	"fmt"
	"strconv"
	"strings"
)

// Template is a string with `{{ expr }}` interpolations. Rendered
// output becomes one argv element or one env value — never shell text
// (docs/workflows.md §Templating).
type Template struct {
	parts []part
}

type part struct {
	lit  string
	expr *Expr // nil for literal parts
}

// ParseTemplate compiles a string containing `{{ ... }}` segments.
// `{{`/`}}` inside literals have no escape — put a space between
// braces if literal braces are needed.
func ParseTemplate(src string) (*Template, error) {
	var t Template
	for {
		i := strings.Index(src, "{{")
		if i < 0 {
			if src != "" {
				t.parts = append(t.parts, part{lit: src})
			}
			return &t, nil
		}
		if i > 0 {
			t.parts = append(t.parts, part{lit: src[:i]})
		}
		j := strings.Index(src[i+2:], "}}")
		if j < 0 {
			return nil, fmt.Errorf("template: unclosed {{ at %d", i)
		}
		inner := strings.TrimSpace(src[i+2 : i+2+j])
		e, err := Parse(inner)
		if err != nil {
			return nil, err
		}
		t.parts = append(t.parts, part{expr: e})
		src = src[i+2+j+2:]
	}
}

// SoleExpr returns the single interpolation when the template is
// exactly `{{ expr }}` with no literal text around it.
func (t *Template) SoleExpr() (*Expr, bool) {
	if len(t.parts) == 1 && t.parts[0].expr != nil {
		return t.parts[0].expr, true
	}
	return nil, false
}

// SoleRef returns the path when the expression is a bare reference.
func (e *Expr) SoleRef() (Path, bool) {
	if e.root != nil && e.root.kind == nRef {
		return e.root.path, true
	}
	return nil, false
}

// Refs returns every path referenced by any interpolation.
func (t *Template) Refs() []Path {
	var out []Path
	seen := map[string]bool{}
	for _, p := range t.parts {
		if p.expr == nil {
			continue
		}
		for _, r := range p.expr.Refs() {
			if !seen[r.String()] {
				seen[r.String()] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// Part is one rendered template segment: a literal string or a
// runtime variable reference (Runtime is allow-listed, e.g.
// SLURM_ARRAY_TASK_ID). Exactly one field is set.
type Part struct {
	Literal string
	Runtime string
}

// RenderParts renders the template into literal and runtime segments.
// A runtime reference ({{ array.taskId }}) is legal only as the entire
// value — mixing it with literal text or other expressions is an
// error. Adjacent literal segments are merged.
func (t *Template) RenderParts(s Scope) ([]Part, error) {
	var out []Part
	lit := func(v string) {
		if v == "" {
			return
		}
		if n := len(out); n > 0 && out[n-1].Runtime == "" {
			out[n-1].Literal += v
			return
		}
		out = append(out, Part{Literal: v})
	}
	for _, p := range t.parts {
		if p.expr == nil {
			lit(p.lit)
			continue
		}
		v, err := p.expr.Eval(s)
		if err != nil {
			return nil, err
		}
		switch v.Kind {
		case Runtime:
			if len(t.parts) != 1 {
				return nil, &EvalError{"runtime reference " +
					"{{ array.taskId }} must be the whole value"}
			}
			out = append(out, Part{Runtime: v.Runtime})
		case Int:
			lit(strconv.FormatInt(v.Int, 10))
		case Bool:
			lit(strconv.FormatBool(v.Bool))
		case String:
			lit(v.Str)
		}
	}
	return out, nil
}

// Render evaluates the template; values stringify as decimal ints,
// true/false, or the string itself. Runtime references are an error —
// they need RenderParts (argv/env admit path).
func (t *Template) Render(s Scope) (string, error) {
	var b strings.Builder
	parts, err := t.RenderParts(s)
	if err != nil {
		return "", err
	}
	for _, p := range parts {
		if p.Runtime != "" {
			return "", &EvalError{"runtime reference " +
				"{{ array.taskId }} cannot render to literal text"}
		}
		b.WriteString(p.Literal)
	}
	return b.String(), nil
}

// BareExpr strips `{{ }}` wrappers and parses what remains — `when`
// and `fanOut.count` accept either form, and interpolation markers
// around individual references (`{{ x }} > 0`) are unwrapped.
func BareExpr(src string) (*Expr, error) {
	var b strings.Builder
	s := src
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		s = s[i+2:]
		j := strings.Index(s, "}}")
		if j < 0 {
			return nil, fmt.Errorf("template: unclosed {{")
		}
		b.WriteString(s[:j])
		s = s[j+2:]
	}
	return Parse(strings.TrimSpace(b.String()))
}
