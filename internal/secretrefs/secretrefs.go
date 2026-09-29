// Package secretrefs defines tenant secret connectors and references.
package secretrefs

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/tenants"
)

// ConnectorAuthConfig contains only non-secret OpenBao login selectors.
type ConnectorAuthConfig struct {
	Method string `json:"method"`
	RoleID string `json:"role_id,omitempty"`
	Role   string `json:"role,omitempty"`
}

// ConnectorConfig is the allow-listed, non-secret connector configuration.
type ConnectorConfig struct {
	Address   string               `json:"address,omitempty"`
	CAPEM     string               `json:"ca_pem,omitempty"`
	Namespace string               `json:"namespace,omitempty"`
	Mount     string               `json:"mount,omitempty"`
	Auth      *ConnectorAuthConfig `json:"auth,omitempty"`
}

// runtimeConfig adapts the validated public config to the provider port.
func (c ConnectorConfig) runtimeConfig() map[string]any {
	out := map[string]any{}
	if c.Address != "" {
		out["address"] = c.Address
	}
	if c.CAPEM != "" {
		out["ca_pem"] = c.CAPEM
	}
	if c.Namespace != "" {
		out["namespace"] = c.Namespace
	}
	if c.Mount != "" {
		out["mount"] = c.Mount
	}
	if c.Auth != nil {
		auth := map[string]any{"method": c.Auth.Method}
		if c.Auth.RoleID != "" {
			auth["role_id"] = c.Auth.RoleID
		}
		if c.Auth.Role != "" {
			auth["role"] = c.Auth.Role
		}
		out["auth"] = auth
	}
	return out
}

// Connector is non-secret metadata for a tenant secret manager.
type Connector struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	Name          string
	Kind          string
	State         string
	Config        ConnectorConfig
	CredentialRef *secrets.Reference
	CreatedBy     uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Version       int64
}

// Reference is persisted secret metadata; it never contains a value.
type Reference struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	OwnerID       *uuid.UUID
	ProjectID     *uuid.UUID
	Name          string
	ConnectorID   uuid.UUID
	Namespace     string
	Mount         string
	Path          string
	Key           string
	SecretVersion *int
	Kind          string
	AllowedUses   []string
	CreatedBy     uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Version       int64
}

// SecretReference converts persisted metadata to the provider port type.
func (r Reference) SecretReference() secrets.Reference {
	var version int
	if r.SecretVersion != nil {
		version = *r.SecretVersion
	}
	return secrets.Reference{ConnectorID: r.ConnectorID, Namespace: r.Namespace,
		Mount: r.Mount, Path: r.Path, Key: r.Key, Version: version}
}

// Repository persists connectors and references under explicit RLS scope.
type Repository interface {
	CreateConnector(context.Context, tenants.Scope, Connector) error
	GetConnector(context.Context, tenants.Scope, uuid.UUID, string) (Connector, error)
	ListConnectors(context.Context, tenants.Scope, uuid.UUID) ([]Connector, error)
	UpdateConnector(context.Context, tenants.Scope, Connector) error
	DeleteConnector(context.Context, tenants.Scope, uuid.UUID, uuid.UUID) error
	ConnectorReferenceCount(context.Context, tenants.Scope, uuid.UUID) (int, error)

	CreateReference(context.Context, tenants.Scope, Reference) error
	GetReference(context.Context, tenants.Scope, uuid.UUID, string) (Reference, error)
	GetReferenceByName(context.Context, tenants.Scope, uuid.UUID, string) (Reference, error)
	ListReferences(context.Context, tenants.Scope, uuid.UUID) ([]Reference, error)
	UpdateReference(context.Context, tenants.Scope, Reference) error
	DeleteReference(context.Context, tenants.Scope, uuid.UUID, uuid.UUID) error
}
