package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

	importPath := "/tenants/{tenant}/workflow-imports/sbatch"
	importItem, ok := paths[importPath].(map[string]any)
	if !ok {
		t.Fatalf("path %q missing", importPath)
	}
	importOperation, ok := importItem["post"].(map[string]any)
	if !ok {
		t.Fatalf("POST %q missing", importPath)
	}
	importRequestBody := importOperation["requestBody"].(map[string]any)
	importRequestContent := importRequestBody["content"].(map[string]any)
	importRequestSchema := importRequestContent["application/json"].(map[string]any)["schema"].(map[string]any)
	if importRequestSchema["$ref"] != "#/components/schemas/WorkflowSbatchImportRequest" {
		t.Fatalf("workflow import request schema = %v", importRequestSchema)
	}
	importResponses := importOperation["responses"].(map[string]any)
	importResponseSchema := importResponses["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if importResponseSchema["$ref"] != "#/components/schemas/WorkflowSbatchImportResponse" {
		t.Fatalf("workflow import response schema = %v", importResponseSchema)
	}

	exportPath := "/tenants/{tenant}/workflows/{workflow}/versions/{version}/tasks/{task}/sbatch"
	exportItem := paths[exportPath].(map[string]any)
	exportOperation := exportItem["get"].(map[string]any)
	exportResponses := exportOperation["responses"].(map[string]any)
	exportScript := exportResponses["200"].(map[string]any)["content"].(map[string]any)["text/x-shellscript"].(map[string]any)["schema"].(map[string]any)
	if exportScript["type"] != "string" {
		t.Fatalf("sbatch export response schema = %v", exportScript)
	}
	for _, status := range []string{"401", "403", "404", "422"} {
		if got := responseSchemaRef(t, doc, exportPath, status); got != "#/components/schemas/Error" {
			t.Errorf("GET %s %s schema = %q, want Error", exportPath, status, got)
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

type allowVerifier struct{}

func (allowVerifier) Verify(context.Context, string) (authn.Principal, error) {
	return authn.Principal{Subject: "tester", UserID: uuid.New()}, nil
}

func templateMux(v authn.Verifier) *http.ServeMux {
	mux := http.NewServeMux()
	api.Mount(mux, api.Deps{
		Health:      health.NewRegistry(),
		ReadyBudget: time.Second,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier:    v,
	})
	return mux
}

func TestWorkflowTemplatesRequireBearer(t *testing.T) {
	srv := httptest.NewServer(templateMux(authn.DenyAll{}))
	defer srv.Close()
	for _, p := range []string{"/api/v1/workflow-templates", "/api/v1/workflow-templates/openmp"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s -> %d, want 401", p, resp.StatusCode)
		}
	}
}

func TestWorkflowTemplatesListAndGet(t *testing.T) {
	srv := httptest.NewServer(templateMux(allowVerifier{}))
	defer srv.Close()
	get := func(p string, out any) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+p, nil)
		req.Header.Set("Authorization", "Bearer x")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if code := get("/api/v1/workflow-templates", &list); code != 200 || len(list.Items) != 8 {
		t.Fatalf("list -> %d with %d items", code, len(list.Items))
	}
	if _, ok := list.Items[0]["spec"]; ok {
		t.Fatal("list must not carry full specs")
	}
	var one struct {
		ID   string         `json:"id"`
		YAML string         `json:"yaml"`
		Spec map[string]any `json:"spec"`
	}
	if code := get("/api/v1/workflow-templates/mpi-tasks", &one); code != 200 ||
		one.ID != "mpi-tasks" || one.Spec["apiVersion"] != "custos.io/v1alpha1" ||
		!strings.Contains(one.YAML, "name: openmpi") {
		t.Fatalf("get -> %d %+v", code, one)
	}
	if code := get("/api/v1/workflow-templates/nope", nil); code != http.StatusNotFound {
		t.Fatalf("unknown template -> %d", code)
	}
}
