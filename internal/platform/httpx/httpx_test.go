package httpx

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/log"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

func TestRequestID(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := log.RequestIDFrom(r.Context()); id == "" {
			t.Error("request id missing from ctx")
		} else {
			w.Header().Set("X-Echo", id)
		}
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Header().Get("X-Request-ID") == "" ||
		rec.Header().Get("X-Echo") != rec.Header().Get("X-Request-ID") {
		t.Fatalf("generated id not propagated: %v", rec.Header())
	}

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", "custom-id-123")
	h.ServeHTTP(rec2, req)
	if rec2.Header().Get("X-Request-ID") != "custom-id-123" {
		t.Fatalf("incoming id rejected")
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.Header.Set("X-Request-ID", "bad id with space")
	h.ServeHTTP(rec3, req3)
	if rec3.Header().Get("X-Request-ID") == "bad id with space" {
		t.Fatal("invalid id accepted")
	}

	rec4 := httptest.NewRecorder()
	req4 := httptest.NewRequest("GET", "/", nil)
	req4.Header.Set("X-Request-ID", strings.Repeat("a", 129))
	h.ServeHTTP(rec4, req4)
	if rec4.Header().Get("X-Request-ID") == strings.Repeat("a", 129) {
		t.Fatal("oversized id accepted")
	}
}

func TestRecover(t *testing.T) {
	h := Recover(testLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret panic text")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret panic text") {
		t.Fatal("panic text leaked")
	}
	if !strings.Contains(rec.Body.String(), "INTERNAL") {
		t.Fatalf("no envelope: %s", rec.Body.String())
	}
}

func TestRecoverAbortHandler(t *testing.T) {
	h := Recover(testLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Fatal("ErrAbortHandler not re-panicked")
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}

func TestRealIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	var got string
	h := RealIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = ClientIPFrom(r.Context())
	}))

	// Untrusted peer: XFF ignored.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	req.Header.Set("X-Forwarded-For", "1.1.1.1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != "203.0.113.5" {
		t.Fatalf("untrusted peer: got %q", got)
	}

	// Trusted peer: right-most untrusted XFF wins.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "10.1.2.3:443"
	req2.Header.Set("X-Forwarded-For", "198.51.100.7, 10.9.9.9, 10.8.8.8")
	h.ServeHTTP(httptest.NewRecorder(), req2)
	if got != "198.51.100.7" {
		t.Fatalf("trusted peer: got %q", got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(ok))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	for k := range map[string]bool{
		"X-Content-Type-Options": true, "X-Frame-Options": true,
		"Referrer-Policy": true, "Cache-Control": true,
		"Content-Security-Policy": true,
	} {
		if rec.Header().Get(k) == "" {
			t.Fatalf("missing header %s", k)
		}
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS set on plain HTTP")
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.TLS = &tls.ConnectionState{}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("HSTS missing on TLS")
	}
}

func TestCORS(t *testing.T) {
	h := CORS([]string{"https://cli.example"})(http.HandlerFunc(ok))

	// Denied origin: no CORS headers, request still handled.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("origin reflected")
	}

	// Allowed origin.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Origin", "https://cli.example")
	h.ServeHTTP(rec2, req2)
	if rec2.Header().Get("Access-Control-Allow-Origin") != "https://cli.example" {
		t.Fatal("allowed origin not echoed")
	}

	// Preflight denied.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("OPTIONS", "/", nil)
	req3.Header.Set("Origin", "https://evil.example")
	req3.Header.Set("Access-Control-Request-Method", "POST")
	h.ServeHTTP(rec3, req3)
	if rec3.Code != 403 {
		t.Fatalf("preflight deny = %d", rec3.Code)
	}

	// Preflight allowed.
	rec4 := httptest.NewRecorder()
	req4 := httptest.NewRequest("OPTIONS", "/", nil)
	req4.Header.Set("Origin", "https://cli.example")
	req4.Header.Set("Access-Control-Request-Method", "POST")
	req4.Header.Set("Access-Control-Request-Headers", "X-Evil-Header")
	h.ServeHTTP(rec4, req4)
	if rec4.Code != 204 ||
		rec4.Header().Get("Access-Control-Allow-Headers") !=
			"Authorization, Content-Type, Idempotency-Key, X-Request-ID" {
		t.Fatalf("preflight allow wrong: %d %v", rec4.Code, rec4.Header())
	}
}

func TestBodyLimit(t *testing.T) {
	var readErr error
	h := BodyLimit(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))

	// Early reject via Content-Length.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader("0123456789"))
	h.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatalf("early reject = %d", rec.Code)
	}

	// MaxBytesReader path (unknown length).
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/", io.NopCloser(strings.NewReader("0123456789")))
	req2.ContentLength = -1
	h.ServeHTTP(rec2, req2)
	if readErr == nil {
		t.Fatal("expected MaxBytesReader error")
	}
}

func TestTimeout(t *testing.T) {
	h := Timeout(20 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			w.WriteHeader(200)
		case <-time.After(time.Second):
			t.Error("ctx not canceled")
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestWriteErrorMapping(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
		leak   string
	}{
		{apperr.New(apperr.Invalid, "bad.field", "nope"), 400, "bad.field", ""},
		{apperr.New(apperr.Unauthenticated, "auth", "who"), 401, "auth", ""},
		{apperr.New(apperr.Forbidden, "authz", "denied"), 403, "authz", ""},
		{apperr.New(apperr.NotFound, "nf", "gone"), 404, "nf", ""},
		{apperr.New(apperr.Conflict, "dup", "exists"), 409, "dup", ""},
		{apperr.New(apperr.RateLimited, "rl", "slow"), 429, "rl", ""},
		{apperr.New(apperr.Unavailable, "up", "down"), 503, "up", ""},
		{apperr.New(apperr.Internal, "x", "secret internal detail"), 500, "INTERNAL", "secret internal detail"},
		{context.DeadlineExceeded, 503, "INTERNAL", ""},
		{fmt.Errorf("wrap: %w", &http.MaxBytesError{Limit: 10}), 413, "BODY_TOO_LARGE", ""},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		WriteError(context.Background(), rec, tt.err)
		if rec.Code != tt.status {
			t.Fatalf("%v: status = %d want %d", tt.err, rec.Code, tt.status)
		}
		var body ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("bad envelope: %v", err)
		}
		if body.Error.Code != tt.code {
			t.Fatalf("%v: code = %q want %q", tt.err, body.Error.Code, tt.code)
		}
		if tt.leak != "" && strings.Contains(rec.Body.String(), tt.leak) {
			t.Fatalf("internal message leaked: %s", rec.Body.String())
		}
		if tt.err != nil && tt.status == 503 &&
			body.Error.Message != "service unavailable" {
			t.Fatalf("503 message = %q", body.Error.Message)
		}
	}
}

func TestServerGracefulShutdown(t *testing.T) {
	cfg := config.Server{
		Listen:          "127.0.0.1:0",
		TLS:             config.TLS{Mode: "disabled"},
		ShutdownTimeout: 5 * time.Second,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	srv, err := NewServer(cfg, mux, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = srv.Serve(ln)
	}()
	addr := ln.Addr().String()
	resp, err := http.Get("http://" + addr + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shCtx, shCancel := context.WithTimeout(ctx, 3*time.Second)
	defer shCancel()
	if err := srv.Shutdown(shCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestChainOrder(t *testing.T) {
	var order []string
	mw := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	Chain(mw("a"), mw("b"), mw("c"))(http.HandlerFunc(ok)).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if strings.Join(order, ",") != "a,b,c" {
		t.Fatalf("order = %v", order)
	}
}
