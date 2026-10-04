package secretrefs_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/secretrefs"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/tenants"
)

type allowAll struct{}

func (allowAll) Check(context.Context, authn.Principal, authz.Action, authz.Resource) (authz.Decision, error) {
	return authz.Decision{Allow: true}, nil
}

type fakeRepo struct {
	secretrefs.Repository
	connector secretrefs.Connector
	reference secretrefs.Reference
	createErr error
	deleted   bool
}

func (r *fakeRepo) CreateConnector(context.Context, tenants.Scope, secretrefs.Connector) error {
	return r.createErr
}

func (r *fakeRepo) GetConnector(context.Context, tenants.Scope, uuid.UUID, string) (secretrefs.Connector, error) {
	return r.connector, nil
}

func (r *fakeRepo) GetReference(context.Context, tenants.Scope, uuid.UUID, string) (secretrefs.Reference, error) {
	return r.reference, nil
}

func (r *fakeRepo) ConnectorReferenceCount(context.Context, tenants.Scope, uuid.UUID) (int, error) {
	return 0, nil
}

func (r *fakeRepo) DeleteConnector(context.Context, tenants.Scope, uuid.UUID, uuid.UUID) error {
	r.deleted = true
	return nil
}

type fakeStore struct {
	deleteErr error
	deletes   []string
}

func (f *fakeStore) Put(context.Context, string, string, string, string, []byte) error { return nil }

func (f *fakeStore) Delete(_ context.Context, ns, mount, path string) error {
	f.deletes = append(f.deletes, ns+"|"+mount+"|"+path)
	return f.deleteErr
}

func (f *fakeStore) EnsureTenantNamespace(context.Context, string) error { return nil }

type captureRecorder struct{ events []audit.Event }

func (c *captureRecorder) Record(_ context.Context, e audit.Event) error {
	c.events = append(c.events, e)
	return nil
}

func newService(repo *fakeRepo, store *fakeStore, rec audit.Recorder, logs *bytes.Buffer) *secretrefs.Service {
	svc := secretrefs.NewService(repo, secretrefs.NewRuntime(repo, nil, nil), allowAll{}, rec, store, "custos")
	svc.SetLogger(slog.New(slog.NewTextHandler(logs, nil)))
	return svc
}

func tenantContext() tenants.TenantContext {
	return tenants.TenantContext{Tenant: tenants.Tenant{ID: uuid.Must(uuid.NewV7())}}
}

func TestDeleteConnectorCredentialCleanupFailureIsLoggedAndAudited(t *testing.T) {
	tc := tenantContext()
	ref := secrets.Reference{Namespace: "custos/tenants/" + tc.Tenant.ID.String(), Mount: "kv", Path: "connectors/x", Key: "credential"}
	repo := &fakeRepo{connector: secretrefs.Connector{ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID, Name: "byo", Kind: "openbao", CredentialRef: &ref}}
	store := &fakeStore{deleteErr: errors.New("openbao unavailable")}
	rec := &captureRecorder{}
	var logs bytes.Buffer
	svc := newService(repo, store, rec, &logs)

	if err := svc.DeleteConnector(context.Background(), authn.Principal{UserID: uuid.Must(uuid.NewV7())}, tc, "byo"); err != nil {
		t.Fatalf("DeleteConnector() error = %v, want nil (best-effort cleanup)", err)
	}
	if !repo.deleted || len(store.deletes) != 1 {
		t.Fatalf("deleted=%v credential deletes=%v", repo.deleted, store.deletes)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "path=connectors/x") {
		t.Fatalf("missing cleanup warning in logs: %s", logs.String())
	}
	if len(rec.events) != 1 || rec.events[0].Action != "secret.connector.deleted" {
		t.Fatalf("events = %+v", rec.events)
	}
	d := rec.events[0].Details
	if d["credential_cleanup"] != "failed" || d["credential_path"] != "connectors/x" || d["credential_mount"] != "kv" {
		t.Fatalf("audit details = %v", d)
	}
}

func TestDeliverImagePullRequiresGenericAllowedReference(t *testing.T) {
	tc := tenantContext()
	refID := uuid.New()
	repo := &fakeRepo{
		connector: secretrefs.Connector{
			ID: uuid.New(), TenantID: tc.Tenant.ID, Name: "ext",
			Kind: "openbao", State: "active",
		},
		reference: secretrefs.Reference{
			ID: refID, TenantID: tc.Tenant.ID, ConnectorID: uuid.New(),
			Kind: "generic",
		},
	}
	recorder := &captureRecorder{}
	service := secretrefs.NewService(repo, secretrefs.NewRuntime(repo, nil, nil),
		allowAll{}, recorder, nil, "custos")
	_, err := service.Deliver(context.Background(), secretrefs.DeliveryRequest{
		TenantID: tc.Tenant.ID, ReferenceID: refID, Mode: "image_pull",
		JobID: uuid.New(),
	})
	var domainErr *apperr.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "SECRET_USE_NOT_ALLOWED" {
		t.Fatalf("image pull delivery error = %v, want SECRET_USE_NOT_ALLOWED", err)
	}
	if len(recorder.events) != 1 ||
		recorder.events[0].Details["purpose"] != "image_pull" ||
		recorder.events[0].Details["result"] != "error" {
		t.Fatalf("image pull access audit = %+v", recorder.events)
	}
}

func TestDeleteConnectorCredentialCleanupSuccessIsSilent(t *testing.T) {
	tc := tenantContext()
	ref := secrets.Reference{Namespace: "ns", Mount: "kv", Path: "connectors/x"}
	repo := &fakeRepo{connector: secretrefs.Connector{ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID, Name: "byo", Kind: "openbao", CredentialRef: &ref}}
	rec := &captureRecorder{}
	var logs bytes.Buffer
	svc := newService(repo, &fakeStore{}, rec, &logs)

	if err := svc.DeleteConnector(context.Background(), authn.Principal{UserID: uuid.Must(uuid.NewV7())}, tc, "byo"); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("unexpected logs: %s", logs.String())
	}
	if len(rec.events) != 1 || len(rec.events[0].Details) != 1 || rec.events[0].Details["kind"] != "openbao" {
		t.Fatalf("events = %+v", rec.events)
	}
}

func TestCreateConnectorRollbackCredentialFailureIsLogged(t *testing.T) {
	tc := tenantContext()
	createErr := errors.New("insert failed")
	repo := &fakeRepo{createErr: createErr}
	store := &fakeStore{deleteErr: errors.New("openbao unavailable")}
	var logs bytes.Buffer
	svc := newService(repo, store, nil, &logs)

	_, err := svc.CreateConnector(context.Background(), authn.Principal{UserID: uuid.Must(uuid.NewV7())}, tc,
		secretrefs.CreateConnector{Name: "byo", Kind: "openbao", Credential: map[string]string{"token": "t"}})
	if !errors.Is(err, createErr) {
		t.Fatalf("CreateConnector() error = %v, want %v", err, createErr)
	}
	if len(store.deletes) != 1 {
		t.Fatalf("credential deletes = %v", store.deletes)
	}
	if !strings.Contains(logs.String(), "secret connector credential delete failed") {
		t.Fatalf("missing rollback warning in logs: %s", logs.String())
	}
}

var errRead = errors.New("transient read failure")

// flakyRepo serves stored rows until a write happens, after which every
// read fails, simulating a transient failure on the post-write re-fetch.
type flakyRepo struct {
	secretrefs.Repository
	connectors        map[string]secretrefs.Connector
	references        map[string]secretrefs.Reference
	written           bool
	deletedConnectors int
}

func (r *flakyRepo) CreateConnector(_ context.Context, _ tenants.Scope, _ secretrefs.Connector) error {
	r.written = true
	return nil
}

func (r *flakyRepo) UpdateConnector(_ context.Context, _ tenants.Scope, _ secretrefs.Connector) error {
	r.written = true
	return nil
}

func (r *flakyRepo) DeleteConnector(context.Context, tenants.Scope, uuid.UUID, uuid.UUID) error {
	r.deletedConnectors++
	return nil
}

func (r *flakyRepo) GetConnector(_ context.Context, _ tenants.Scope, _ uuid.UUID, ref string) (secretrefs.Connector, error) {
	if r.written {
		return secretrefs.Connector{}, errRead
	}
	c, ok := r.connectors[ref]
	if !ok {
		return secretrefs.Connector{}, errRead
	}
	return c, nil
}

func (r *flakyRepo) CreateReference(_ context.Context, _ tenants.Scope, _ secretrefs.Reference) error {
	r.written = true
	return nil
}

func (r *flakyRepo) UpdateReference(_ context.Context, _ tenants.Scope, _ secretrefs.Reference) error {
	r.written = true
	return nil
}

func (r *flakyRepo) GetReference(_ context.Context, _ tenants.Scope, _ uuid.UUID, ref string) (secretrefs.Reference, error) {
	if r.written {
		return secretrefs.Reference{}, errRead
	}
	x, ok := r.references[ref]
	if !ok {
		return secretrefs.Reference{}, errRead
	}
	return x, nil
}

func setupFlaky(t *testing.T) (*secretrefs.Service, *flakyRepo, *fakeStore, authn.Principal, tenants.TenantContext) {
	t.Helper()
	repo := &flakyRepo{connectors: map[string]secretrefs.Connector{}, references: map[string]secretrefs.Reference{}}
	store := &fakeStore{}
	svc := secretrefs.NewService(repo, secretrefs.NewRuntime(repo, nil, nil), allowAll{}, nil, store, "custos")
	p := authn.Principal{UserID: uuid.New()}
	tc := tenants.TenantContext{Tenant: tenants.Tenant{ID: uuid.New()}}
	return svc, repo, store, p, tc
}

func TestCreateConnectorRefetchErrorPropagatesWithoutRollback(t *testing.T) {
	svc, repo, store, p, tc := setupFlaky(t)
	c, err := svc.CreateConnector(context.Background(), p, tc, secretrefs.CreateConnector{
		Name: "ext", Kind: "openbao", Config: secretrefs.ConnectorConfig{}, Credential: map[string]string{"token": "[REDACTED]"},
	})
	if !errors.Is(err, errRead) {
		t.Fatalf("err = %v, want %v", err, errRead)
	}
	if c.ID != uuid.Nil {
		t.Fatalf("connector = %+v, want zero value on error", c)
	}
	if repo.deletedConnectors != 0 || len(store.deletes) != 0 {
		t.Fatalf("rollback ran: repo deletes=%d, store deletes=%v", repo.deletedConnectors, store.deletes)
	}
}

func TestUpdateDefaultConnectorConfigRemainsImmutable(t *testing.T) {
	tc := tenantContext()
	repo := &fakeRepo{connector: secretrefs.Connector{ID: uuid.New(), TenantID: tc.Tenant.ID, Name: "default", Kind: "platform-openbao", State: "active"}}
	var logs bytes.Buffer
	svc := newService(repo, &fakeStore{}, nil, &logs)
	config := secretrefs.ConnectorConfig{}
	_, err := svc.UpdateConnector(context.Background(), authn.Principal{UserID: uuid.New()}, tc, "default", secretrefs.UpdateConnector{Config: &config})
	var domainErr *apperr.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "DEFAULT_CONNECTOR_IMMUTABLE" {
		t.Fatalf("UpdateConnector() error = %v, want DEFAULT_CONNECTOR_IMMUTABLE", err)
	}
}

func TestUpdateConnectorRefetchErrorPropagates(t *testing.T) {
	svc, repo, _, p, tc := setupFlaky(t)
	id := uuid.New()
	repo.connectors[id.String()] = secretrefs.Connector{ID: id, TenantID: tc.Tenant.ID, Name: "ext", Kind: "openbao", State: "active"}
	disabled := "disabled"
	c, err := svc.UpdateConnector(context.Background(), p, tc, id.String(), secretrefs.UpdateConnector{State: &disabled})
	if !errors.Is(err, errRead) {
		t.Fatalf("err = %v, want %v", err, errRead)
	}
	if c.ID != uuid.Nil {
		t.Fatalf("connector = %+v, want zero value on error", c)
	}
}

func TestCreateReferenceRefetchErrorPropagates(t *testing.T) {
	svc, repo, _, p, tc := setupFlaky(t)
	id := uuid.New()
	repo.connectors["ext"] = secretrefs.Connector{ID: id, TenantID: tc.Tenant.ID, Name: "ext", Kind: "openbao", State: "active", Config: secretrefs.ConnectorConfig{Mount: "kv"}}
	x, err := svc.CreateReference(context.Background(), p, tc, secretrefs.CreateReference{
		Name: "ref", Connector: "ext", Path: "app/db", Key: "password", Kind: "generic",
	})
	if !errors.Is(err, errRead) {
		t.Fatalf("err = %v, want %v", err, errRead)
	}
	if x.ID != uuid.Nil {
		t.Fatalf("reference = %+v, want zero value on error", x)
	}
}

func TestUpdateReferenceRefetchErrorPropagates(t *testing.T) {
	svc, repo, _, p, tc := setupFlaky(t)
	id := uuid.New()
	repo.references[id.String()] = secretrefs.Reference{ID: id, TenantID: tc.Tenant.ID, OwnerID: &p.UserID, Name: "ref", Mount: "kv", Path: "app/db", Key: "password", Kind: "generic"}
	name := "renamed"
	x, err := svc.UpdateReference(context.Background(), p, tc, id.String(), secretrefs.UpdateReference{Name: &name})
	if !errors.Is(err, errRead) {
		t.Fatalf("err = %v, want %v", err, errRead)
	}
	if x.ID != uuid.Nil {
		t.Fatalf("reference = %+v, want zero value on error", x)
	}
}
