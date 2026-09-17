package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/tenants"
)

var (
	tenantA = uuid.Must(uuid.NewV7())
	userX   = uuid.Must(uuid.NewV7())
)

func ctxWithRoles(tenantID uuid.UUID, roles []string) context.Context {
	tc := tenants.TenantContext{
		Tenant:     tenants.Tenant{ID: tenantID},
		Membership: &tenants.Membership{Roles: roles},
	}
	return tenants.WithTenantContext(context.Background(), tc)
}

func check(ctx context.Context, p authn.Principal, a Action, r Resource) bool {
	d, err := RBAC{}.Check(ctx, p, a, r)
	return err == nil && d.Allow
}

func TestRBACMatrix(t *testing.T) {
	res := Resource{Kind: "tenant", ID: tenantA.String(), TenantID: tenantA.String()}
	cases := []struct {
		name string
		p    authn.Principal
		ctx  context.Context
		a    Action
		r    Resource
		want bool
	}{
		{"platform-admin any", authn.Principal{UserID: userX, PlatformRoles: []string{RolePlatformAdmin}},
			context.Background(), TenantManage, res, true},
		{"platform-auditor read", authn.Principal{UserID: userX, PlatformRoles: []string{RolePlatformAuditor}},
			context.Background(), TenantRead, res, true},
		{"platform-auditor no mutate", authn.Principal{UserID: userX, PlatformRoles: []string{RolePlatformAuditor}},
			context.Background(), TenantManage, res, false},
		{"platform-auditor platform.manage denied", authn.Principal{UserID: userX, PlatformRoles: []string{RolePlatformAuditor}},
			context.Background(), PlatformManage, Resource{Kind: "platform"}, false},
		{"tenant-admin manages", authn.Principal{UserID: userX},
			ctxWithRoles(tenantA, []string{RoleTenantAdmin}), TenantManage, res, true},
		{"researcher cannot manage tenant", authn.Principal{UserID: userX},
			ctxWithRoles(tenantA, []string{RoleResearcher}), TenantManage, res, false},
		{"researcher submits job", authn.Principal{UserID: userX},
			ctxWithRoles(tenantA, []string{RoleResearcher}), JobSubmit, res, true},
		{"viewer reads tenant", authn.Principal{UserID: userX},
			ctxWithRoles(tenantA, []string{RoleViewer}), TenantRead, res, true},
		{"viewer cannot add members", authn.Principal{UserID: userX},
			ctxWithRoles(tenantA, []string{RoleViewer}), TenantMembersManage, res, false},
		{"no membership denies", authn.Principal{UserID: userX},
			context.Background(), TenantRead, res, false},
		{"membership in other tenant denies", authn.Principal{UserID: userX},
			ctxWithRoles(uuid.Must(uuid.NewV7()), []string{RoleTenantAdmin}), TenantRead, res, false},
		{"platform principal no membership (auditor path covered above)",
			authn.Principal{UserID: userX, PlatformRoles: []string{RolePlatformAdmin}},
			context.Background(), TenantRead, res, true},
		{"tenant ctx without membership denies",
			authn.Principal{UserID: userX},
			tenants.WithTenantContext(context.Background(),
				tenants.TenantContext{Tenant: tenants.Tenant{ID: tenantA}}),
			TenantRead, res, false},
	}
	for _, tc := range cases {
		if got := check(tc.ctx, tc.p, tc.a, tc.r); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestRBACProjectRoles(t *testing.T) {
	projectID := uuid.Must(uuid.NewV7())
	otherProject := uuid.Must(uuid.NewV7())
	res := Resource{Kind: "project", ID: projectID.String(),
		TenantID: tenantA.String(), ProjectID: projectID.String()}
	otherRes := Resource{Kind: "project", ID: otherProject.String(),
		TenantID: tenantA.String(), ProjectID: otherProject.String()}

	// memberCtx: tenant researcher + project roles for projectID.
	memberCtx := WithProjectRoles(
		ctxWithRoles(tenantA, []string{RoleResearcher}), projectID.String(),
		[]string{"project-admin"})
	// nilMemberCtx: project roles but no tenant membership.
	nilMemberCtx := WithProjectRoles(
		tenants.WithTenantContext(context.Background(),
			tenants.TenantContext{Tenant: tenants.Tenant{ID: tenantA}}),
		projectID.String(), []string{"project-admin"})

	cases := []struct {
		name string
		ctx  context.Context
		a    Action
		r    Resource
		want bool
	}{
		{"project-admin manages own project", memberCtx, ProjectManage, res, true},
		{"project-admin members manage", memberCtx, ProjectMembersManage, res, true},
		{"project role does not leak to other project", memberCtx, ProjectManage, otherRes, false},
		{"project role without tenant membership denies", nilMemberCtx, ProjectManage, res, false},
		{"project roles ignored when resource has no project",
			memberCtx, TenantManage,
			Resource{Kind: "tenant", ID: tenantA.String(), TenantID: tenantA.String()}, false},
		{"project-member reads project", WithProjectRoles(
			ctxWithRoles(tenantA, []string{RoleResearcher}), projectID.String(),
			[]string{"project-member"}), ProjectRead, res, true},
		{"project-member cannot manage members", WithProjectRoles(
			ctxWithRoles(tenantA, []string{RoleResearcher}), projectID.String(),
			[]string{"project-member"}), ProjectMembersManage, res, false},
		{"project-viewer reads only", WithProjectRoles(
			ctxWithRoles(tenantA, []string{RoleResearcher}), projectID.String(),
			[]string{"project-viewer"}), WorkflowCreate, res, false},
		{"tenant-admin manages without project membership",
			ctxWithRoles(tenantA, []string{RoleTenantAdmin}), ProjectManage, res, true},
	}
	p := authn.Principal{UserID: userX}
	for _, tc := range cases {
		if got := check(tc.ctx, p, tc.a, tc.r); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestSelfOwnership(t *testing.T) {
	other := uuid.Must(uuid.NewV7())
	ownRes := Resource{Kind: "job", TenantID: tenantA.String(), OwnerID: userX.String()}
	notOwn := Resource{Kind: "job", TenantID: tenantA.String(), OwnerID: other.String()}
	p := authn.Principal{UserID: userX}
	ctx := ctxWithRoles(tenantA, []string{RoleResearcher})
	if !check(ctx, p, JobReadSelf, ownRes) {
		t.Fatal("own job should be readable")
	}
	if check(ctx, p, JobReadSelf, notOwn) {
		t.Fatal("other's job must not match .self")
	}
	// tenant scope but no owner -> .self never matches.
	if check(ctx, p, JobReadSelf, Resource{Kind: "job", TenantID: tenantA.String()}) {
		t.Fatal("no owner must not match .self")
	}
}

type fakeRec struct{ events []audit.Event }

func (f *fakeRec) Record(_ context.Context, e audit.Event) error {
	f.events = append(f.events, e)
	return nil
}

type failAuthz struct{}

func (failAuthz) Check(context.Context, authn.Principal, Action, Resource) (Decision, error) {
	return Decision{}, errors.New("pdp unreachable")
}

func TestRequireAuditsDenies(t *testing.T) {
	rec := &fakeRec{}
	p := authn.Principal{UserID: userX}
	res := Resource{Kind: "tenant", ID: tenantA.String(), TenantID: tenantA.String()}

	// Deny: no roles.
	if err := Require(context.Background(), RBAC{}, p, TenantManage, res, rec); err == nil {
		t.Fatal("expected deny")
	}
	// Infra error denies too.
	if err := Require(context.Background(), failAuthz{}, p, TenantManage, res, rec); err == nil {
		t.Fatal("expected deny on error")
	}
	if len(rec.events) != 2 {
		t.Fatalf("events = %d", len(rec.events))
	}
	for _, e := range rec.events {
		if e.Action != "authz.denied" || e.Result != audit.ResultDeny {
			t.Fatalf("bad event: %+v", e)
		}
	}
	// Allow records nothing.
	ap := authn.Principal{UserID: userX, PlatformRoles: []string{RolePlatformAdmin}}
	if err := Require(context.Background(), RBAC{}, ap, TenantManage, res, rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 2 {
		t.Fatalf("allow should not audit; events = %d", len(rec.events))
	}
}
