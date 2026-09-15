package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func readyBody(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Status string `json:"status"`
	Checks []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"checks"`
} {
	t.Helper()
	var body struct {
		Status string `json:"status"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestLive(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRegistry().LiveHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("live = %d %s", rec.Code, rec.Body.String())
	}
}

func TestReadyRequiredFail(t *testing.T) {
	reg := NewRegistry()
	reg.Register(CheckerFunc("db", func(context.Context) error {
		return errors.New("postgres://user:SECRET@host down")
	}), true)
	reg.Register(CheckerFunc("opt", func(context.Context) error { return nil }), false)

	rec := httptest.NewRecorder()
	reg.ReadyHandler(500*time.Millisecond, discardLogger()).
		ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := readyBody(t, rec)
	if body.Status != "unavailable" {
		t.Fatalf("status = %q", body.Status)
	}
	if strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatal("checker error text leaked into body")
	}
}

func TestReadyOptionalFailDegraded(t *testing.T) {
	reg := NewRegistry()
	reg.Register(CheckerFunc("db", func(context.Context) error { return nil }), true)
	reg.Register(CheckerFunc("bao", func(context.Context) error {
		return errors.New("unreachable")
	}), false)

	rec := httptest.NewRecorder()
	reg.ReadyHandler(500*time.Millisecond, discardLogger()).
		ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := readyBody(t, rec).Status; got != "degraded" {
		t.Fatalf("status = %q", got)
	}
}

func TestReadySlowCheckerExceedsBudget(t *testing.T) {
	reg := NewRegistry()
	reg.Register(CheckerFunc("slow", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	}), true)

	rec := httptest.NewRecorder()
	reg.ReadyHandler(20*time.Millisecond, discardLogger()).
		ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := readyBody(t, rec).Status; got != "unavailable" {
		t.Fatalf("status = %q", got)
	}
}
