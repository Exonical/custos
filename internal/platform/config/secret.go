package config

import (
	"encoding/json"
	"log/slog"
)

// Secret is a configuration string that must never appear in logs,
// marshaled output, or the redacted config dump. It redacts itself in
// every standard formatting path; the value is only obtainable via
// Reveal.
type Secret string

// String returns the redaction marker.
func (s Secret) String() string { return "[REDACTED]" }

// Reveal returns the underlying secret value. Call sites must not log or
// return the result.
func (s Secret) Reveal() string { return string(s) }

// MarshalText redacts the secret.
func (s Secret) MarshalText() ([]byte, error) { return []byte("[REDACTED]"), nil }

// MarshalJSON redacts the secret.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED]") }

// LogValue redacts the secret for slog.
func (s Secret) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }
