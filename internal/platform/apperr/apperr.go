// Package apperr defines the domain error type shared by all Custos
// packages. It is part of the platform layer and imports only the standard
// library, so domain packages may depend on it freely.
package apperr

import (
	"context"
	"errors"
)

// Kind is the category of an Error, used to map to HTTP status codes and
// to decide retryability in workers.
type Kind uint8

// Error kinds, ordered roughly by severity of client fault.
const (
	Unknown         Kind = iota // unclassified; treated as internal
	Invalid                     // malformed or failed validation
	NotFound                    // resource does not exist (or is hidden)
	Conflict                    // version/idempotency/state conflict
	Forbidden                   // authenticated but not allowed
	Unauthenticated             // missing or invalid credentials
	Unavailable                 // upstream (DB, OpenBao, Slurm) failure
	RateLimited                 // caller exceeded a rate limit
	Internal                    // bug or unexpected failure
)

// String returns a stable lowercase name for the kind.
func (k Kind) String() string {
	switch k {
	case Invalid:
		return "invalid"
	case NotFound:
		return "not_found"
	case Conflict:
		return "conflict"
	case Forbidden:
		return "forbidden"
	case Unauthenticated:
		return "unauthenticated"
	case Unavailable:
		return "unavailable"
	case RateLimited:
		return "rate_limited"
	case Internal:
		return "internal"
	default:
		return "unknown"
	}
}

// Detail is a field-scoped reason attached to an Error, safe to return to
// clients (e.g. path-addressed validation failures).
type Detail struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Error is the shared domain error. Message is safe to return to clients;
// Err is the wrapped cause and is never exposed in responses.
type Error struct {
	Kind    Kind
	Code    string // stable machine code, e.g. "config.invalid"
	Message string
	Details []Detail
	Err     error
}

// Error implements error.
func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

// Unwrap returns the wrapped cause, if any.
func (e *Error) Unwrap() error { return e.Err }

// New creates an Error with the given kind, machine code, and message.
func New(kind Kind, code, msg string) *Error {
	return &Error{Kind: kind, Code: code, Message: msg}
}

// Wrap creates an Error wrapping err as its cause.
func Wrap(err error, kind Kind, code, msg string) *Error {
	return &Error{Kind: kind, Code: code, Message: msg, Err: err}
}

// KindOf returns the Kind of err, unwrapping to find an *Error.
// context.Canceled and context.DeadlineExceeded map to Unavailable;
// anything else (including nil) maps to Unknown.
func KindOf(err error) Kind {
	if err == nil {
		return Unknown
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Unavailable
	}
	return Unknown
}

// Is reports whether err resolves to kind via KindOf.
func Is(err error, k Kind) bool { return KindOf(err) == k }
