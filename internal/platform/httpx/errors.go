// Package httpx provides the HTTP middleware chain, error envelope, and
// server construction for the Custos API. Domain logic never lives here.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/log"
)

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain composes middleware left-to-right: the first element is outermost.
func Chain(mw ...Middleware) Middleware {
	return func(h http.Handler) http.Handler {
		for i := len(mw) - 1; i >= 0; i-- {
			h = mw[i](h)
		}
		return h
	}
}

// ErrorBody is the standard error envelope: {"error":{...}}.
type ErrorBody struct {
	Error struct {
		Code      string          `json:"code"`
		Message   string          `json:"message"`
		Details   []apperr.Detail `json:"details,omitempty"`
		RequestID string          `json:"request_id"`
	} `json:"error"`
}

var statusByKind = map[apperr.Kind]int{
	apperr.Invalid:         http.StatusBadRequest,
	apperr.Unauthenticated: http.StatusUnauthorized,
	apperr.Forbidden:       http.StatusForbidden,
	apperr.NotFound:        http.StatusNotFound,
	apperr.Conflict:        http.StatusConflict,
	apperr.RateLimited:     http.StatusTooManyRequests,
	apperr.Unavailable:     http.StatusServiceUnavailable,
}

// WriteJSON encodes v with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError maps err to the envelope. 5xx and Unknown produce a fixed
// generic message; the real error is logged server-side, never echoed.
func WriteError(ctx context.Context, w http.ResponseWriter, err error) {
	kind := apperr.KindOf(err)
	status, ok := statusByKind[kind]
	if !ok {
		status = http.StatusInternalServerError
	}
	var body ErrorBody
	var ae *apperr.Error
	code, msg := "INTERNAL", "internal error"
	if errors.As(err, &ae) {
		if ae.Code != "" {
			code = ae.Code
		}
		if status < 500 {
			msg = ae.Message
			body.Error.Details = ae.Details
		}
	}
	// MaxBytesReader overflow and our own pre-check share one code/status.
	var mbErr *http.MaxBytesError
	if errors.As(err, &mbErr) || code == "BODY_TOO_LARGE" {
		status = http.StatusRequestEntityTooLarge
		code = "BODY_TOO_LARGE"
		msg = "request body too large"
	}
	if status >= 500 {
		// Message never leaks internals; the stable code is kept only for
		// expected upstream failures (e.g. UPSTREAM_UNAVAILABLE).
		if kind == apperr.Unavailable {
			msg = "service unavailable"
		} else {
			msg = "internal error"
			code = "INTERNAL"
		}
		slog.ErrorContext(ctx, "request failed",
			"status", status, "error", err.Error())
	}
	body.Error.Code = code
	body.Error.Message = msg
	body.Error.RequestID = log.RequestIDFrom(ctx)
	WriteJSON(w, status, body)
}
