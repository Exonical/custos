package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPrincipalRateLimiter(t *testing.T) {
	l := NewPrincipalRateLimiter(60, 3, 0)
	now := time.Now()
	l.now = func() time.Time { return now }
	h := l.Middleware(func(r *http.Request) string {
		return r.Header.Get("X-Principal")
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	code := func(principal string) int {
		r := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v", nil)
		req.Header.Set("X-Principal", principal)
		h.ServeHTTP(r, req)
		return r.Code
	}

	// burst of 3 allowed, 4th rejected
	for i := 0; i < 3; i++ {
		if code("a") != http.StatusNoContent {
			t.Fatalf("burst %d rejected", i)
		}
	}
	if code("a") != http.StatusTooManyRequests {
		t.Fatal("over-burst must 429")
	}
	// other principals unaffected
	if code("b") != http.StatusNoContent {
		t.Fatal("principal isolation")
	}
	// refill: +30s = 30 tokens/min rate → ~0.5 token; need 1s more
	now = now.Add(61 * time.Second)
	if code("a") != http.StatusNoContent {
		t.Fatal("bucket did not refill")
	}
	// empty key bypasses
	h2 := l.Middleware(func(*http.Request) string { return "" })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
	for i := 0; i < 10; i++ {
		r := httptest.NewRecorder()
		h2.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v", nil))
		if r.Code != http.StatusNoContent {
			t.Fatal("empty key must bypass")
		}
	}
}

func TestPrincipalRateLimiterEviction(t *testing.T) {
	l := NewPrincipalRateLimiter(60, 1, 4)
	for i, k := range []string{"a", "b", "c", "d", "e"} {
		if !l.allow(k) {
			t.Fatalf("key %d rejected", i)
		}
	}
	if len(l.buckets) != 4 {
		t.Fatalf("map not bounded: %d", len(l.buckets))
	}
}
