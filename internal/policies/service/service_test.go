package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/policies"
	policiesvc "github.com/Exonical/custos/internal/policies/service"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/workflowspec"
)

var errNF = apperr.New(apperr.NotFound, "NOT_FOUND", "not found")

type fakeRepo struct {
	tenant  policies.Policy
	project map[uuid.UUID]policies.Policy
	err     error
}

func (r *fakeRepo) Upsert(_ context.Context, _ tenants.Scope, p policies.Policy) error {
	if r.err != nil {
		return r.err
	}
	switch p.Scope {
	case policies.ScopeTenant:
		r.tenant = p
		r.tenant.Version++
	case policies.ScopeProject:
		if r.project == nil {
			r.project = map[uuid.UUID]policies.Policy{}
		}
		q := r.project[*p.ProjectID]
		q = p
		q.Version++
		r.project[*p.ProjectID] = q
	}
	return nil
}

func (r *fakeRepo) GetTenant(_ context.Context, _ tenants.Scope, tenantID uuid.UUID) (policies.Policy, error) {
	if r.err != nil {
		return policies.Policy{}, r.err
	}
	if r.tenant.ID == uuid.Nil || r.tenant.TenantID != tenantID {
		return policies.Policy{}, errNF
	}
	return r.tenant, nil
}

func (r *fakeRepo) GetProject(_ context.Context, _ tenants.Scope, projectID uuid.UUID) (policies.Policy, error) {
	if r.err != nil {
		return policies.Policy{}, r.err
	}
	p, ok := r.project[projectID]
	if !ok {
		return policies.Policy{}, errNF
	}
	return p, nil
}

func ctxFor(tid uuid.UUID, roles ...string) (context.Context, authn.Principal, tenants.TenantContext) {
	tc := tenants.TenantContext{Tenant: tenants.Tenant{ID: tid}}
	if roles != nil {
		tc.Membership = &tenants.Membership{TenantID: tid, Roles: roles}
	}
	return tenants.WithTenantContext(context.Background(), tc),
		authn.Principal{UserID: uuid.Must(uuid.NewV7()), Kind: authn.KindUser}, tc
}

func TestPolicyAuthz(t *testing.T) {
	svc := policiesvc.NewService(&fakeRepo{}, authz.RBAC{}, nil)
	tid := uuid.Must(uuid.NewV7())

	// tenant-admin has policy.manage.
	ctx, p, tc := ctxFor(tid, "tenant-admin")
	if _, err := svc.SetTenantPolicy(ctx, p, tc,
		admission.ResourcePolicy{MaxGPUsPerJob: 8}); err != nil {
		t.Fatalf("tenant-admin set: %v", err)
	}
	// researcher does not.
	ctx, p, tc = ctxFor(tid, "researcher")
	if _, err := svc.SetTenantPolicy(ctx, p, tc,
		admission.ResourcePolicy{}); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("researcher set: %v", err)
	}
	// project policy requires tenant-level policy.manage: a bare
	// project-admin role in ctx must not suffice (no permission anyway —
	// the point is the check is tenant-scope).
	ctx, p, tc = ctxFor(tid, "researcher")
	ctx = authz.WithProjectRoles(ctx, uuid.Must(uuid.NewV7()).String(),
		[]string{"project-admin"})
	pid := uuid.Must(uuid.NewV7())
	if _, err := svc.SetProjectPolicy(ctx, p, tc, pid,
		admission.ResourcePolicy{}); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("project-admin set: %v", err)
	}
	// policy.read principals (platform-auditor, tenant-admin) can read;
	// a bare viewer cannot.
	ctx, p, tc = ctxFor(tid, "viewer")
	if _, _, err := svc.GetTenantPolicy(ctx, p, tc); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("viewer read must be denied: %v", err)
	}
	pa := authn.Principal{UserID: uuid.Must(uuid.NewV7()),
		Kind: authn.KindUser, PlatformRoles: []string{"platform-auditor"}}
	tc = tenants.TenantContext{Tenant: tenants.Tenant{ID: tid}}
	ctx = tenants.WithTenantContext(context.Background(), tc)
	if _, _, err := svc.GetTenantPolicy(ctx, pa, tc); err != nil {
		t.Fatalf("auditor read: %v", err)
	}
	// Unset reads return zero policy.
	ctx, p, tc = ctxFor(tid, "tenant-admin")
	pol, ver, err := svc.GetProjectPolicy(ctx, p, tc, uuid.Must(uuid.NewV7()))
	if err != nil || ver != 0 || pol.MaxNodes != 0 {
		t.Fatalf("unset: %v %+v v=%d", err, pol, ver)
	}
}

func TestEffective(t *testing.T) {
	repo := &fakeRepo{}
	svc := policiesvc.NewService(repo, authz.RBAC{}, nil)
	tid := uuid.Must(uuid.NewV7())
	pid := uuid.Must(uuid.NewV7())
	ctx := context.Background()

	// Tenant only.
	if err := repo.Upsert(ctx, tenants.PlatformScope(), policies.Policy{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, Scope: policies.ScopeTenant,
		Policy: admission.ResourcePolicy{MaxGPUsPerJob: 8,
			MaxWalltime: workflowspec.Duration(2 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	eff, err := svc.Effective(ctx, tenants.PlatformScope(), tid, pid)
	if err != nil || eff.MaxGPUsPerJob != 8 {
		t.Fatalf("tenant only: %v %+v", err, eff)
	}

	// Project tightens.
	if err := repo.Upsert(ctx, tenants.PlatformScope(), policies.Policy{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, Scope: policies.ScopeProject,
		ProjectID: &pid,
		Policy:    admission.ResourcePolicy{MaxGPUsPerJob: 4},
	}); err != nil {
		t.Fatal(err)
	}
	eff, err = svc.Effective(ctx, tenants.PlatformScope(), tid, pid)
	if err != nil || eff.MaxGPUsPerJob != 4 {
		t.Fatalf("tighten: %v %+v", err, eff)
	}

	// Project cannot loosen.
	repo.project[pid] = policies.Policy{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, Scope: policies.ScopeProject,
		ProjectID: &pid, Policy: admission.ResourcePolicy{MaxGPUsPerJob: 16},
	}
	eff, err = svc.Effective(ctx, tenants.PlatformScope(), tid, pid)
	if err != nil || eff.MaxGPUsPerJob != 8 {
		t.Fatalf("loosen: %v %+v", err, eff)
	}

	// No policies -> zero (unlimited).
	repo2 := &fakeRepo{}
	svc2 := policiesvc.NewService(repo2, authz.RBAC{}, nil)
	eff, err = svc2.Effective(ctx, tenants.PlatformScope(), tid, uuid.Must(uuid.NewV7()))
	if err != nil || eff.MaxGPUsPerJob != 0 || eff.AllowedPartitions != nil {
		t.Fatalf("empty: %v %+v", err, eff)
	}

	// Repo errors propagate.
	repo3 := &fakeRepo{err: errors.New("db down")}
	svc3 := policiesvc.NewService(repo3, authz.RBAC{}, nil)
	if _, err := svc3.Effective(ctx, tenants.PlatformScope(), tid, pid); err == nil {
		t.Fatal("repo error must propagate")
	}
}
