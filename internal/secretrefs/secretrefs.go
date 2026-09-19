// Package secretrefs defines tenant secret connectors and references.
package secretrefs

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/tenants"
)

// Connector is non-secret metadata for a tenant secret manager.
type Connector struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	Name          string
	Kind          string
	State         string
	Config        map[string]any
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
