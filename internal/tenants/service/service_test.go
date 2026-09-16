package service_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
	tenantsvc "github.com/Exonical/custos/internal/tenants/service"
	"github.com/Exonical/custos/internal/users"
	userpg "github.com/Exonical/custos/internal/users/postgres"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type fakeRec struct{ events []audit.Event }

func (f *fakeRec) Record(_ context.Context, e audit.Event) error {
	f.events = append(f.events, e)
	return nil
}

type fixture struct {
	repo  tenants.Repository
	users users.Repository
	rec   *fakeRec
	svc   *tenantsvc.Service
	ctx   context.Context
}

func newFixture(t *testing.T) *fixture {
	pool := dbtest.Pool(t)
	repo := tenantpg.New(pool)
	urepo := userpg.New(pool)
	rec := &fakeRec{}
	return &fixture{
		repo: repo, users: urepo, rec: rec,
		svc: tenantsvc.NewService(repo, repo, repo, urepo, authz.RBAC{}, rec),
		ctx: context.Background(),
	}
}

func (f *fixture) mkUser(t *testing.T, sub string, roles ...string) users.User {
	u, _, err := f.users.UpsertByIdentity(f.ctx, users.User{
		Issuer: "iss", Subject: sub, Kind: authn.KindUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		if err := f.users.GrantPlatformRole(f.ctx, u.ID, r, nil); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

func (f *fixture) mkTenant(t *testing.T, slug string) tenants.Tenant {
	tn := tenants.Tenant{
		ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: "T",
		State: tenants.StateActive, Settings: map[string]any{}, Version: 1,
	}
	if err := f.repo.Create(f.ctx, tn); err != nil {
		t.Fatal(err)
	}
	return tn
}

func tcFor(_ *testing.T, tn tenants.Tenant, roles []string) tenants.TenantContext {
	tc := tenants.TenantContext{Tenant: tn}
	if roles != nil {
		tc.Membership = &tenants.Membership{
			TenantID: tn.ID, UserID: uuid.Nil, Roles: roles,
		}
	}
	return tc
}

// TestAuthorizationMatrix covers every service method against the
// principal kinds the tenancy doc requires.
func TestAuthorizationMatrix(t *testing.T) {
	f := newFixture(t)
	tn := f.mkTenant(t, "matrix")

	mkP := func(platformRoles ...string) authn.Principal {
		return authn.Principal{
			UserID: uuid.Must(uuid.NewV7()), Kind: authn.KindUser,
			PlatformRoles: platformRoles,
		}
	}
	member := func(roles []string) tenants.TenantContext {
		return tcFor(t, tn, roles)
	}

	cases := []struct {
		name string
		run  func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error
		// per caller kind: admin, auditor, tenant-admin, operator,
		// researcher, viewer, non-member
		want [7]bool
	}{
		{"Create", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			_, err := f.svc.Create(ctx, p, tenantsvc.CreateTenant{Slug: "x" + uuid.NewString()[:8], Name: "x"})
			return err
		}, [7]bool{true, false, false, false, false, false, false}},
		{"List", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			_, _, err := f.svc.List(ctx, p, tenants.Page{})
			return err
		}, [7]bool{true, true, false, false, false, false, false}},
		{"Get", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, err := f.svc.Get(ctx, p, tc)
			return err
		}, [7]bool{true, true, true, true, true, true, false}},
		{"ListMembers", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, _, err := f.svc.ListMembers(ctx, p, tc, tenants.Page{})
			return err
		}, [7]bool{true, true, true, true, true, true, false}},
		{"AddMember", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, err := f.svc.AddMember(ctx, p, tc, tenantsvc.AddMember{UserID: uuid.Must(uuid.NewV7()), Roles: []string{"viewer"}})
			return err
		}, [7]bool{true, false, true, false, false, false, false}},
		{"RemoveMember", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			return f.svc.RemoveMember(ctx, p, tc, uuid.Must(uuid.NewV7()))
		}, [7]bool{true, false, true, false, false, false, false}},
		{"Update", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			name := "n"
			_, err := f.svc.Update(ctx, p, tc, tenantsvc.UpdateTenant{Name: &name, Version: tc.Tenant.Version})
			return err
		}, [7]bool{true, false, true, false, false, false, false}},
		{"CreateGroup", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, err := f.svc.CreateGroup(ctx, p, tc, tenantsvc.CreateGroup{Name: "g" + uuid.NewString()[:8]})
			return err
		}, [7]bool{true, false, true, false, false, false, false}},
		{"ListGroups", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, _, err := f.svc.ListGroups(ctx, p, tc, tenants.Page{})
			return err
		}, [7]bool{true, true, true, true, true, true, false}},
		{"UpdateGroup", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, err := f.svc.UpdateGroup(ctx, p, tc, "nogroup", tenantsvc.UpdateGroup{Version: 1})
			return err
		}, [7]bool{true, false, true, false, false, false, false}},
		{"DeleteGroup", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			return f.svc.DeleteGroup(ctx, p, tc, "nogroup")
		}, [7]bool{true, false, true, false, false, false, false}},
		{"ListGroupMembers", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, _, err := f.svc.ListGroupMembers(ctx, p, tc, "nogroup", tenants.Page{})
			return err
		}, [7]bool{true, true, true, true, true, true, false}},
		{"AddGroupMember", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			return f.svc.AddGroupMember(ctx, p, tc, "nogroup", uuid.Must(uuid.NewV7()))
		}, [7]bool{true, false, true, false, false, false, false}},
		{"RemoveGroupMember", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			return f.svc.RemoveGroupMember(ctx, p, tc, "nogroup", uuid.Must(uuid.NewV7()))
		}, [7]bool{true, false, true, false, false, false, false}},
		{"CreateClaimRule", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, err := f.svc.CreateClaimRule(ctx, p, tc, tenantsvc.CreateClaimRule{
				Claim: "groups", MatchValue: "m", Roles: []string{"viewer"},
			})
			return err
		}, [7]bool{true, false, true, false, false, false, false}},
		{"ListClaimRules", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, _, err := f.svc.ListClaimRules(ctx, p, tc, tenants.Page{})
			return err
		}, [7]bool{true, true, true, true, true, true, false}},
		{"UpdateClaimRule", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, err := f.svc.UpdateClaimRule(ctx, p, tc, uuid.Must(uuid.NewV7()),
				tenantsvc.UpdateClaimRule{Version: 1})
			return err
		}, [7]bool{true, false, true, false, false, false, false}},
		{"DeleteClaimRule", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			return f.svc.DeleteClaimRule(ctx, p, tc, uuid.Must(uuid.NewV7()))
		}, [7]bool{true, false, true, false, false, false, false}},
		{"DeleteTenant", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			return f.svc.Delete(ctx, p, tc)
		}, [7]bool{true, false, false, false, false, false, false}},
		{"LookupUsers", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, err := f.svc.LookupUsers(ctx, p, tc, "x@y.z")
			return err
		}, [7]bool{true, false, true, false, false, false, false}},
	}

	for _, c := range cases {
		callers := []struct {
			p  authn.Principal
			tc tenants.TenantContext
		}{
			{mkP("platform-admin"), member(nil)},
			{mkP("platform-auditor"), member(nil)},
			{mkP(), member([]string{"tenant-admin"})},
			{mkP(), member([]string{"tenant-operator"})},
			{mkP(), member([]string{"researcher"})},
			{mkP(), member([]string{"viewer"})},
			{mkP(), member(nil)}, // platform ctx without membership
		}
		for i, caller := range callers {
			// The middleware puts the resolved TenantContext on ctx;
			// the authorizer reads membership from there.
			ctx := tenants.WithTenantContext(f.ctx, caller.tc)
			err := c.run(ctx, caller.p, caller.tc)
			// Anything other than a Forbidden means authorization
			// passed (Validation/NotFound/Conflict are post-authz).
			allowed := err == nil || !apperr.Is(err, apperr.Forbidden)
			if allowed != c.want[i] {
				t.Errorf("%s caller %d: allowed=%v want=%v (err=%v)", c.name, i, allowed, c.want[i], err)
			}
		}
	}
}

func TestLastAdminProtection(t *testing.T) {
	f := newFixture(t)
	tn := f.mkTenant(t, "admins")
	admin := f.mkUser(t, "admin1")
	member := f.mkUser(t, "m1")

	svc := f.svc
	p := authn.Principal{UserID: admin.ID, Kind: authn.KindUser,
		PlatformRoles: []string{"platform-admin"}}
	tc := tcFor(t, tn, nil)

	// Seed memberships directly.
	for _, m := range []tenants.Membership{
		{TenantID: tn.ID, UserID: admin.ID, Roles: []string{"tenant-admin"}, Source: tenants.SourceManual},
		{TenantID: tn.ID, UserID: member.ID, Roles: []string{"viewer"}, Source: tenants.SourceManual},
	} {
		if err := f.repo.UpsertMembership(f.ctx, tenants.PlatformScope(), m); err != nil {
			t.Fatal(err)
		}
	}

	// Removing the only tenant-admin is refused.
	if err := svc.RemoveMember(f.ctx, p, tc, admin.ID); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want LAST_ADMIN conflict, got %v", err)
	}
	// Downgrading the only tenant-admin is refused.
	if _, err := svc.UpdateMemberRoles(f.ctx, p, tc, admin.ID,
		tenantsvc.UpdateMember{Roles: []string{"viewer"}}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want LAST_ADMIN conflict, got %v", err)
	}
	// Removing the viewer is fine.
	if err := svc.RemoveMember(f.ctx, p, tc, member.ID); err != nil {
		t.Fatal(err)
	}
	// Audit events recorded for grant/revoke paths.
	var sawRevoke bool
	for _, e := range f.rec.events {
		if e.Action == "membership.revoked" {
			sawRevoke = true
		}
	}
	if !sawRevoke {
		t.Fatal("missing membership.revoked audit event")
	}
}

func TestStateGating(t *testing.T) {
	f := newFixture(t)
	tn := f.mkTenant(t, "stately")
	admin := f.mkUser(t, "a")
	p := authn.Principal{UserID: admin.ID, Kind: authn.KindUser,
		PlatformRoles: []string{"platform-admin"}}
	tc := tcFor(t, tn, nil)

	// active -> suspended allowed.
	s := tenants.StateSuspended
	updated, err := f.svc.Update(f.ctx, p, tc,
		tenantsvc.UpdateTenant{State: &s, Version: tn.Version})
	if err != nil || updated.State != tenants.StateSuspended {
		t.Fatalf("suspend: %v %+v", err, updated)
	}
	// suspended -> deleting is not allowed via Update.
	d := tenants.StateDeleting
	tc.Tenant.State = tenants.StateSuspended
	if _, err := f.svc.Update(f.ctx, p, tc,
		tenantsvc.UpdateTenant{State: &d, Version: updated.Version}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want TENANT_STATE conflict, got %v", err)
	}

	// Mutations on a deleting tenant are blocked; reads are not.
	tc.Tenant.State = tenants.StateDeleting
	if _, err := f.svc.AddMember(f.ctx, p, tc,
		tenantsvc.AddMember{UserID: uuid.Must(uuid.NewV7()), Roles: []string{"viewer"}}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want TENANT_STATE, got %v", err)
	}
	if _, err := f.svc.Get(f.ctx, p, tc); err != nil {
		t.Fatalf("read on deleting tenant: %v", err)
	}
}

func TestAddMemberValidations(t *testing.T) {
	f := newFixture(t)
	tn := f.mkTenant(t, "valids")
	admin := f.mkUser(t, "a")
	p := authn.Principal{UserID: admin.ID, Kind: authn.KindUser,
		PlatformRoles: []string{"platform-admin"}}
	tc := tcFor(t, tn, nil)

	// Unknown user -> 422 USER_UNKNOWN.
	if _, err := f.svc.AddMember(f.ctx, p, tc,
		tenantsvc.AddMember{UserID: uuid.Must(uuid.NewV7()), Roles: []string{"viewer"}}); !apperr.Is(err, apperr.Validation) {
		t.Fatalf("want USER_UNKNOWN, got %v", err)
	}
	// Bogus role -> 422 ROLES_INVALID.
	u := f.mkUser(t, "b")
	if _, err := f.svc.AddMember(f.ctx, p, tc,
		tenantsvc.AddMember{UserID: u.ID, Roles: []string{"superroot"}}); !apperr.Is(err, apperr.Validation) {
		t.Fatalf("want ROLES_INVALID, got %v", err)
	}
	// Happy path + membership.granted audit.
	if _, err := f.svc.AddMember(f.ctx, p, tc,
		tenantsvc.AddMember{UserID: u.ID, Roles: []string{"researcher"}}); err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, e := range f.rec.events {
		if e.Action == "membership.granted" && e.Target.ID == u.ID.String() {
			saw = true
		}
	}
	if !saw {
		t.Fatal("missing membership.granted audit event")
	}
}
