package expr

import (
	"math"
	"strings"
	"testing"
)

func scope() Scope {
	return MapScope{
		"parameters": map[string]any{"shards": int64(4), "molecule": "h2o"},
		"tasks": map[string]any{
			"simulate": map[string]any{"succeededCount": int64(3), "state": "COMPLETED"},
		},
		"item":  map[string]any{"index": int64(7)},
		"array": map[string]any{"taskId": int64(11)},
		"run":   map[string]any{"scratch": "/scratch/x"},
	}
}

func evalOK(t *testing.T, src string, want Value) {
	t.Helper()
	e, err := Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	v, err := e.Eval(scope())
	if err != nil {
		t.Fatalf("eval %q: %v", src, err)
	}
	if v != want {
		t.Fatalf("%q = %+v, want %+v", src, v, want)
	}
}

func TestPrecedence(t *testing.T) {
	evalOK(t, "1 + 2 * 3", Value{Kind: Int, Int: 7})
	evalOK(t, "(1 + 2) * 3", Value{Kind: Int, Int: 9})
	evalOK(t, "10 - 2 - 3", Value{Kind: Int, Int: 5}) // left assoc
	evalOK(t, "2 * 3 % 2", Value{Kind: Int, Int: 0})
	evalOK(t, "1 + 2 > 2 && 3 < 4", Value{Kind: Bool, Bool: true})
	evalOK(t, "true || false && false", Value{Kind: Bool, Bool: true})
	evalOK(t, "!false && !true || true", Value{Kind: Bool, Bool: true})
	evalOK(t, "-2 + 5", Value{Kind: Int, Int: 3})
	evalOK(t, "parameters.shards * 2", Value{Kind: Int, Int: 8})
	evalOK(t, "tasks.simulate.succeededCount > 0",
		Value{Kind: Bool, Bool: true})
	evalOK(t, `parameters.molecule == "h2o"`, Value{Kind: Bool, Bool: true})
	evalOK(t, `parameters.molecule != "h2o"`, Value{Kind: Bool, Bool: false})
	evalOK(t, `tasks.simulate.state == "COMPLETED"`,
		Value{Kind: Bool, Bool: true})
	evalOK(t, "item.index + array.taskId", Value{Kind: Int, Int: 18})
	evalOK(t, "1 == true", Value{Kind: Bool, Bool: false}) // cross-kind
}

func TestEvalErrors(t *testing.T) {
	for _, src := range []string{
		"parameters.missing", "1 / 0", "1 % 0", `"a" + "b"`,
		"true + 1", "1 && true", "!1", "-\"x\"",
	} {
		e, err := Parse(src)
		if err != nil {
			continue // parse errors fine too
		}
		if _, err := e.Eval(scope()); err == nil {
			t.Errorf("%q should fail eval", src)
		}
	}
}

func TestOverflow(t *testing.T) {
	big := "9223372036854775807"
	for _, src := range []string{
		big + " + 1", "-" + big + " - 2", big + " * 2",
		"-9223372036854775808 / -1",
	} {
		e, err := Parse(src)
		if err != nil {
			continue
		}
		if _, err := e.Eval(scope()); err == nil {
			t.Errorf("%q should overflow", src)
		}
	}
	evalOK(t, big+" - "+big, Value{Kind: Int, Int: 0})
	evalOK(t, "9223372036854775807 * 1", Value{Kind: Int, Int: math.MaxInt64})
}

func TestBounds(t *testing.T) {
	if _, err := Parse(strings.Repeat("1+", 3000) + "1"); err == nil {
		t.Error("oversized input accepted")
	}
	deep := strings.Repeat("(", 40) + "1" + strings.Repeat(")", 40)
	if _, err := Parse(deep); err == nil {
		t.Error("depth bomb accepted")
	}
	if _, err := Parse(strings.Repeat("1 + ", 600) + "1"); err == nil {
		t.Error("token bomb accepted")
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{
		"", "1 +", "(1", "1)", "foo(", "a.b..c", "1 2", `"unterminated`,
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("%q should not parse", src)
		}
	}
}

func TestRefs(t *testing.T) {
	e, err := Parse("parameters.a + tasks.x.succeededCount > item.index")
	if err != nil {
		t.Fatal(err)
	}
	refs := e.Refs()
	if len(refs) != 3 {
		t.Fatalf("refs: %v", refs)
	}
}

func TestTemplate(t *testing.T) {
	tpl, err := ParseTemplate("out-{{ parameters.molecule }}-{{ item.index + 1 }}")
	if err != nil {
		t.Fatal(err)
	}
	if len(tpl.Refs()) != 2 {
		t.Fatalf("refs: %v", tpl.Refs())
	}
	s, err := tpl.Render(scope())
	if err != nil || s != "out-h2o-8" {
		t.Fatalf("render: %q %v", s, err)
	}
	if _, err := ParseTemplate("a {{ 1 + b"); err == nil {
		t.Error("unclosed template accepted")
	}
	if _, err := ParseTemplate("a {{ 1 + b"); err == nil {
		t.Error("unclosed template accepted")
	}
	be, err := BareExpr("{{ parameters.shards > 0 }}")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := be.Eval(scope())
	if v.Kind != Bool || !v.Bool {
		t.Fatalf("bare expr: %+v", v)
	}
	be2, err := BareExpr("parameters.shards > 0")
	if err != nil {
		t.Fatal(err)
	}
	if v2, _ := be2.Eval(scope()); v2.Kind != Bool || !v2.Bool {
		t.Fatalf("bare expr no braces: %+v", v2)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"1 + 2 * 3", "parameters.x", "a && b || c", "(1",
		"{{ x }}", "((((1))))", "-9223372036854775808", "a.b.c.d",
		"1 == 'x'", "!true", "x..y",
	} {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, s string) {
		if e, err := Parse(s); err == nil {
			_, _ = e.Eval(scope())
			_ = e.Refs()
		}
	})
}
