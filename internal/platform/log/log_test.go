package log

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/platform/config"
)

func newJSON(t *testing.T, level string) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	l, err := New(config.Log{Level: level, Format: "json"}, buf)
	if err != nil {
		t.Fatal(err)
	}
	return l, buf
}

func TestRequestIDInjection(t *testing.T) {
	l, buf := newJSON(t, "info")
	ctx := WithRequestID(context.Background(), "req-123")
	l.InfoContext(ctx, "hello")
	if !strings.Contains(buf.String(), `"request_id":"req-123"`) {
		t.Fatalf("missing request_id: %s", buf.String())
	}
	buf.Reset()
	l.InfoContext(context.Background(), "no id")
	if strings.Contains(buf.String(), "request_id") {
		t.Fatalf("unexpected request_id: %s", buf.String())
	}
}

func TestLevelFiltering(t *testing.T) {
	l, buf := newJSON(t, "warn")
	l.Debug("hidden")
	l.Info("also hidden")
	l.Warn("shown")
	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, "shown") {
		t.Fatalf("level filtering wrong: %s", out)
	}
}

func TestTraceIDHook(t *testing.T) {
	l, buf := newJSON(t, "info")
	TraceIDFunc = func(context.Context) (string, string) { return "t-1", "s-1" }
	defer func() { TraceIDFunc = nil }()
	l.InfoContext(context.Background(), "traced")
	out := buf.String()
	if !strings.Contains(out, `"trace_id":"t-1"`) || !strings.Contains(out, `"span_id":"s-1"`) {
		t.Fatalf("missing trace attrs: %s", out)
	}
}

func TestSecretRedactionInLogs(t *testing.T) {
	l, buf := newJSON(t, "info")
	l.Info("db", "url", config.Secret("postgres://u:hunter2@h/db"))
	out := buf.String()
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("secret leaked: %s", out)
	}
}

func TestBadLevel(t *testing.T) {
	if _, err := New(config.Log{Level: "bogus", Format: "json"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for bad level")
	}
}
