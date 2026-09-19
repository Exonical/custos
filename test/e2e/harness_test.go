// Package e2e drives the Custos API against the real end-to-end stack
// (deploy/e2e): Slurm 26.05 + Keycloak 26.7 + nginx TLS + the hardened
// runtime services. Tests are skipped unless CUSTOS_E2E=1; when set,
// the stack must already be up (scripts/e2e.sh up).
//
// Host resolution: the issuer https://keycloak.e2e:8443 must resolve
// identically inside and outside the stack. Inside, nginx carries a
// compose-network alias. Outside, either /etc/hosts maps keycloak.e2e
// to 127.0.0.1, or the default DNS pinning below dials 127.0.0.1 for
// any *.e2e host (CUSTOS_E2E_DNS overrides the target IP).
package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	defAPI       = "https://127.0.0.1:8080"
	defKeycloak  = "https://keycloak.e2e:8443/realms/custos"
	defClientID  = "custos-e2e"
	defClientSec = "e2e-client-secret"
	defEngine    = "podman"
)

// Test-only credentials, identical to deploy/e2e/keycloak/realm-custos.json.
var passwords = map[string]string{
	"alice":          "alice-e2e-password",
	"bob":            "bob-e2e-password",
	"platform-admin": "platform-admin-e2e-password",
}

type env struct {
	api, keycloak, clientID, clientSecret, ca, engine string
	hc                                                *http.Client
}

var e *env

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func caPath() string {
	if v := os.Getenv("CUSTOS_E2E_CA"); v != "" {
		return v
	}
	// test/e2e -> repo root -> deploy/e2e/.secrets/e2e-ca.crt
	return filepath.Join("..", "..", "deploy", "e2e", ".secrets", "e2e-ca.crt")
}

func TestMain(m *testing.M) {
	if os.Getenv("CUSTOS_E2E") != "1" {
		fmt.Fprintln(os.Stderr,
			"e2e: skipped (set CUSTOS_E2E=1 and run scripts/e2e.sh up)")
		os.Exit(0)
	}
	e = &env{
		api:          envOr("CUSTOS_E2E_API", defAPI),
		keycloak:     envOr("CUSTOS_E2E_KEYCLOAK", defKeycloak),
		clientID:     envOr("CUSTOS_E2E_CLIENT_ID", defClientID),
		clientSecret: envOr("CUSTOS_E2E_CLIENT_SECRET", defClientSec),
		ca:           caPath(),
		engine:       envOr("CUSTOS_E2E_ENGINE", defEngine),
	}
	pem, err := os.ReadFile(e.ca)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: read CA %s: %v\n", e.ca, err)
		os.Exit(1)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		fmt.Fprintf(os.Stderr, "e2e: no certificates in %s\n", e.ca)
		os.Exit(1)
	}
	dnsTarget := envOr("CUSTOS_E2E_DNS", "127.0.0.1")
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	e.hc = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Keepalives disabled: a stale reused connection makes
			// net/http transparently resend a POST whose response was
			// lost — a legitimate Idempotency-Key replay (200) the
			// suite then misreads as a duplicate submission.
			DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{
				RootCAs:    roots,
				MinVersion: tls.VersionTLS13,
			},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				// Pin *.e2e to loopback when the OS cannot resolve it
				// (no /etc/hosts entry needed on dev machines without
				// admin rights). If it does resolve, use the resolver.
				if strings.HasSuffix(host, ".e2e") {
					if _, err := net.DefaultResolver.LookupHost(
						context.Background(), host); err != nil {
						host = dnsTarget
					}
				}
				return dialer.DialContext(ctx, network,
					net.JoinHostPort(host, port))
			},
		},
	}
	// Fail fast: the stack must be up when the suite is enabled.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		e.api+"/health/ready", nil)
	if _, err := e.hc.Do(req); err != nil {
		fmt.Fprintf(os.Stderr,
			"e2e: CUSTOS_E2E=1 but API %s is unreachable: %v\n"+
				"(run scripts/e2e.sh up first)\n", e.api, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// token fetches an access token for a realm test user via the password
// grant. client may be "" for the default confidential client or
// "custos-wrong-audience" for the public client without the aud mapper.
func token(t *testing.T, user, client string) string {
	t.Helper()
	if client == "" {
		client = e.clientID
	}
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {client},
		"username":   {user},
		"password":   {passwords[user]},
		"scope":      {"openid"},
	}
	if client == e.clientID {
		form.Set("client_secret", e.clientSecret)
	}
	resp, err := e.hc.PostForm(
		e.keycloak+"/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("token %s: %v", user, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token %s: HTTP %d: %s", user, resp.StatusCode, b)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.AccessToken == "" {
		t.Fatalf("token %s: bad response %s", user, b)
	}
	return out.AccessToken
}

// api performs an API call and returns status + decoded body (or nil).
func api(t *testing.T, method, path string, body any,
	tok string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.api+"/api/v1"+path, rdr)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(b) > 0 {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%s %s: HTTP %d non-JSON body: %.200s",
				method, path, resp.StatusCode, b)
		}
	}
	return resp.StatusCode, out
}

// want asserts the status and returns the decoded body.
func want(t *testing.T, status int, body map[string]any,
	expected int, what string) map[string]any {
	t.Helper()
	if status != expected {
		t.Fatalf("%s: HTTP %d (want %d): %v", what, status, expected, body)
	}
	return body
}

// errCode extracts error.code from a decoded error body.
func errCode(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return ""
}

// diagCodes returns the set of diagnostics[].code values.
func diagCodes(body map[string]any) map[string]bool {
	out := map[string]bool{}
	if ds, ok := body["diagnostics"].([]any); ok {
		for _, d := range ds {
			if dm, ok := d.(map[string]any); ok {
				if c, ok := dm["code"].(string); ok {
					out[c] = true
				}
			}
		}
	}
	return out
}

// cexec runs a command inside the first container of a compose service.
func cexec(t *testing.T, service string, args ...string) string {
	t.Helper()
	engine := e.engine
	cf := filepath.Join("..", "..", "deploy", "e2e", "compose.yaml")
	q := exec.Command(engine, "compose", "-f", cf, "ps", "-q", service)
	qb, err := q.Output()
	if err != nil {
		t.Fatalf("compose ps %s: %v", service, err)
	}
	cid := strings.TrimSpace(strings.SplitN(string(qb), "\n", 2)[0])
	if cid == "" {
		t.Fatalf("no container for service %s", service)
	}
	cmd := exec.Command(engine, append([]string{"exec", cid}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("exec %s %v: %v\n%s", service, args, err, out)
	}
	return string(out)
}

// readRepoFile reads a repo-relative path (test/e2e is two dirs down).
func readRepoFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join("..", "..", rel))
}

// poll calls fn until it returns true or the deadline expires.
func poll(t *testing.T, what string, timeout time.Duration,
	fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s: not satisfied within %s", what, timeout)
}
