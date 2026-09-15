// Package log configures slog for Custos: JSON/text output, level from
// config, and request-scoped attribute injection via context.
package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/Exonical/custos/internal/platform/config"
)

type ctxKey int

const requestIDKey ctxKey = iota

// TraceIDFunc extracts trace/span IDs from a context for log records.
// Slice C wires this to the OTel implementation; nil means no trace attrs.
var TraceIDFunc func(ctx context.Context) (traceID, spanID string)

// WithRequestID returns a context carrying the request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom returns the request ID in ctx, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// New builds a slog.Logger writing to w in the configured format/level.
func New(cfg config.Log, w io.Writer) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, fmt.Errorf("log.level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.Format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(&contextHandler{next: h}), nil
}

// contextHandler injects request_id (and trace_id/span_id when the
// TraceIDFunc hook is set) from the record's context.
type contextHandler struct{ next slog.Handler }

func (h *contextHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestIDFrom(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if TraceIDFunc != nil {
		if tid, sid := TraceIDFunc(ctx); tid != "" {
			r.AddAttrs(slog.String("trace_id", tid))
			if sid != "" {
				r.AddAttrs(slog.String("span_id", sid))
			}
		}
	}
	return h.next.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &contextHandler{next: h.next.WithAttrs(a)}
}

func (h *contextHandler) WithGroup(g string) slog.Handler {
	return &contextHandler{next: h.next.WithGroup(g)}
}
