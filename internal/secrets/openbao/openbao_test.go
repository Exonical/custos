package openbao_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/secrets/openbao"
)

func provider(t *testing.T, h http.Handler) (*openbao.Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jwt := t.TempDir() + "/jwt"
	if err := os.WriteFile(jwt, []byte("workload-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := openbao.New(context.Background(), openbao.Config{Address: srv.URL, Namespace: "custos", Timeout: time.Second, DevMode: true, Auth: openbao.AuthConfig{Method: "jwt", JWT: openbao.JWTAuth{Role: "custos", TokenFile: jwt}}}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, srv
}

func TestLoginKVVersionAndChildToken(t *testing.T) {
	var child, revoke atomic.Int32
	var query, ns string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/jwt/login":
			if r.Header.Get("X-Vault-Namespace") != "custos" {
				t.Errorf("login namespace %q", r.Header.Get("X-Vault-Namespace"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "parent", "lease_duration": 3600, "renewable": true}})
		case "/v1/auth/token/create-orphan":
			child.Add(1)
			ns = r.Header.Get("X-Vault-Namespace")
			_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "child"}})
		case "/v1/kv/data/users/u/token":
			query = r.URL.RawQuery
			if r.Header.Get("X-Vault-Token") != "child" {
				t.Errorf("token %q", r.Header.Get("X-Vault-Token"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]any{"value": "super-secret"}, "metadata": map[string]any{"version": 3}}})
		case "/v1/auth/token/revoke-self":
			revoke.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	p, _ := provider(t, h)
	v, err := p.Resolve(context.Background(), secrets.Reference{Provider: "openbao", Namespace: "custos/tenants/t1", Mount: "kv", Path: "users/u/token", Key: "value", Version: 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(v.Reveal()) != "super-secret" {
		t.Fatal("wrong value")
	}
	v.Wipe()
	if child.Load() != 1 || ns != "custos/tenants/t1" || query != "version=3" {
		t.Fatalf("flow child=%d ns=%s query=%s", child.Load(), ns, query)
	}
	if revoke.Load() != 1 {
		t.Fatalf("child revoke=%d", revoke.Load())
	}
}

func TestRenewAndRelogin(t *testing.T) {
	var login, renew atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/jwt/login":
			n := login.Add(1)
			renewable := n == 1
			_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "token", "lease_duration": 1, "renewable": renewable}})
		case "/v1/auth/token/renew-self":
			renew.Add(1)
			http.Error(w, "no", http.StatusInternalServerError)
		case "/v1/auth/token/revoke-self":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	p, _ := provider(t, h)
	defer func() { _ = p.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	for login.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if renew.Load() == 0 || login.Load() < 2 {
		t.Fatalf("renew=%d login=%d", renew.Load(), login.Load())
	}
}

func TestDebugLogRedactsValueAndPath(t *testing.T) {
	const secretValue = "super-secret-value"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/jwt/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{
				"client_token": "parent", "lease_duration": 3600}})
		case "/v1/kv/data/users/alice/private":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"data": map[string]any{"value": secretValue}}})
		case "/v1/auth/token/revoke-self":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	jwt := t.TempDir() + "/jwt"
	if err := os.WriteFile(jwt, []byte("workload-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p, err := openbao.New(context.Background(), openbao.Config{Address: srv.URL,
		Namespace: "custos", Timeout: time.Second, DevMode: true,
		Auth: openbao.AuthConfig{Method: "jwt", JWT: openbao.JWTAuth{
			Role: "custos", TokenFile: jwt}}}, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := p.Resolve(context.Background(), secrets.Reference{Provider: "openbao",
		Namespace: "custos", Mount: "kv", Path: "users/alice/private", Key: "value"})
	if err != nil {
		t.Fatal(err)
	}
	v.Wipe()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), secretValue) || strings.Contains(logs.String(), "users/alice/private") {
		t.Fatalf("sensitive value or path leaked: %s", logs.String())
	}
}

func TestErrorMappingAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   apperr.Kind
		code   string
	}{{403, apperr.Forbidden, "secrets.forbidden"}, {404, apperr.NotFound, "secrets.not_found"}, {500, apperr.Unavailable, "SECRETS_UNAVAILABLE"}} {
		t.Run(tc.code, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/auth/jwt/login" {
					_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "parent", "lease_duration": 3600}})
					return
				}
				http.Error(w, "response contains top-secret", tc.status)
			})
			p, _ := provider(t, h)
			_, err := p.Resolve(context.Background(), secrets.Reference{Provider: "openbao", Namespace: "custos", Mount: "kv", Path: "x", Key: "value"})
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Kind != tc.kind || ae.Code != tc.code {
				t.Fatalf("%v", err)
			}
			if strings.Contains(err.Error(), "top-secret") {
				t.Fatal("secret leaked in error")
			}
		})
	}
}
