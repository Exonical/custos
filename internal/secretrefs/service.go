package secretrefs

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/tenants"
)

var pathRE = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
var kinds = []string{"ssh_key", "slurm_token", "api_token", "generic", "storage_credential"}

// CredentialStore writes connector login credentials directly to OpenBao.
type CredentialStore interface {
	Put(context.Context, string, string, string, string, []byte) error
	Delete(context.Context, string, string, string) error
	EnsureTenantNamespace(context.Context, string) error
}

// Service applies authorization, validation, audit, and provider operations.
type Service struct {
	repo       Repository
	runtime    *Runtime
	az         authz.Authorizer
	audit      audit.Recorder
	platform   CredentialStore
	platformNS string
}

// NewService wires the tenant secret application service.
func NewService(repo Repository, runtime *Runtime, az authz.Authorizer, rec audit.Recorder, platform CredentialStore, platformNS string) *Service {
	return &Service{repo: repo, runtime: runtime, az: az, audit: rec, platform: platform, platformNS: strings.TrimSuffix(platformNS, "/")}
}

// CreateConnector is the connector creation input; Credential is write-only.
type CreateConnector struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Config     map[string]any    `json:"config"`
	Credential map[string]string `json:"credential,omitempty"`
}

// UpdateConnector is the optimistic connector update and rotation input.
type UpdateConnector struct {
	Name       *string           `json:"name,omitempty"`
	State      *string           `json:"state,omitempty"`
	Config     map[string]any    `json:"config,omitempty"`
	Credential map[string]string `json:"credential,omitempty"`
	Version    int64             `json:"version"`
}

// CreateReference is the tenant secret-reference creation input.
type CreateReference struct {
	Name          string     `json:"name"`
	Connector     string     `json:"connector,omitempty"`
	OwnerID       *uuid.UUID `json:"owner_id,omitempty"`
	ProjectID     *uuid.UUID `json:"project_id,omitempty"`
	Namespace     string     `json:"namespace,omitempty"`
	Mount         string     `json:"mount,omitempty"`
	Path          string     `json:"path"`
	Key           string     `json:"key"`
	SecretVersion *int       `json:"secret_version,omitempty"`
	Kind          string     `json:"kind"`
	AllowedUses   []string   `json:"allowed_uses,omitempty"`
}

// UpdateReference is the optimistic secret-reference update input.
type UpdateReference struct {
	Name          *string     `json:"name,omitempty"`
	OwnerID       **uuid.UUID `json:"owner_id,omitempty"`
	ProjectID     **uuid.UUID `json:"project_id,omitempty"`
	Path          *string     `json:"path,omitempty"`
	Key           *string     `json:"key,omitempty"`
	SecretVersion **int       `json:"secret_version,omitempty"`
	Kind          *string     `json:"kind,omitempty"`
	AllowedUses   *[]string   `json:"allowed_uses,omitempty"`
	Version       int64       `json:"version"`
}

func resource(tid uuid.UUID, owner *uuid.UUID) authz.Resource {
	r := authz.Resource{Kind: "secret-reference", TenantID: tid.String()}
	if owner != nil {
		r.OwnerID = owner.String()
	}
	return r
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) require(ctx context.Context, p authn.Principal, a authz.Action, tid uuid.UUID, owner *uuid.UUID) error {
	return authz.Require(ctx, s.az, p, a, resource(tid, owner), s.audit)
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) isManager(ctx context.Context, p authn.Principal, tid uuid.UUID) bool {
	d, e := s.az.Check(ctx, p, authz.SecretConnectorManage, resource(tid, nil))
	return e == nil && d.Allow
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) record(ctx context.Context, p authn.Principal, tid uuid.UUID, action, typ, id string, details map[string]any) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: p.UserID.String()}, TenantID: &tid, Action: action, Target: audit.Target{Type: typ, ID: id}, Result: audit.ResultAllow, Details: details})
}

// EnsureTenant creates the default connector for tenant creation hooks.
//
//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) EnsureTenant(ctx context.Context, tenantID, createdBy uuid.UUID) error {
	if s.platform == nil {
		return nil
	}
	_, err := s.EnsureDefault(ctx, tenantID, createdBy)
	return err
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) EnsureDefault(ctx context.Context, tenantID, createdBy uuid.UUID) (Connector, error) {
	if s.platform == nil {
		return Connector{}, apperr.New(apperr.Validation, "PLATFORM_SECRETS_REQUIRED", "platform OpenBao is not configured")
	}
	if c, e := s.repo.GetConnector(ctx, tenants.TenantScope(tenantID), tenantID, "default"); e == nil {
		return c, nil
	}
	if err := s.platform.EnsureTenantNamespace(ctx, tenantID.String()); err != nil {
		return Connector{}, err
	}
	c := Connector{ID: uuid.Must(uuid.NewV7()), TenantID: tenantID, Name: "default", Kind: "platform-openbao", State: "active", Config: map[string]any{}, CreatedBy: createdBy}
	if err := s.repo.CreateConnector(ctx, tenants.TenantScope(tenantID), c); err != nil {
		if existing, e := s.repo.GetConnector(ctx, tenants.TenantScope(tenantID), tenantID, "default"); e == nil {
			return existing, nil
		}
		return Connector{}, err
	}
	return s.repo.GetConnector(ctx, tenants.TenantScope(tenantID), tenantID, c.ID.String())
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) CreateConnector(ctx context.Context, p authn.Principal, tc tenants.TenantContext, in CreateConnector) (Connector, error) {
	if err := s.require(ctx, p, authz.SecretConnectorManage, tc.Tenant.ID, nil); err != nil {
		return Connector{}, err
	}
	if in.Kind != "openbao" {
		return Connector{}, apperr.New(apperr.Validation, "CONNECTOR_KIND_INVALID", "only openbao connectors may be created")
	}
	if s.platform == nil {
		return Connector{}, apperr.New(apperr.Validation, "PLATFORM_SECRETS_REQUIRED", "platform OpenBao is required for connector credentials")
	}
	c := Connector{ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID, Name: in.Name, Kind: in.Kind, State: "active", Config: in.Config, CreatedBy: p.UserID}
	ref := secrets.Reference{Provider: "openbao", Namespace: s.platformNS + "/tenants/" + tc.Tenant.ID.String(), Mount: "kv", Path: "connectors/" + c.ID.String(), Key: "credential"}
	raw, _ := json.Marshal(in.Credential)
	if len(in.Credential) == 0 {
		return Connector{}, apperr.New(apperr.Validation, "CONNECTOR_CREDENTIAL_REQUIRED", "connector credential is required")
	}
	if err := s.platform.EnsureTenantNamespace(ctx, tc.Tenant.ID.String()); err != nil {
		return Connector{}, err
	}
	if err := s.platform.Put(ctx, ref.Namespace, ref.Mount, ref.Path, ref.Key, raw); err != nil {
		return Connector{}, err
	}
	c.CredentialRef = &ref
	if err := s.repo.CreateConnector(ctx, tenants.ScopeFor(&tc), c); err != nil {
		_ = s.platform.Delete(ctx, ref.Namespace, ref.Mount, ref.Path)
		return Connector{}, err
	}
	c, _ = s.repo.GetConnector(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, c.ID.String())
	conn, checkErr := s.runtime.connector(ctx, c)
	if checkErr == nil {
		checkErr = conn.Check(ctx)
	}
	if checkErr != nil {
		s.runtime.Invalidate(c.ID)
		_ = s.repo.DeleteConnector(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, c.ID)
		_ = s.platform.Delete(ctx, ref.Namespace, ref.Mount, ref.Path)
		var ae *apperr.Error
		if errors.As(checkErr, &ae) && (ae.Kind == apperr.Forbidden || ae.Kind == apperr.Invalid) {
			return Connector{}, apperr.New(apperr.Validation, "CONNECTOR_ENDPOINT_DENIED", "connector endpoint or authentication configuration is denied")
		}
		return Connector{}, checkErr
	}
	s.record(ctx, p, tc.Tenant.ID, "secret.connector.created", "secret-connector", c.ID.String(), map[string]any{"kind": c.Kind})
	return c, nil
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) ListConnectors(ctx context.Context, p authn.Principal, tc tenants.TenantContext) ([]Connector, error) {
	if err := s.require(ctx, p, authz.SecretConnectorRead, tc.Tenant.ID, nil); err != nil {
		return nil, err
	}
	return s.repo.ListConnectors(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID)
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) GetConnector(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string) (Connector, error) {
	if err := s.require(ctx, p, authz.SecretConnectorRead, tc.Tenant.ID, nil); err != nil {
		return Connector{}, err
	}
	return s.repo.GetConnector(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) UpdateConnector(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string, in UpdateConnector) (Connector, error) {
	if err := s.require(ctx, p, authz.SecretConnectorManage, tc.Tenant.ID, nil); err != nil {
		return Connector{}, err
	}
	c, err := s.repo.GetConnector(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
	if err != nil {
		return c, err
	}
	if c.Kind == "platform-openbao" && in.Config != nil {
		return c, apperr.New(apperr.Validation, "DEFAULT_CONNECTOR_IMMUTABLE", "default connector configuration is managed by the platform")
	}
	if in.Name != nil {
		c.Name = *in.Name
	}
	if in.State != nil {
		if *in.State != "active" && *in.State != "disabled" {
			return c, apperr.New(apperr.Validation, "CONNECTOR_STATE_INVALID", "state must be active or disabled")
		}
		c.State = *in.State
	}
	if in.Config != nil {
		c.Config = in.Config
	}
	c.Version = in.Version
	var credential *secrets.Value
	var raw []byte
	if len(in.Credential) > 0 {
		raw, _ = json.Marshal(in.Credential)
		v := secrets.NewValue(raw)
		credential = &v
		defer credential.Wipe()
	}
	if c.State == "active" {
		if err := s.runtime.CheckCandidate(ctx, c, credential); err != nil {
			var ae *apperr.Error
			if errors.As(err, &ae) && (ae.Kind == apperr.Forbidden || ae.Kind == apperr.Invalid) {
				return c, apperr.New(apperr.Validation, "CONNECTOR_ENDPOINT_DENIED", "connector endpoint or authentication configuration is denied")
			}
			return c, err
		}
	}
	if credential != nil {
		if err := s.platform.Put(ctx, c.CredentialRef.Namespace, c.CredentialRef.Mount, c.CredentialRef.Path, c.CredentialRef.Key, raw); err != nil {
			return c, err
		}
	}
	if err := s.repo.UpdateConnector(ctx, tenants.ScopeFor(&tc), c); err != nil {
		return c, err
	}
	s.runtime.Invalidate(c.ID)
	c, _ = s.repo.GetConnector(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, c.ID.String())
	s.record(ctx, p, tc.Tenant.ID, "secret.connector.updated", "secret-connector", c.ID.String(), map[string]any{"kind": c.Kind})
	return c, nil
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) DeleteConnector(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string) error {
	if err := s.require(ctx, p, authz.SecretConnectorManage, tc.Tenant.ID, nil); err != nil {
		return err
	}
	c, err := s.repo.GetConnector(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
	if err != nil {
		return err
	}
	if c.Name == "default" {
		return apperr.New(apperr.Conflict, "DEFAULT_CONNECTOR_REQUIRED", "default connector cannot be deleted")
	}
	n, err := s.repo.ConnectorReferenceCount(ctx, tenants.ScopeFor(&tc), c.ID)
	if err != nil {
		return err
	}
	if n > 0 {
		return apperr.New(apperr.Conflict, "CONNECTOR_IN_USE", "connector has secret references")
	}
	if err := s.repo.DeleteConnector(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, c.ID); err != nil {
		return err
	}
	s.runtime.Invalidate(c.ID)
	if c.CredentialRef != nil {
		_ = s.platform.Delete(ctx, c.CredentialRef.Namespace, c.CredentialRef.Mount, c.CredentialRef.Path)
	}
	s.record(ctx, p, tc.Tenant.ID, "secret.connector.deleted", "secret-connector", c.ID.String(), map[string]any{"kind": c.Kind})
	return nil
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) TestConnector(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string) (Connector, time.Duration, error) {
	if err := s.require(ctx, p, authz.SecretConnectorManage, tc.Tenant.ID, nil); err != nil {
		return Connector{}, 0, err
	}
	c, err := s.repo.GetConnector(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
	if err != nil {
		return c, 0, err
	}
	start := time.Now()
	conn, err := s.runtime.connector(ctx, c)
	if err == nil {
		err = conn.Check(ctx)
	}
	d := time.Since(start)
	result := "allow"
	if err != nil {
		result = "error"
	}
	s.record(ctx, p, tc.Tenant.ID, "secret.connector.tested", "secret-connector", c.ID.String(), map[string]any{"kind": c.Kind, "result": result})
	return c, d, err
}

func validPath(v string) bool {
	if !pathRE.MatchString(v) || strings.HasPrefix(v, "/") || strings.HasSuffix(v, "/") {
		return false
	}
	for _, p := range strings.Split(v, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return true
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) CreateReference(ctx context.Context, p authn.Principal, tc tenants.TenantContext, in CreateReference) (Reference, error) {
	if err := s.require(ctx, p, authz.SecretReferenceCreate, tc.Tenant.ID, in.OwnerID); err != nil {
		return Reference{}, err
	}
	manager := s.isManager(ctx, p, tc.Tenant.ID)
	if in.OwnerID == nil {
		in.OwnerID = &p.UserID
	} else if *in.OwnerID != p.UserID && !manager {
		return Reference{}, apperr.New(apperr.Forbidden, "OWNER_INVALID", "owner must be the caller")
	}
	if in.Namespace != "" {
		return Reference{}, apperr.New(apperr.Validation, "SECRET_NAMESPACE_INVALID", "namespace is derived by the server")
	}
	if !validPath(in.Path) || in.Key == "" {
		return Reference{}, apperr.New(apperr.Validation, "SECRET_PATH_INVALID", "invalid secret path or key")
	}
	if !slices.Contains(kinds, in.Kind) {
		return Reference{}, apperr.New(apperr.Validation, "SECRET_KIND_INVALID", "invalid secret kind")
	}
	connector := in.Connector
	if connector == "" {
		connector = "default"
	}
	c, err := s.repo.GetConnector(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, connector)
	if err != nil && connector == "default" {
		c, err = s.EnsureDefault(ctx, tc.Tenant.ID, p.UserID)
	}
	if err != nil {
		return Reference{}, err
	}
	mount := in.Mount
	namespace := ""
	if c.Kind == "platform-openbao" {
		namespace = s.platformNS + "/tenants/" + tc.Tenant.ID.String()
		mount = "kv"
	} else {
		namespace, _ = c.Config["namespace"].(string)
		if mount == "" {
			mount, _ = c.Config["mount"].(string)
		}
	}
	if mount == "" {
		return Reference{}, apperr.New(apperr.Validation, "SECRET_MOUNT_INVALID", "secret mount is required")
	}
	x := Reference{ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID, OwnerID: in.OwnerID, ProjectID: in.ProjectID, Name: in.Name, ConnectorID: c.ID, Namespace: namespace, Mount: mount, Path: in.Path, Key: in.Key, SecretVersion: in.SecretVersion, Kind: in.Kind, AllowedUses: in.AllowedUses, CreatedBy: p.UserID}
	if err := s.repo.CreateReference(ctx, tenants.ScopeFor(&tc), x); err != nil {
		return x, err
	}
	x, _ = s.repo.GetReference(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, x.ID.String())
	s.record(ctx, p, tc.Tenant.ID, "secret.reference.created", "secret-reference", x.ID.String(), nil)
	return x, nil
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) visible(ctx context.Context, p authn.Principal, tid uuid.UUID, x Reference) error {
	if err := s.require(ctx, p, authz.SecretReferenceRead, tid, x.OwnerID); err != nil {
		return err
	}
	if x.OwnerID != nil && *x.OwnerID != p.UserID && !s.isManager(ctx, p, tid) {
		return apperr.New(apperr.NotFound, "NOT_FOUND", "not found")
	}
	return nil
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) GetReference(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string) (Reference, error) {
	x, err := s.repo.GetReference(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, ref)
	if err != nil {
		return x, err
	}
	if err := s.visible(ctx, p, tc.Tenant.ID, x); err != nil {
		return x, err
	}
	return x, nil
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) ListReferences(ctx context.Context, p authn.Principal, tc tenants.TenantContext) ([]Reference, error) {
	if err := s.require(ctx, p, authz.SecretReferenceRead, tc.Tenant.ID, nil); err != nil {
		return nil, err
	}
	xs, err := s.repo.ListReferences(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID)
	if err != nil {
		return nil, err
	}
	if s.isManager(ctx, p, tc.Tenant.ID) {
		return xs, nil
	}
	out := xs[:0]
	for _, x := range xs {
		if x.OwnerID != nil && *x.OwnerID == p.UserID {
			out = append(out, x)
		}
	}
	return out, nil
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) UpdateReference(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string, in UpdateReference) (Reference, error) {
	x, err := s.GetReference(ctx, p, tc, ref)
	if err != nil {
		return x, err
	}
	if in.Name != nil {
		x.Name = *in.Name
	}
	if in.OwnerID != nil {
		if *in.OwnerID != nil && **in.OwnerID != p.UserID && !s.isManager(ctx, p, tc.Tenant.ID) {
			return x, apperr.New(apperr.Forbidden, "OWNER_INVALID", "owner must be caller")
		}
		x.OwnerID = *in.OwnerID
	}
	if in.ProjectID != nil {
		x.ProjectID = *in.ProjectID
	}
	if in.Path != nil {
		x.Path = *in.Path
	}
	if !validPath(x.Path) {
		return x, apperr.New(apperr.Validation, "SECRET_PATH_INVALID", "invalid secret path")
	}
	if in.Key != nil {
		x.Key = *in.Key
	}
	if in.SecretVersion != nil {
		x.SecretVersion = *in.SecretVersion
	}
	if in.Kind != nil {
		x.Kind = *in.Kind
	}
	if in.AllowedUses != nil {
		x.AllowedUses = *in.AllowedUses
	}
	x.Version = in.Version
	if err := s.repo.UpdateReference(ctx, tenants.ScopeFor(&tc), x); err != nil {
		return x, err
	}
	x, _ = s.repo.GetReference(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, x.ID.String())
	s.record(ctx, p, tc.Tenant.ID, "secret.reference.updated", "secret-reference", x.ID.String(), nil)
	return x, nil
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) DeleteReference(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string) error {
	x, err := s.GetReference(ctx, p, tc, ref)
	if err != nil {
		return err
	}
	if err := s.require(ctx, p, authz.SecretReferenceDelete, tc.Tenant.ID, x.OwnerID); err != nil {
		return err
	}
	if err := s.repo.DeleteReference(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, x.ID); err != nil {
		return err
	}
	s.record(ctx, p, tc.Tenant.ID, "secret.reference.deleted", "secret-reference", x.ID.String(), nil)
	return nil
}

// ReferenceExists is the workflow contextual-validation lookup hook.
//
//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) ReferenceExists(ctx context.Context, tenantID uuid.UUID, name string) bool {
	_, err := s.repo.GetReferenceByName(ctx, tenants.TenantScope(tenantID), tenantID, name)
	return err == nil
}

//nolint:revive // Public service methods mirror authorized API operations.
func (s *Service) TestReference(ctx context.Context, p authn.Principal, tc tenants.TenantContext, ref string) (Reference, time.Time, error) {
	x, err := s.GetReference(ctx, p, tc, ref)
	if err != nil {
		return x, time.Time{}, err
	}
	if err := s.require(ctx, p, authz.SecretReferenceUse, tc.Tenant.ID, x.OwnerID); err != nil {
		return x, time.Time{}, err
	}
	v, err := s.runtime.Resolve(ctx, tc.Tenant.ID, x)
	if err != nil {
		return x, time.Time{}, err
	}
	v.Wipe()
	now := time.Now().UTC()
	s.record(ctx, p, tc.Tenant.ID, "secret.accessed", "secret-reference", x.ID.String(), map[string]any{"purpose": "test"})
	return x, now, nil
}
