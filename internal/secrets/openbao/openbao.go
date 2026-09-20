// Package openbao implements the production OpenBao secret provider.
package openbao

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/secrets"
)

// Config configures an OpenBao provider.
type Config struct {
	Address   string
	CAFile    string
	Namespace string
	Auth      AuthConfig
	Timeout   time.Duration
	DevMode   bool
	External  bool
}

// AuthConfig configures workload authentication.
type AuthConfig struct {
	Method  string
	JWT     JWTAuth
	AppRole AppRoleAuth
}

// JWTAuth configures auth/jwt login.
type JWTAuth struct {
	Role                  string
	TokenFile             string
	Token                 config.Secret
	OIDCClientCredentials *OIDCClientCredentials
}

// OIDCClientCredentials obtains a workload JWT with client_credentials.
type OIDCClientCredentials struct {
	TokenURL     string
	ClientID     string
	ClientSecret config.Secret
	Scopes       []string
}

// AppRoleAuth configures development-only AppRole login.
type AppRoleAuth struct {
	RoleID       string
	SecretIDFile string
	SecretID     config.Secret
}

// Metrics holds the bounded secret resolution counter.
type Metrics struct {
	total metric.Int64Counter
	kind  string
}

// NewMetrics registers custos_secrets_resolve_total{kind,result}.
func NewMetrics(mp metric.MeterProvider, kinds ...string) *Metrics {
	kind := "openbao"
	if len(kinds) > 0 {
		kind = kinds[0]
	}
	m := &Metrics{kind: kind}
	if mp != nil {
		m.total, _ = mp.Meter("custos/secrets").Int64Counter(
			"custos_secrets_resolve_total",
			metric.WithDescription("secret resolution results"))
	}
	return m
}

func (m *Metrics) record(ctx context.Context, result string) {
	if m != nil && m.total != nil {
		m.total.Add(ctx, 1, metric.WithAttributes(
			attribute.String("kind", m.kind),
			attribute.String("result", result)))
	}
}

// Provider resolves KV v2 values and owns its short-lived parent token.
type Provider struct {
	cfg     Config
	client  *http.Client
	logger  *slog.Logger
	metrics *Metrics

	mu             sync.RWMutex
	token          string
	ttl            time.Duration
	renewable      bool
	cancel         context.CancelFunc
	done           chan struct{}
	revokeOwned    bool
	childIsolation bool
}

// New validates cfg, logs in, and starts the token renewer.
func New(ctx context.Context, cfg Config, logger *slog.Logger, metrics *Metrics) (*Provider, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile) // #nosec G304 -- operator configuration
		if err != nil {
			return nil, fmt.Errorf("openbao CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("openbao CA contains no certificates")
		}
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	return newAuthenticated(ctx, cfg, &http.Client{Transport: tr, Timeout: cfg.Timeout},
		logger, metrics, true)
}

func newAuthenticated(ctx context.Context, cfg Config, client *http.Client,
	logger *slog.Logger, metrics *Metrics, childIsolation bool) (*Provider, error) {
	p := &Provider{cfg: cfg, client: client, logger: logger, metrics: metrics,
		done: make(chan struct{}), revokeOwned: true, childIsolation: childIsolation}
	if err := p.login(ctx); err != nil {
		return nil, err
	}
	rctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.renewLoop(rctx)
	return p, nil
}

// NewAuthenticatedConnector logs a BYO connector in over an SSRF-safe client.
func NewAuthenticatedConnector(ctx context.Context, cfg Config, client *http.Client,
	logger *slog.Logger, metrics *Metrics) (*Provider, error) {
	cfg.External = true
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, apperr.New(apperr.Validation, "SECRET_CONNECTOR_INVALID", "connector HTTP client is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	client.Timeout = cfg.Timeout
	return newAuthenticated(ctx, cfg, client, logger, metrics, false)
}

// NewTokenConnector opens a BYO OpenBao connector using a caller-owned
// static token and an SSRF-safe client. Closing it never revokes that token.
func NewTokenConnector(address, namespace string, timeout time.Duration,
	client *http.Client, token string, logger *slog.Logger, metrics *Metrics) (*Provider, error) {
	if client == nil || address == "" || namespace == "" || token == "" {
		return nil, apperr.New(apperr.Validation, "SECRET_CONNECTOR_INVALID", "incomplete OpenBao connector")
	}
	if logger == nil {
		logger = slog.Default()
	}
	client.Timeout = timeout
	return &Provider{cfg: Config{Address: address, Namespace: namespace, Timeout: timeout},
		client: client, token: token, logger: logger, metrics: metrics,
		done: make(chan struct{})}, nil
}

// ValidateConfig applies the provider's fail-closed security rules.
func ValidateConfig(c Config) error {
	if c.Address == "" || c.Namespace == "" || c.Timeout <= 0 {
		return apperr.New(apperr.Invalid, "config.invalid", "openbao address, namespace, and positive timeout are required")
	}
	u, err := url.Parse(c.Address)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return apperr.New(apperr.Invalid, "config.invalid", "openbao address must be an absolute http(s) URL")
	}
	if u.Scheme != "https" && !c.DevMode {
		return apperr.New(apperr.Invalid, "config.invalid", "plaintext OpenBao requires dev_mode")
	}
	switch c.Auth.Method {
	case "jwt":
		if c.Auth.JWT.Role == "" {
			return apperr.New(apperr.Invalid, "config.invalid", "openbao JWT role is required")
		}
		sources := 0
		if c.Auth.JWT.TokenFile != "" {
			sources++
		}
		if c.Auth.JWT.Token.Reveal() != "" {
			sources++
		}
		if c.Auth.JWT.OIDCClientCredentials != nil {
			sources++
		}
		if sources != 1 {
			return apperr.New(apperr.Invalid, "config.invalid", "exactly one OpenBao JWT source is required")
		}
		oidc := c.Auth.JWT.OIDCClientCredentials != nil
		if oidc {
			o := c.Auth.JWT.OIDCClientCredentials
			if o.TokenURL == "" || o.ClientID == "" || o.ClientSecret.Reveal() == "" {
				return apperr.New(apperr.Invalid, "config.invalid", "OpenBao OIDC client credentials are incomplete")
			}
		}
	case "approle":
		if !c.DevMode && !c.External {
			return apperr.New(apperr.Invalid, "config.invalid", "platform OpenBao AppRole requires dev_mode")
		}
		secretSources := 0
		if c.Auth.AppRole.SecretIDFile != "" {
			secretSources++
		}
		if c.Auth.AppRole.SecretID.Reveal() != "" {
			secretSources++
		}
		if c.Auth.AppRole.RoleID == "" || secretSources != 1 {
			return apperr.New(apperr.Invalid, "config.invalid", "OpenBao AppRole requires role_id and exactly one secret-id source")
		}
	default:
		return apperr.New(apperr.Invalid, "config.invalid", "OpenBao auth method must be jwt or approle")
	}
	return nil
}

func (p *Provider) login(ctx context.Context) error {
	var endpoint string
	body := map[string]any{}
	switch p.cfg.Auth.Method {
	case "jwt":
		jwt, err := p.workloadJWT(ctx)
		if err != nil {
			return err
		}
		endpoint = "auth/jwt/login"
		body["role"], body["jwt"] = p.cfg.Auth.JWT.Role, jwt
	case "approle":
		secretID := p.cfg.Auth.AppRole.SecretID.Reveal()
		if p.cfg.Auth.AppRole.SecretIDFile != "" {
			b, err := os.ReadFile(p.cfg.Auth.AppRole.SecretIDFile) // #nosec G304 -- operator configuration
			if err != nil {
				return apperr.Wrap(err, apperr.Unavailable, "SECRETS_UNAVAILABLE", "OpenBao AppRole credential unavailable")
			}
			secretID = strings.TrimSpace(string(b))
		}
		endpoint = "auth/approle/login"
		body["role_id"], body["secret_id"] = p.cfg.Auth.AppRole.RoleID, secretID
	}
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
			Renewable     bool   `json:"renewable"`
		} `json:"auth"`
	}
	if err := p.request(ctx, http.MethodPost, p.cfg.Namespace, endpoint, "", body, &out); err != nil {
		return err
	}
	if out.Auth.ClientToken == "" {
		return apperr.New(apperr.Unavailable, "SECRETS_UNAVAILABLE", "OpenBao login returned no token")
	}
	p.mu.Lock()
	p.token = out.Auth.ClientToken
	p.ttl = time.Duration(out.Auth.LeaseDuration) * time.Second
	p.renewable = out.Auth.Renewable
	p.mu.Unlock()
	return nil
}

func (p *Provider) workloadJWT(ctx context.Context) (string, error) {
	j := p.cfg.Auth.JWT
	if j.Token.Reveal() != "" {
		return j.Token.Reveal(), nil
	}
	if j.TokenFile != "" {
		b, err := os.ReadFile(j.TokenFile) // #nosec G304 -- operator configuration
		if err != nil {
			return "", apperr.Wrap(err, apperr.Unavailable, "SECRETS_UNAVAILABLE", "OpenBao workload JWT unavailable")
		}
		return strings.TrimSpace(string(b)), nil
	}
	o := j.OIDCClientCredentials
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {o.ClientID},
		"client_secret": {o.ClientSecret.Reveal()}}
	if len(o.Scopes) > 0 {
		form.Set("scope", strings.Join(o.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err != nil {
		return "", apperr.Wrap(err, apperr.Unavailable, "SECRETS_UNAVAILABLE", "workload identity provider unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return "", apperr.New(apperr.Unavailable, "SECRETS_UNAVAILABLE", "workload identity login failed")
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.AccessToken == "" {
		return "", apperr.New(apperr.Unavailable, "SECRETS_UNAVAILABLE", "workload identity response invalid")
	}
	return out.AccessToken, nil
}

func (p *Provider) renewLoop(ctx context.Context) {
	defer close(p.done)
	for {
		p.mu.RLock()
		ttl, renewable := p.ttl, p.renewable
		p.mu.RUnlock()
		wait := ttl * 2 / 3
		if wait <= 0 {
			wait = time.Minute
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if renewable && p.renew(ctx) == nil {
			continue
		}
		for attempt := 0; ; attempt++ {
			if err := p.login(ctx); err == nil {
				break
			}
			backoff := time.Second << min(attempt, 5)
			var b [1]byte
			_, _ = rand.Read(b[:])
			backoff += time.Duration(b[0]) * backoff / 1024
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}
	}
}

func (p *Provider) renew(ctx context.Context) error {
	var out struct {
		Auth struct {
			LeaseDuration int  `json:"lease_duration"`
			Renewable     bool `json:"renewable"`
		} `json:"auth"`
	}
	if err := p.request(ctx, http.MethodPost, p.cfg.Namespace, "auth/token/renew-self", p.parentToken(), nil, &out); err != nil {
		return err
	}
	p.mu.Lock()
	p.ttl = time.Duration(out.Auth.LeaseDuration) * time.Second
	p.renewable = out.Auth.Renewable
	p.mu.Unlock()
	return nil
}

func (p *Provider) parentToken() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.token
}

// Resolve reads a KV v2 value. Tenant references use a one-use child token.
func (p *Provider) Resolve(ctx context.Context, ref secrets.Reference) (secrets.Value, error) {
	if err := p.validateRef(ref); err != nil {
		p.metrics.record(ctx, "invalid")
		return secrets.Value{}, err
	}
	p.logger.DebugContext(ctx, "resolving OpenBao secret",
		"namespace", ref.Namespace, "mount", ref.Mount,
		"path_segments", len(strings.Split(ref.Path, "/")))
	token := p.parentToken()
	child := false
	if p.childIsolation && ref.Namespace != p.cfg.Namespace {
		var err error
		token, err = p.childToken(ctx, ref.Namespace)
		if err != nil {
			p.metrics.record(ctx, resultOf(err))
			return secrets.Value{}, err
		}
		child = true
		defer func() {
			_ = p.request(context.Background(), http.MethodPost, ref.Namespace,
				"auth/token/revoke-self", token, nil, nil)
		}()
	}
	endpoint := path.Join(ref.Mount, "data", ref.Path)
	if ref.Version > 0 {
		endpoint += "?version=" + strconv.Itoa(ref.Version)
	}
	var out struct {
		Data struct {
			Data     map[string]any `json:"data"`
			Metadata struct {
				Version int `json:"version"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := p.request(ctx, http.MethodGet, ref.Namespace, endpoint, token, nil, &out); err != nil {
		p.metrics.record(ctx, resultOf(err))
		return secrets.Value{}, err
	}
	value, ok := out.Data.Data[ref.Key]
	if !ok {
		p.metrics.record(ctx, "not_found")
		return secrets.Value{}, apperr.New(apperr.NotFound, "secrets.not_found", "secret key not found")
	}
	var raw []byte
	if s, ok := value.(string); ok {
		raw = []byte(s)
	} else {
		raw, _ = json.Marshal(value)
	}
	p.metrics.record(ctx, "ok")
	_ = child
	return secrets.NewValue(raw), nil
}

// Address returns the configured platform address for wrapped-token delivery.
func (p *Provider) Address() string { return p.cfg.Address }

// WrapReference creates a narrowly scoped child token and returns a one-use
// response-wrapping token instead of the child token itself.
func (p *Provider) WrapReference(ctx context.Context, ref secrets.Reference,
	policyName string, ttl time.Duration) (secrets.Value, error) {
	if err := p.validateRef(ref); err != nil {
		return secrets.Value{}, err
	}
	if !p.childIsolation || ref.Namespace == p.cfg.Namespace {
		return secrets.Value{}, apperr.New(apperr.Validation,
			"WRAPPED_TOKEN_UNSUPPORTED_CONNECTOR", "wrapped tokens require a tenant platform connector")
	}
	if ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	dataPath := path.Join(ref.Mount, "data", ref.Path)
	metadataPath := path.Join(ref.Mount, "metadata", ref.Path)
	policy := fmt.Sprintf("path %q { capabilities = [\"read\"] }\npath %q { capabilities = [\"read\"] }", dataPath, metadataPath)
	if err := p.request(ctx, http.MethodPut, ref.Namespace,
		"sys/policies/acl/"+policyName, p.parentToken(),
		map[string]any{"policy": policy}, nil); err != nil {
		return secrets.Value{}, err
	}
	body := map[string]any{"policies": []string{policyName},
		"ttl": ttl.String(), "num_uses": 2, "no_parent": true}
	var out struct {
		WrapInfo struct {
			Token string `json:"token"`
		} `json:"wrap_info"`
	}
	if err := p.requestWithHeaders(ctx, http.MethodPost, ref.Namespace,
		"auth/token/create-orphan", p.parentToken(), body, &out,
		map[string]string{"X-Vault-Wrap-TTL": "1h"}); err != nil {
		return secrets.Value{}, err
	}
	if out.WrapInfo.Token == "" {
		return secrets.Value{}, apperr.New(apperr.Unavailable,
			"SECRETS_UNAVAILABLE", "OpenBao wrapping response invalid")
	}
	return secrets.NewValue([]byte(out.WrapInfo.Token)), nil
}

func (p *Provider) validateRef(ref secrets.Reference) error {
	if (ref.Provider != "" && ref.Provider != "openbao") || ref.Namespace == "" || ref.Mount == "" || ref.Path == "" || ref.Key == "" {
		return apperr.New(apperr.Validation, "SECRET_REFERENCE_INVALID", "invalid OpenBao secret reference")
	}
	if p.childIsolation && ref.Namespace != p.cfg.Namespace &&
		!strings.HasPrefix(ref.Namespace, p.cfg.Namespace+"/tenants/") {
		return apperr.New(apperr.Validation, "SECRET_NAMESPACE_INVALID", "secret namespace is outside Custos")
	}
	if strings.Contains(ref.Mount, "/") || unsafePath(ref.Path) {
		return apperr.New(apperr.Validation, "SECRET_PATH_INVALID", "invalid secret mount or path")
	}
	return nil
}

func unsafePath(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
		return true
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return true
		}
	}
	return false
}

func (p *Provider) childToken(ctx context.Context, namespace string) (string, error) {
	id := strings.TrimPrefix(namespace, p.cfg.Namespace+"/tenants/")
	if id == namespace || strings.Contains(id, "/") {
		return "", apperr.New(apperr.Validation, "SECRET_NAMESPACE_INVALID", "invalid tenant secret namespace")
	}
	body := map[string]any{"policies": []string{"tenant-" + id + "-runtime"},
		"ttl": "10m", "num_uses": 1, "no_parent": true}
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := p.request(ctx, http.MethodPost, namespace, "auth/token/create-orphan", p.parentToken(), body, &out); err != nil {
		return "", err
	}
	if out.Auth.ClientToken == "" {
		return "", apperr.New(apperr.Unavailable, "SECRETS_UNAVAILABLE", "OpenBao child token response invalid")
	}
	return out.Auth.ClientToken, nil
}

// Put stores a value in KV v2 using the platform token. It is used only
// for connector credentials, which are never persisted by Custos.
func (p *Provider) Put(ctx context.Context, namespace, mount, secretPath, key string, value []byte) error {
	if unsafePath(secretPath) || strings.Contains(mount, "/") || key == "" {
		return apperr.New(apperr.Validation, "SECRET_PATH_INVALID", "invalid secret path")
	}
	return p.request(ctx, http.MethodPost, namespace, path.Join(mount, "data", secretPath),
		p.parentToken(), map[string]any{"data": map[string]string{key: string(value)}}, nil)
}

// Delete removes all versions and metadata for a KV v2 path.
func (p *Provider) Delete(ctx context.Context, namespace, mount, secretPath string) error {
	return p.request(ctx, http.MethodDelete, namespace, path.Join(mount, "metadata", secretPath),
		p.parentToken(), nil, nil)
}

// EnsureTenantNamespace idempotently creates a tenant namespace, KV mount and runtime policy.
func (p *Provider) EnsureTenantNamespace(ctx context.Context, tenantID string) error {
	if tenantID == "" || strings.Contains(tenantID, "/") {
		return apperr.New(apperr.Validation, "SECRET_NAMESPACE_INVALID", "invalid tenant id")
	}
	// Each namespace is created relative to its parent namespace.
	err := p.request(ctx, http.MethodPost, p.cfg.Namespace, "sys/namespaces/tenants",
		p.parentToken(), map[string]any{}, nil)
	if err != nil && !isAlreadyExists(err) {
		return err
	}
	err = p.request(ctx, http.MethodPost, p.cfg.Namespace+"/tenants",
		"sys/namespaces/"+tenantID, p.parentToken(), map[string]any{}, nil)
	if err != nil && !isAlreadyExists(err) {
		return err
	}
	ns := p.cfg.Namespace + "/tenants/" + tenantID
	err = p.request(ctx, http.MethodPost, ns, "sys/mounts/kv", p.parentToken(),
		map[string]any{"type": "kv", "options": map[string]string{"version": "2"}}, nil)
	if err != nil && !isAlreadyExists(err) {
		return err
	}
	policy := `path "kv/data/*" { capabilities = ["read"] }`
	return p.request(ctx, http.MethodPut, ns, "sys/policies/acl/tenant-"+tenantID+"-runtime",
		p.parentToken(), map[string]any{"policy": policy}, nil)
}

func isAlreadyExists(err error) bool {
	var ae *apperr.Error
	return errors.As(err, &ae) && ae.Code == "secrets.already_exists"
}

// Check implements health.Checker.
func (p *Provider) Check(ctx context.Context) error {
	return p.request(ctx, http.MethodGet, "", "sys/health", "", nil, nil)
}

// Name implements health.Checker.
func (p *Provider) Name() string { return "openbao" }

// Close stops renewal and revokes the provider-owned token.
func (p *Provider) Close() error {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	var err error
	if p.revokeOwned {
		ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
		defer cancel()
		err = p.request(ctx, http.MethodPost, p.cfg.Namespace, "auth/token/revoke-self", p.parentToken(), nil, nil)
	}
	p.mu.Lock()
	p.token = ""
	p.mu.Unlock()
	return err
}

func (p *Provider) request(ctx context.Context, method, namespace, endpoint, token string, body any, out any) error {
	return p.requestWithHeaders(ctx, method, namespace, endpoint, token, body, out, nil)
}

func (p *Provider) requestWithHeaders(ctx context.Context, method, namespace,
	endpoint, token string, body, out any, headers map[string]string) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	u := strings.TrimRight(p.cfg.Address, "/") + "/v1/" + strings.TrimLeft(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Namespace", namespace)
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if apperr.Is(err, apperr.Forbidden) || apperr.Is(err, apperr.Validation) {
			return err
		}
		return apperr.Wrap(err, apperr.Unavailable, "SECRETS_UNAVAILABLE", "OpenBao unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		switch resp.StatusCode {
		case http.StatusForbidden:
			return apperr.New(apperr.Forbidden, "secrets.forbidden", "OpenBao denied secret access")
		case http.StatusNotFound:
			return apperr.New(apperr.NotFound, "secrets.not_found", "secret not found")
		case http.StatusBadRequest:
			return apperr.New(apperr.Invalid, "secrets.already_exists", "OpenBao request rejected")
		default:
			return apperr.New(apperr.Unavailable, "SECRETS_UNAVAILABLE", "OpenBao unavailable")
		}
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return apperr.Wrap(err, apperr.Unavailable, "SECRETS_UNAVAILABLE", "OpenBao response invalid")
		}
	}
	return nil
}

func resultOf(err error) string {
	switch apperr.KindOf(err) {
	case apperr.Forbidden:
		return "forbidden"
	case apperr.NotFound:
		return "not_found"
	case apperr.Validation, apperr.Invalid:
		return "invalid"
	default:
		return "unavailable"
	}
}
