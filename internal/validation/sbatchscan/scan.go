package sbatchscan

import (
	"bytes"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/shsyntax"
	"github.com/Exonical/custos/internal/workflowspec"
)

// ParsedOption is one option token inside a directive.
type ParsedOption struct {
	Option   *Option // nil when unrecognized
	Name     string  // as written ("--qos", "-q", "--parti")
	Value    string  // quotes removed; no expansion (sbatch reads literals)
	HasValue bool
	Field    Field // canonical field when Option resolved, else unknown
	Abbrev   bool  // long name was an unambiguous prefix abbreviation
}

// Directive is one candidate #SBATCH comment.
type Directive struct {
	Line, Column int
	Raw          string // the comment text after '#'
	Honored      bool   // before the first non-blank non-comment line
	Options      []ParsedOption
	Unknown      []string
	ParseErr     string
}

// ScanResult is the scanner's output.
type ScanResult struct {
	Directives  []Directive
	Diagnostics []validation.Diagnostic // INFO/WARNING (CUSTOS011, CUSTOS014)
}

// directiveRe finds "# optional-space optional-! SBATCH" occurrences in a
// raw line (case-insensitive).
var directiveRe = regexp.MustCompile(`(?i)#[ \t]*!?[ \t]*SBATCH`)

// zeroWidth are invisible runes that can hide directive-looking text.
func zeroWidth(r rune) bool {
	return r == 0x200B || r == 0x200C || r == 0x200D || r == 0xFEFF
}

// isDirectiveText reports whether comment text (after '#') begins a
// directive: optional spaces, optional '!', then SBATCH case-insensitive.
// Returns the remainder after the keyword and whether a zero-width
// character had to be stripped to match.
func isDirectiveText(text string) (rest string, suspicious bool, ok bool) {
	t := strings.TrimLeft(text, " \t")
	if strings.HasPrefix(t, "!") {
		t = t[1:]
		t = strings.TrimLeft(t, " \t")
	}
	if len(t) >= 6 && strings.EqualFold(t[:6], "SBATCH") {
		return t[6:], false, true
	}
	// Zero-width characters can hide "SBATCH" from a naive eye; strip and
	// re-check — flagged as suspicious, never a directive.
	var b strings.Builder
	for _, r := range t {
		if !zeroWidth(r) {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) >= 6 && strings.EqualFold(s[:6], "SBATCH") {
		return s[6:], true, true
	}
	return "", false, false
}

// firstContentLine returns the 1-based line of the first non-blank,
// non-comment line (shebang counts as a comment); 0 when none exists.
func firstContentLine(script []byte) int {
	for i, ln := range bytes.Split(script, []byte("\n")) {
		t := bytes.TrimLeft(ln, " \t\r")
		if len(t) == 0 || t[0] == '#' {
			continue
		}
		return i + 1
	}
	return 0
}

// Scan collects directive candidates from comment nodes (unioned with a
// raw-line fallback) plus directive-like text outside comments.
func Scan(script []byte, lang workflowspec.Language) (ScanResult, error) {
	var res ScanResult
	f, _ := shsyntax.ParseForScan(script, lang) // parse errors are reported by shsyntax; fallback still runs
	limit := firstContentLine(script)

	type cand struct {
		line, col int
		text      string // comment text after '#'
	}
	seen := map[int]bool{}
	var cands []cand
	commentCols := map[[2]int]bool{}

	if f != nil {
		syntax.Walk(f, func(n syntax.Node) bool {
			c, ok := n.(*syntax.Comment)
			if !ok {
				return true
			}
			ln, col := int(c.Pos().Line()), int(c.Pos().Col())
			commentCols[[2]int{ln, col}] = true
			if _, dup := seen[ln]; !dup {
				seen[ln] = true
				cands = append(cands, cand{ln, col, c.Text})
			}
			return true
		})
	}
	// Raw-line fallback over comment-shaped lines, only when the AST is
	// unavailable — a broken script cannot hide directives. When the AST
	// parsed, only its comment nodes count (heredoc/string bodies are
	// not comments and are handled via CUSTOS011 below).
	if f == nil {
		for i, ln := range bytes.Split(script, []byte("\n")) {
			line := i + 1
			t := bytes.TrimLeft(ln, " \t")
			if len(t) == 0 || t[0] != '#' || seen[line] {
				continue
			}
			col := len(ln) - len(t) + 1
			commentCols[[2]int{line, col}] = true
			seen[line] = true
			cands = append(cands, cand{line, col, string(t[1:])})
		}
	}

	for _, c := range cands {
		rest, suspicious, ok := isDirectiveText(c.text)
		if !ok {
			continue
		}
		if suspicious {
			// Not a directive to sbatch; warn only, never judged.
			res.Diagnostics = append(res.Diagnostics, validation.Diagnostic{
				Source: "sbatchscan", Code: "CUSTOS014",
				Severity: validation.SeverityWarning,
				Line:     c.line, Column: c.col,
				Message: "suspicious directive-like comment (zero-width characters)",
			})
			continue
		}
		d := Directive{Line: c.line, Column: c.col,
			Raw:     c.text,
			Honored: limit == 0 || c.line < limit}
		d.Options, d.Unknown, d.ParseErr = tokenize(rest)
		res.Directives = append(res.Directives, d)
	}

	// Directive-like text outside comments: heredoc bodies, strings.
	for i, ln := range bytes.Split(script, []byte("\n")) {
		line := i + 1
		for _, m := range directiveRe.FindAllIndex(ln, -1) {
			if commentCols[[2]int{line, m[0] + 1}] {
				continue // a real comment; handled above
			}
			res.Diagnostics = append(res.Diagnostics, validation.Diagnostic{
				Source: "sbatchscan", Code: "CUSTOS011",
				Severity: validation.SeverityInfo,
				Line:     line, Column: m[0] + 1,
				Message: "directive-like text outside a comment; sbatch will not honor it",
			})
		}
	}
	validation.SortDiagnostics(res.Diagnostics)
	return res, nil
}

// tokenize splits a directive remainder into options using shell-word
// rules (quotes removed, no expansion — sbatch reads literal text) and
// getopt_long semantics.
func tokenize(rest string) (opts []ParsedOption, unknown []string, perr string) {
	words, err := splitWords(rest)
	if err != nil {
		return nil, nil, err.Error()
	}
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case strings.HasPrefix(w, "--"):
			name, val, has := strings.Cut(w[2:], "=")
			o, abbr := LookupLong(name)
			if o == nil {
				unknown = append(unknown, "--"+name)
				continue
			}
			po := ParsedOption{Option: o, Name: "--" + name, Value: val,
				HasValue: has, Field: o.Field, Abbrev: abbr}
			if !has && o.Value == ValueRequired {
				if i+1 < len(words) {
					i++
					po.Value, po.HasValue = words[i], true
				} else {
					perr = "option --" + name + " requires a value"
				}
			}
			opts = append(opts, po)
		case strings.HasPrefix(w, "-") && len(w) > 1:
			rest := w[1:]
			for j := 0; j < len(rest); j++ {
				o := LookupShort(rest[j])
				if o == nil {
					unknown = append(unknown, "-"+string(rest[j]))
					continue
				}
				po := ParsedOption{Option: o, Name: "-" + string(rest[j]), Field: o.Field}
				rem := rest[j+1:]
				if o.Value == ValueRequired {
					if rem != "" {
						po.Value, po.HasValue = rem, true
					} else if i+1 < len(words) {
						i++
						po.Value, po.HasValue = words[i], true
					} else {
						perr = "option -" + string(rest[j]) + " requires a value"
					}
					opts = append(opts, po)
					break
				}
				if rem != "" {
					if v, ok := strings.CutPrefix(rem, "="); ok {
						po.Value, po.HasValue = v, true
						opts = append(opts, po)
						break
					}
					// Bundled flags: continue to next letter.
				}
				opts = append(opts, po)
			}
		default:
			unknown = append(unknown, w)
		}
	}
	return opts, unknown, perr
}

// splitWords performs shell-word splitting with quote removal and no
// expansion (sbatch treats $VAR as literal text).
func splitWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '\'':
			inWord = true
			for i < len(s) && s[i] != '\'' {
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, errf("unterminated single quote")
			}
			i++
		case r == '"':
			inWord = true
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, errf("unterminated double quote")
			}
			i++
		case r == '\\' && i < len(s):
			inWord = true
			cur.WriteByte(s[i])
			i++
		case unicode.IsSpace(r):
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			inWord = true
			cur.WriteRune(r)
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

type scanError string

func (e scanError) Error() string { return string(e) }

func errf(msg string) error { return scanError(msg) }
