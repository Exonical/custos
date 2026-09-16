package authn

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/config"
)

// staleCap bounds stale-while-error: past it, Ready reports degraded and
// verification of still-cached keys remains allowed only until the
// snapshot is this old.
const staleCap = 24 * time.Hour

// OIDCVerifier validates JWT access tokens against the issuer's JWKS.
// Discovery uses go-oidc (which also validates the discovered issuer
// string); the JWKS snapshot is fetched and refreshed directly so the
// refresh policy (singleflight min-interval, stale-while-error) is ours.
type OIDCVerifier struct {
	cfg    config.OIDC
	client *http.Client
	logger *slog.Logger
	now    func() time.Time // injectable for tests

	jwksURI string

	mu          sync.Mutex
	keys        []jose.JSONWebKey
	fetchedAt   time.Time // zero until first successful fetch
	lastRefresh time.Time // on-demand (unknown-kid) refreshes only
	refreshing  bool
	stop        chan struct{}
	stopped     chan struct{}
}

// NewOIDCVerifier performs discovery (unless disabled) and the initial
// JWKS fetch. Discovery failure is fatal; a failed initial JWKS fetch
// leaves a verifier that fails closed (no keys) and reports not-ready.
func NewOIDCVerifier(ctx context.Context, cfg config.OIDC,
	httpClient *http.Client, logger *slog.Logger) (*OIDCVerifier, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	v := &OIDCVerifier{
		cfg:     cfg,
		client:  httpClient,
		logger:  logger,
		now:     time.Now,
		jwksURI: cfg.JWKSURI,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	if cfg.Discovery {
		p, err := oidc.NewProvider(oidc.ClientContext(ctx, httpClient), cfg.Issuer)
		if err != nil {
			return nil, apperr.Wrap(err, apperr.Unavailable, "authn.discovery",
				"OIDC discovery failed")
		}
		// go-oidc already asserts discovery issuer == cfg.Issuer.
		var meta struct {
			JWKSURI string `json:"jwks_uri"`
		}
		if err := p.Claims(&meta); err != nil {
			return nil, apperr.Wrap(err, apperr.Unavailable, "authn.discovery",
				"cannot parse discovery document")
		}
		v.jwksURI = meta.JWKSURI
		if v.jwksURI == "" {
			return nil, apperr.New(apperr.Unavailable, "authn.discovery",
				"discovery document has no jwks_uri")
		}
	}
	if v.jwksURI == "" {
		return nil, apperr.New(apperr.Invalid, "authn.config",
			"jwks_uri required when discovery is disabled")
	}
	if err := v.fetchJWKS(ctx); err != nil {
		logger.WarnContext(ctx, "initial JWKS fetch failed; failing closed",
			"error", err)
	}
	// #nosec G118 -- the refresher intentionally outlives the startup ctx
	go v.refreshLoop()
	return v, nil
}

// Close stops the background JWKS refresh.
func (v *OIDCVerifier) Close() {
	close(v.stop)
	<-v.stopped
}

// Ready implements a health.Checker for the "jwks" dependency: fails
// when no snapshot was ever fetched or the snapshot is older than the
// stale-while-error cap (24h).
func (v *OIDCVerifier) Ready(_ context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fetchedAt.IsZero() {
		return errors.New("jwks: never fetched")
	}
	if v.now().Sub(v.fetchedAt) > staleCap {
		return errors.New("jwks: snapshot stale beyond 24h cap")
	}
	return nil
}

// Name implements health.Checker.
func (v *OIDCVerifier) Name() string { return "jwks" }

// Check implements health.Checker (required check for readiness).
func (v *OIDCVerifier) Check(ctx context.Context) error { return v.Ready(ctx) }

func (v *OIDCVerifier) refreshLoop() {
	defer close(v.stopped)
	t := time.NewTicker(v.cfg.JWKSCacheTTL)
	defer t.Stop()
	for {
		select {
		case <-v.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := v.fetchJWKS(ctx); err != nil {
				v.logger.Warn("JWKS background refresh failed; keeping previous snapshot",
					"error", err)
			}
			cancel()
		}
	}
}

// fetchJWKS replaces the key snapshot on success; on error the previous
// snapshot is kept (stale-while-error).
func (v *OIDCVerifier) fetchJWKS(ctx context.Context) error {
	// jwksURI comes from validated config/discovery, not request input.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURI, nil) // #nosec G704
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req) // #nosec G704 -- configured endpoint
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch: status %d", resp.StatusCode)
	}
	var raw struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return fmt.Errorf("jwks fetch: %w", err)
	}
	if len(raw.Keys) == 0 {
		return errors.New("jwks fetch: empty key set")
	}
	keys := v.filterKeys(raw.Keys)
	if len(keys) == 0 {
		return errors.New("jwks fetch: no usable verification keys")
	}
	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = v.now()
	v.mu.Unlock()
	return nil
}

// filterKeys drops keys that cannot verify tokens: `use` set to anything
// but "sig", `key_ops` present without "verify" (go-jose does not model
// key_ops, so it is read from the raw JSON), and RSA keys below 2048
// bits. Dropped keys resolve as unknown -> TOKEN_UNKNOWN_KEY.
func (v *OIDCVerifier) filterKeys(raw []json.RawMessage) []jose.JSONWebKey {
	out := make([]jose.JSONWebKey, 0, len(raw))
	for _, r := range raw {
		var k jose.JSONWebKey
		if err := json.Unmarshal(r, &k); err != nil {
			v.logger.Warn("dropping unparseable JWK")
			continue
		}
		var shadow struct {
			KeyOps []string `json:"key_ops"`
		}
		_ = json.Unmarshal(r, &shadow)
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if len(shadow.KeyOps) > 0 && !slices.Contains(shadow.KeyOps, "verify") {
			continue
		}
		if rsa, ok := k.Key.(*rsa.PublicKey); ok && rsa.N.BitLen() < 2048 {
			v.logger.Warn("dropping RSA JWK below 2048 bits", "kid", k.KeyID)
			continue
		}
		out = append(out, k)
	}
	return out
}

// refreshOnUnknownKid runs one bounded refresh: at most one in flight,
// and at most one per JWKSRefreshMinInterval. Returns false when a
// refresh is not permitted (caller should reject TOKEN_UNKNOWN_KEY).
func (v *OIDCVerifier) refreshOnUnknownKid(ctx context.Context) bool {
	v.mu.Lock()
	if v.refreshing || v.now().Sub(v.lastRefresh) < v.cfg.JWKSRefreshMinInterval {
		v.mu.Unlock()
		return false
	}
	v.refreshing = true
	v.lastRefresh = v.now()
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.refreshing = false
		v.mu.Unlock()
	}()
	if err := v.fetchJWKS(ctx); err != nil {
		v.logger.WarnContext(ctx, "JWKS on-demand refresh failed; keeping previous snapshot",
			"error", err)
	}
	return true
}

func (v *OIDCVerifier) snapshot() []jose.JSONWebKey {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.keys)
}

// Verify implements Verifier.
func (v *OIDCVerifier) Verify(ctx context.Context, raw string) (Principal, error) {
	fail := func(code, msg string) (Principal, error) {
		return Principal{}, errUnauthenticated(code, msg)
	}

	// 1. Parse the header only; alg must be on the allow-list.
	hdr, err := parseHeader(raw)
	if err != nil {
		return fail("TOKEN_MALFORMED", "cannot parse token header")
	}
	if hdr.Alg == "" || !slices.Contains(v.cfg.AllowedAlgorithms, hdr.Alg) {
		return fail("TOKEN_ALG_NOT_ALLOWED", "token algorithm not permitted")
	}

	// 2. Resolve the key by kid; single key may omit kid in the token.
	keys := v.snapshot()
	key := findKey(keys, hdr.Kid)
	if key == nil {
		if !v.refreshOnUnknownKid(ctx) {
			return fail("TOKEN_UNKNOWN_KEY", "token key id unknown")
		}
		keys = v.snapshot()
		if key = findKey(keys, hdr.Kid); key == nil {
			return fail("TOKEN_UNKNOWN_KEY", "token key id unknown")
		}
	}

	// 3. Key-confusion guard: verify with the algorithm implied by the
	// JWK, and require the token header alg to equal it.
	wantAlg := jwkAlg(*key)
	if wantAlg == "" || hdr.Alg != string(wantAlg) {
		return fail("TOKEN_ALG_NOT_ALLOWED", "token algorithm does not match the resolved key")
	}

	tok, err := josejwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(hdr.Alg)})
	if err != nil {
		return fail("TOKEN_MALFORMED", "cannot parse token")
	}
	var claims map[string]any
	if err := tok.Claims(key.Public(), &claims); err != nil {
		return fail("TOKEN_SIGNATURE_INVALID", "signature verification failed")
	}

	// 4. iss exact match.
	if str(claims, "iss") != v.cfg.Issuer {
		return fail("TOKEN_ISSUER_MISMATCH", "issuer mismatch")
	}
	// 5. aud (string or array) must intersect configured audiences.
	if !audIntersects(claims["aud"], v.cfg.Audiences) {
		return fail("TOKEN_AUDIENCE_MISMATCH", "audience mismatch")
	}
	// 6. exp required; exp/nbf/iat with skew; lifetime cap needs iat.
	now := v.now()
	exp, hasExp := numericDate(claims["exp"])
	if !hasExp {
		return fail("TOKEN_EXPIRED", "token has no expiry")
	}
	if now.After(exp.Add(v.cfg.ClockSkew)) {
		return fail("TOKEN_EXPIRED", "token expired")
	}
	if nbf, ok := numericDate(claims["nbf"]); ok && now.Add(v.cfg.ClockSkew).Before(nbf) {
		return fail("TOKEN_NOT_YET_VALID", "token not yet valid")
	}
	iat, hasIat := numericDate(claims["iat"])
	if hasIat && now.Add(v.cfg.ClockSkew).Before(iat) {
		return fail("TOKEN_NOT_YET_VALID", "token issued in the future")
	}
	if v.cfg.MaxTokenLifetime > 0 {
		if !hasIat {
			// Without iat the lifetime is unbounded — reject when a cap
			// is configured.
			return fail("TOKEN_LIFETIME_EXCEEDED", "token missing iat")
		}
		if exp.Sub(iat) > v.cfg.MaxTokenLifetime {
			return fail("TOKEN_LIFETIME_EXCEEDED", "token lifetime exceeds maximum")
		}
	}
	// 7. typ header, if present, must be accepted.
	if typ, ok := tok.Headers[0].ExtraHeaders[jose.HeaderType].(string); ok {
		if !slices.Contains(v.cfg.AcceptedTokenTypes, typ) {
			return fail("TOKEN_TYPE_REJECTED", "token type not accepted")
		}
	}
	// 8. Required scopes from `scope` (string) or `scp` (array).
	scopes := tokenScopes(claims)
	for _, req := range v.cfg.RequiredScopes {
		if !slices.Contains(scopes, req) {
			return fail("TOKEN_SCOPE_INSUFFICIENT", "required scope missing")
		}
	}

	// 9. Build the principal via the configured claim names.
	cm := v.cfg.Claims
	p := Principal{
		Issuer:  v.cfg.Issuer,
		Subject: str(claims, cm.Subject),
		Email:   str(claims, cm.Email),
		Name:    str(claims, cm.Name),
		Scopes:  scopes,
		TokenID: str(claims, "jti"),
	}
	if g, ok := claims[cm.Groups].([]any); ok {
		for _, gi := range g {
			if s, ok := gi.(string); ok {
				p.Groups = append(p.Groups, s)
			}
		}
	}
	if p.Subject == "" {
		return fail("TOKEN_MALFORMED", "subject claim missing")
	}
	// Service-kind heuristic: no email and the caller is its own subject
	// (client_id or azp equals sub). Overridable when richer signals land.
	if p.Email == "" &&
		(str(claims, "client_id") == p.Subject || str(claims, "azp") == p.Subject) {
		p.Kind = KindService
	} else {
		p.Kind = KindUser
	}
	return p, nil
}

type tokenHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

func parseHeader(raw string) (tokenHeader, error) {
	var h tokenHeader
	part, _, ok := strings.Cut(raw, ".")
	if !ok {
		return h, errors.New("no segment separator")
	}
	b, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return h, err
	}
	return h, nil
}

// findKey resolves kid; an absent kid matches only when the JWKS holds
// exactly one key.
func findKey(keys []jose.JSONWebKey, kid string) *jose.JSONWebKey {
	if kid == "" {
		if len(keys) == 1 {
			return &keys[0]
		}
		return nil
	}
	for i := range keys {
		if keys[i].KeyID == kid {
			return &keys[i]
		}
	}
	return nil
}

// jwkAlg returns the signature algorithm implied by the JWK: its `alg`
// member when present, else inferred from key type and curve.
func jwkAlg(k jose.JSONWebKey) jose.SignatureAlgorithm {
	if k.Algorithm != "" {
		return jose.SignatureAlgorithm(k.Algorithm)
	}
	switch key := k.Key.(type) {
	case *rsa.PublicKey:
		return jose.RS256
	case *ecdsa.PublicKey:
		bits := key.Curve.Params().BitSize
		switch {
		case bits <= 256:
			return jose.ES256
		case bits <= 384:
			return jose.ES384
		default:
			return jose.ES512
		}
	case ed25519.PublicKey:
		return jose.EdDSA
	}
	return ""
}

func str(claims map[string]any, name string) string {
	s, _ := claims[name].(string)
	return s
}

func numericDate(v any) (time.Time, bool) {
	f, ok := v.(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

// audIntersects fails closed: an empty configured audience list can
// never match. Config validation requires audiences when issuer is set.
func audIntersects(aud any, want []string) bool {
	if len(want) == 0 {
		return false
	}
	have := map[string]bool{}
	switch a := aud.(type) {
	case string:
		have[a] = true
	case []any:
		for _, e := range a {
			if s, ok := e.(string); ok {
				have[s] = true
			}
		}
	}
	for _, w := range want {
		if have[w] {
			return true
		}
	}
	return false
}

func tokenScopes(claims map[string]any) []string {
	var out []string
	if s, ok := claims["scope"].(string); ok {
		out = append(out, strings.Fields(s)...)
	}
	if arr, ok := claims["scp"].([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
