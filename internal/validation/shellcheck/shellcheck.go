// Package shellcheck adapts the validator sidecar (cmd/custos-validator)
// to the ScriptValidator port. The endpoint must be loopback — the
// sidecar holds no credentials and the client must never dial out.
package shellcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// Config wires the sidecar client.
type Config struct {
	// Endpoint must be http://127.0.0.1:<port> or http://localhost:<port>.
	Endpoint string
	Timeout  time.Duration // default 10s
	Shell    string        // default bash
}

func (c Config) checkEndpoint() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "http" {
		return fmt.Errorf("shellcheck endpoint %q must be http://127.0.0.1 or http://localhost", c.Endpoint)
	}
	h := u.Hostname()
	if h != "127.0.0.1" && h != "localhost" {
		return fmt.Errorf("shellcheck endpoint %q is not loopback", c.Endpoint)
	}
	return nil
}

// Validator runs ShellCheck via the sidecar.
type Validator struct {
	cfg    Config
	client *http.Client
}

// New returns the sidecar-backed validator; the endpoint is enforced
// loopback at construction.
func New(cfg Config) (*Validator, error) {
	if err := cfg.checkEndpoint(); err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Shell == "" {
		cfg.Shell = "bash"
	}
	return &Validator{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}, nil
}

// Name returns the validator source name.
func (*Validator) Name() string { return "shellcheck" }

// External reports that validation happens in the sidecar process; the
// pipeline may apply a dev-mode UnavailableSeverity downgrade to its
// failures (in-process validators always fail closed at ERROR).
func (*Validator) External() bool { return true }

// Languages supported by ShellCheck.
func (*Validator) Languages() []workflowspec.Language {
	return []workflowspec.Language{workflowspec.LanguageBash, workflowspec.LanguageSh}
}

// shellcheckRequest is the sidecar protocol body.
type shellcheckRequest struct {
	Shell  string `json:"shell"`
	Script string `json:"script"`
}

// json1Comment is one ShellCheck json1 finding.
type json1Comment struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	EndLine   int    `json:"endLine"`
	Column    int    `json:"column"`
	EndColumn int    `json:"endColumn"`
	Level     string `json:"level"`
	Code      int    `json:"code"`
	Message   string `json:"message"`
}

// response is the sidecar wrapper: {"version": ..., "result": {json1}}.
type response struct {
	Version string `json:"version"`
	Result  struct {
		Comments []json1Comment `json:"comments"`
	} `json:"result"`
}

func sevOf(level string) validation.Severity {
	switch level {
	case "error":
		return validation.SeverityError
	case "warning":
		return validation.SeverityWarning
	default: // info, style
		return validation.SeverityInfo
	}
}

// Validate posts the script to the sidecar and maps json1 comments to
// diagnostics. A transport or sidecar failure is returned as an error
// so the pipeline degrades it to CUSTOS900.
func (v *Validator) Validate(ctx context.Context, in validation.Input) (validation.Result, error) {
	res := validation.Result{Tool: validation.ToolVersion{Name: "shellcheck"}}
	body, err := json.Marshal(shellcheckRequest{
		Shell:  v.cfg.Shell,
		Script: string(in.Script),
	})
	if err != nil {
		return res, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		v.cfg.Endpoint+"/v1/shellcheck", bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	rsp, err := v.client.Do(req)
	if err != nil {
		return res, err
	}
	defer func() { _ = rsp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(rsp.Body, 1<<20))
	if err != nil {
		return res, err
	}
	if rsp.StatusCode != http.StatusOK {
		return res, fmt.Errorf("shellcheck sidecar: HTTP %d", rsp.StatusCode)
	}
	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return res, fmt.Errorf("shellcheck sidecar: bad response: %w", err)
	}
	res.Tool.Version = out.Version
	for _, c := range out.Result.Comments {
		res.Diagnostics = append(res.Diagnostics, validation.Diagnostic{
			Source:    "shellcheck",
			Code:      fmt.Sprintf("SC%d", c.Code),
			Severity:  sevOf(c.Level),
			Line:      c.Line,
			Column:    c.Column,
			EndLine:   c.EndLine,
			EndColumn: c.EndColumn,
			Message:   c.Message,
		})
	}
	return res, nil
}
