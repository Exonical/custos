// Package config loads, validates, and redacts Custos configuration.
// Sources: a YAML file, then CUSTOS_* environment overrides (`__` for
// nesting). Secret fields also accept a <NAME>_FILE variant. Validation
// fails closed: anything that would make the deployment insecure aborts
// startup unless dev_mode is set.
package config

import (
	"os"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Exonical/custos/internal/platform/apperr"
)

// Config is the root configuration.
type Config struct {
	DevMode    bool       `yaml:"dev_mode" doc:"Permit insecure settings for local development; never enable in production"`
	Server     Server     `yaml:"server" doc:"Public HTTP API listener"`
	Metrics    Metrics    `yaml:"metrics" doc:"Separate Prometheus metrics listener"`
	Log        Log        `yaml:"log" doc:"Structured logging"`
	Database   Database   `yaml:"database" doc:"PostgreSQL connection pool"`
	Auth       Auth       `yaml:"auth" doc:"OIDC authentication"`
	Telemetry  Telemetry  `yaml:"telemetry" doc:"OpenTelemetry export"`
	Worker     Worker     `yaml:"worker" doc:"Work-queue lease loop"`
	Validation Validation `yaml:"validation" doc:"Script-validation pipeline and ShellCheck sidecar (docs/script-validation.md)"`
	Secrets    Secrets    `yaml:"secrets" doc:"Secret-provider settings (docs/secrets.md)"`
	Slurm      Slurm      `yaml:"slurm" doc:"Slurm adapter defaults (docs/slurm.md)"`
}

// Slurm configures Slurm connectivity policy.
type Slurm struct {
	DialPolicy DialPolicy `yaml:"dial_policy" doc:"SSRF policy enforced when dialing slurmrestd"`
}

// DialPolicy controls which resolved addresses slurmrestd endpoints may
// dial (docs/slurm.md "SSRF protection").
type DialPolicy struct {
	AllowPrivate  bool     `yaml:"allow_private" doc:"Permit RFC1918/ULA slurmrestd endpoints"`
	AllowLoopback bool     `yaml:"allow_loopback" doc:"Permit loopback endpoints (dev only)"`
	AllowHTTP     bool     `yaml:"allow_http" doc:"Permit plaintext http endpoints (dev only; loopback required)"`
	DenyCIDRs     []string `yaml:"deny_cidrs" doc:"Additional CIDRs always denied"`
}

// Secrets configures secret providers.
type Secrets struct {
	FileRoots []string `yaml:"file_roots" doc:"Allow-listed absolute roots for the file secret provider"`
}

// Server configures the public HTTP listener.
type Server struct {
	Listen            string        `yaml:"listen" doc:"Listen address (host:port) for the API"`
	TLS               TLS           `yaml:"tls" doc:"TLS settings for the API listener"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout" doc:"Max time to read request headers"`
	ReadTimeout       time.Duration `yaml:"read_timeout" doc:"Max time to read the whole request"`
	WriteTimeout      time.Duration `yaml:"write_timeout" doc:"Max time to write the response"`
	IdleTimeout       time.Duration `yaml:"idle_timeout" doc:"Keep-alive idle connection timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout" doc:"Graceful drain budget on shutdown"`
	TrustedProxies    []string      `yaml:"trusted_proxies" doc:"CIDRs whose X-Forwarded-For values are trusted"`
	CORS              CORS          `yaml:"cors" doc:"Cross-origin allow-list"`
	MaxBodyBytes      int64         `yaml:"max_body_bytes" doc:"Max request body size in bytes"`
	RequestTimeout    time.Duration `yaml:"request_timeout" doc:"Per-request deadline"`
}

// CORS holds the allow-list of origins for non-browser bearer clients.
type CORS struct {
	AllowedOrigins []string `yaml:"allowed_origins" doc:"Exact-match allowed origins (no wildcards)"`
}

// TLS configures a listener's TLS mode.
type TLS struct {
	Mode              string `yaml:"mode" doc:"required (default) | disabled (dev only) | upstream"`
	CertFile          string `yaml:"cert_file" doc:"PEM certificate chain file (required when mode=required)"`
	KeyFile           string `yaml:"key_file" doc:"PEM private key file (required when mode=required)"`
	ClientCAFile      string `yaml:"client_ca_file" doc:"PEM CA file for verifying client certificates"`
	RequireClientCert bool   `yaml:"require_client_cert" doc:"Require and verify client certificates (mTLS)"`
}

// Metrics configures the separate metrics listener.
type Metrics struct {
	Enabled bool   `yaml:"enabled" doc:"Expose GET /metrics on the metrics listener"`
	Listen  string `yaml:"listen" doc:"Listen address for the metrics listener"`
	TLS     TLS    `yaml:"tls" doc:"TLS settings for the metrics listener"`
}

// Log configures slog output.
type Log struct {
	Level  string `yaml:"level" doc:"debug|info|warn|error"`
	Format string `yaml:"format" doc:"json|text"`
}

// Database configures the PostgreSQL pool.
type Database struct {
	URL              Secret        `yaml:"url" doc:"postgres:// DSN; TLS params are set via ssl_mode, never in the URL"`
	MaxConns         int32         `yaml:"max_conns" doc:"Maximum pool connections"`
	MinConns         int32         `yaml:"min_conns" doc:"Minimum pool connections kept warm"`
	StatementTimeout time.Duration `yaml:"statement_timeout" doc:"Per-statement server-side timeout"`
	ConnectTimeout   time.Duration `yaml:"connect_timeout" doc:"Connection establishment timeout"`
	SSLMode          string        `yaml:"ssl_mode" doc:"disable|require|verify-ca|verify-full (disable is dev only)"`
	TLS              DatabaseTLS   `yaml:"tls" doc:"Client-side TLS material for the database connection"`
	AppRole          string        `yaml:"app_role" doc:"Runtime role granted least-privilege DML by migrate up; empty = skip"`
	RequireSCRAM     bool          `yaml:"require_scram" doc:"Fail startup unless server password_encryption is scram-sha-256"`
}

// DatabaseTLS holds PEM paths for the database connection.
type DatabaseTLS struct {
	RootCAFile string `yaml:"root_ca_file" doc:"PEM CA file for verify-ca/verify-full (empty = system roots)"`
	CertFile   string `yaml:"cert_file" doc:"PEM client certificate (mTLS); requires key_file"`
	KeyFile    string `yaml:"key_file" doc:"PEM client key (mTLS); requires cert_file"`
}

// Auth configures OIDC authentication.
type Auth struct {
	OIDC OIDC `yaml:"oidc" doc:"OIDC relying-party settings"`
}

// OIDC holds relying-party settings. Validated here even though authn
// ships in a later milestone, so misconfiguration fails at startup.
type OIDC struct {
	Issuer                 string        `yaml:"issuer" doc:"OIDC issuer URL (https, no query/fragment)"`
	ClientID               string        `yaml:"client_id" doc:"OIDC client identifier"`
	ClientSecret           Secret        `yaml:"client_secret" doc:"OIDC client secret"`
	CAFile                 string        `yaml:"ca_file" doc:"PEM CA bundle for the OIDC issuer's TLS (empty = system roots)"`
	Audiences              []string      `yaml:"audiences" doc:"Accepted token audiences"`
	AllowedAlgorithms      []string      `yaml:"allowed_algorithms" doc:"Permitted JWS algorithms"`
	Discovery              bool          `yaml:"discovery" doc:"Fetch issuer .well-known/openid-configuration for jwks_uri"`
	JWKSURI                string        `yaml:"jwks_uri" doc:"JWKS endpoint override; required when discovery=false"`
	JWKSCacheTTL           time.Duration `yaml:"jwks_cache_ttl" doc:"Background JWKS refresh cadence"`
	JWKSRefreshMinInterval time.Duration `yaml:"jwks_refresh_min_interval" doc:"Min interval between on-demand JWKS refreshes (unknown kid)"`
	ClockSkew              time.Duration `yaml:"clock_skew" doc:"Allowed issuer clock skew"`
	RequiredScopes         []string      `yaml:"required_scopes" doc:"Scopes every access token must carry (many IdPs omit openid; default empty)"`
	AcceptedTokenTypes     []string      `yaml:"accepted_token_types" doc:"Permitted JOSE typ header values (empty string = typ absent)"`
	MaxTokenLifetime       time.Duration `yaml:"max_token_lifetime" doc:"Max exp-iat; requires iat when > 0"`
	Claims                 OIDCClaims    `yaml:"claims" doc:"Claim names mapped onto Principal fields"`
	RedirectURL            string        `yaml:"redirect_url" doc:"OIDC redirect URL for the frontend"`
}

// OIDCClaims names the JWT claims mapped onto Principal fields.
type OIDCClaims struct {
	Subject string `yaml:"subject" doc:"Claim carrying the subject (default sub)"`
	Email   string `yaml:"email" doc:"Claim carrying the email address"`
	Name    string `yaml:"name" doc:"Claim carrying the display name"`
	Groups  string `yaml:"groups" doc:"Claim carrying group memberships (string array)"`
}

// Telemetry configures OpenTelemetry export.
type Telemetry struct {
	Exporter    string  `yaml:"exporter" doc:"none|otlp"`
	Endpoint    string  `yaml:"endpoint" doc:"OTLP collector endpoint (required when exporter=otlp)"`
	Insecure    bool    `yaml:"insecure" doc:"Plaintext OTLP (dev_mode only)"`
	SampleRatio float64 `yaml:"sample_ratio" doc:"Trace sampling ratio in [0,1]"`
	ServiceName string  `yaml:"service_name" doc:"service.name resource attribute"`
}

// Worker configures the work-queue lease loop.
type Worker struct {
	PollInterval        time.Duration         `yaml:"poll_interval" doc:"Delay between empty lease polls"`
	LeaseDuration       time.Duration         `yaml:"lease_duration" doc:"Duration of a work-item lease"`
	HeartbeatInterval   time.Duration         `yaml:"heartbeat_interval" doc:"Lease heartbeat cadence"`
	ShutdownTimeout     time.Duration         `yaml:"shutdown_timeout" doc:"Drain budget for in-flight handlers"`
	DefaultConcurrency  int                   `yaml:"default_concurrency" doc:"Default per-kind handler concurrency"`
	Kinds               map[string]KindLimits `yaml:"kinds" doc:"Per-kind limits keyed by work-item kind"`
	ClusterSyncInterval time.Duration         `yaml:"cluster_sync_interval" doc:"Base interval between cluster.sync runs per cluster"`
}

// Validation configures the synchronous validation pipeline and the
// ShellCheck sidecar client.
type Validation struct {
	Shellcheck          Shellcheck `yaml:"shellcheck" doc:"ShellCheck sidecar client"`
	UnavailableSeverity string     `yaml:"unavailable_severity" doc:"Severity for CUSTOS900 when a validator is down; WARNING requires dev_mode"`
}

// Shellcheck points the pipeline at the custos-validator sidecar.
type Shellcheck struct {
	Enabled  bool          `yaml:"enabled" doc:"Enable the ShellCheck validator; false requires dev_mode"`
	Endpoint string        `yaml:"endpoint" doc:"Sidecar base URL; loopback only"`
	Timeout  time.Duration `yaml:"timeout" doc:"Per-request timeout"`
}

// KindLimits tunes one work-item kind. Zero fields inherit defaults.
type KindLimits struct {
	Concurrency int           `yaml:"concurrency" doc:"Max concurrent handlers for this kind"`
	MaxAttempts int           `yaml:"max_attempts" doc:"Operator ceiling on attempts for this kind"`
	BaseBackoff time.Duration `yaml:"base_backoff" doc:"Retry backoff base for this kind"`
	MaxBackoff  time.Duration `yaml:"max_backoff" doc:"Retry backoff cap for this kind"`
}

// Default returns the configuration before file/env overrides.
func Default() Config {
	var c Config
	c.Server.Listen = ":8443"
	c.Server.TLS.Mode = "required"
	c.Server.ReadHeaderTimeout = 5 * time.Second
	c.Server.ReadTimeout = 30 * time.Second
	c.Server.WriteTimeout = 60 * time.Second
	c.Server.IdleTimeout = 120 * time.Second
	c.Server.ShutdownTimeout = 20 * time.Second
	c.Server.MaxBodyBytes = 1 << 20
	c.Server.RequestTimeout = 30 * time.Second
	c.Metrics.Enabled = true
	c.Metrics.Listen = ":9090"
	c.Metrics.TLS.Mode = "required"
	c.Log.Level = "info"
	c.Log.Format = "json"
	c.Database.MaxConns = 16
	c.Database.MinConns = 2
	c.Database.StatementTimeout = 30 * time.Second
	c.Database.ConnectTimeout = 5 * time.Second
	c.Database.SSLMode = "verify-full"
	c.Database.RequireSCRAM = true
	c.Auth.OIDC.AllowedAlgorithms = []string{"RS256", "ES256"}
	c.Auth.OIDC.Discovery = true
	c.Auth.OIDC.JWKSCacheTTL = time.Hour
	c.Auth.OIDC.JWKSRefreshMinInterval = 30 * time.Second
	c.Auth.OIDC.AcceptedTokenTypes = []string{"at+jwt", "JWT", ""}
	c.Auth.OIDC.MaxTokenLifetime = 24 * time.Hour
	c.Auth.OIDC.Claims = OIDCClaims{
		Subject: "sub", Email: "email", Name: "name", Groups: "groups",
	}
	c.Auth.OIDC.ClockSkew = 30 * time.Second
	c.Telemetry.Exporter = "none"
	c.Telemetry.SampleRatio = 0.1
	c.Telemetry.ServiceName = "custos"
	c.Worker.PollInterval = time.Second
	c.Worker.LeaseDuration = 30 * time.Second
	c.Worker.HeartbeatInterval = 10 * time.Second
	c.Worker.ShutdownTimeout = 20 * time.Second
	c.Worker.DefaultConcurrency = 4
	c.Worker.ClusterSyncInterval = 60 * time.Second
	c.Validation.Shellcheck.Enabled = true
	c.Validation.Shellcheck.Endpoint = "http://127.0.0.1:8481"
	c.Validation.Shellcheck.Timeout = 10 * time.Second
	c.Validation.UnavailableSeverity = "ERROR"
	if runtime.GOOS == "windows" {
		c.Secrets.FileRoots = []string{`C:\custos\secrets`}
	} else {
		c.Secrets.FileRoots = []string{"/run/secrets", "/etc/custos/secrets"}
	}
	return c
}

// LookupEnv is the environment source, e.g. os.LookupEnv.
type LookupEnv func(string) (string, bool)

// Load builds the effective config: Default() overlaid with the YAML file
// at path ("" = none), then CUSTOS_* environment overrides, then
// Validate(). Unknown YAML keys are an error.
func Load(path string, lookupEnv LookupEnv) (Config, error) {
	cfg := Default()
	if path != "" {
		f, err := os.Open(path) // #nosec G304 -- path is the operator-supplied --config flag
		if err != nil {
			return cfg, apperr.Wrap(err, apperr.Invalid, "config.read", "cannot open config file")
		}
		defer func() { _ = f.Close() }()
		var doc yaml.Node
		if err := yaml.NewDecoder(f).Decode(&doc); err != nil {
			return cfg, apperr.Wrap(err, apperr.Invalid, "config.parse", "cannot parse config file")
		}
		if err := applyYAMLNode(&cfg, &doc); err != nil {
			return cfg, apperr.Wrap(err, apperr.Invalid, "config.parse", err.Error())
		}
	}
	if err := applyEnv(&cfg, lookupEnv); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}
