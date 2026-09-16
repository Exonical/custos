// Package secrets defines the Custos secret-resolution port (see
// docs/secrets.md). Secret values are resolved through a Resolver at call
// time and are wrapped in a Value that redacts itself for logging and can
// be wiped.
package secrets

import (
	"bytes"
	"context"
	"log/slog"

	"github.com/Exonical/custos/internal/platform/apperr"
)

// Reference points at a secret held by an external provider. PostgreSQL
// rows store references only — never values (docs/secrets.md).
type Reference struct {
	// Provider selects the resolver: "file" now, "openbao" in M6.
	Provider string `json:"provider"`
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

// Multi dispatches on Reference.Provider.
type Multi map[string]Resolver

// Resolve implements Resolver.
func (m Multi) Resolve(ctx context.Context, ref Reference) (Value, error) {
	r, ok := m[ref.Provider]
	if !ok {
		return Value{}, apperr.New(apperr.Invalid, "secrets.unknown_provider",
			"unknown secret provider "+ref.Provider)
	}
	return r.Resolve(ctx, ref)
}
