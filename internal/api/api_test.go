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
	"github.com/Exonical/custos/internal/authn"
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

func responseSchemaRef(t *testing.T, doc map[string]any, path, status string) string {
	t.Helper()
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("no paths object")
	}
	pathItem, ok := paths[path].(map[string]any)
	if !ok {
		t.Fatalf("path %q missing", path)
	}
	operation, ok := pathItem["get"].(map[string]any)
	if !ok {
		t.Fatalf("GET %q missing", path)
	}
	responses, ok := operation["responses"].(map[string]any)
	if !ok {
		t.Fatalf("GET %q has no responses", path)
	}
	response, ok := responses[status].(map[string]any)
	if !ok {
		t.Fatalf("GET %q has no %s response", path, status)
	}
	content, ok := response["content"].(map[string]any)
	if !ok {
		t.Fatalf("GET %q %s response has no content", path, status)
	}
	media, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("GET %q %s response has no JSON content", path, status)
	}
	schema, ok := media["schema"].(map[string]any)
	if !ok {
		t.Fatalf("GET %q %s response has no schema", path, status)
	}
	ref, ok := schema["$ref"].(string)
	if !ok {
		t.Fatalf("GET %q %s response has no schema ref", path, status)
	}
	return ref
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
	for _, p := range []string{"/openapi.json", "/health/live", "/health/ready", "/me"} {
		if _, ok := paths[p]; !ok {
			t.Fatalf("path %q missing; have %v", p, paths)
		}
	}
	contracts := map[string]string{
		"/tenants/{tenant}/projects/{project}/allocations": "#/components/schemas/AllocationList",
		"/tenants/{tenant}/secret-connectors":              "#/components/schemas/SecretConnectorList",
		"/tenants/{tenant}/secret-references":              "#/components/schemas/SecretReferenceList",
		"/tenants/{tenant}/accounting/top":                 "#/components/schemas/AccountingTopList",
		"/tenants/{tenant}/accounting/allocations":         "#/components/schemas/AccountingAllocationList",
	}
	for path, schema := range contracts {
		if got := responseSchemaRef(t, doc, path, "200"); got != schema {
			t.Errorf("GET %s 200 schema = %q, want %q", path, got, schema)
		}
		if got := responseSchemaRef(t, doc, path, "403"); got != "#/components/schemas/Error" {
			t.Errorf("GET %s 403 schema = %q, want Error", path, got)
		}
	}
	components, ok := doc["components"].(map[string]any)
	if !ok {
		t.Fatal("no components object")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("no schemas object")
	}
	connector, ok := schemas["SecretConnector"].(map[string]any)
	if !ok {
		t.Fatal("SecretConnector schema missing")
	}
	connectorProperties, ok := connector["properties"].(map[string]any)
	if !ok {
		t.Fatal("SecretConnector has no properties")
	}
	if _, ok := connectorProperties["credential_ref"]; ok {
		t.Fatal("SecretConnector exposes credential_ref")
	}
	if _, ok := connectorProperties["has_credential"]; !ok {
		t.Fatal("SecretConnector has no has_credential property")
	}
	connectorConfig, ok := schemas["ConnectorConfig"].(map[string]any)
	if !ok || connectorConfig["additionalProperties"] != false {
		t.Fatal("ConnectorConfig must reject unknown fields")
	}
	connectorAuth, ok := schemas["ConnectorAuthConfig"].(map[string]any)
	if !ok || connectorAuth["additionalProperties"] != false {
		t.Fatal("ConnectorAuthConfig must reject unknown fields")
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

func TestMeRequiresBearer(t *testing.T) {
	mux := http.NewServeMux()
	api.Mount(mux, api.Deps{
		Health:      health.NewRegistry(),
		ReadyBudget: time.Second,
		Logger:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Verifier:    authn.DenyAll{},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate")
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
