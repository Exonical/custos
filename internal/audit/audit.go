// Package audit defines the immutable audit-event model and recorder
// interfaces. Sinks live in subpackages (pgaudit for PostgreSQL).
package audit

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Actor types.
const (
	ActorUser    = "user"
	ActorService = "service"
	ActorSystem  = "system"
)

// Result values.
const (
	ResultAllow = "allow"
	ResultDeny  = "deny"
	ResultError = "error"
)

// Actor describes who performed the audited action.
type Actor struct {
	Type    string // user | service | system
	ID      string
	Display string
}

// Target identifies the resource acted upon.
type Target struct {
	Type string
	ID   string
}

// Event is one audit record.
type Event struct {
	ID         uuid.UUID
	OccurredAt time.Time
	TenantID   *uuid.UUID
	Actor      Actor
	Action     string
	Target     Target
	Result     string // allow | deny | error
	Reason     string
	RequestID  string
	TraceID    string
	ClientIP   string
	// Details must not contain secret values or script bodies; digests
	// and IDs only. Recorders log/serialize this map verbatim.
	Details map[string]any
}

// Stream returns the hash-chain stream name: "tenant:<id>" for tenant
// events, "platform" otherwise.
func (e Event) Stream() string {
	if e.TenantID != nil {
		return "tenant:" + e.TenantID.String()
	}
	return "platform"
}

// Recorder persists one event.
type Recorder interface {
	Record(ctx context.Context, e Event) error
}

// Multi fans a record out to all recorders; errors are joined.
type Multi []Recorder

// Record implements Recorder.
func (m Multi) Record(ctx context.Context, e Event) error {
	var errs []error
	for _, r := range m {
		if err := r.Record(ctx, e); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SlogRecorder emits audit events as structured log records.
type SlogRecorder struct {
	Logger *slog.Logger
}

// Record writes one info-level "audit" record. Details are included as a
// group; callers are responsible for keeping secrets and script bodies
// out of Event.Details.
func (s SlogRecorder) Record(ctx context.Context, e Event) error {
	s.Logger.InfoContext(ctx, "audit",
		"event_id", e.ID,
		"occurred_at", e.OccurredAt,
		"stream", e.Stream(),
		"actor_type", e.Actor.Type,
		"actor_id", e.Actor.ID,
		"actor_display", e.Actor.Display,
		"action", e.Action,
		"target_type", e.Target.Type,
		"target_id", e.Target.ID,
		"result", e.Result,
		"reason", e.Reason,
		"request_id", e.RequestID,
		"trace_id", e.TraceID,
		"client_ip", e.ClientIP,
		slog.Any("details", e.Details))
	return nil
}
