package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/api"
	"github.com/Exonical/custos/internal/platform/health"
)

func testMux() *http.ServeMux {
	mux := http.NewServeMux()
	api.Mount(mux, api.Deps{
		Health:      health.NewRegistry(),
		ReadyBudget: time.Second,
		Logger:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	return mux
}

func TestOpenAPIJSON(t *testing.T) {
	srv := httptest.NewServer(testMux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type %q", ct)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("spec is not JSON: %v", err)
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("no paths object")
	}
	for _, p := range []string{"/openapi.json", "/health/live", "/health/ready"} {
		if _, ok := paths[p]; !ok {
			t.Fatalf("path %q missing; have %v", p, paths)
		}
	}

	// ETag is stable and honored.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/openapi.json", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match got %d", resp2.StatusCode)
	}
}

func TestHealthRoutes(t *testing.T) {
	srv := httptest.NewServer(testMux())
	defer srv.Close()
	for _, p := range []string{"/health/live", "/health/ready"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s -> %d", p, resp.StatusCode)
		}
	}
}

func TestNotFoundEnvelope(t *testing.T) {
	srv := httptest.NewServer(testMux())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &body); err != nil || body.Error.Code == "" {
		t.Fatalf("bad envelope: %s", b)
	}
}
