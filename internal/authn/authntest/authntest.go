// Package authntest is a minimal in-process OIDC issuer for tests: it
// serves a discovery document and a JWKS (RSA RS256 + EC P-256 ES256,
// each with a kid), mints signed tokens, supports key rotation, and
// counts JWKS fetches so refresh rate-limiting can be asserted.
package authntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
)

// IDP is a test OIDC issuer backed by httptest.
type IDP struct {
	Issuer string

	srv *httptest.Server

	mu      sync.Mutex
	rsaKey  *rsa.PrivateKey
	ecKey   *ecdsa.PrivateKey
	rsaKid  string
	ecKid   string
	extra   []jose.JSONWebKey
	rawKeys []json.RawMessage
	fetch   atomic.Int64
	fail    atomic.Bool
}

// New starts a test issuer. The issuer URL is http — config validation
// requires https outside dev_mode, so tests pass a dev-mode config.
func New(t *testing.T) *IDP {
	t.Helper()
	idp := &IDP{}
	var err error
	if idp.rsaKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if idp.ecKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		t.Fatal(err)
	}
	idp.rsaKid, idp.ecKid = "rsa-1", "ec-1"

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   idp.Issuer,
			"jwks_uri": idp.Issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		idp.fetch.Add(1)
		if idp.fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		idp.mu.Lock()
		defer idp.mu.Unlock()
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: idp.rsaKey.Public(), KeyID: idp.rsaKid, Algorithm: string(jose.RS256), Use: "sig"},
			{Key: idp.ecKey.Public(), KeyID: idp.ecKid, Algorithm: string(jose.ES256), Use: "sig"},
		}}
		set.Keys = append(set.Keys, idp.extra...)
		out, err := json.Marshal(set)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if len(idp.rawKeys) > 0 {
			var m map[string]json.RawMessage
			_ = json.Unmarshal(out, &m)
			var arr []json.RawMessage
			_ = json.Unmarshal(m["keys"], &arr)
			arr = append(arr, idp.rawKeys...)
			m["keys"], _ = json.Marshal(arr)
			out, _ = json.Marshal(m)
		}
		_, _ = w.Write(out)
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	idp.Issuer = idp.srv.URL
	return idp
}

// FetchCount returns how many times /jwks was requested.
func (i *IDP) FetchCount() int64 { return i.fetch.Load() }

// AddJWK publishes an additional key on the next JWKS fetch (e.g. a
// use:"enc" key or a weak RSA key).
func (i *IDP) AddJWK(k jose.JSONWebKey) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.extra = append(i.extra, k)
}

// AddRawJWK publishes a raw JWK JSON object (for fields go-jose does not
// model, such as key_ops).
func (i *IDP) AddRawJWK(raw json.RawMessage) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.rawKeys = append(i.rawKeys, raw)
}

// RotateKeys publishes a fresh RSA key under a new kid.
func (i *IDP) RotateKeys() {
	i.mu.Lock()
	defer i.mu.Unlock()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	i.rsaKey = k
	i.rsaKid = i.rsaKid + "x"
}

// FailJWKS makes /jwks return 500 until the returned func is called.
func (i *IDP) FailJWKS() (restore func()) {
	i.fail.Store(true)
	return func() { i.fail.Store(false) }
}

type tokenOpts struct {
	alg jose.SignatureAlgorithm
	kid string
	key any
	typ *string
}

// TokenOpt customizes minted tokens.
type TokenOpt func(*tokenOpts)

// WithAlg overrides the signing algorithm (must match WithKey when the
// algorithm family changes).
func WithAlg(a string) TokenOpt {
	return func(o *tokenOpts) { o.alg = jose.SignatureAlgorithm(a) }
}

// WithKid overrides the kid header (does not change the signing key).
func WithKid(k string) TokenOpt {
	return func(o *tokenOpts) { o.kid = k }
}

// WithKey signs with an arbitrary private key.
func WithKey(k any) TokenOpt {
	return func(o *tokenOpts) { o.key = k }
}

// WithTyp sets the JOSE typ header (nil = omit).
func WithTyp(typ string) TokenOpt {
	return func(o *tokenOpts) { o.typ = &typ }
}

// Token mints a signed JWT. Defaults: RS256 with the current RSA key
// and kid; claims is marshalled verbatim (set iss/aud/exp/iat yourself).
func (i *IDP) Token(t *testing.T, claims map[string]any, opts ...TokenOpt) string {
	t.Helper()
	o := tokenOpts{alg: jose.RS256}
	for _, f := range opts {
		f(&o)
	}
	i.mu.Lock()
	var key any = i.rsaKey
	kid := i.rsaKid
	if o.alg == jose.ES256 {
		key, kid = i.ecKey, i.ecKid
	}
	i.mu.Unlock()
	if o.key != nil {
		key = o.key
	}
	if o.kid != "" {
		kid = o.kid
	}
	so := &jose.SignerOptions{}
	so = so.WithHeader("kid", kid)
	if o.typ != nil {
		so = so.WithHeader("typ", *o.typ)
	}
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: o.alg, Key: key}, so)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := josejwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

// Claims returns a valid claim set for this issuer (aud "custos",
// exp/iat now-ish); callers mutate before minting.
func (i *IDP) Claims() map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":   i.Issuer,
		"sub":   "user-123",
		"aud":   "custos",
		"email": "u@example.com",
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
	}
}
