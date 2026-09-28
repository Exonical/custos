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
	createErr error
	deleted   bool
}

func (r *fakeRepo) CreateConnector(context.Context, tenants.Scope, secretrefs.Connector) error {
	return r.createErr
}

func (r *fakeRepo) GetConnector(context.Context, tenants.Scope, uuid.UUID, string) (secretrefs.Connector, error) {
	return r.connector, nil
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
