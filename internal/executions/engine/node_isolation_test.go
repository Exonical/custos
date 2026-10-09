package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/nodehooks"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/tenants"
)

type nodeConfigStub struct {
	st  nodehooks.Stored
	err error
}

func (s nodeConfigStub) GetConfig(context.Context, uuid.UUID) (nodehooks.Stored, error) {
	return s.st, s.err
}

type tenantStub struct {
	tenants.Repository
	tenant tenants.Tenant
	err    error
}

func (s tenantStub) GetBySlugOrID(context.Context, tenants.Scope, string) (tenants.Tenant, error) {
	return s.tenant, s.err
}

func TestNodeIsolationLoading(t *testing.T) {
	ctx := context.Background()
	tenantID := uuid.New()
	cfg := nodehooks.Default()
	cfg.IsolationMode = nodehooks.ModeTenantExclusive
	good := Deps{
		NodeConfig: nodeConfigStub{st: nodehooks.Stored{Config: cfg}},
		Tenants:    tenantStub{tenant: tenants.Tenant{ID: tenantID, Slug: "tenant-a"}},
	}

	iso, err := good.nodeIsolation(ctx, tenants.PlatformScope(), uuid.New(), tenantID)
	if err != nil || iso.Mode != "tenant_exclusive" || iso.TenantSlug != "tenant-a" {
		t.Fatalf("iso=%+v err=%v", iso, err)
	}

	boom := errors.New("db down")
	bad := good
	bad.NodeConfig = nodeConfigStub{err: boom}
	if _, err := bad.nodeIsolation(ctx, tenants.PlatformScope(), uuid.New(), tenantID); !errors.Is(err, boom) {
		t.Fatalf("a config read error must be returned for retry, got %v", err)
	}

	missing := good
	missing.NodeConfig = nodeConfigStub{err: apperr.New(apperr.NotFound, "NOT_FOUND", "none")}
	iso, err = missing.nodeIsolation(ctx, tenants.PlatformScope(), uuid.New(), tenantID)
	if err != nil || iso.Mode != "namespace" || len(iso.Mounts) != 0 {
		t.Fatalf("not found must use defaults: %+v %v", iso, err)
	}

	noTenant := good
	noTenant.Tenants = tenantStub{err: boom}
	if _, err := noTenant.nodeIsolation(ctx, tenants.PlatformScope(), uuid.New(), tenantID); !errors.Is(err, boom) {
		t.Fatalf("tenant lookup error must be returned, got %v", err)
	}

	unwired := Deps{}
	iso, err = unwired.nodeIsolation(ctx, tenants.PlatformScope(), uuid.New(), tenantID)
	if err != nil || iso.Mode != "namespace" {
		t.Fatalf("unwired deps must default: %+v %v", iso, err)
	}
}
