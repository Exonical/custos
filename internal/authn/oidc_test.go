package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/Exonical/custos/internal/authn/authntest"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/config"
)

func testCfg(iss string) config.OIDC {
	return config.OIDC{
		Issuer:                 iss,
		Audiences:              []string{"custos"},
		AllowedAlgorithms:      []string{"RS256", "ES256", "PS256"},
		Discovery:              true,
		JWKSCacheTTL:           time.Hour,
		JWKSRefreshMinInterval: 30 * time.Second,
		ClockSkew:              time.Minute,
		AcceptedTokenTypes:     []string{"at+jwt", "JWT", ""},
		MaxTokenLifetime:       24 * time.Hour,
		Claims: config.OIDCClaims{
			Subject: "sub", Email: "email", Name: "name", Groups: "groups",
		},
	}
}

func newVerifier(t *testing.T, idp *authntest.IDP) *OIDCVerifier {
	t.Helper()
	cfg := testCfg(idp.Issuer)
	// dev-mode equivalent: http issuer allowed because the verifier
	// itself does not police the scheme — config validation does.
	v, err := NewOIDCVerifier(context.Background(), cfg,
		idpHTTPClient(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	t.Cleanup(v.Close)
	return v
}

func idpHTTPClient() *http.Client { return http.DefaultClient }

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("want apperr.Error, got %v", err)
	}
	return ae.Code
}

func TestVerifyHappyPath(t *testing.T) {
	idp := authntest.New(t)
	v := newVerifier(t, idp)

	p, err := v.Verify(context.Background(), idp.Token(t, idp.Claims()))
	if err != nil {
		t.Fatal(err)
	}
	if p.Subject != "user-123" || p.Kind != KindUser || p.Email != "u@example.com" ||
		p.Issuer != idp.Issuer {
		t.Fatalf("bad principal: %+v", p)
	}

	// ES256 path.
	c := idp.Claims()
	c["groups"] = []any{"admins", "ops"}
	c["scope"] = "openid profile"
	p, err = v.Verify(context.Background(), idp.Token(t, c, authntest.WithAlg("ES256")))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Groups) != 2 || len(p.Scopes) != 2 {
		t.Fatalf("bad groups/scopes: %+v", p)
	}

	// Service-kind heuristic: no email, client_id == sub.
	c = idp.Claims()
	delete(c, "email")
	c["sub"] = "svc-deploy"
	c["client_id"] = "svc-deploy"
	p, err = v.Verify(context.Background(), idp.Token(t, c))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindService {
		t.Fatalf("kind = %v", p.Kind)
	}

	// aud as array, scp array.
	c = idp.Claims()
	c["aud"] = []any{"other", "custos"}
	c["scp"] = []any{"jobs:read"}
	if _, err := v.Verify(context.Background(), idp.Token(t, c)); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejections(t *testing.T) {
	idp := authntest.New(t)
	v := newVerifier(t, idp)
	verify := func(tok string) string {
		_, err := v.Verify(context.Background(), tok)
		if err == nil {
			t.Fatal("expected rejection")
		}
		return codeOf(t, err)
	}
	valid := idp.Token(t, idp.Claims())

	cases := []struct {
		name string
		tok  func() string
		want string
	}{
		{"garbage", func() string { return "not-a-jwt" }, "TOKEN_MALFORMED"},
		{"bad-b64-header", func() string { return "%%%.x.y" }, "TOKEN_MALFORMED"},
		{"alg-none", func() string {
			c := idp.Claims()
			return idp.Token(t, c, authntest.WithAlg("RS256")) // placeholder replaced below
		}, "TOKEN_ALG_NOT_ALLOWED"},
	}
	_ = cases

	// none alg: hand-craft unsigned JWT.
	if got := verify("eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0."); got != "TOKEN_ALG_NOT_ALLOWED" {
		t.Fatalf("alg none: %s", got)
	}
	// HS256 not on the list.
	c := idp.Claims()
	if got := verify(idp.Token(t, c, authntest.WithAlg("HS256"), authntest.WithKey([]byte("0123456789abcdef0123456789abcdef")))); got != "TOKEN_ALG_NOT_ALLOWED" {
		t.Fatalf("hs256: %s", got)
	}
	// Key confusion: PS256-signed token against RS256-labelled JWK.
	if got := verify(idp.Token(t, idp.Claims(), authntest.WithAlg("PS256"))); got != "TOKEN_ALG_NOT_ALLOWED" {
		t.Fatalf("ps256-confusion: %s", got)
	}
	// Unknown kid.
	if got := verify(idp.Token(t, idp.Claims(), authntest.WithKid("nope"))); got != "TOKEN_UNKNOWN_KEY" {
		t.Fatalf("unknown kid: %s", got)
	}
	// Wrong key signature.
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	if got := verify(idp.Token(t, idp.Claims(), authntest.WithKey(other))); got != "TOKEN_SIGNATURE_INVALID" {
		t.Fatalf("wrong key: %s", got)
	}
	// iss mismatch.
	c = idp.Claims()
	c["iss"] = "https://evil.example.com"
	if got := verify(idp.Token(t, c)); got != "TOKEN_ISSUER_MISMATCH" {
		t.Fatalf("iss: %s", got)
	}
	// aud mismatch.
	c = idp.Claims()
	c["aud"] = "someone-else"
	if got := verify(idp.Token(t, c)); got != "TOKEN_AUDIENCE_MISMATCH" {
		t.Fatalf("aud: %s", got)
	}
	// expired.
	c = idp.Claims()
	c["exp"] = time.Now().Add(-time.Hour).Unix()
	c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	if got := verify(idp.Token(t, c)); got != "TOKEN_EXPIRED" {
		t.Fatalf("expired: %s", got)
	}
	// missing exp.
	c = idp.Claims()
	delete(c, "exp")
	if got := verify(idp.Token(t, c)); got != "TOKEN_EXPIRED" {
		t.Fatalf("no exp: %s", got)
	}
	// nbf in future.
	c = idp.Claims()
	c["nbf"] = time.Now().Add(time.Hour).Unix()
	if got := verify(idp.Token(t, c)); got != "TOKEN_NOT_YET_VALID" {
		t.Fatalf("nbf: %s", got)
	}
	// lifetime > 24h.
	c = idp.Claims()
	c["iat"] = time.Now().Unix()
	c["exp"] = time.Now().Add(48 * time.Hour).Unix()
	if got := verify(idp.Token(t, c)); got != "TOKEN_LIFETIME_EXCEEDED" {
		t.Fatalf("lifetime: %s", got)
	}
	// missing iat with cap.
	c = idp.Claims()
	delete(c, "iat")
	if got := verify(idp.Token(t, c)); got != "TOKEN_LIFETIME_EXCEEDED" {
		t.Fatalf("no iat: %s", got)
	}
	// bad typ.
	if got := verify(idp.Token(t, idp.Claims(), authntest.WithTyp("refresh+jwt"))); got != "TOKEN_TYPE_REJECTED" {
		t.Fatalf("typ: %s", got)
	}
	// missing required scope.
	cfg := testCfg(idp.Issuer)
	cfg.RequiredScopes = []string{"jobs:write"}
	v2, err := NewOIDCVerifier(context.Background(), cfg, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer v2.Close()
	if _, err := v2.Verify(context.Background(), valid); codeOf(t, err) != "TOKEN_SCOPE_INSUFFICIENT" {
		t.Fatalf("scope: %v", err)
	}
	// sufficient scope passes.
	c = idp.Claims()
	c["scope"] = "openid jobs:write"
	if _, err := v2.Verify(context.Background(), idp.Token(t, c)); err != nil {
		t.Fatalf("scope ok: %v", err)
	}
}

func TestVerifyUnusableKeys(t *testing.T) {
	idp := authntest.New(t)

	// A use:"enc" key carrying the token's kid must not verify it.
	encKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp.AddJWK(jose.JSONWebKey{
		Key: encKey.Public(), KeyID: "enc-1",
		Algorithm: string(jose.RS256), Use: "enc",
	})

	// A 1024-bit RSA key is dropped at JWKS load.
	weakKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	idp.AddJWK(jose.JSONWebKey{
		Key: weakKey.Public(), KeyID: "weak-1",
		Algorithm: string(jose.RS256), Use: "sig",
	})

	// A key_ops list without "verify" is dropped.
	koKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jose.JSONWebKey{
		Key: koKey.Public(), KeyID: "ko-1",
		Algorithm: string(jose.RS256), Use: "sig",
	}.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["key_ops"] = []string{"encrypt"}
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	idp.AddRawJWK(raw)

	v := newVerifier(t, idp)
	for _, tc := range []struct {
		name string
		key  *rsa.PrivateKey
		kid  string
	}{
		{"enc-key", encKey, "enc-1"},
		{"weak-rsa", weakKey, "weak-1"},
		{"key-ops-no-verify", koKey, "ko-1"},
	} {
		tok := idp.Token(t, idp.Claims(),
			authntest.WithKey(tc.key), authntest.WithKid(tc.kid))
		if _, err := v.Verify(context.Background(), tok); codeOf(t, err) != "TOKEN_UNKNOWN_KEY" {
			t.Fatalf("%s: got %v", tc.name, err)
		}
	}
}

func TestAudienceRequiredWhenIssuerSet(t *testing.T) {
	idp := authntest.New(t)
	v := newVerifier(t, idp)

	// Empty configured audiences fail closed even for a valid token.
	cfg := testCfg(idp.Issuer)
	cfg.Audiences = nil
	v2, err := NewOIDCVerifier(context.Background(), cfg, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer v2.Close()
	if _, err := v2.Verify(context.Background(),
		idp.Token(t, idp.Claims())); codeOf(t, err) != "TOKEN_AUDIENCE_MISMATCH" {
		t.Fatalf("empty audiences must fail closed: %v", err)
	}
	// Sanity: the normal verifier still accepts.
	if _, err := v.Verify(context.Background(),
		idp.Token(t, idp.Claims())); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownKidRateLimited(t *testing.T) {
	idp := authntest.New(t)
	v := newVerifier(t, idp)
	before := idp.FetchCount()

	// 20 concurrent tokens with an unknown kid → exactly one refresh.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := v.Verify(context.Background(),
				idp.Token(t, idp.Claims(), authntest.WithKid("unknown")))
			if codeOf(t, err) != "TOKEN_UNKNOWN_KEY" {
				t.Errorf("got %v", err)
			}
		}()
	}
	wg.Wait()
	if got := idp.FetchCount(); got != before+1 {
		t.Fatalf("jwks fetches = %d, want %d", got, before+1)
	}

	// Rotation: new kid succeeds after the single refresh.
	idp.RotateKeys()
	// Force the min-interval window shut so the next verify refetches:
	v.mu.Lock()
	v.lastRefresh = time.Now().Add(-time.Hour)
	v.mu.Unlock()
	if _, err := v.Verify(context.Background(),
		idp.Token(t, idp.Claims())); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
}

func TestStaleWhileError(t *testing.T) {
	idp := authntest.New(t)
	v := newVerifier(t, idp)
	if err := v.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}

	restore := idp.FailJWKS()
	// Old snapshot still verifies while JWKS is down.
	if _, err := v.Verify(context.Background(), idp.Token(t, idp.Claims())); err != nil {
		t.Fatalf("stale snapshot: %v", err)
	}
	if err := v.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Push the snapshot past the 24h cap.
	v.mu.Lock()
	v.fetchedAt = v.now().Add(-25 * time.Hour)
	v.mu.Unlock()
	if err := v.Ready(context.Background()); err == nil {
		t.Fatal("expected stale-cap readiness failure")
	}
	restore()
}

func TestDiscoveryFailure(t *testing.T) {
	cfg := testCfg("http://127.0.0.1:1")
	if _, err := NewOIDCVerifier(context.Background(), cfg, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("expected discovery failure")
	}
}

func TestIssuerMismatchDiscovery(t *testing.T) {
	// IDP that claims a different issuer in its discovery doc.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   "https://other.example.com",
			"jwks_uri": "https://other.example.com/jwks",
		})
	}))
	defer srv.Close()
	cfg := testCfg(srv.URL)
	if _, err := NewOIDCVerifier(context.Background(), cfg, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("expected issuer mismatch")
	}
}
