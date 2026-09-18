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

// Render evaluates the template; values stringify as decimal ints,
// true/false, or the string itself.
func (t *Template) Render(s Scope) (string, error) {
	var b strings.Builder
	for _, p := range t.parts {
		if p.expr == nil {
			b.WriteString(p.lit)
			continue
		}
		v, err := p.expr.Eval(s)
		if err != nil {
			return "", err
		}
		switch v.Kind {
		case Int:
			b.WriteString(strconv.FormatInt(v.Int, 10))
		case Bool:
			b.WriteString(strconv.FormatBool(v.Bool))
		case String:
			b.WriteString(v.Str)
		}
	}
	return b.String(), nil
}

// BareExpr strips an optional surrounding `{{ }}` and parses what
// remains — `when` and `fanOut.count` accept either form.
func BareExpr(src string) (*Expr, error) {
	s := strings.TrimSpace(src)
	if strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}") {
		s = strings.TrimSpace(s[2 : len(s)-2])
	}
	return Parse(s)
}
