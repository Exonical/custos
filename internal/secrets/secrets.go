// Package secrets defines the Custos secret-resolution port (see
// docs/secrets.md). Secret values are resolved through a Resolver at call
// time and are wrapped in a Value that redacts itself for logging and can
// be wiped.
package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/platform/apperr"
)

// Reference points at a secret held by an external provider. PostgreSQL
// rows store references only — never values (docs/secrets.md).
type Reference struct {
	// Provider selects the platform resolver for platform-owned references.
	// Tenant references instead identify a connector.
	Provider    string    `json:"provider,omitempty"`
	ConnectorID uuid.UUID `json:"connector_id,omitempty"`
	// Namespace is the OpenBao namespace. It is empty for file references.
	Namespace string `json:"namespace,omitempty"`
	// Mount is the OpenBao KV v2 mount. It is empty for file references.
	Mount string `json:"mount,omitempty"`
	// Path is provider-specific; for "file" it is a filesystem path.
	Path string `json:"path"`
	// Key, when set, extracts one string field from a JSON document.
	Key string `json:"key,omitempty"`
	// Version selects a versioned secret (OpenBao; unused for file).
	Version int `json:"version,omitempty"`
}

// Value is an in-memory secret. It redacts itself in String and
// slog.LogValue; the underlying bytes are only reachable via Reveal.
type Value struct {
	bytes []byte
}

// NewValue copies b into a Value.
func NewValue(b []byte) Value {
	return Value{bytes: bytes.Clone(b)}
}

// Reveal returns the raw secret bytes. Callers must treat the result as
// sensitive: never log it, and keep its lifetime short.
func (v Value) Reveal() []byte { return v.bytes }

// String always redacts.
func (v Value) String() string { return "[REDACTED]" }

// LogValue always redacts (slog).
func (v Value) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// MarshalJSON always redacts.
func (v Value) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED]") }

// Wipe zeroes the underlying bytes. After Wipe, Reveal returns an empty
// slice.
func (v *Value) Wipe() {
	for i := range v.bytes {
		v.bytes[i] = 0
	}
	v.bytes = v.bytes[:0]
}

// Resolver fetches a secret value for a reference.
type Resolver interface {
	Resolve(ctx context.Context, ref Reference) (Value, error)
}

// Connector is a tenant-scoped secret manager session.
type Connector interface {
	Resolver
	Check(ctx context.Context) error
	Close() error
}

// ConnectorSpec contains a connector's non-secret configuration.
type ConnectorSpec struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	Kind          string
	Version       int64
	Config        map[string]any
	CredentialRef *Reference
}

// ConnectorFactory opens a connector. cred resolves its login credential
// without placing that credential in PostgreSQL or connector configuration.
type ConnectorFactory interface {
	Open(ctx context.Context, spec ConnectorSpec,
		cred func(context.Context) (Value, error)) (Connector, error)
}

// TenantProvisioner creates the platform namespace and policy for a tenant.
type TenantProvisioner interface {
	EnsureTenantNamespace(ctx context.Context, tenantID string) error
}

// Multi dispatches on Reference.Provider for platform-owned references.
type Multi map[string]Resolver

// Resolve implements Resolver.
func (m Multi) Resolve(ctx context.Context, ref Reference) (Value, error) {
	if ref.Provider != "file" && ref.Provider != "openbao" {
		return Value{}, apperr.New(apperr.Validation, "SECRET_PROVIDER_INVALID",
			"secret provider must be file or openbao")
	}
	r, ok := m[ref.Provider]
	if !ok {
		return Value{}, apperr.New(apperr.Validation, "SECRET_PROVIDER_UNAVAILABLE",
			"secret provider is not configured")
	}
	return r.Resolve(ctx, ref)
}

// Available reports whether provider is configured.
func (m Multi) Available(provider string) bool {
	_, ok := m[provider]
	return ok
}
