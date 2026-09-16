package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
)

func mkGroup(tenantID uuid.UUID, name string) tenants.Group {
	return tenants.Group{
		ID: uuid.Must(uuid.NewV7()), TenantID: tenantID,
		Name: name, Source: tenants.SourceManual,
	}
}

func seedUser(t *testing.T, pool *pgxpool.Pool, sub string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, issuer, subject, kind) VALUES ($1,'iss',$2,'user')`,
		id, sub); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGroupCRUD(t *testing.T) {
	repo := tenantpg.New(dbtest.Pool(t))
	ctx := context.Background()
	tn := mkTenant("grp")
	if err := repo.Create(ctx, tn); err != nil {
		t.Fatal(err)
	}
	scope := tenants.PlatformScope()

	g := mkGroup(tn.ID, "hpc-a")
	if err := repo.CreateGroup(ctx, scope, g); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetGroup(ctx, scope, tn.ID, g.ID.String())
	if err != nil || got.Name != "hpc-a" {
		t.Fatalf("get by id: %v %+v", err, got)
	}
	got, err = repo.GetGroup(ctx, scope, tn.ID, "hpc-a")
	if err != nil || got.ID != g.ID {
		t.Fatalf("get by name: %v %+v", err, got)
	}

	g.Name = "hpc-a2"
	g.Description = "d"
	g.Version = 1
	if err := repo.UpdateGroup(ctx, scope, g); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateGroup(ctx, scope, g); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want version conflict, got %v", err)
	}
	if err := repo.DeleteGroup(ctx, scope, tn.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetGroup(ctx, scope, tn.ID, g.ID.String()); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestGroupPagination(t *testing.T) {
	repo := tenantpg.New(dbtest.Pool(t))
	ctx := context.Background()
	tn := mkTenant("gpage")
	if err := repo.Create(ctx, tn); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"g1", "g2", "g3", "g4", "g5"} {
		if err := repo.CreateGroup(ctx, tenants.PlatformScope(),
			mkGroup(tn.ID, n)); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	cursor := ""
	for i := 0; i < 3; i++ {
		page, next, err := repo.ListGroups(ctx, tenants.PlatformScope(), tn.ID,
			tenants.Page{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range page {
			seen = append(seen, g.Name)
		}
		cursor = next
		if next == "" {
			break
		}
	}
	if len(seen) != 5 || seen[0] != "g1" || seen[4] != "g5" {
		t.Fatalf("pages: %v", seen)
	}
}

func TestGroupMembersAndTrigger(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := tenantpg.New(pool)
	ctx := context.Background()
	tn := mkTenant("gmem")
	if err := repo.Create(ctx, tn); err != nil {
		t.Fatal(err)
	}
	g := mkGroup(tn.ID, "g")
	if err := repo.CreateGroup(ctx, tenants.PlatformScope(), g); err != nil {
		t.Fatal(err)
	}
	member := seedUser(t, pool, "m1")
	nonmember := seedUser(t, pool, "m2")

	// Non-tenant-member insert fails the trigger.
	err := repo.UpsertGroupMember(ctx, tenants.PlatformScope(), tenants.GroupMembership{
		TenantID: tn.ID, GroupID: g.ID, UserID: nonmember,
		Source: tenants.SourceManual,
	})
	if err == nil {
		t.Fatal("non-tenant-member group insert allowed")
	}

	if err := repo.UpsertMembership(ctx, tenants.PlatformScope(), tenants.Membership{
		TenantID: tn.ID, UserID: member, Roles: []string{"viewer"},
		Source: tenants.SourceManual,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertGroupMember(ctx, tenants.PlatformScope(), tenants.GroupMembership{
		TenantID: tn.ID, GroupID: g.ID, UserID: member,
		Source: tenants.SourceManual,
	}); err != nil {
		t.Fatal(err)
	}
	ms, _, err := repo.ListGroupMembers(ctx, tenants.PlatformScope(), tn.ID, g.ID,
		tenants.Page{})
	if err != nil || len(ms) != 1 || ms[0].UserID != member {
		t.Fatalf("members: %v %+v", err, ms)
	}
	if err := repo.DeleteGroupMember(ctx, tenants.PlatformScope(), tn.ID, g.ID, member); err != nil {
		t.Fatal(err)
	}
}

// TestGroupTriggerUnderAppRole verifies the trigger sees the tenant
// membership through RLS when inserting under tenant scope as the
// non-superuser app role.
func TestGroupTriggerUnderAppRole(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	repo := tenantpg.New(admin)

	tn := mkTenant("gtrig")
	if err := repo.Create(ctx, tn); err != nil {
		t.Fatal(err)
	}
	uid := seedUser(t, admin, "tm")
	if err := repo.UpsertMembership(ctx, tenants.PlatformScope(), tenants.Membership{
		TenantID: tn.ID, UserID: uid, Roles: []string{"viewer"},
		Source: tenants.SourceManual,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateGroup(ctx, tenants.PlatformScope(), mkGroup(tn.ID, "tg")); err != nil {
		t.Fatal(err)
	}
	var gid uuid.UUID
	if err := admin.QueryRow(ctx,
		`SELECT id FROM groups WHERE tenant_id=$1`, tn.ID).Scan(&gid); err != nil {
		t.Fatal(err)
	}

	app := dbtest.AppPoolOn(t, dsn)
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, tn.ID); err != nil {
			return err
		}
		// Tenant-scope insert: membership visible to trigger → ok.
		if _, err := tx.Exec(ctx,
			`INSERT INTO group_memberships (tenant_id, group_id, user_id, source)
			 VALUES ($1,$2,$3,'manual')`, tn.ID, gid, uid); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tenant-scope group insert: %v", err)
	}

	// RLS on the new tables: scope A sees only its own rows; unset → 0.
	tn2 := mkTenant("gtrig2")
	if err := repo.Create(ctx, tn2); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateGroup(ctx, tenants.PlatformScope(), mkGroup(tn2.ID, "tg2")); err != nil {
		t.Fatal(err)
	}
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, tn.ID); err != nil {
			return err
		}
		var ng, nm int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM groups`).Scan(&ng); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM group_memberships`).Scan(&nm); err != nil {
			return err
		}
		if ng != 1 || nm != 1 {
			t.Fatalf("scope A sees %d groups, %d memberships", ng, nm)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var none int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM groups`).Scan(&none); err != nil {
		t.Fatal(err)
	}
	if none != 0 {
		t.Fatalf("unscoped groups = %d", none)
	}
}

func TestClaimRuleCRUDAndMatch(t *testing.T) {
	repo := tenantpg.New(dbtest.Pool(t))
	ctx := context.Background()
	tn := mkTenant("rules")
	if err := repo.Create(ctx, tn); err != nil {
		t.Fatal(err)
	}
	g := mkGroup(tn.ID, "rule-group")
	if err := repo.CreateGroup(ctx, tenants.PlatformScope(), g); err != nil {
		t.Fatal(err)
	}
	scope := tenants.PlatformScope()

	cr := tenants.ClaimRule{
		ID: uuid.Must(uuid.NewV7()), TenantID: tn.ID,
		Claim: "groups", MatchValue: "hpc-a",
		Roles: []string{"researcher"}, GroupID: &g.ID, Enabled: true,
	}
	if err := repo.CreateClaimRule(ctx, scope, cr); err != nil {
		t.Fatal(err)
	}
	cr2 := tenants.ClaimRule{
		ID: uuid.Must(uuid.NewV7()), TenantID: tn.ID,
		Claim: "groups", MatchValue: "hpc-b",
		Roles: []string{"viewer"}, Enabled: false,
	}
	if err := repo.CreateClaimRule(ctx, scope, cr2); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetClaimRule(ctx, scope, tn.ID, cr.ID)
	if err != nil || got.MatchValue != "hpc-a" || got.GroupID == nil {
		t.Fatalf("get: %v %+v", err, got)
	}

	matched, err := repo.MatchRules(ctx, map[string][]string{
		"groups": {"hpc-a", "hpc-b", "other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 || matched[0].ID != cr.ID {
		t.Fatalf("match (disabled rule excluded): %+v", matched)
	}

	cr.Roles = []string{"viewer", "researcher"}
	cr.Enabled = false
	cr.Version = 1
	if err := repo.UpdateClaimRule(ctx, scope, cr); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateClaimRule(ctx, scope, cr); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want version conflict, got %v", err)
	}
	matched, err = repo.MatchRules(ctx, map[string][]string{"groups": {"hpc-a"}})
	if err != nil || len(matched) != 0 {
		t.Fatalf("disabled rule matched: %+v", matched)
	}
	if err := repo.DeleteClaimRule(ctx, scope, tn.ID, cr.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRequestDeletionAndPurge(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := tenantpg.New(pool)
	ctx := context.Background()
	tn := mkTenant("del")
	if err := repo.Create(ctx, tn); err != nil {
		t.Fatal(err)
	}
	uid := seedUser(t, pool, "dm")
	if err := repo.UpsertMembership(ctx, tenants.PlatformScope(), tenants.Membership{
		TenantID: tn.ID, UserID: uid, Roles: []string{"viewer"},
		Source: tenants.SourceManual,
	}); err != nil {
		t.Fatal(err)
	}
	g := mkGroup(tn.ID, "dg")
	if err := repo.CreateGroup(ctx, tenants.PlatformScope(), g); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertGroupMember(ctx, tenants.PlatformScope(), tenants.GroupMembership{
		TenantID: tn.ID, GroupID: g.ID, UserID: uid,
		Source: tenants.SourceManual,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateClaimRule(ctx, tenants.PlatformScope(), tenants.ClaimRule{
		ID: uuid.Must(uuid.NewV7()), TenantID: tn.ID,
		Claim: "groups", MatchValue: "x", Roles: []string{"viewer"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.RequestDeletion(ctx, tenants.PlatformScope(), tn.ID); err != nil {
		t.Fatal(err)
	}
	// State flipped + work item enqueued atomically.
	var state, kind, key string
	if err := pool.QueryRow(ctx, `SELECT state FROM tenants WHERE id=$1`, tn.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "deleting" {
		t.Fatalf("state = %s", state)
	}
	if err := pool.QueryRow(ctx,
		`SELECT kind, key FROM work_items WHERE key=$1`,
		"tenant:"+tn.ID.String()).Scan(&kind, &key); err != nil {
		t.Fatalf("work item missing: %v", err)
	}
	if kind != "tenant.delete" {
		t.Fatalf("kind = %s", kind)
	}
	// Second request conflicts.
	if err := repo.RequestDeletion(ctx, tenants.PlatformScope(), tn.ID); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want TENANT_STATE, got %v", err)
	}

	// Purge removes tenant data, tombstones the row, is idempotent.
	if err := repo.PurgeTenantData(ctx, tn.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	for _, table := range []string{
		"tenant_memberships", "group_memberships", "groups", "claim_mapping_rules",
	} {
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM `+table+` WHERE tenant_id=$1`, tn.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s still has %d rows", table, n)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM tenants WHERE id=$1`, tn.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "deleted" {
		t.Fatalf("state = %s", state)
	}
	if err := repo.PurgeTenantData(ctx, tn.ID); err != nil {
		t.Fatalf("re-purge not idempotent: %v", err)
	}
}
