package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/platform/apperr"
)

func noEnv(string) (string, bool) { return "", false }

func envMap(m map[string]string) LookupEnv {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// minimalValid returns a config that validates outside dev mode.
func minimalValid() map[string]string {
	return map[string]string{
		"CUSTOS_DATABASE__URL":         "postgres://u:p@db:5432/custos",
		"CUSTOS_AUTH__OIDC__ISSUER":    "https://idp.example.org/realms/hpc",
		"CUSTOS_AUTH__OIDC__CLIENT_ID": "custos-api",
		"CUSTOS_AUTH__OIDC__AUDIENCES": "custos-api",
		"CUSTOS_SERVER__TLS__MODE":     "disabled", // requires dev_mode
		"CUSTOS_METRICS__TLS__MODE":    "disabled",
		"CUSTOS_DEV_MODE":              "true",
	}
}

func TestLoadDevDefaults(t *testing.T) {
	cfg, err := Load("", envMap(map[string]string{"CUSTOS_DEV_MODE": "true",
		"CUSTOS_SERVER__TLS__MODE":  "disabled",
		"CUSTOS_METRICS__TLS__MODE": "disabled",
		"CUSTOS_DATABASE__URL":      "postgres://u:p@db/custos",
		"CUSTOS_DATABASE__SSL_MODE": "disable"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != ":8443" || cfg.Server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("defaults not applied: %+v", cfg.Server)
	}
}

func writeTemp(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "custos.yaml")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestYAMLOverrideAndUnknownKey(t *testing.T) {
	p := writeTemp(t, `
dev_mode: true
server:
  listen: "127.0.0.1:9999"
  tls: { mode: disabled }
metrics:
  tls: { mode: disabled }
database:
  url: "postgres://u:p@db/custos"
  ssl_mode: disable
worker:
  kinds:
    job.submit: { concurrency: 8, max_attempts: 5, base_backoff: 1s, max_backoff: 5m }
`)
	cfg, err := Load(p, noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:9999" {
		t.Fatalf("listen = %q", cfg.Server.Listen)
	}
	if cfg.Worker.Kinds["job.submit"].Concurrency != 8 ||
		cfg.Worker.Kinds["job.submit"].MaxBackoff != 5*time.Minute {
		t.Fatalf("kinds = %+v", cfg.Worker.Kinds)
	}

	bad := writeTemp(t, "bogus_key: 1\n")
	if _, err := Load(bad, noEnv); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	p := writeTemp(t, "dev_mode: true\nserver:\n  listen: \"file:1\"\n  tls: {mode: disabled}\n")
	// file:1 won't pass validation, proving env takes precedence when set
	cfg, err := Load(p, envMap(map[string]string{
		"CUSTOS_SERVER__LISTEN":     "127.0.0.1:8080",
		"CUSTOS_METRICS__TLS__MODE": "disabled",
		"CUSTOS_DATABASE__URL":      "postgres://u:p@db/custos",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:8080" {
		t.Fatalf("env did not override file: %q", cfg.Server.Listen)
	}
}

func TestSecretFileAndConflict(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "dburl")
	if err := os.WriteFile(secretFile, []byte("  postgres://u:p@db/custos\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := envMap(map[string]string{
		"CUSTOS_DEV_MODE":           "true",
		"CUSTOS_SERVER__TLS__MODE":  "disabled",
		"CUSTOS_METRICS__TLS__MODE": "disabled",
		"CUSTOS_DATABASE__URL_FILE": secretFile,
	})
	cfg, err := Load("", env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Database.URL.Reveal() != "postgres://u:p@db/custos" {
		t.Fatalf("secret file contents = %q", cfg.Database.URL.Reveal())
	}

	env2 := envMap(map[string]string{
		"CUSTOS_DEV_MODE":           "true",
		"CUSTOS_DATABASE__URL":      "postgres://x",
		"CUSTOS_DATABASE__URL_FILE": secretFile,
	})
	if _, err := Load("", env2); err == nil {
		t.Fatal("expected error when both var and _FILE set")
	}
}

func detailFields(err error) string {
	var e *apperr.Error
	if !errors.As(err, &e) {
		return "not-invalid"
	}
	var fields []string
	for _, d := range e.Details {
		fields = append(fields, d.Field)
	}
	return strings.Join(fields, ",")
}

func TestValidateFailClosed(t *testing.T) {
	base := minimalValid()
	tests := []struct {
		name      string
		mutate    map[string]string // env to add/override
		unset     []string
		wantField string
	}{
		{"db url required", map[string]string{"CUSTOS_DATABASE__URL": ""}, nil, "database.url"},
		{"db url scheme", map[string]string{"CUSTOS_DATABASE__URL": "mysql://x"}, nil, "database.url"},
		{"db url sslmode param", map[string]string{"CUSTOS_DATABASE__URL": "postgres://u:p@db/custos?sslmode=disable"}, nil, "database.url"},
		{"bad listen", map[string]string{"CUSTOS_SERVER__LISTEN": "nope"}, nil, "server.listen"},
		{"tls required needs cert", map[string]string{"CUSTOS_SERVER__TLS__MODE": "required"}, nil, "server.tls"},
		{"tls disabled needs dev", map[string]string{"CUSTOS_DEV_MODE": "false"}, nil, "server.tls.mode"},
		{"sslmode disable needs dev", map[string]string{"CUSTOS_DEV_MODE": "false", "CUSTOS_SERVER__TLS__MODE": "upstream", "CUSTOS_METRICS__TLS__MODE": "upstream", "CUSTOS_DATABASE__SSL_MODE": "disable"}, nil, "database.ssl_mode"},
		{"issuer https required", map[string]string{"CUSTOS_AUTH__OIDC__ISSUER": "http://idp", "CUSTOS_DEV_MODE": "false", "CUSTOS_SERVER__TLS__MODE": "upstream", "CUSTOS_METRICS__TLS__MODE": "upstream", "CUSTOS_DATABASE__SSL_MODE": "require"}, nil, "auth.oidc.issuer"},
		{"issuer query", map[string]string{"CUSTOS_AUTH__OIDC__ISSUER": "https://idp?x=1"}, nil, "auth.oidc.issuer"},
		{"audiences required", map[string]string{"CUSTOS_DEV_MODE": "false", "CUSTOS_SERVER__TLS__MODE": "upstream", "CUSTOS_METRICS__TLS__MODE": "upstream", "CUSTOS_DATABASE__SSL_MODE": "require"}, []string{"CUSTOS_AUTH__OIDC__AUDIENCES"}, "auth.oidc.audiences"},
		{"audiences required in dev when issuer set", nil, []string{"CUSTOS_AUTH__OIDC__AUDIENCES"}, "auth.oidc.audiences"},
		{"bad algorithm", map[string]string{"CUSTOS_AUTH__OIDC__ALLOWED_ALGORITHMS": "HS256"}, nil, "auth.oidc.allowed_algorithms[0]"},
		{"bad log level", map[string]string{"CUSTOS_LOG__LEVEL": "shout"}, nil, "log.level"},
		{"otlp needs endpoint", map[string]string{"CUSTOS_TELEMETRY__EXPORTER": "otlp"}, nil, "telemetry.endpoint"},
		{"sample ratio", map[string]string{"CUSTOS_TELEMETRY__SAMPLE_RATIO": "1.5"}, nil, "telemetry.sample_ratio"},
		{"bad proxy cidr", map[string]string{"CUSTOS_SERVER__TRUSTED_PROXIES": "10.0.0.0/33"}, nil, "server.trusted_proxies[0]"},
		{"cors wildcard", map[string]string{"CUSTOS_SERVER__CORS__ALLOWED_ORIGINS": "*"}, nil, "server.cors.allowed_origins[0]"},
		{"cors http needs dev", map[string]string{"CUSTOS_DEV_MODE": "false", "CUSTOS_SERVER__TLS__MODE": "upstream", "CUSTOS_METRICS__TLS__MODE": "upstream", "CUSTOS_DATABASE__SSL_MODE": "require", "CUSTOS_SERVER__CORS__ALLOWED_ORIGINS": "http://app.example"}, nil, "server.cors.allowed_origins[0]"},
		{"bad duration", map[string]string{"CUSTOS_SERVER__READ_TIMEOUT": "-1s"}, nil, "server.read_timeout"},
		{"body bytes", map[string]string{"CUSTOS_SERVER__MAX_BODY_BYTES": "0"}, nil, "server.max_body_bytes"},
		{"worker concurrency", map[string]string{"CUSTOS_WORKER__DEFAULT_CONCURRENCY": "0"}, nil, "worker.default_concurrency"},
		{"min conns > max", map[string]string{"CUSTOS_DATABASE__MIN_CONNS": "99"}, nil, "database.min_conns"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range base {
				env[k] = v
			}
			for _, k := range tt.unset {
				delete(env, k)
			}
			for k, v := range tt.mutate {
				env[k] = v
			}
			_, err := Load("", envMap(env))
			if err == nil {
				t.Fatalf("expected failure for %s", tt.wantField)
			}
			fields := detailFields(err)
			if !strings.Contains(fields, tt.wantField) {
				t.Fatalf("details %q missing %q", fields, tt.wantField)
			}
		})
	}
}

func TestRedactedNeverLeaks(t *testing.T) {
	const sentinel = "postgres://s3cr3t-user:hunter2@db/custos"
	cfg, err := Load("", envMap(map[string]string{
		"CUSTOS_DEV_MODE":                  "true",
		"CUSTOS_SERVER__TLS__MODE":         "disabled",
		"CUSTOS_METRICS__TLS__MODE":        "disabled",
		"CUSTOS_DATABASE__URL":             sentinel,
		"CUSTOS_AUTH__OIDC__CLIENT_SECRET": "topsecret-value",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for name, out := range map[string]string{
		"Redacted()": fmt.Sprint(cfg.Redacted()),
		"Sprintf %v": fmt.Sprintf("%v", cfg),
		"json":       mustJSON(t, cfg),
		"sprintf %s": cfg.Database.URL.String(),
	} {
		if strings.Contains(out, "s3cr3t-user") || strings.Contains(out, "hunter2") ||
			strings.Contains(out, "topsecret-value") {
			t.Fatalf("%s leaked secret: %s", name, out)
		}
	}
}

func TestLoadOpenBaoPointerConfig(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("workload-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, `dev_mode: true
server: {tls: {mode: disabled}}
metrics: {tls: {mode: disabled}}
database: {url: "postgres://u:p@db/custos", ssl_mode: disable}
secrets:
  openbao:
    address: "https://bao.example"
    namespace: custos
    timeout: 5s
    auth:
      method: jwt
      jwt:
        role: custos
        oidc_client_credentials:
          token_url: "https://idp.example/token"
          client_id: custos
`)
	cfg, err := Load(path, envMap(map[string]string{
		"CUSTOS_SECRETS__OPENBAO__AUTH__JWT__OIDC_CLIENT_CREDENTIALS__CLIENT_SECRET_FILE": secretFile,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Secrets.OpenBao == nil || cfg.Secrets.OpenBao.Auth.JWT.OIDCClientCredentials == nil ||
		cfg.Secrets.OpenBao.Auth.JWT.OIDCClientCredentials.ClientSecret.Reveal() != "workload-secret" {
		t.Fatal("OpenBao pointer configuration was not loaded")
	}
	if strings.Contains(fmt.Sprint(cfg.Redacted()), "workload-secret") {
		t.Fatal("OpenBao client secret leaked from redacted config")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
