// Package expr is the restricted expression language of
// docs/workflows.md §Templating: dotted lookups (parameters, run,
// task, item, array, tasks.<name>.*, secrets.<handle>), comparison and
// boolean operators, and integer arithmetic for `when` and
// `fanOut.count`. There are no function calls, no loops, no indexing,
// no string-to-code — the small grammar is a security feature.
//
// Templates (`{{ ... }}` inside a string) render into one argv
// element or one env value, never into shell text.
package expr

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxSrc    = 4 << 10 // 4 KiB
	maxDepth  = 32
	maxTokens = 512
)

// Value is a typed evaluation result.
type Value struct {
	Kind Kind
	Int  int64
	Str  string
	Bool bool
}

// Kind tags a Value.
type Kind int

// Value kinds.
const (
	Int Kind = iota
	String
	Bool
)

// Path is a dotted reference ("parameters.molecule").
type Path []string

// String renders the path dotted.
func (p Path) String() string { return strings.Join(p, ".") }

// Scope resolves a dotted path to a value; ok=false = undeclared.
type Scope interface {
	Lookup(path []string) (Value, bool)
}

// MapScope is a convenience Scope over nested string-keyed maps.
type MapScope map[string]any

// Lookup implements Scope over nested map[string]any values.
func (m MapScope) Lookup(path []string) (Value, bool) {
	var cur any = map[string]any(m)
	for _, seg := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return Value{}, false
		}
		cur, ok = mm[seg]
		if !ok {
			return Value{}, false
		}
	}
	switch v := cur.(type) {
	case int64:
		return Value{Kind: Int, Int: v}, true
	case int:
		return Value{Kind: Int, Int: int64(v)}, true
	case string:
		return Value{Kind: String, Str: v}, true
	case bool:
		return Value{Kind: Bool, Bool: v}, true
	case float64:
		if v == float64(int64(v)) {
			return Value{Kind: Int, Int: int64(v)}, true
		}
	}
	return Value{}, false
}

// --- lexer ---------------------------------------------------------------

type tokKind int

const (
	tEOF tokKind = iota
	tInt
	tStr
	tBool
	tPath
	tOp
	tLParen
	tRParen
)

type tok struct {
	kind tokKind
	s    string // literal text or operator
	i    int64
	b    bool
	pos  int
}

// lexError reports a syntax problem at a byte offset.
type lexError struct {
	pos int
	msg string
}

func (e *lexError) Error() string { return fmt.Sprintf("expr:%d: %s", e.pos, e.msg) }

func lex(src string) ([]tok, error) {
	if len(src) > maxSrc {
		return nil, &lexError{0, "expression too large"}
	}
	var out []tok
	i := 0
	for i < len(src) {
		if len(out) >= maxTokens {
			return nil, &lexError{i, "too many tokens"}
		}
		c := src[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case c == '(':
			out = append(out, tok{kind: tLParen, pos: i})
			i++
		case c == ')':
			out = append(out, tok{kind: tRParen, pos: i})
			i++
		case c == '"' || c == '\'':
			j := i + 1
			var sb strings.Builder
			for j < len(src) && src[j] != c {
				if src[j] == '\\' && j+1 < len(src) {
					j++
					switch src[j] {
					case 'n':
						sb.WriteByte('\n')
					case 't':
						sb.WriteByte('\t')
					default:
						sb.WriteByte(src[j])
					}
					j++
					continue
				}
				sb.WriteByte(src[j])
				j++
			}
			if j >= len(src) {
				return nil, &lexError{i, "unterminated string"}
			}
			out = append(out, tok{kind: tStr, s: sb.String(), pos: i})
			i = j + 1
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			n, err := strconv.ParseInt(src[i:j], 10, 64)
			if err != nil {
				return nil, &lexError{i, "integer overflow"}
			}
			out = append(out, tok{kind: tInt, i: n, pos: i})
			i = j
		case unicode.IsLetter(rune(c)) || c == '_':
			j := i
			for j < len(src) && (unicode.IsLetter(rune(src[j])) ||
				unicode.IsDigit(rune(src[j])) || src[j] == '_' ||
				src[j] == '-' || src[j] == '.') {
				j++
			}
			word := src[i:j]
			if word == "true" || word == "false" {
				out = append(out, tok{kind: tBool, b: word == "true", pos: i})
			} else {
				parts := strings.Split(word, ".")
				for _, p := range parts {
					if p == "" {
						return nil, &lexError{i, "malformed path " + strconv.Quote(word)}
					}
				}
				out = append(out, tok{kind: tPath, s: word, pos: i})
			}
			i = j
		case strings.ContainsRune("!<>=&|+-*/%", rune(c)):
			two := ""
			if i+1 < len(src) {
				two = src[i : i+2]
			}
			switch {
			case two == "==", two == "!=", two == "<=", two == ">=",
				two == "&&", two == "||":
				out = append(out, tok{kind: tOp, s: two, pos: i})
				i += 2
			case c == '!', c == '<', c == '>', c == '+', c == '-',
				c == '*', c == '/', c == '%':
				out = append(out, tok{kind: tOp, s: string(c), pos: i})
				i++
			default:
				return nil, &lexError{i, "bad operator"}
			}
		default:
			return nil, &lexError{i, "unexpected character " + strconv.QuoteRune(rune(c))}
		}
	}
	out = append(out, tok{kind: tEOF, pos: len(src)})
	return out, nil
}

// --- parser --------------------------------------------------------------

type nodeKind int

const (
	nLit nodeKind = iota
	nRef
	nUnary
	nBinary
)

type node struct {
	kind  nodeKind
	op    string
	lit   Value
	path  Path
	left  *node
	right *node
}

// Expr is a parsed, evaluatable expression.
type Expr struct {
	root *node
	src  string
}

type parser struct {
	toks  []tok
	i     int
	depth int
}

func (p *parser) peek() tok { return p.toks[p.i] }
func (p *parser) next() tok { t := p.toks[p.i]; p.i++; return t }

// Parse compiles src into an Expr.
func Parse(src string) (*Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	root, err := p.expr(0)
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, &lexError{p.peek().pos, "trailing input"}
	}
	return &Expr{root: root, src: src}, nil
}

// expr parses at a precedence level (0 = loosest).
func (p *parser) expr(level int) (*node, error) {
	if level > maxDepth {
		return nil, &lexError{p.peek().pos, "expression too deep"}
	}
	// Binary precedence groups, loosest first.
	if level >= len(binLevels) {
		return p.unary(level)
	}
	left, err := p.expr(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tOp || !binLevels[level][t.s] {
			return left, nil
		}
		p.next()
		right, err := p.expr(level + 1)
		if err != nil {
			return nil, err
		}
		left = &node{kind: nBinary, op: t.s, left: left, right: right}
	}
}

// binLevels orders operators loosest-first.
var binLevels = []map[string]bool{
	{"||": true},
	{"&&": true},
	{"==": true, "!=": true},
	{"<": true, "<=": true, ">": true, ">=": true},
	{"+": true, "-": true},
	{"*": true, "/": true, "%": true},
}

func (p *parser) unary(level int) (*node, error) {
	t := p.peek()
	if t.kind == tOp && (t.s == "!" || t.s == "-") {
		p.next()
		if level+1 > maxDepth {
			return nil, &lexError{t.pos, "expression too deep"}
		}
		n, err := p.unary(level + 1)
		if err != nil {
			return nil, err
		}
		return &node{kind: nUnary, op: t.s, left: n}, nil
	}
	return p.primary()
}

func (p *parser) primary() (*node, error) {
	t := p.next()
	switch t.kind {
	case tInt:
		return &node{kind: nLit, lit: Value{Kind: Int, Int: t.i}}, nil
	case tStr:
		return &node{kind: nLit, lit: Value{Kind: String, Str: t.s}}, nil
	case tBool:
		return &node{kind: nLit, lit: Value{Kind: Bool, Bool: t.b}}, nil
	case tPath:
		return &node{kind: nRef, path: strings.Split(t.s, ".")}, nil
	case tLParen:
		p.depth++
		if p.depth > maxDepth {
			return nil, &lexError{t.pos, "expression too deep"}
		}
		n, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tRParen {
			return nil, &lexError{p.peek().pos, "missing )"}
		}
		p.next()
		p.depth--
		return n, nil
	}
	return nil, &lexError{t.pos, "unexpected token"}
}

// Refs returns every dotted path the expression references.
func (e *Expr) Refs() []Path {
	var out []Path
	seen := map[string]bool{}
	var walk func(n *node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		if n.kind == nRef && !seen[n.path.String()] {
			seen[n.path.String()] = true
			out = append(out, n.path)
		}
		walk(n.left)
		walk(n.right)
	}
	walk(e.root)
	return out
}

// --- evaluator -----------------------------------------------------------

// EvalError reports an evaluation-time problem.
type EvalError struct{ msg string }

func (e *EvalError) Error() string { return "expr: " + e.msg }

// Eval evaluates the expression against scope.
func (e *Expr) Eval(s Scope) (Value, error) {
	return eval(e.root, s)
}

func eval(n *node, s Scope) (Value, error) {
	switch n.kind {
	case nLit:
		return n.lit, nil
	case nRef:
		v, ok := s.Lookup(n.path)
		if !ok {
			return Value{}, &EvalError{"unresolved reference " + n.path.String()}
		}
		return v, nil
	case nUnary:
		v, err := eval(n.left, s)
		if err != nil {
			return v, err
		}
		switch n.op {
		case "!":
			if v.Kind != Bool {
				return Value{}, &EvalError{"! on non-bool"}
			}
			return Value{Kind: Bool, Bool: !v.Bool}, nil
		case "-":
			if v.Kind != Int {
				return Value{}, &EvalError{"- on non-int"}
			}
			return Value{Kind: Int, Int: -v.Int}, nil
		}
	case nBinary:
		l, err := eval(n.left, s)
		if err != nil {
			return l, err
		}
		r, err := eval(n.right, s)
		if err != nil {
			return r, err
		}
		return apply(n.op, l, r)
	}
	return Value{}, &EvalError{"bad node"}
}

func apply(op string, l, r Value) (Value, error) {
	switch op {
	case "==", "!=":
		if l.Kind != r.Kind {
			return Value{Kind: Bool, Bool: op == "!="}, nil
		}
		var eq bool
		switch l.Kind {
		case Int:
			eq = l.Int == r.Int
		case String:
			eq = l.Str == r.Str
		case Bool:
			eq = l.Bool == r.Bool
		}
		if op == "!=" {
			eq = !eq
		}
		return Value{Kind: Bool, Bool: eq}, nil
	case "&&", "||":
		if l.Kind != Bool || r.Kind != Bool {
			return Value{}, &EvalError{op + " on non-bool"}
		}
		if op == "&&" {
			return Value{Kind: Bool, Bool: l.Bool && r.Bool}, nil
		}
		return Value{Kind: Bool, Bool: l.Bool || r.Bool}, nil
	case "<", "<=", ">", ">=":
		if l.Kind != Int || r.Kind != Int {
			return Value{}, &EvalError{op + " on non-int"}
		}
		var b bool
		switch op {
		case "<":
			b = l.Int < r.Int
		case "<=":
			b = l.Int <= r.Int
		case ">":
			b = l.Int > r.Int
		case ">=":
			b = l.Int >= r.Int
		}
		return Value{Kind: Bool, Bool: b}, nil
	case "+", "-", "*", "/", "%":
		if l.Kind != Int || r.Kind != Int {
			return Value{}, &EvalError{op + " on non-int"}
		}
		switch op {
		case "+":
			v := l.Int + r.Int
			if (r.Int < 0 && v > l.Int) || (r.Int > 0 && v < l.Int) {
				return Value{}, &EvalError{"integer overflow"}
			}
			return Value{Kind: Int, Int: v}, nil
		case "-":
			v := l.Int - r.Int
			if (r.Int < 0 && v < l.Int) || (r.Int > 0 && v > l.Int) {
				return Value{}, &EvalError{"integer overflow"}
			}
			return Value{Kind: Int, Int: v}, nil
		case "*":
			if (l.Int == -1 && r.Int == math.MinInt64) ||
				(r.Int == -1 && l.Int == math.MinInt64) {
				return Value{}, &EvalError{"integer overflow"}
			}
			v := l.Int * r.Int
			if l.Int != 0 && v/l.Int != r.Int {
				return Value{}, &EvalError{"integer overflow"}
			}
			return Value{Kind: Int, Int: v}, nil
		case "/", "%":
			if r.Int == 0 {
				return Value{}, &EvalError{"division by zero"}
			}
			if l.Int == math.MinInt64 && r.Int == -1 {
				return Value{}, &EvalError{"integer overflow"}
			}
			if op == "/" {
				return Value{Kind: Int, Int: l.Int / r.Int}, nil
			}
			return Value{Kind: Int, Int: l.Int % r.Int}, nil
		}
	}
	return Value{}, &EvalError{"unknown op " + op}
}
