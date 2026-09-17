// Command custos-validator is the sandboxed validator sidecar
// (docs/script-validation.md §Validation sandboxing). It serves
// ShellCheck over loopback HTTP only, holds no credentials, and runs
// the external tool with a bounded timeout in a fresh temp dir.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListen = "127.0.0.1:8481"
	maxBody       = 256*1024 + 64*1024 // script limit + envelope
	maxStdout     = 4 << 20            // cap shellcheck output
	execTimeout   = 10 * time.Second
)

// cappedBuffer is an io.Writer that keeps at most maxStdout bytes and
// silently drops the rest — a pathological shellcheck output cannot
// balloon sidecar memory.
type cappedBuffer struct {
	b bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if rem := maxStdout - c.b.Len(); rem > 0 {
		n := len(p)
		if n > rem {
			n = rem
		}
		_, _ = c.b.Write(p[:n])
	}
	return len(p), nil
}

// allowedShells is the sidecar's fixed shell allow-list.
var allowedShells = map[string]bool{
	"bash": true, "sh": true, "dash": true, "ksh": true,
}

type request struct {
	Shell  string `json:"shell"`
	Script string `json:"script"`
}

// shellcheckVersion is resolved once at startup via `shellcheck
// --version` ("version: 0.11.0").
var versionRe = regexp.MustCompile(`(?m)^version:\s*(\S+)`)

func detectVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "shellcheck", "--version").Output()
	if err != nil {
		return "unknown"
	}
	if m := versionRe.FindSubmatch(out); m != nil {
		return string(m[1])
	}
	return "unknown"
}

// runShellcheck executes shellcheck with the script on stdin in a fresh
// temp dir with a minimal environment. Exit code 1 = findings (normal).
func runShellcheck(ctx context.Context, shell, script string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "custos-validator-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	// #nosec G204 -- fixed binary name; shell is allow-listed to
	// {bash,sh,dash,ksh}; the untrusted script arrives via stdin only.
	cmd := exec.CommandContext(ctx, "shellcheck",
		"--format=json1", "--shell="+shell, "-")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr cappedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return stdout.b.Bytes(), nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return stdout.b.Bytes(), nil
	default:
		return nil, fmt.Errorf("shellcheck failed: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": msg},
	})
}

func handler(version string, sem chan struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/shellcheck",
		func(w http.ResponseWriter, r *http.Request) {
			// Bound concurrent shellcheck processes; a saturated
			// sidecar answers 503 which the client maps to a validator
			// failure (CUSTOS900).
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			default:
				writeErr(w, http.StatusServiceUnavailable,
					"validator busy")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
			var req request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeErr(w, http.StatusRequestEntityTooLarge,
					"body too large or malformed")
				return
			}
			if !allowedShells[req.Shell] {
				writeErr(w, http.StatusBadRequest, "unsupported shell")
				return
			}
			out, err := runShellcheck(r.Context(), req.Shell, req.Script)
			if err != nil {
				writeErr(w, http.StatusBadGateway, "shellcheck failed")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// json.RawMessage passes ShellCheck's output through
			// verbatim inside the version wrapper.
			_ = json.NewEncoder(w).Encode(struct {
				Version string          `json:"version"`
				Result  json.RawMessage `json:"result"`
			}{version, out})
		})
	return mux
}

func main() {
	listen := os.Getenv("CUSTOS_VALIDATOR_LISTEN")
	if listen == "" {
		listen = defaultListen
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil || (host != "127.0.0.1" && host != "localhost") {
		_, _ = fmt.Fprintf(os.Stderr,
			"custos-validator: listen address %q must be loopback\n", listen)
		os.Exit(2)
	}
	maxConc := 4
	if v := os.Getenv("CUSTOS_VALIDATOR_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 64 {
			maxConc = n
		}
	}
	version := detectVersion()
	srv := &http.Server{
		Addr:              net.JoinHostPort(host, port),
		Handler:           handler(version, make(chan struct{}, maxConc)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil &&
		!errors.Is(err, http.ErrServerClosed) {
		_, _ = fmt.Fprintf(os.Stderr, "custos-validator: %v\n", err)
		os.Exit(1)
	}
}
