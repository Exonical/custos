package secretrefs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/metric"

	"github.com/Exonical/custos/internal/platform/apperr"
	platformconfig "github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/safehttp"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/secrets/openbao"
	"github.com/Exonical/custos/internal/tenants"
)

// ConnectorFactory opens platform and BYO OpenBao connectors.
type ConnectorFactory struct {
	Platform *openbao.Provider
	Policy   safehttp.DialPolicy
	Logger   *slog.Logger
	Meter    metric.MeterProvider
}

// Open implements secrets.ConnectorFactory.
func (f *ConnectorFactory) Open(ctx context.Context, s secrets.ConnectorSpec, cred func(context.Context) (secrets.Value, error)) (secrets.Connector, error) {
	if s.Kind == "platform-openbao" {
		if f.Platform == nil {
			return nil, apperr.New(apperr.Validation, "PLATFORM_SECRETS_REQUIRED", "platform OpenBao is not configured")
		}
		return borrowedConnector{Connector: f.Platform}, nil
	}
	if s.Kind != "openbao" {
		return nil, apperr.New(apperr.Validation, "CONNECTOR_KIND_INVALID", "unsupported connector kind")
	}
	var cfg struct {
		Address   string `json:"address"`
		CAPEM     string `json:"ca_pem"`
		Namespace string `json:"namespace"`
		Mount     string `json:"mount"`
		Auth      struct {
			Method      string `json:"method"`
			RoleID      string `json:"role_id"`
			Role        string `json:"role"`
			JWTAudience string `json:"jwt_audience"`
		} `json:"auth"`
	}
	b, _ := json.Marshal(s.Config)
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, apperr.New(apperr.Validation, "CONNECTOR_CONFIG_INVALID", "invalid connector config")
	}
	if cfg.Auth.Method != "token" && cfg.Auth.Method != "approle" && cfg.Auth.Method != "jwt" {
		return nil, apperr.New(apperr.Validation, "CONNECTOR_AUTH_UNSUPPORTED", "connector auth method must be token, approle, or jwt")
	}
	hc, err := safehttp.New(cfg.Address, []byte(cfg.CAPEM), f.Policy)
	if err != nil {
		return nil, err
	}
	v, err := cred(ctx)
	if err != nil {
		return nil, err
	}
	defer v.Wipe()
	var credential map[string]string
	if err := json.Unmarshal(v.Reveal(), &credential); err != nil {
		return nil, apperr.New(apperr.Validation, "CONNECTOR_CREDENTIAL_INVALID", "connector credential is invalid")
	}
	metrics := openbao.NewMetrics(f.Meter, "openbao")
	switch cfg.Auth.Method {
	case "token":
		if credential["token"] == "" {
			return nil, apperr.New(apperr.Validation, "CONNECTOR_CREDENTIAL_INVALID", "connector token is required")
		}
		return openbao.NewTokenConnector(cfg.Address, cfg.Namespace, 10*time.Second, hc, credential["token"], f.Logger, metrics)
	case "approle":
		return openbao.NewAuthenticatedConnector(ctx, openbao.Config{Address: cfg.Address,
			Namespace: cfg.Namespace, Timeout: 10 * time.Second,
			Auth: openbao.AuthConfig{Method: "approle", AppRole: openbao.AppRoleAuth{
				RoleID: cfg.Auth.RoleID, SecretID: platformconfig.Secret(credential["secret_id"])}},
		}, hc, f.Logger, metrics)
	default:
		return openbao.NewAuthenticatedConnector(ctx, openbao.Config{Address: cfg.Address,
			Namespace: cfg.Namespace, Timeout: 10 * time.Second,
			Auth: openbao.AuthConfig{Method: "jwt", JWT: openbao.JWTAuth{
				Role: cfg.Auth.Role, Token: platformconfig.Secret(credential["jwt"])}},
		}, hc, f.Logger, metrics)
	}
}

type borrowedConnector struct{ secrets.Connector }

func (borrowedConnector) Close() error { return nil }

// Runtime resolves references through a connector cache keyed by id+version.
type Runtime struct {
	repo     Repository
	factory  secrets.ConnectorFactory
	platform secrets.Resolver
	mu       sync.Mutex
	cache    map[uuid.UUID]cached
}
type cached struct {
	version int64
	c       secrets.Connector
}

// NewRuntime returns a connector-caching tenant resolver.
func NewRuntime(repo Repository, f secrets.ConnectorFactory, platform secrets.Resolver) *Runtime {
	return &Runtime{repo: repo, factory: f, platform: platform, cache: map[uuid.UUID]cached{}}
}

//nolint:revive // Methods are the runtime resolver's public lifecycle API.
func (r *Runtime) Invalidate(id uuid.UUID) {
	r.mu.Lock()
	if c, ok := r.cache[id]; ok {
		_ = c.c.Close()
		delete(r.cache, id)
	}
	r.mu.Unlock()
}

//nolint:revive // Methods are the runtime resolver's public lifecycle API.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var err error
	for id, c := range r.cache {
		if e := c.c.Close(); e != nil {
			err = e
		}
		delete(r.cache, id)
	}
	return err
}

// CheckCandidate validates connectivity/authentication without caching it.
func (r *Runtime) CheckCandidate(ctx context.Context, c Connector, credential *secrets.Value) error {
	spec := secrets.ConnectorSpec{ID: c.ID, TenantID: c.TenantID, Kind: c.Kind,
		Version: c.Version, Config: c.Config, CredentialRef: c.CredentialRef}
	conn, err := r.factory.Open(ctx, spec, func(ctx context.Context) (secrets.Value, error) {
		if credential != nil {
			return secrets.NewValue(credential.Reveal()), nil
		}
		if c.CredentialRef == nil {
			return secrets.Value{}, apperr.New(apperr.Validation,
				"CONNECTOR_CREDENTIAL_REQUIRED", "connector credential is required")
		}
		return r.platform.Resolve(ctx, *c.CredentialRef)
	})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.Check(ctx)
}

//nolint:revive // Methods are the runtime resolver's public lifecycle API.
func (r *Runtime) Resolve(ctx context.Context, tenantID uuid.UUID, ref Reference) (secrets.Value, error) {
	c, err := r.repo.GetConnector(ctx, tenants.TenantScope(tenantID), tenantID, ref.ConnectorID.String())
	if err != nil {
		return secrets.Value{}, err
	}
	if c.State != "active" {
		return secrets.Value{}, apperr.New(apperr.Validation, "CONNECTOR_DISABLED", "secret connector is disabled")
	}
	conn, err := r.connector(ctx, c)
	if err != nil {
		return secrets.Value{}, err
	}
	return conn.Resolve(ctx, ref.SecretReference())
}

//nolint:revive // Methods are the runtime resolver's public lifecycle API.
func (r *Runtime) connector(ctx context.Context, c Connector) (secrets.Connector, error) {
	r.mu.Lock()
	if x, ok := r.cache[c.ID]; ok && x.version == c.Version {
		r.mu.Unlock()
		return x.c, nil
	}
	r.mu.Unlock()
	spec := secrets.ConnectorSpec{ID: c.ID, TenantID: c.TenantID, Kind: c.Kind, Version: c.Version, Config: c.Config, CredentialRef: c.CredentialRef}
	conn, err := r.factory.Open(ctx, spec, func(ctx context.Context) (secrets.Value, error) {
		if c.CredentialRef == nil {
			return secrets.Value{}, fmt.Errorf("connector has no credential reference")
		}
		return r.platform.Resolve(ctx, *c.CredentialRef)
	})
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if old, ok := r.cache[c.ID]; ok {
		_ = old.c.Close()
	}
	r.cache[c.ID] = cached{version: c.Version, c: conn}
	r.mu.Unlock()
	return conn, nil
}
