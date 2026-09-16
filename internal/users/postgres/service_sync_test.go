package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/users"
	userpg "github.com/Exonical/custos/internal/users/postgres"
)

type fakeRec struct {
	mu     sync.Mutex
	events []audit.Event
}

func (f *fakeRec) Record(_ context.Context, e audit.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func seedTenant(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO tenants (id, slug, name, state) VALUES ($1,$2,'T','active')`,
		id, "t-"+id.String()[24:]); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedGroup(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO groups (id, tenant_id, name, source)
		VALUES ($1,$2,$3,'manual')`, id, tenantID, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedRule(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID,
	match string, roles []string, groupID *uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO claim_mapping_rules
		 (id, tenant_id, claim, match_value, roles, group_id, enabled)
		VALUES ($1,$2,'groups',$3,$4,$5,true)`,
		id, tenantID, match, roles, groupID); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedMember(t *testing.T, pool *pgxpool.Pool, tenantID, userID uuid.UUID,
	roles []string, source string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO tenant_memberships (tenant_id, user_id, roles, source)
		VALUES ($1,$2,$3,$4)`, tenantID, userID, roles, source); err != nil {
		t.Fatal(err)
	}
}

func memberRoles(t *testing.T, pool *pgxpool.Pool, tenantID, userID uuid.UUID) (roles []string, source string, ok bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT roles, source FROM tenant_memberships
		 WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).
		Scan(&roles, &source)
	if err != nil {
		return nil, "", false
	}
	return roles, source, true
}

func groupMemberExists(t *testing.T, pool *pgxpool.Pool, groupID, userID uuid.UUID) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM group_memberships
		 WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func provision(t *testing.T, svc *users.Service, sub string, groups ...string) authn.Principal {
	t.Helper()
	p, err := svc.Provision(context.Background(), authn.Principal{
		Issuer: "iss", Subject: sub, Kind: authn.KindUser,
		Groups: groups,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func resetSync(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET claims_synced_at = now() - interval '1 hour' WHERE id=$1`,
		userID); err != nil {
		t.Fatal(err)
	}
}

func newSvc(t *testing.T, pool *pgxpool.Pool, rec *fakeRec) *users.Service {
	t.Helper()
	return users.NewService(userpg.New(pool), rec, users.WithGroupsClaim("groups"))
}

// (a) groups claim matches rule → idp tenant + group memberships, audits.
func TestSyncGrantsFromClaims(t *testing.T) {
	pool := dbtest.Pool(t)
	rec := &fakeRec{}
	svc := newSvc(t, pool, rec)
	tn := seedTenant(t, pool)
	gid := seedGroup(t, pool, tn, "hpc-a")
	seedRule(t, pool, tn, "hpc-a", []string{"researcher"}, &gid)

	p := provision(t, svc, "u1", "hpc-a")
	roles, source, ok := memberRoles(t, pool, tn, p.UserID)
	if !ok || source != "idp" || len(roles) != 1 || roles[0] != "researcher" {
		t.Fatalf("membership: %v %s %v", roles, source, ok)
	}
	if !groupMemberExists(t, pool, gid, p.UserID) {
		t.Fatal("group membership missing")
	}
	var granted, added bool
	for _, e := range rec.events {
		switch e.Action {
		case "membership.granted":
			granted = true
		case "group.member.added":
			added = true
		}
	}
	if !granted || !added {
		t.Fatalf("audit events: %+v", rec.events)
	}
}

// (b) claim disappears → idp membership revoked, manual untouched.
func TestSyncRevokesAbsentClaim(t *testing.T) {
	pool := dbtest.Pool(t)
	rec := &fakeRec{}
	svc := newSvc(t, pool, rec)
	tn, tn2 := seedTenant(t, pool), seedTenant(t, pool)
	seedRule(t, pool, tn, "hpc-a", []string{"researcher"}, nil)

	p := provision(t, svc, "u1", "hpc-a")
	if _, _, ok := memberRoles(t, pool, tn, p.UserID); !ok {
		t.Fatal("idp membership missing")
	}
	// Manual membership in tn2 survives any sync.
	seedMember(t, pool, tn2, p.UserID, []string{"viewer"}, "manual")

	p = provision(t, svc, "u1") // no groups → different hash → sync
	if _, _, ok := memberRoles(t, pool, tn, p.UserID); ok {
		t.Fatal("idp membership not revoked")
	}
	roles, source, ok := memberRoles(t, pool, tn2, p.UserID)
	if !ok || source != "manual" || roles[0] != "viewer" {
		t.Fatalf("manual membership disturbed: %v %s", roles, source)
	}
	var revoked bool
	for _, e := range rec.events {
		if e.Action == "membership.revoked" {
			revoked = true
		}
	}
	if !revoked {
		t.Fatal("membership.revoked audit missing")
	}
}

// (c) rule roles change → idp roles updated on next sync.
func TestSyncUpdatesRoles(t *testing.T) {
	pool := dbtest.Pool(t)
	rec := &fakeRec{}
	svc := newSvc(t, pool, rec)
	tn := seedTenant(t, pool)
	rid := seedRule(t, pool, tn, "hpc-a", []string{"viewer"}, nil)

	p := provision(t, svc, "u1", "hpc-a")
	roles, _, _ := memberRoles(t, pool, tn, p.UserID)
	if len(roles) != 1 || roles[0] != "viewer" {
		t.Fatalf("roles = %v", roles)
	}

	// Change the rule; claims hash is unchanged so force staleness.
	if _, err := pool.Exec(context.Background(),
		`UPDATE claim_mapping_rules SET roles='{researcher}' WHERE id=$1`,
		rid); err != nil {
		t.Fatal(err)
	}
	resetSync(t, pool, p.UserID)
	provision(t, svc, "u1", "hpc-a")
	roles, _, _ = memberRoles(t, pool, tn, p.UserID)
	if len(roles) != 1 || roles[0] != "researcher" {
		t.Fatalf("roles after resync = %v", roles)
	}
	var updated bool
	for _, e := range rec.events {
		if e.Action == "membership.updated" {
			updated = true
		}
	}
	if !updated {
		t.Fatal("membership.updated audit missing")
	}
}

// (d) last tenant-admin via idp with claim removed → kept + revoke_blocked.
func TestSyncLastAdminBlocked(t *testing.T) {
	pool := dbtest.Pool(t)
	rec := &fakeRec{}
	svc := newSvc(t, pool, rec)
	tn := seedTenant(t, pool)
	seedRule(t, pool, tn, "boss", []string{"tenant-admin"}, nil)

	p := provision(t, svc, "boss1", "boss")
	roles, _, ok := memberRoles(t, pool, tn, p.UserID)
	if !ok || roles[0] != "tenant-admin" {
		t.Fatalf("admin membership: %v %v", roles, ok)
	}

	provision(t, svc, "boss1") // claim gone → revocation must be blocked
	_, source, ok := memberRoles(t, pool, tn, p.UserID)
	if !ok || source != "idp" {
		t.Fatal("last tenant-admin membership was revoked")
	}
	var blocked bool
	for _, e := range rec.events {
		if e.Action == "membership.revoke_blocked" {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("membership.revoke_blocked audit missing")
	}
}

// (e) fast path: second Provision with same claims within 5 min → no
// membership writes, no new audit events.
func TestSyncFastPath(t *testing.T) {
	pool := dbtest.Pool(t)
	rec := &fakeRec{}
	svc := newSvc(t, pool, rec)
	tn := seedTenant(t, pool)
	seedRule(t, pool, tn, "hpc-a", []string{"viewer"}, nil)

	p := provision(t, svc, "u1", "hpc-a")
	nEvents := len(rec.events)
	var before time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT updated_at FROM tenant_memberships
		 WHERE tenant_id=$1 AND user_id=$2`, tn, p.UserID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	provision(t, svc, "u1", "hpc-a")
	var after time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT updated_at FROM tenant_memberships
		 WHERE tenant_id=$1 AND user_id=$2`, tn, p.UserID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) {
		t.Fatal("membership row rewritten on fast path")
	}
	if len(rec.events) != nEvents {
		t.Fatalf("audit events grew on fast path: %d -> %d", nEvents, len(rec.events))
	}
}

// (f) two concurrent Provisions → exactly one membership row, one grant.
func TestSyncConcurrent(t *testing.T) {
	pool := dbtest.Pool(t)
	rec := &fakeRec{}
	svc := newSvc(t, pool, rec)
	tn := seedTenant(t, pool)
	seedRule(t, pool, tn, "hpc-a", []string{"viewer"}, nil)

	// Pre-provision so both goroutines share one user row.
	p := provision(t, svc, "u1")
	resetSync(t, pool, p.UserID)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Provision(context.Background(), authn.Principal{
				Issuer: "iss", Subject: "u1", Kind: authn.KindUser,
				Groups: []string{"hpc-a"},
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tenant_memberships
		 WHERE tenant_id=$1 AND user_id=$2`, tn, p.UserID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("membership rows = %d", n)
	}
	grants := 0
	for _, e := range rec.events {
		if e.Action == "membership.granted" {
			grants++
		}
	}
	if grants != 1 {
		t.Fatalf("membership.granted events = %d", grants)
	}
}
