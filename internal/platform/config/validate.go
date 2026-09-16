package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/Exonical/custos/internal/platform/apperr"
)

var appRoleRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

var allowedAlgorithms = map[string]bool{
	"RS256": true, "RS384": true, "RS512": true,
	"ES256": true, "ES384": true, "ES512": true,
	"PS256": true, "PS384": true, "PS512": true,
}

type validator struct {
	dev     bool
	details []apperr.Detail
}

func (v *validator) fail(field, reason string) {
	v.details = append(v.details, apperr.Detail{Field: field, Reason: reason})
}

func (v *validator) oneOf(field, val string, opts ...string) {
	for _, o := range opts {
		if val == o {
			return
		}
	}
	v.fail(field, "must be one of "+strings.Join(opts, "|"))
}

func (v *validator) durPos(field string, d time.Duration) {
	if d <= 0 {
		v.fail(field, "must be > 0")
	}
}

func (v *validator) listen(field, addr string) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		v.fail(field, "must be host:port: "+err.Error())
	}
}

func (v *validator) tls(prefix string, t TLS) {
	v.oneOf(prefix+".mode", t.Mode, "required", "disabled", "upstream")
	switch t.Mode {
	case "required":
		if t.CertFile == "" || t.KeyFile == "" {
			v.fail(prefix, "mode=required needs cert_file and key_file")
		}
	case "disabled":
		if !v.dev {
			v.fail(prefix+".mode", "disabled requires dev_mode")
		}
	}
	if t.RequireClientCert && t.ClientCAFile == "" {
		v.fail(prefix+".client_ca_file", "required when require_client_cert is set")
	}
}

// Validate enforces the fail-closed rules. The error is always
// *apperr.Error with Kind Invalid, code "config.invalid", and one Detail
// per offending field.
func (c Config) Validate() error {
	v := &validator{dev: c.DevMode}

	v.listen("server.listen", c.Server.Listen)
	v.tls("server.tls", c.Server.TLS)
	for _, d := range []struct {
		name string
		d    time.Duration
	}{
		{"server.read_header_timeout", c.Server.ReadHeaderTimeout},
		{"server.read_timeout", c.Server.ReadTimeout},
		{"server.write_timeout", c.Server.WriteTimeout},
		{"server.idle_timeout", c.Server.IdleTimeout},
		{"server.shutdown_timeout", c.Server.ShutdownTimeout},
		{"server.request_timeout", c.Server.RequestTimeout},
		{"database.statement_timeout", c.Database.StatementTimeout},
		{"database.connect_timeout", c.Database.ConnectTimeout},
		{"auth.oidc.jwks_cache_ttl", c.Auth.OIDC.JWKSCacheTTL},
		{"auth.oidc.jwks_refresh_min_interval", c.Auth.OIDC.JWKSRefreshMinInterval},
		{"auth.oidc.clock_skew", c.Auth.OIDC.ClockSkew},
		{"worker.poll_interval", c.Worker.PollInterval},
		{"worker.lease_duration", c.Worker.LeaseDuration},
		{"worker.heartbeat_interval", c.Worker.HeartbeatInterval},
		{"worker.shutdown_timeout", c.Worker.ShutdownTimeout},
	} {
		v.durPos(d.name, d.d)
	}
	if c.Server.MaxBodyBytes <= 0 {
		v.fail("server.max_body_bytes", "must be > 0")
	}
	for i, p := range c.Server.TrustedProxies {
		if _, err := netip.ParsePrefix(p); err != nil {
			v.fail(fmt.Sprintf("server.trusted_proxies[%d]", i), "invalid CIDR")
		}
	}
	for i, o := range c.Server.CORS.AllowedOrigins {
		v.corsOrigin(fmt.Sprintf("server.cors.allowed_origins[%d]", i), o)
	}

	if c.Metrics.Enabled {
		v.listen("metrics.listen", c.Metrics.Listen)
		v.tls("metrics.tls", c.Metrics.TLS)
	}

	for i, r := range c.Secrets.FileRoots {
		if !filepath.IsAbs(r) {
			v.fail(fmt.Sprintf("secrets.file_roots[%d]", i),
				"must be an absolute path")
		}
	}

	v.oneOf("log.level", c.Log.Level, "debug", "info", "warn", "error")
	v.oneOf("log.format", c.Log.Format, "json", "text")

	dsn := c.Database.URL.Reveal()
	if dsn == "" {
		v.fail("database.url", "required")
	} else if u, err := url.Parse(dsn); err != nil ||
		(u.Scheme != "postgres" && u.Scheme != "postgresql") {
		v.fail("database.url", "must be a postgres:// or postgresql:// URL")
	} else {
		// TLS must be configured via database.ssl_mode / database.tls so it
		// cannot be weakened by a URL param (e.g. ?sslmode=disable).
		for _, p := range []string{"sslmode", "sslrootcert", "sslcert", "sslkey"} {
			if u.Query().Has(p) {
				v.fail("database.url",
					"configure TLS via database.ssl_mode / database.tls; not in the URL")
				break
			}
		}
	}
	v.oneOf("database.ssl_mode", c.Database.SSLMode, "disable", "require", "verify-ca", "verify-full")
	if c.Database.SSLMode == "disable" && !c.DevMode {
		v.fail("database.ssl_mode", "disable requires dev_mode")
	}
	if c.Database.MaxConns <= 0 {
		v.fail("database.max_conns", "must be > 0")
	}
	if c.Database.MinConns < 0 || c.Database.MinConns > c.Database.MaxConns {
		v.fail("database.min_conns", "must be >= 0 and <= max_conns")
	}
	if (c.Database.TLS.CertFile == "") != (c.Database.TLS.KeyFile == "") {
		v.fail("database.tls.cert_file", "cert_file and key_file must be set together")
	}
	if c.Database.AppRole != "" && !appRoleRE.MatchString(c.Database.AppRole) {
		v.fail("database.app_role", "must match ^[a-z_][a-z0-9_]{0,62}$")
	}
	if !c.Database.RequireSCRAM && !c.DevMode {
		v.fail("database.require_scram", "false requires dev_mode")
	}

	o := c.Auth.OIDC
	if !c.DevMode {
		if o.Issuer == "" {
			v.fail("auth.oidc.issuer", "required outside dev_mode")
		}
		if o.ClientID == "" {
			v.fail("auth.oidc.client_id", "required outside dev_mode")
		}
	}
	if o.Issuer != "" && len(o.Audiences) == 0 {
		v.fail("auth.oidc.audiences", "required when auth.oidc.issuer is set")
	}
	if o.Issuer != "" {
		u, err := url.Parse(o.Issuer)
		switch {
		case err != nil || u.Host == "":
			v.fail("auth.oidc.issuer", "invalid URL")
		case u.Scheme != "https" && !c.DevMode:
			v.fail("auth.oidc.issuer", "must be https outside dev_mode")
		case u.RawQuery != "" || u.Fragment != "":
			v.fail("auth.oidc.issuer", "must not contain query or fragment")
		}
	}
	if len(o.AllowedAlgorithms) == 0 {
		v.fail("auth.oidc.allowed_algorithms", "must be non-empty")
	}
	for i, a := range o.AllowedAlgorithms {
		if !allowedAlgorithms[a] {
			v.fail(fmt.Sprintf("auth.oidc.allowed_algorithms[%d]", i), "unsupported algorithm")
		}
	}
	if !o.Discovery && o.JWKSURI == "" {
		v.fail("auth.oidc.jwks_uri", "required when discovery=false")
	}
	if o.JWKSURI != "" {
		u, err := url.Parse(o.JWKSURI)
		switch {
		case err != nil || u.Host == "":
			v.fail("auth.oidc.jwks_uri", "invalid URL")
		case u.Scheme != "https" && !c.DevMode:
			v.fail("auth.oidc.jwks_uri", "must be https outside dev_mode")
		}
	}
	if o.MaxTokenLifetime < 0 {
		v.fail("auth.oidc.max_token_lifetime", "must be >= 0")
	}
	if o.Claims.Subject == "" {
		v.fail("auth.oidc.claims.subject", "required")
	}

	v.oneOf("telemetry.exporter", c.Telemetry.Exporter, "none", "otlp")
	if c.Telemetry.Exporter == "otlp" && c.Telemetry.Endpoint == "" {
		v.fail("telemetry.endpoint", "required when exporter=otlp")
	}
	if c.Telemetry.SampleRatio < 0 || c.Telemetry.SampleRatio > 1 {
		v.fail("telemetry.sample_ratio", "must be in [0,1]")
	}
	if c.Telemetry.Insecure && !c.DevMode {
		v.fail("telemetry.insecure", "plaintext OTLP is allowed only with dev_mode")
	}

	if c.Worker.DefaultConcurrency <= 0 {
		v.fail("worker.default_concurrency", "must be > 0")
	}
	for name, k := range c.Worker.Kinds {
		p := "worker.kinds." + name
		if k.Concurrency <= 0 {
			v.fail(p+".concurrency", "must be > 0")
		}
		if k.MaxAttempts <= 0 {
			v.fail(p+".max_attempts", "must be > 0")
		}
		v.durPos(p+".base_backoff", k.BaseBackoff)
		v.durPos(p+".max_backoff", k.MaxBackoff)
	}

	if len(v.details) == 0 {
		return nil
	}
	return &apperr.Error{
		Kind:    apperr.Invalid,
		Code:    "config.invalid",
		Message: "invalid configuration",
		Details: v.details,
	}
}

func (v *validator) corsOrigin(field, raw string) {
	if raw == "*" {
		v.fail(field, "wildcard origin not allowed")
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		v.fail(field, "must be an absolute origin")
		return
	}
	if u.Scheme == "http" && !v.dev {
		v.fail(field, "http origins require dev_mode")
		return
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !v.dev) {
		v.fail(field, "origin must be https")
	}
}

// Redacted returns the effective config as a yaml-tag-keyed nested map
// with every Secret replaced by "[REDACTED]" — safe to log at startup.
func (c Config) Redacted() map[string]any {
	return redactValue(reflect.ValueOf(c)).(map[string]any)
}

func redactValue(v reflect.Value) any {
	t := v.Type()
	if t == secretType {
		return "[REDACTED]"
	}
	if t == durationType {
		return time.Duration(v.Int()).String()
	}
	switch v.Kind() {
	case reflect.Struct:
		out := make(map[string]any, t.NumField())
		for i := range t.NumField() {
			name, ok := yamlTag(t.Field(i))
			if !ok {
				continue
			}
			out[name] = redactValue(v.Field(i))
		}
		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		for _, k := range v.MapKeys() {
			out[k.String()] = redactValue(v.MapIndex(k))
		}
		return out
	case reflect.Slice:
		out := make([]any, 0, v.Len())
		for i := range v.Len() {
			out = append(out, redactValue(v.Index(i)))
		}
		return out
	default:
		return v.Interface()
	}
}
