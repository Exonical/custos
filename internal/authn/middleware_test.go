package authn

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn/authntest"
)

type recordingSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordingSink) Record(_ context.Context, e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingSink) last() audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[len(r.events)-1]
}

func TestRequireBearer(t *testing.T) {
	idp := authntest.New(t)
	v := newVerifier(t, idp)
	rec := &recordingSink{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	okHandler := RequireBearer(v, rec, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			t.Error("principal missing")
		}
		w.WriteHeader(http.StatusTeapot)
		_ = p
	}))

	valid := idp.Token(t, idp.Claims())

	cases := []struct {
		name   string
		header string
		want   int
		code   string // expected audit reason when rejected
	}{
		{"valid", "Bearer " + valid, http.StatusTeapot, ""},
		{"valid-lowercase", "bearer " + valid, http.StatusTeapot, ""},
		{"missing", "", http.StatusUnauthorized, "TOKEN_MISSING"},
		{"basic", "Basic dXNlcjpwYXNz", http.StatusUnauthorized, "TOKEN_MISSING"},
		{"two-tokens", "Bearer " + valid + " " + valid, http.StatusUnauthorized, "TOKEN_MALFORMED"},
		{"bad-token", "Bearer garbage.token.here", http.StatusUnauthorized, "TOKEN_MALFORMED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			okHandler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if tc.want == http.StatusUnauthorized {
				if w.Header().Get("WWW-Authenticate") == "" {
					t.Error("missing WWW-Authenticate")
				}
				e := rec.last()
				if e.Action != "auth.token_rejected" ||
					e.Details["reason"] != tc.code {
					t.Fatalf("audit event: %+v", e)
				}
				// The token must never appear in audit details or body.
				b, _ := json.Marshal(e.Details)
				if strings.Contains(string(b), valid) ||
					strings.Contains(w.Body.String(), valid) {
					t.Fatal("token leaked into audit/response")
				}
			}
		})
	}
}

func TestDenyAll(t *testing.T) {
	if _, err := (DenyAll{}).Verify(context.Background(), "x"); err == nil {
		t.Fatal("expected rejection")
	}
}
