package shellcheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/sc2086.json1")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func serve(t *testing.T, status int, version string, result []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/shellcheck" {
			http.NotFound(w, r)
			return
		}
		var req shellcheckRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		if req.Shell != "bash" {
			t.Errorf("shell: %q", req.Shell)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Version string          `json:"version"`
			Result  json.RawMessage `json:"result"`
		}{version, result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func input() validation.Input {
	return validation.Input{Language: workflowspec.LanguageBash,
		Script: []byte("#!/bin/bash\necho $x\n")}
}

func TestMapsComments(t *testing.T) {
	srv := serve(t, 200, "0.11.0", fixture(t))
	v, err := New(Config{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	res, err := v.Validate(context.Background(), input())
	if err != nil {
		t.Fatal(err)
	}
	if res.Tool.Version != "0.11.0" {
		t.Errorf("version: %q", res.Tool.Version)
	}
	if len(res.Diagnostics) != 2 {
		t.Fatalf("diags: %+v", res.Diagnostics)
	}
	d := res.Diagnostics[0]
	if d.Source != "shellcheck" || d.Code != "SC2086" ||
		d.Severity != validation.SeverityWarning ||
		d.Line != 2 || d.Column != 6 || d.EndColumn != 9 {
		t.Errorf("diag: %+v", d)
	}
	if res.Diagnostics[1].Severity != validation.SeverityInfo {
		t.Errorf("level mapping: %+v", res.Diagnostics[1])
	}
}

func TestSidecarError(t *testing.T) {
	srv := serve(t, 502, "0.11.0", []byte(`{}`))
	v, _ := New(Config{Endpoint: srv.URL})
	if _, err := v.Validate(context.Background(), input()); err == nil {
		t.Fatal("non-200 must surface as error (pipeline -> CUSTOS900)")
	}
}

func TestEndpointMustBeLoopback(t *testing.T) {
	for _, ep := range []string{
		"http://example.com:8481", "https://127.0.0.1:8481",
		"http://0.0.0.0:8481", "http://[::1]:8481",
	} {
		if _, err := New(Config{Endpoint: ep}); err == nil {
			t.Errorf("%s accepted", ep)
		}
	}
	if _, err := New(Config{Endpoint: "http://localhost:8481"}); err != nil {
		t.Errorf("localhost rejected: %v", err)
	}
}
