package engine

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/audit"
)

// failingRecorder fails every Record call after counting it.
type failingRecorder struct{ calls int }

func (r *failingRecorder) Record(context.Context, audit.Event) error {
	r.calls++
	return errors.New("audit sink down")
}

func TestRecordLogsAuditWriteFailure(t *testing.T) {
	rec := &failingRecorder{}
	var logs bytes.Buffer
	d := Deps{
		Audit:  rec,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}
	d.record(context.Background(), audit.Event{Action: "workflow.task.admit"})
	if rec.calls != 1 {
		t.Fatalf("record calls = %d, want 1", rec.calls)
	}
	if !strings.Contains(logs.String(), "audit record failed") ||
		!strings.Contains(logs.String(), "workflow.task.admit") {
		t.Fatalf("missing audit failure warning:\n%s", logs.String())
	}
}

func TestRecordSkipsNilAuditAndDefaultsLogger(t *testing.T) {
	// Nil recorder: nothing attempted, nothing logged.
	Deps{}.record(context.Background(), audit.Event{Action: "x"})

	// Nil logger falls back to slog.Default without panicking.
	rec := &failingRecorder{}
	Deps{Audit: rec}.record(context.Background(), audit.Event{Action: "x"})
	if rec.calls != 1 {
		t.Fatalf("record calls = %d, want 1", rec.calls)
	}
}
