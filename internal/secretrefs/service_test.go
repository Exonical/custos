package secretrefs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/secretrefs"
	"github.com/Exonical/custos/internal/tenants"
)

var errRead = errors.New("transient read failure")

type allowAll struct{}

func (allowAll) Check(context.Context, authn.Principal, authz.Action, authz.Resource) (authz.Decision, error) {
	return authz.Decision{Allow: true}, nil
}

type nopStore struct{ deleted int }

func (*nopStore) Put(context.Context, string, string, string, string, []byte) error { return nil }
func (s *nopStore) Delete(context.Context, string, string, string) error {
	s.deleted++
	return nil
}
func (*nopStore) EnsureTenantNamespace(context.Context, string) error { return nil }

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

func setup(t *testing.T) (*secretrefs.Service, *flakyRepo, *nopStore, authn.Principal, tenants.TenantContext) {
	t.Helper()
	repo := &flakyRepo{connectors: map[string]secretrefs.Connector{}, references: map[string]secretrefs.Reference{}}
	store := &nopStore{}
	svc := secretrefs.NewService(repo, secretrefs.NewRuntime(repo, nil, nil), allowAll{}, nil, store, "custos")
	p := authn.Principal{UserID: uuid.New()}
	tc := tenants.TenantContext{Tenant: tenants.Tenant{ID: uuid.New()}}
	return svc, repo, store, p, tc
}

func TestCreateConnectorRefetchErrorPropagatesWithoutRollback(t *testing.T) {
	svc, repo, store, p, tc := setup(t)
	c, err := svc.CreateConnector(context.Background(), p, tc, secretrefs.CreateConnector{
		Name: "ext", Kind: "openbao", Config: map[string]any{}, Credential: map[string]string{"token": "[REDACTED]"},
	})
	if !errors.Is(err, errRead) {
		t.Fatalf("err = %v, want %v", err, errRead)
	}
	if c.ID != uuid.Nil {
		t.Fatalf("connector = %+v, want zero value on error", c)
	}
	if repo.deletedConnectors != 0 || store.deleted != 0 {
		t.Fatalf("rollback ran: repo deletes=%d, store deletes=%d", repo.deletedConnectors, store.deleted)
	}
}

func TestUpdateConnectorRefetchErrorPropagates(t *testing.T) {
	svc, repo, _, p, tc := setup(t)
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
	svc, repo, _, p, tc := setup(t)
	id := uuid.New()
	repo.connectors["ext"] = secretrefs.Connector{ID: id, TenantID: tc.Tenant.ID, Name: "ext", Kind: "openbao", State: "active", Config: map[string]any{"mount": "kv"}}
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
	svc, repo, _, p, tc := setup(t)
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
