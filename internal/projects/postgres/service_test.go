package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/projects"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
)

// Service-layer tests run against the real repos; they live in the
// postgres test package because they need pool access to seed users and
// tenant memberships (depguard keeps pgx out of the domain packages).

type svcFixture struct {
	pool     *pgxpool.Pool
	svc      *projectsvc.Service
	repo     *projectpg.Repository
	tenants  *tenantpg.Repository
	clusters *clusterpg.Repository
	ctx      context.Context
}

func newSvcFixture(t *testing.T) *svcFixture {
	t.Helper()
	pool := dbtest.Pool(t)
	repo := projectpg.New(pool)
	trepo := tenantpg.New(pool)
	crepo := clusterpg.New(pool)
	return &svcFixture{
		pool: pool,
		svc:  projectsvc.NewService(repo, repo, repo, trepo, crepo, authz.RBAC{}, nil),
		repo: repo, tenants: trepo, clusters: crepo, ctx: context.Background(),
	}
}

func (f *svcFixture) tenant(t *testing.T, slug string) tenants.Tenant {
	t.Helper()
	return tenants.Tenant{ID: mkTenant(t, f.pool, slug), Slug: slug,
		State: tenants.StateActive}
}

func (f *svcFixture) member(t *testing.T, tn tenants.Tenant, uid uuid.UUID, roles ...string) {
	t.Helper()
	if err := f.tenants.UpsertMembership(f.ctx, tenants.PlatformScope(),
		tenants.Membership{TenantID: tn.ID, UserID: uid,
			Roles: roles, Source: tenants.SourceManual}); err != nil {
		t.Fatal(err)
	}
}

func (f *svcFixture) project(t *testing.T, tn tenants.Tenant, slug string) projects.Project {
	t.Helper()
	p := mkProject(tn.ID, slug)
	if err := f.repo.Create(f.ctx, tenants.PlatformScope(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *svcFixture) projectMember(t *testing.T, tn tenants.Tenant, p projects.Project,
	uid uuid.UUID, roles ...string) {
	t.Helper()
	if err := f.repo.UpsertMembership(f.ctx, tenants.PlatformScope(), projects.Membership{
		TenantID: tn.ID, ProjectID: p.ID, UserID: uid,
		Roles: roles, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
}

func (f *svcFixture) assignedCluster(t *testing.T, tn tenants.Tenant, name string,
	defaults clusters.AssignmentDefaults) uuid.UUID {
	t.Helper()
	cid := mkCluster(t, f.pool, name)
	if err := f.clusters.UpsertAssignment(f.ctx, tenants.PlatformScope(), clusters.Assignment{
		ClusterID: cid, TenantID: tn.ID, Source: clusters.SourceManual,
		Defaults: defaults,
	}); err != nil {
		t.Fatal(err)
	}
	return cid
}

// req builds the (ctx, principal, tc, pc) the request pipeline produces:
// tenant context from the tenant middleware, project context + project
// roles from the project middleware.
func (f *svcFixture) req(t *testing.T, tn tenants.Tenant, uid uuid.UUID,
	p *projects.Project) (context.Context, authn.Principal, tenants.TenantContext, projects.ProjectContext) {
	t.Helper()
	tc := tenants.TenantContext{Tenant: tn}
	m, err := f.tenants.GetMembership(f.ctx, tenants.PlatformScope(), tn.ID, uid)
	if err == nil {
		tc.Membership = &m
	}
	ctx := tenants.WithTenantContext(f.ctx, tc)
	pc := projects.ProjectContext{}
	if p != nil {
		pc.Project = *p
		pm, err := f.repo.GetMembership(f.ctx, tenants.PlatformScope(), p.ID, uid)
		if err == nil {
			pc.Membership = &pm
			ctx = authz.WithProjectRoles(ctx, p.ID.String(), pm.Roles)
		}
	}
	return ctx, authn.Principal{UserID: uid, Kind: authn.KindUser}, tc, pc
}

func wantErr(t *testing.T, err error, kind apperr.Kind, code string) {
	t.Helper()
	if !apperr.Is(err, kind) {
		t.Fatalf("want kind %v, got %v", kind, err)
	}
	if code != "" {
		if ae, ok := err.(*apperr.Error); ok && ae.Code != code {
			t.Fatalf("want code %s, got %s", code, ae.Code)
		}
	}
}

// TestCallerMatrix covers platform-admin, tenant-admin, tenant member
// without project membership, project-admin, project-member and
// project-viewer.
func TestCallerMatrix(t *testing.T) {
	f := newSvcFixture(t)
	tn := f.tenant(t, "m-a")
	other := f.tenant(t, "m-b")

	taID := f.mkUser2(t, "ta")
	reID := f.mkUser2(t, "re") // researcher, no project membership
	padID := f.mkUser2(t, "pad")
	pmID := f.mkUser2(t, "pm")
	pvID := f.mkUser2(t, "pv")
	f.member(t, tn, taID, "tenant-admin")
	f.member(t, tn, reID, "researcher")
	f.member(t, tn, padID, "researcher")
	f.member(t, tn, pmID, "researcher")
	f.member(t, tn, pvID, "researcher")

	p := f.project(t, tn, "proj")
	f.projectMember(t, tn, p, padID, "project-admin")
	f.projectMember(t, tn, p, pmID, "project-member")
	f.projectMember(t, tn, p, pvID, "project-viewer")

	// Create requires tenant project.create.
	ctx, pr, tc, _ := f.req(t, tn, taID, nil)
	if _, err := f.svc.Create(ctx, pr, tc,
		projectsvc.CreateProject{Slug: "p2", Name: "P2"}); err != nil {
		t.Fatalf("tenant-admin create: %v", err)
	}
	ctx, pr, tc, _ = f.req(t, tn, reID, nil)
	_, err := f.svc.Create(ctx, pr, tc, projectsvc.CreateProject{Slug: "p3", Name: "P3"})
	wantErr(t, err, apperr.Forbidden, "")

	// Get: tenant members read via tenant project.read; members too.
	for _, uid := range []uuid.UUID{reID, padID, pmID, pvID} {
		ctx, pr, tc, pc := f.req(t, tn, uid, &p)
		if _, err := f.svc.Get(ctx, pr, tc, pc); err != nil {
			t.Fatalf("get uid=%s: %v", uid, err)
		}
	}
	// A principal scoped to tenant B reading A's project denies: the
	// request ctx is B while the resource names A.
	oid := f.mkUser2(t, "ob")
	f.member(t, other, oid, "tenant-admin")
	ctx, pr, tc, _ = f.req(t, other, oid, nil)
	pc := projects.ProjectContext{Project: p}
	tc.Tenant = tn // resource names A, but ctx membership is B's
	if _, err := f.svc.Get(ctx, pr, tc, pc); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("cross-tenant get: %v", err)
	}

	// Update/manage: project-admin ok; member/viewer denied;
	// tenant-admin ok without membership.
	name := "renamed"
	ctx, pr, tc, pc = f.req(t, tn, padID, &p)
	if _, err := f.svc.Update(ctx, pr, tc, pc,
		projectsvc.UpdateProject{Name: &name, Version: 1}); err != nil {
		t.Fatalf("project-admin update: %v", err)
	}
	for _, uid := range []uuid.UUID{pmID, pvID} {
		ctx, pr, tc, pc := f.req(t, tn, uid, &p)
		_, err := f.svc.Update(ctx, pr, tc, pc,
			projectsvc.UpdateProject{Name: &name, Version: 2})
		wantErr(t, err, apperr.Forbidden, "")
	}
	ctx, pr, tc, pc = f.req(t, tn, taID, &p)
	if _, err := f.svc.Update(ctx, pr, tc, pc,
		projectsvc.UpdateProject{Name: &name, Version: 2}); err != nil {
		t.Fatalf("tenant-admin update: %v", err)
	}
	ctx, pr, tc, pc = f.req(t, tn, reID, &p)
	_, err = f.svc.Update(ctx, pr, tc, pc,
		projectsvc.UpdateProject{Name: &name, Version: 3})
	wantErr(t, err, apperr.Forbidden, "")

	// Platform-admin reads and manages without any membership.
	pa := authn.Principal{UserID: uuid.Must(uuid.NewV7()),
		Kind: authn.KindUser, PlatformRoles: []string{"platform-admin"}}
	tc = tenants.TenantContext{Tenant: tn}
	ctx = tenants.WithTenantContext(f.ctx, tc)
	pc = projects.ProjectContext{Project: p}
	if _, err := f.svc.Get(ctx, pa, tc, pc); err != nil {
		t.Fatalf("platform-admin get: %v", err)
	}
	if _, err := f.svc.Update(ctx, pa, tc, pc,
		projectsvc.UpdateProject{Name: &name, Version: 3}); err != nil {
		t.Fatalf("platform-admin update: %v", err)
	}

	// Members manage: project-admin and tenant-admin only.
	for uid, ok := range map[uuid.UUID]bool{padID: true, pmID: false, pvID: false, taID: true} {
		ctx, pr, tc, pc := f.req(t, tn, uid, &p)
		err := f.addViewer(ctx, t, pr, tc, pc)
		if ok && err != nil {
			t.Fatalf("add member uid=%s: %v", uid, err)
		}
		if !ok {
			wantErr(t, err, apperr.Forbidden, "")
		}
	}

	// Archive/Unarchive: project-admin. Refresh the project row first —
	// the middleware resolves a fresh copy per request.
	p, err = f.repo.GetBySlugOrID(f.ctx, tenants.PlatformScope(), tn.ID, "proj")
	if err != nil {
		t.Fatal(err)
	}
	ctx, pr, tc, pc = f.req(t, tn, padID, &p)
	if _, err := f.svc.Archive(ctx, pr, tc, pc); err != nil {
		t.Fatalf("archive: %v", err)
	}
	// Archived blocks member mutation.
	pc.Project.State = projects.StateArchived
	wantErr(t, f.addViewer(ctx, t, pr, tc, pc), apperr.Conflict, "PROJECT_STATE")
	p, err = f.repo.GetBySlugOrID(f.ctx, tenants.PlatformScope(), tn.ID, "proj")
	if err != nil {
		t.Fatal(err)
	}
	pc.Project = p
	if _, err := f.svc.Unarchive(ctx, pr, tc, pc); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
}

func (f *svcFixture) mkUser2(t *testing.T, sub string) uuid.UUID {
	t.Helper()
	return mkUser(t, f.pool, sub)
}

// addViewer adds a fresh user as a project-viewer.
func (f *svcFixture) addViewer(ctx context.Context, t *testing.T, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext) error {
	uid := mkUser(t, f.pool, "x"+uuid.Must(uuid.NewV7()).String())
	if err := f.tenants.UpsertMembership(ctx, tenants.PlatformScope(),
		tenants.Membership{TenantID: tc.Tenant.ID, UserID: uid,
			Roles: []string{"researcher"}, Source: tenants.SourceManual}); err != nil {
		return err
	}
	_, err := f.svc.AddMember(ctx, p, tc, pc,
		projectsvc.UpsertMember{UserID: uid, Roles: []string{"project-viewer"}})
	return err
}

// TestBindingValidation covers the assignment-constraint cases and
// ResolveBinding.
func TestSvcBindingValidation(t *testing.T) {
	f := newSvcFixture(t)
	tn := f.tenant(t, "b-a")
	uid := f.mkUser2(t, "ba")
	f.member(t, tn, uid, "tenant-admin")
	p := f.project(t, tn, "bproj")
	assigned := f.assignedCluster(t, tn, "cb1", clusters.AssignmentDefaults{
		AllowedPartitions:    []string{"gpu", "batch"},
		DefaultAccountPrefix: "acct-"})
	unassigned := mkCluster(t, f.pool, "cb2")

	ctx, pr, tc, pc := f.req(t, tn, uid, &p)
	ok := projectsvc.UpsertBinding{ClusterID: assigned, SlurmAccount: "acct-p",
		AllowedPartitions: []string{"gpu"}, DefaultPartition: "gpu"}
	if _, err := f.svc.CreateBinding(ctx, pr, tc, pc, ok); err != nil {
		t.Fatalf("valid binding: %v", err)
	}

	cases := []struct {
		name string
		in   projectsvc.UpsertBinding
		code string
	}{
		{"cluster not assigned", projectsvc.UpsertBinding{
			ClusterID: unassigned, SlurmAccount: "acct-x"}, "CLUSTER_NOT_ASSIGNED"},
		{"partition not allowed", projectsvc.UpsertBinding{
			ClusterID: assigned, SlurmAccount: "acct-x",
			AllowedPartitions: []string{"restricted"}}, "PARTITION_NOT_ALLOWED"},
		{"default outside allowed", projectsvc.UpsertBinding{
			ClusterID: assigned, SlurmAccount: "acct-x",
			AllowedPartitions: []string{"gpu"}, DefaultPartition: "batch"},
			"PARTITION_NOT_ALLOWED"},
		{"account prefix", projectsvc.UpsertBinding{
			ClusterID: assigned, SlurmAccount: "other-x"}, "ACCOUNT_PREFIX"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.svc.CreateBinding(ctx, pr, tc, pc, c.in)
			wantErr(t, err, apperr.Validation, c.code)
		})
	}

	// ResolveBinding returns the entitlement input; disabled -> NotFound.
	b, err := f.svc.ResolveBinding(ctx, tenants.PlatformScope(), p.ID, assigned)
	if err != nil || b.Account != "acct-p" || b.DefaultPartition != "gpu" {
		t.Fatalf("resolve: %v %+v", err, b)
	}
	bs, _ := f.svc.ListBindings(ctx, pr, tc, pc)
	if len(bs) != 1 {
		t.Fatalf("list bindings: %+v", bs)
	}
	bid := bs[0].ID
	dis := false
	if _, err := f.svc.UpdateBinding(ctx, pr, tc, pc, bid,
		projectsvc.UpsertBinding{Enabled: &dis, Version: 1}); err != nil {
		t.Fatalf("disable binding: %v", err)
	}
	if _, err := f.svc.ResolveBinding(ctx, tenants.PlatformScope(), p.ID, assigned); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("disabled binding must not resolve: %v", err)
	}
	if err := f.svc.DeleteBinding(ctx, pr, tc, pc, bid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetBinding(ctx, pr, tc, pc, bid); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("deleted binding: %v", err)
	}
}

// TestLastAdminProtection: the last project-admin cannot be demoted or
// removed; a second admin unlocks both.
func TestLastAdminProtection(t *testing.T) {
	f := newSvcFixture(t)
	tn := f.tenant(t, "la-a")
	uid := f.mkUser2(t, "la")
	adm := f.mkUser2(t, "ladmin")
	f.member(t, tn, uid, "tenant-admin")
	f.member(t, tn, adm, "researcher")
	p := f.project(t, tn, "laproj")
	f.projectMember(t, tn, p, adm, "project-admin")

	ctx, pr, tc, pc := f.req(t, tn, uid, &p)
	if _, err := f.svc.UpdateRoles(ctx, pr, tc, pc, adm,
		[]string{"project-member"}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("demote last admin: %v", err)
	}
	if err := f.svc.RemoveMember(ctx, pr, tc, pc, adm); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("remove last admin: %v", err)
	}
	// Add a second admin, then demotion succeeds.
	if _, err := f.svc.AddMember(ctx, pr, tc, pc,
		projectsvc.UpsertMember{UserID: uid, Roles: []string{"project-admin"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateRoles(ctx, pr, tc, pc, adm,
		[]string{"project-member"}); err != nil {
		t.Fatalf("demote with second admin: %v", err)
	}
	// Invalid role rejected.
	if _, err := f.svc.UpdateRoles(ctx, pr, tc, pc, adm,
		[]string{"superuser"}); !apperr.Is(err, apperr.Validation) {
		t.Fatalf("bad role: %v", err)
	}
	// Non-tenant member cannot be added.
	stranger := f.mkUser2(t, "stranger")
	if _, err := f.svc.AddMember(ctx, pr, tc, pc,
		projectsvc.UpsertMember{UserID: stranger, Roles: []string{"project-member"}}); !apperr.Is(err, apperr.Validation) {
		t.Fatalf("non-tenant member: %v", err)
	}
	// Members list works for a project member.
	ctx2, pr2, tc2, pc2 := f.req(t, tn, adm, &p)
	ms, _, err := f.svc.ListMembers(ctx2, pr2, tc2, pc2, tenants.Page{})
	if err != nil || len(ms) != 2 {
		t.Fatalf("list members: %v %+v", err, ms)
	}
}
