package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/secretrefs"
	"github.com/Exonical/custos/internal/secrets"
)

func TestConnectorDTOHidesCredentialReference(t *testing.T) {
	ref := &secrets.Reference{Provider: "openbao", Namespace: "custos/tenants/t1", Mount: "kv", Path: "connectors/c1", Key: "credential"}
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		connector   secretrefs.Connector
		wantHasCred bool
	}{
		{
			name:        "platform default",
			connector:   secretrefs.Connector{ID: uuid.New(), TenantID: uuid.New(), Name: "default", Kind: "platform-openbao", State: "active", Config: secretrefs.ConnectorConfig{}, CredentialRef: ref, CreatedAt: now, UpdatedAt: now},
			wantHasCred: false,
		},
		{
			name:        "BYO credential present",
			connector:   secretrefs.Connector{ID: uuid.New(), TenantID: uuid.New(), Name: "byo", Kind: "openbao", State: "active", Config: secretrefs.ConnectorConfig{Address: "https://vault.example.test", Auth: &secretrefs.ConnectorAuthConfig{Method: "approle", RoleID: "role-1"}}, CredentialRef: ref, CreatedAt: now, UpdatedAt: now},
			wantHasCred: true,
		},
		{
			name:        "BYO credential absent",
			connector:   secretrefs.Connector{ID: uuid.New(), TenantID: uuid.New(), Name: "byo", Kind: "openbao", State: "disabled", Config: secretrefs.ConnectorConfig{}, CreatedAt: now, UpdatedAt: now},
			wantHasCred: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := connectorDTO(test.connector)
			if _, found := response["credential_ref"]; found {
				t.Fatal("connector DTO exposes credential_ref")
			}
			if got, ok := response["has_credential"].(bool); !ok || got != test.wantHasCred {
				t.Fatalf("has_credential = %#v, want %t", response["has_credential"], test.wantHasCred)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "connectors/c1") || strings.Contains(string(encoded), "credential_ref") {
				t.Fatalf("connector DTO exposed the stored credential reference: %s", encoded)
			}
			if test.name == "platform default" {
				var body struct {
					Config map[string]any `json:"config"`
				}
				if err := json.Unmarshal(encoded, &body); err != nil {
					t.Fatal(err)
				}
				if len(body.Config) != 0 {
					t.Fatalf("platform default config = %#v, want empty object", body.Config)
				}
			}
		})
	}
}

func TestConnectorConfigRoundTripsThroughResponse(t *testing.T) {
	raw := json.RawMessage(`{"address":"https://vault.example.test","ca_pem":"public-ca-pem","namespace":"customer","mount":"kv","auth":{"method":"jwt","role":"custos"}}`)
	config, err := parseConnectorConfig(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	connector := secretrefs.Connector{
		ID: uuid.New(), TenantID: uuid.New(), Name: "byo", Kind: "openbao", State: "active",
		Config: *config, CredentialRef: &secrets.Reference{Provider: "openbao", Namespace: "internal", Mount: "kv", Path: "credential", Key: "value"},
	}
	encoded, err := json.Marshal(connectorDTO(connector))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Config        secretrefs.ConnectorConfig `json:"config"`
		HasCredential bool                       `json:"has_credential"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response.Config, *config) {
		t.Fatalf("config round-trip = %#v, want %#v", response.Config, *config)
	}
	if !response.HasCredential || strings.Contains(string(encoded), "credential_ref") || strings.Contains(string(encoded), "secret_id") || strings.Contains(string(encoded), `"path":"credential"`) || strings.Contains(string(encoded), `"namespace":"internal"`) {
		t.Fatalf("connector response leaked credential metadata: %s", encoded)
	}
}

func TestConnectorConfigRejectsUnknownKeysBeforeServiceCall(t *testing.T) {
	tests := []struct {
		name   string
		method string
		body   string
		handle func(http.ResponseWriter, *http.Request)
	}{
		{
			name:   "create",
			method: http.MethodPost,
			body:   `{"name":"byo","kind":"openbao","config":{"address":"https://vault.example.test","auth":{"method":"approle","role_id":"role-1","secret_id":"must-not-be-config"}},"credential":{"secret_id":"write-only"}}`,
			handle: func(w http.ResponseWriter, r *http.Request) { (&secretHandlers{}).createConnector(w, r) },
		},
		{
			name:   "update",
			method: http.MethodPatch,
			body:   `{"version":1,"config":{"address":"https://vault.example.test","auth":{"method":"jwt","role":"custos","jwt_audience":"unused"}}}`,
			handle: func(w http.ResponseWriter, r *http.Request) { (&secretHandlers{}).updateConnector(w, r) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, "/api/v1/tenants/acme/secret-connectors", strings.NewReader(test.body))
			test.handle(response, request)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "CONNECTOR_CONFIG_INVALID" {
				t.Fatalf("error code = %q, want CONNECTOR_CONFIG_INVALID", body.Error.Code)
			}
		})
	}
}
