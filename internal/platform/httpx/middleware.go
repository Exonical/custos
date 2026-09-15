package httpx

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/log"
)

type ctxKey int

const clientIPKey ctxKey = iota

// ClientIPFrom returns the client IP established by RealIP, or "".
func ClientIPFrom(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey).(string)
	return ip
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7E {
			return false
		}
	}
	return true
}

// RequestID accepts a well-formed X-Request-ID (≤128 printable ASCII) or
// generates a UUIDv7, attaches it to the context, and echoes it back.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = uuid.Must(uuid.NewV7()).String()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(log.WithRequestID(r.Context(), id)))
	})
}

// Recover converts panics into a 500 envelope. http.ErrAbortHandler is
// re-panicked to preserve net/http semantics.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					logger.ErrorContext(r.Context(), "panic recovered",
						"error", rec, "stack", string(debug.Stack()))
					WriteError(r.Context(), w,
						apperr.New(apperr.Internal, "INTERNAL", "internal error"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RealIP derives the client IP. When the direct peer is a trusted proxy,
// the right-most untrusted X-Forwarded-For entry wins; otherwise the peer
// address itself is used. Untrusted XFF headers are ignored entirely.
func RealIP(trusted []netip.Prefix) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := remoteIP(r.RemoteAddr)
			if addr, err := netip.ParseAddr(ip); err == nil && inPrefixes(addr, trusted) {
				// Walk XFF right-to-left: first non-trusted entry is the client.
				parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
				for i := len(parts) - 1; i >= 0; i-- {
					p := strings.TrimSpace(parts[i])
					a, err := netip.ParseAddr(p)
					if err != nil {
						continue
					}
					if !inPrefixes(a, trusted) {
						ip = p
						break
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(
				context.WithValue(r.Context(), clientIPKey, ip)))
		})
	}
}

func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func inPrefixes(a netip.Addr, prefixes []netip.Prefix) bool {
	return slices.ContainsFunc(prefixes, func(p netip.Prefix) bool { return p.Contains(a) })
}

// SecurityHeaders sets the API response headers. HSTS only on TLS.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// CORS is deny-by-default: only exact-match origins get CORS headers.
// Credentials are never allowed.
func CORS(allowed []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			w.Header().Add("Vary", "Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !slices.Contains(allowed, origin) {
				if r.Method == http.MethodOptions &&
					r.Header.Get("Access-Control-Request-Method") != "" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			if r.Method == http.MethodOptions &&
				r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				// Fixed allow-list; never reflect the requested headers.
				h.Set("Access-Control-Allow-Headers",
					"Authorization, Content-Type, Idempotency-Key, X-Request-ID")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit caps request bodies. Oversized Content-Length is rejected
// before reading; streaming bodies are capped by MaxBytesReader.
func BodyLimit(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				WriteError(r.Context(), w, apperr.New(
					apperr.Invalid, "BODY_TOO_LARGE", "request body too large"))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout gives each request a deadline via context.WithTimeout — not
// http.TimeoutHandler — so handlers observe cancellation through ctx and
// can still write a response while the deadline is firm.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush forwards streaming flushes when the underlying writer supports it.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying ResponseWriter for ResponseController.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog emits one record per request. It deliberately never logs
// headers, query strings, or bodies.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.InfoContext(r.Context(), "http request",
				"method", r.Method,
				"route", r.Pattern,
				"status", status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"client_ip", ClientIPFrom(r.Context()),
				"user_agent", r.UserAgent())
		})
	}
}
