package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
	"github.com/Exonical/custos/internal/secretrefs"
	"github.com/Exonical/custos/internal/tenants"
)

type secretHandlers struct{ svc *secretrefs.Service }

type createConnectorRequest struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Config     json.RawMessage   `json:"config"`
	Credential map[string]string `json:"credential"`
}

type updateConnectorRequest struct {
	Name       *string           `json:"name,omitempty"`
	State      *string           `json:"state,omitempty"`
	Config     json.RawMessage   `json:"config,omitempty"`
	Credential map[string]string `json:"credential,omitempty"`
	Version    int64             `json:"version"`
}

func parseConnectorConfig(raw json.RawMessage, required bool) (*secretrefs.ConnectorConfig, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		if required {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, nil
	}
	var config secretrefs.ConnectorConfig
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, io.ErrUnexpectedEOF
	}
	if config.Auth != nil && config.Auth.Method != "token" && config.Auth.Method != "approle" && config.Auth.Method != "jwt" {
		return nil, errors.New("unsupported connector auth method")
	}
	return &config, nil
}

func writeConnectorConfigError(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(r.Context(), w, apperr.New(apperr.Validation, "CONNECTOR_CONFIG_INVALID", "connector config contains unsupported or invalid fields"))
}

func connectorDTO(c secretrefs.Connector) map[string]any {
	hasCredential := c.Kind == "openbao" && c.CredentialRef != nil
	return map[string]any{"id": c.ID, "tenant_id": c.TenantID, "name": c.Name, "kind": c.Kind, "state": c.State, "config": c.Config, "has_credential": hasCredential, "version": c.Version, "created_at": c.CreatedAt.UTC().Format(time.RFC3339Nano), "updated_at": c.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}
func referenceDTO(x secretrefs.Reference) map[string]any {
	return map[string]any{"id": x.ID, "tenant_id": x.TenantID, "owner_id": x.OwnerID, "project_id": x.ProjectID, "name": x.Name, "connector_id": x.ConnectorID, "namespace": x.Namespace, "mount": x.Mount, "path": x.Path, "key": x.Key, "secret_version": x.SecretVersion, "kind": x.Kind, "allowed_uses": x.AllowedUses, "version": x.Version, "created_at": x.CreatedAt.UTC().Format(time.RFC3339Nano), "updated_at": x.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}
func tc(r *http.Request) tenants.TenantContext { return tenants.MustTenantContext(r.Context()) }
func (h *secretHandlers) createConnector(w http.ResponseWriter, r *http.Request) {
	var request createConnectorRequest
	if !decodeDTO(w, r, &request) {
		return
	}
	config, err := parseConnectorConfig(request.Config, true)
	if err != nil {
		writeConnectorConfigError(w, r)
		return
	}
	in := secretrefs.CreateConnector{Name: request.Name, Kind: request.Kind, Config: *config, Credential: request.Credential}
	c, err := h.svc.CreateConnector(r.Context(), authn.MustPrincipal(r.Context()), tc(r), in)
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, connectorDTO(c))
}
func (h *secretHandlers) listConnectors(w http.ResponseWriter, r *http.Request) {
	xs, err := h.svc.ListConnectors(r.Context(), authn.MustPrincipal(r.Context()), tc(r))
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	items := make([]any, 0, len(xs))
	for _, x := range xs {
		items = append(items, connectorDTO(x))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (h *secretHandlers) getConnector(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.GetConnector(r.Context(), authn.MustPrincipal(r.Context()), tc(r), r.PathValue("connector"))
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, connectorDTO(c))
}
func (h *secretHandlers) updateConnector(w http.ResponseWriter, r *http.Request) {
	var request updateConnectorRequest
	if !decodeDTO(w, r, &request) {
		return
	}
	config, err := parseConnectorConfig(request.Config, false)
	if err != nil {
		writeConnectorConfigError(w, r)
		return
	}
	in := secretrefs.UpdateConnector{Name: request.Name, State: request.State, Config: config, Credential: request.Credential, Version: request.Version}
	c, err := h.svc.UpdateConnector(r.Context(), authn.MustPrincipal(r.Context()), tc(r), r.PathValue("connector"), in)
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, connectorDTO(c))
}
func (h *secretHandlers) deleteConnector(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteConnector(r.Context(), authn.MustPrincipal(r.Context()), tc(r), r.PathValue("connector")); err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *secretHandlers) testConnector(w http.ResponseWriter, r *http.Request) {
	c, d, err := h.svc.TestConnector(r.Context(), authn.MustPrincipal(r.Context()), tc(r), r.PathValue("connector"))
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "kind": c.Kind, "latency_ms": d.Milliseconds()})
}
func (h *secretHandlers) createReference(w http.ResponseWriter, r *http.Request) {
	var in secretrefs.CreateReference
	if !decodeDTO(w, r, &in) {
		return
	}
	x, err := h.svc.CreateReference(r.Context(), authn.MustPrincipal(r.Context()), tc(r), in)
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, referenceDTO(x))
}
func (h *secretHandlers) listReferences(w http.ResponseWriter, r *http.Request) {
	xs, err := h.svc.ListReferences(r.Context(), authn.MustPrincipal(r.Context()), tc(r))
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	items := make([]any, 0, len(xs))
	for _, x := range xs {
		items = append(items, referenceDTO(x))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (h *secretHandlers) getReference(w http.ResponseWriter, r *http.Request) {
	x, err := h.svc.GetReference(r.Context(), authn.MustPrincipal(r.Context()), tc(r), r.PathValue("reference"))
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, referenceDTO(x))
}
func (h *secretHandlers) updateReference(w http.ResponseWriter, r *http.Request) {
	var in secretrefs.UpdateReference
	if !decodeDTO(w, r, &in) {
		return
	}
	x, err := h.svc.UpdateReference(r.Context(), authn.MustPrincipal(r.Context()), tc(r), r.PathValue("reference"), in)
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, referenceDTO(x))
}
func (h *secretHandlers) deleteReference(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteReference(r.Context(), authn.MustPrincipal(r.Context()), tc(r), r.PathValue("reference")); err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *secretHandlers) testReference(w http.ResponseWriter, r *http.Request) {
	x, at, err := h.svc.TestReference(r.Context(), authn.MustPrincipal(r.Context()), tc(r), r.PathValue("reference"))
	if err != nil {
		httpx.WriteError(r.Context(), w, err)
		return
	}
	var version any
	if x.SecretVersion != nil {
		version = *x.SecretVersion
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "kind": x.Kind, "version": version, "resolved_at": at.Format(time.RFC3339Nano)})
}
func mountSecretRoutes(mux *http.ServeMux, h *secretHandlers, tr func(http.Handler) http.Handler) {
	base := "/api/v1/tenants/{tenant}"
	cb := base + "/secret-connectors"
	mux.Handle("POST "+cb, tr(http.HandlerFunc(h.createConnector)))
	mux.Handle("GET "+cb, tr(http.HandlerFunc(h.listConnectors)))
	mux.Handle("GET "+cb+"/{connector}", tr(http.HandlerFunc(h.getConnector)))
	mux.Handle("PATCH "+cb+"/{connector}", tr(http.HandlerFunc(h.updateConnector)))
	mux.Handle("DELETE "+cb+"/{connector}", tr(http.HandlerFunc(h.deleteConnector)))
	mux.Handle("POST "+cb+"/{connector}/test", tr(http.HandlerFunc(h.testConnector)))
	rb := base + "/secret-references"
	mux.Handle("POST "+rb, tr(http.HandlerFunc(h.createReference)))
	mux.Handle("GET "+rb, tr(http.HandlerFunc(h.listReferences)))
	mux.Handle("GET "+rb+"/{reference}", tr(http.HandlerFunc(h.getReference)))
	mux.Handle("PATCH "+rb+"/{reference}", tr(http.HandlerFunc(h.updateReference)))
	mux.Handle("DELETE "+rb+"/{reference}", tr(http.HandlerFunc(h.deleteReference)))
	mux.Handle("POST "+rb+"/{reference}/test", tr(http.HandlerFunc(h.testReference)))
}
