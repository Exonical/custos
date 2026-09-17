package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/projects"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/tenants"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func mkTenant(t *testing.T, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(context.Background(), `
		INSERT INTO tenants (id, slug, name, state, settings, version)
		VALUES ($1,$2,'T','active','{}',1)`, id, slug)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mkUser(t *testing.T, pool *pgxpool.Pool, sub string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, issuer, subject, kind) VALUES ($1,'iss',$2,'user')`,
		id, sub)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func tenantMember(t *testing.T, pool *pgxpool.Pool, tenantID, userID uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO tenant_memberships (tenant_id, user_id, roles, source)
		VALUES ($1,$2,'{researcher}','manual')`, tenantID, userID)
	if err != nil {
		t.Fatal(err)
	}
}

func mkCluster(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	c := clusters.Cluster{
		ID:           uuid.Must(uuid.NewV7()),
		Name:         name,
		DisplayName:  name,
		BaseURL:      "https://slurm.example:6820",
		APIVersion:   "v0.0.45",
		IdentityMode: clusters.IdentityService,
		ServiceUser:  "custos",
		TokenRef:     secrets.Reference{Provider: "file", Path: "slurm/token"},
		Visibility:   clusters.VisibilityAssigned,
		State:        clusters.StateActive,
		Version:      1,
	}
	if err := clusterpg.New(pool).Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func mkProject(tenantID uuid.UUID, slug string) projects.Project {
	return projects.Project{
		ID: uuid.Must(uuid.NewV7()), TenantID: tenantID,
		Slug: slug, Name: slug, State: projects.StateActive,
		Settings: map[string]any{}, Version: 1,
	}
}

func scope(tenantID uuid.UUID) tenants.Scope {
	return tenants.ScopeFor(&tenants.TenantContext{
		Tenant: tenants.Tenant{ID: tenantID},
	})
}

func TestProjectCRUD(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := projectpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "proj-a")
	ps := tenants.PlatformScope()

	p := mkProject(tid, "alpha")
	if err := repo.Create(ctx, ps, p); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetBySlugOrID(ctx, ps, tid, "alpha")
	if err != nil || got.ID != p.ID {
		t.Fatalf("get by slug: %v %+v", err, got)
	}
	got, err = repo.GetBySlugOrID(ctx, ps, tid, p.ID.String())
	if err != nil || got.Slug != "alpha" {
		t.Fatalf("get by id: %v %+v", err, got)
	}
	if _, err := repo.GetBySlugOrID(ctx, ps, tid, "nope"); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want not found, got %v", err)
	}

	// Duplicate slug in the same tenant -> conflict.
	if err := repo.Create(ctx, ps, mkProject(tid, "alpha")); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want conflict, got %v", err)
	}

	p.Name = "renamed"
	p.Version = 1
	if err := repo.Update(ctx, ps, p); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetBySlugOrID(ctx, ps, tid, "alpha")
	if got.Name != "renamed" || got.Version != 2 {
		t.Fatalf("update: %+v", got)
	}
	// Stale version -> conflict.
	p.Version = 1
	if err := repo.Update(ctx, ps, p); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want version conflict, got %v", err)
	}
}

func TestProjectListKeyset(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := projectpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "proj-list")
	for _, s := range []string{"p1", "p2", "p3"} {
		if err := repo.Create(ctx, tenants.PlatformScope(), mkProject(tid, s)); err != nil {
			t.Fatal(err)
		}
	}
	items, next, err := repo.List(ctx, tenants.PlatformScope(), tid, tenants.Page{Limit: 2})
	if err != nil || len(items) != 2 || next == "" {
		t.Fatalf("page 1: %v n=%d next=%q", err, len(items), next)
	}
	items, next, err = repo.List(ctx, tenants.PlatformScope(), tid, tenants.Page{Limit: 2, Cursor: next})
	if err != nil || len(items) != 1 || next != "" {
		t.Fatalf("page 2: %v n=%d next=%q", err, len(items), next)
	}
}

func TestMemberships(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := projectpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "proj-mem")
	uid := mkUser(t, pool, "m1")
	tenantMember(t, pool, tid, uid)
	ps := tenants.PlatformScope()

	p := mkProject(tid, "mem")
	if err := repo.Create(ctx, ps, p); err != nil {
		t.Fatal(err)
	}
	m := projects.Membership{TenantID: tid, ProjectID: p.ID, UserID: uid,
		Roles: []string{"project-admin"}, Source: "manual"}
	if err := repo.UpsertMembership(ctx, ps, m); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetMembership(ctx, ps, p.ID, uid)
	if err != nil || got.Roles[0] != "project-admin" {
		t.Fatalf("get: %v %+v", err, got)
	}
	if n, _ := repo.CountProjectAdmins(ctx, ps, p.ID); n != 1 {
		t.Fatalf("admins = %d", n)
	}
	m.Roles = []string{"project-member"}
	if err := repo.UpsertMembership(ctx, ps, m); err != nil {
		t.Fatal(err)
	}
	if n, _ := repo.CountProjectAdmins(ctx, ps, p.ID); n != 0 {
		t.Fatalf("admins = %d", n)
	}
	if err := repo.DeleteMembership(ctx, ps, p.ID, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetMembership(ctx, ps, p.ID, uid); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

// TestMembershipRequiresTenantMember exercises the trigger under the
// app role (defense in depth below the service check).
func TestMembershipRequiresTenantMember(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := projectpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "proj-trig")
	uid := mkUser(t, pool, "m2")
	p := mkProject(tid, "trig")
	if err := repo.Create(ctx, tenants.PlatformScope(), p); err != nil {
		t.Fatal(err)
	}
	err := repo.UpsertMembership(ctx, tenants.PlatformScope(), projects.Membership{
		TenantID: tid, ProjectID: p.ID, UserID: uid,
		Roles: []string{"project-member"}, Source: "manual"})
	if err == nil {
		t.Fatal("non-tenant-member insert must fail")
	}
}

func TestBindings(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := projectpg.New(pool)
	crepo := clusterpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "proj-bind")
	cid := mkCluster(t, pool, "c1")
	ps := tenants.PlatformScope()
	if err := crepo.UpsertAssignment(ctx, ps, clusters.Assignment{
		ClusterID: cid, TenantID: tid, Source: clusters.SourceManual,
		Defaults: clusters.AssignmentDefaults{AllowedPartitions: []string{"gpu", "batch"}},
	}); err != nil {
		t.Fatal(err)
	}
	p := mkProject(tid, "bind")
	if err := repo.Create(ctx, ps, p); err != nil {
		t.Fatal(err)
	}

	b := projects.ClusterBinding{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: p.ID,
		ClusterID: cid, SlurmAccount: "acct-p", DefaultPartition: "gpu",
		AllowedPartitions: []string{"gpu"}, Enabled: true, Version: 1,
	}
	if err := repo.CreateBinding(ctx, ps, b); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetBindingByCluster(ctx, ps, p.ID, cid)
	if err != nil || got.SlurmAccount != "acct-p" {
		t.Fatalf("get by cluster: %v %+v", err, got)
	}
	if got, err = repo.GetBinding(ctx, ps, p.ID, b.ID); err != nil || !got.Enabled {
		t.Fatalf("get: %v %+v", err, got)
	}
	bs, err := repo.ListBindings(ctx, ps, p.ID)
	if err != nil || len(bs) != 1 {
		t.Fatalf("list: %v %+v", err, bs)
	}
	// Unique (project, cluster).
	if err := repo.CreateBinding(ctx, ps, b); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	b.SlurmAccount = "acct-q"
	b.Version = 1
	if err := repo.UpdateBinding(ctx, ps, b); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetBinding(ctx, ps, p.ID, b.ID)
	if got.SlurmAccount != "acct-q" || got.Version != 2 {
		t.Fatalf("update: %+v", got)
	}
	if err := repo.DeleteBinding(ctx, ps, p.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetBinding(ctx, ps, p.ID, b.ID); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

// TestBindingRequiresAssignedCluster exercises the trigger: no
// assignment row -> insert fails even under platform scope.
func TestBindingRequiresAssignedCluster(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := projectpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "proj-trig2")
	cid := mkCluster(t, pool, "c2")
	p := mkProject(tid, "trig2")
	if err := repo.Create(ctx, tenants.PlatformScope(), p); err != nil {
		t.Fatal(err)
	}
	err := repo.CreateBinding(ctx, tenants.PlatformScope(), projects.ClusterBinding{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: p.ID,
		ClusterID: cid, SlurmAccount: "acct", Enabled: true})
	if err == nil {
		t.Fatal("binding for unassigned cluster must fail")
	}
}

// TestProjectRLS verifies all project tables under the app role: a
// tenant scope sees only its own rows.
func TestProjectRLS(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	ta := mkTenant(t, admin, "rls-pa")
	tb := mkTenant(t, admin, "rls-pb")
	uid := mkUser(t, admin, "rls-u")
	tenantMember(t, admin, ta, uid)
	tenantMember(t, admin, tb, uid)
	cid := mkCluster(t, admin, "rls-c")

	repo := projectpg.New(admin)
	crepo := clusterpg.New(admin)
	ps := tenants.PlatformScope()
	for _, tid := range []uuid.UUID{ta, tb} {
		if err := crepo.UpsertAssignment(ctx, ps, clusters.Assignment{
			ClusterID: cid, TenantID: tid, Source: clusters.SourceManual,
		}); err != nil {
			t.Fatal(err)
		}
	}
	pa := mkProject(ta, "pa")
	pb := mkProject(tb, "pb")
	for _, p := range []projects.Project{pa, pb} {
		if err := repo.Create(ctx, ps, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []projects.Membership{
		{TenantID: ta, ProjectID: pa.ID, UserID: uid, Roles: []string{"project-member"}, Source: "manual"},
		{TenantID: tb, ProjectID: pb.ID, UserID: uid, Roles: []string{"project-member"}, Source: "manual"},
	} {
		if err := repo.UpsertMembership(ctx, ps, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []projects.Project{pa, pb} {
		if err := repo.CreateBinding(ctx, ps, projects.ClusterBinding{
			ID: uuid.Must(uuid.NewV7()), TenantID: p.TenantID,
			ProjectID: p.ID, ClusterID: cid, SlurmAccount: "a", Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `
		INSERT INTO resource_policies (id, tenant_id, scope, policy)
		VALUES ($1,$2,'tenant','{}'), ($3,$4,'tenant','{}')`,
		uuid.Must(uuid.NewV7()), ta, uuid.Must(uuid.NewV7()), tb); err != nil {
		t.Fatal(err)
	}

	app := dbtest.AppPoolOn(t, dsn)
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, tb); err != nil {
			return err
		}
		for table, want := range map[string]int{
			"projects": 1, "project_memberships": 1,
			"project_cluster_bindings": 1, "resource_policies": 1,
		} {
			var n int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM `+table+` WHERE tenant_id=$1`, ta).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatalf("tenant B saw %d A rows in %s", n, table)
			}
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM `+table+` WHERE tenant_id=$1`, tb).Scan(&n); err != nil {
				return err
			}
			if n != want {
				t.Fatalf("tenant B saw %d own rows in %s", n, table)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Repo-level: scoped List returns only B's projects.
	appRepo := projectpg.New(app)
	items, _, err := appRepo.List(ctx, scope(tb), tb, tenants.Page{})
	if err != nil || len(items) != 1 || items[0].Slug != "pb" {
		t.Fatalf("scoped list: %v %+v", err, items)
	}
	// Cross-tenant GetBySlugOrID under B's scope does not find A's project.
	if _, err := appRepo.GetBySlugOrID(ctx, scope(tb), ta, "pa"); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want not found under B scope, got %v", err)
	}
}

func TestListAllForUser(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := projectpg.New(pool)
	ctx := context.Background()
	tid := mkTenant(t, pool, "proj-me")
	uid := mkUser(t, pool, "me-u")
	tenantMember(t, pool, tid, uid)
	p := mkProject(tid, "me")
	if err := repo.Create(ctx, tenants.PlatformScope(), p); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertMembership(ctx, tenants.PlatformScope(), projects.Membership{
		TenantID: tid, ProjectID: p.ID, UserID: uid,
		Roles: []string{"project-admin"}, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	refs, err := repo.ListAllForUser(ctx, uid)
	if err != nil || len(refs) != 1 {
		t.Fatalf("list all: %v %+v", err, refs)
	}
	if refs[0].ProjectSlug != "me" || refs[0].TenantID != tid ||
		refs[0].Roles[0] != "project-admin" {
		t.Fatalf("ref: %+v", refs[0])
	}
}
