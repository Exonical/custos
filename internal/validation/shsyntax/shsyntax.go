// Package shsyntax validates bash/POSIX shell syntax using
// mvdan.cc/sh/v3 and reports structural findings (CRLF, foreign
// scheduler directives).
package shsyntax

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// Version reported in ToolVersion for reproducibility.
const Version = "mvdan.cc/sh/v3 v3.14.1"

// Validator implements validation.ScriptValidator for bash and sh.
type Validator struct{}

// Name returns the validator source name.
func (Validator) Name() string { return "shsyntax" }

// Languages supported by this validator.
func (Validator) Languages() []workflowspec.Language {
	return []workflowspec.Language{workflowspec.LanguageBash, workflowspec.LanguageSh}
}

// variant maps a language to a parser variant.
func variant(lang workflowspec.Language) syntax.LangVariant {
	if lang == workflowspec.LanguageSh {
		return syntax.LangPOSIX
	}
	return syntax.LangBash
}

// ParseForScan parses the script for the directive scanner so the
// payload is parsed once when the pipeline shares the AST.
func ParseForScan(script []byte, lang workflowspec.Language) (*syntax.File, error) {
	p := syntax.NewParser(syntax.Variant(variant(lang)), syntax.KeepComments(true))
	return p.Parse(bytes.NewReader(script), "script")
}

// foreign directive prefixes from other schedulers.
var foreignPrefixes = []string{"PBS", "$ ", "BSUB", "COBALT"}

// Validate parses the script and reports syntax/structure diagnostics.
func (Validator) Validate(ctx context.Context, in validation.Input) (validation.Result, error) {
	res := validation.Result{Tool: validation.ToolVersion{Name: "shsyntax", Version: Version}}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	f, err := ParseForScan(in.Script, in.Language)
	if err != nil {
		var perr syntax.ParseError
		if errors.As(err, &perr) {
			pos := perr.Pos
			res.Diagnostics = append(res.Diagnostics, validation.Diagnostic{
				Source: "shsyntax", Code: "CUSTOS001",
				Severity: validation.SeverityError,
				Line:     int(pos.Line()), Column: int(pos.Col()),
				Message: "syntax error: " + perr.Text,
			})
		} else {
			return res, err
		}
	}
	lines := bytes.Split(in.Script, []byte("\n"))
	for i, ln := range lines {
		if bytes.HasSuffix(ln, []byte("\r")) {
			res.Diagnostics = append(res.Diagnostics, validation.Diagnostic{
				Source: "shsyntax", Code: "CUSTOS010",
				Severity: validation.SeverityWarning,
				Line:     i + 1, Column: 1,
				Message: "CRLF line endings; bash will choke on \\r — save with LF",
			})
			break
		}
	}
	if f != nil {
		syntax.Walk(f, func(n syntax.Node) bool {
			c, ok := n.(*syntax.Comment)
			if !ok {
				return true
			}
			text := c.Text
			for _, p := range foreignPrefixes {
				if strings.HasPrefix(text, p) {
					res.Diagnostics = append(res.Diagnostics, validation.Diagnostic{
						Source: "shsyntax", Code: "CUSTOS012",
						Severity: validation.SeverityWarning,
						Line:     int(c.Pos().Line()), Column: int(c.Pos().Col()),
						Message: "foreign scheduler directive #" + p + "… is not honored by Slurm",
						Fix:     "Port the directive to the Custos resource panel",
					})
					break
				}
			}
			return true
		})
	}
	validation.SortDiagnostics(res.Diagnostics)
	return res, nil
}
