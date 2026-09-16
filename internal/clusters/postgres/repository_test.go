package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/tenants"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func mkCluster(name string) clusters.Cluster {
	return clusters.Cluster{
		ID:           uuid.Must(uuid.NewV7()),
		Name:         name,
		DisplayName:  name,
		BaseURL:      "https://slurm.example:6820",
		APIVersion:   "v0.0.45",
		IdentityMode: clusters.IdentityService,
		ServiceUser:  "custos",
		TokenRef: secrets.Reference{
			Provider: "file", Path: "slurm/token",
		},
		Visibility: clusters.VisibilityAssigned,
		State:      clusters.StateUnreachable,
		Version:    1,
	}
}

func mkTenant(t *testing.T, pool interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, slug string, state tenants.State) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	var got uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO tenants (id, slug, name, state, settings, version)
		VALUES ($1,$2,'T',$3,'{}',1) RETURNING id`,
		id, slug, state).Scan(&got)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestClusterCRUD(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := clusterpg.New(pool)
	ctx := context.Background()

	c := mkCluster("alpha")
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	byName, err := repo.GetByNameOrID(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if byName.ID != c.ID || byName.TokenRef.Provider != "file" ||
		byName.TokenRef.Path != "slurm/token" {
		t.Fatalf("unexpected: %+v", byName)
	}
	byID, err := repo.GetByNameOrID(ctx, c.ID.String())
	if err != nil || byID.Name != "alpha" {
		t.Fatalf("get by id: %v %+v", err, byID)
	}
	if _, err := repo.GetByNameOrID(ctx, "nope"); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want notfound, got %v", err)
	}

	list, next, err := repo.List(ctx, clusters.Page{})
	if err != nil || len(list) != 1 || next != "" {
		t.Fatalf("list: %v %d %q", err, len(list), next)
	}

	byName.DisplayName = "Alpha Cluster"
	if err := repo.Update(ctx, byName); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByNameOrID(ctx, "alpha")
	if got.DisplayName != "Alpha Cluster" || got.Version != 2 {
		t.Fatalf("update: %+v", got)
	}
	// stale version -> conflict
	stale := got
	stale.Version = 1
	if err := repo.Update(ctx, stale); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want conflict, got %v", err)
	}

	if err := repo.SetState(ctx, c.ID, clusters.StateDisabled); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByNameOrID(ctx, "alpha")
	if got.State != clusters.StateDisabled {
		t.Fatalf("state: %v", got.State)
	}
}

func TestRecordSyncResultHysteresis(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := clusterpg.New(pool)
	ctx := context.Background()
	now := time.Now()

	mk := func(name string, st clusters.State) uuid.UUID {
		c := mkCluster(name)
		c.State = st
		if err := repo.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	run := func(id uuid.UUID, ok bool) clusters.State {
		res := clusters.SyncResult{OK: ok, At: now}
		if ok {
			res.Capabilities = &slurm.Capabilities{SlurmVersion: "26.05"}
		} else {
			res.Err = "dial timeout"
		}
		st, err := repo.RecordSyncResult(ctx, id, res)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	get := func(id uuid.UUID) clusters.Cluster {
		c, err := repo.GetByNameOrID(ctx, id.String())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	t.Run("two successes -> active", func(t *testing.T) {
		id := mk("hyst-a", clusters.StateUnreachable)
		if st := run(id, true); st != clusters.StateDegraded {
			t.Fatalf("after 1 ok: %v", st)
		}
		if st := run(id, true); st != clusters.StateActive {
			t.Fatalf("after 2 ok: %v", st)
		}
		c := get(id)
		if c.ConsecSuccesses != 2 || c.ConsecFailures != 0 ||
			c.LastError != "" || c.Capabilities == nil {
			t.Fatalf("counters: %+v", c)
		}
	})

	t.Run("three failures -> unreachable", func(t *testing.T) {
		id := mk("hyst-b", clusters.StateActive)
		if st := run(id, false); st != clusters.StateDegraded {
			t.Fatalf("after 1 fail: %v", st)
		}
		if st := run(id, false); st != clusters.StateDegraded {
			t.Fatalf("after 2 fail: %v", st)
		}
		if st := run(id, false); st != clusters.StateUnreachable {
			t.Fatalf("after 3 fail: %v", st)
		}
		c := get(id)
		if c.ConsecFailures != 3 || c.ConsecSuccesses != 0 ||
			c.LastError != "dial timeout" {
			t.Fatalf("counters: %+v", c)
		}
	})

	t.Run("success on active stays active", func(t *testing.T) {
		id := mk("hyst-c", clusters.StateActive)
		if st := run(id, true); st != clusters.StateActive {
			t.Fatalf("got %v", st)
		}
	})

	t.Run("disabled never changes", func(t *testing.T) {
		id := mk("hyst-d", clusters.StateDisabled)
		if st := run(id, true); st != clusters.StateDisabled {
			t.Fatalf("ok on disabled: %v", st)
		}
		if st := run(id, false); st != clusters.StateDisabled {
			t.Fatalf("fail on disabled: %v", st)
		}
	})

	t.Run("error scrubbed and truncated", func(t *testing.T) {
		id := mk("hyst-e", clusters.StateActive)
		long := strings.Repeat("x", 1500)
		res := clusters.SyncResult{At: now,
			Err: "hdr X-SLURM-USER-TOKEN: secrettoken123 " + long}
		if _, err := repo.RecordSyncResult(ctx, id, res); err != nil {
			t.Fatal(err)
		}
		c := get(id)
		if strings.Contains(c.LastError, "secrettoken123") {
			t.Fatalf("token leaked: %q", c.LastError)
		}
		if len(c.LastError) > 1024 {
			t.Fatalf("not truncated: %d", len(c.LastError))
		}
	})
}

func TestPartitionsReplacedAtomically(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := clusterpg.New(pool)
	ctx := context.Background()
	c := mkCluster("parts")
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	put := func(names ...string) {
		res := clusters.SyncResult{OK: true, At: time.Now()}
		for _, n := range names {
			res.Partitions = append(res.Partitions,
				slurm.Partition{Name: n, Nodes: 2})
		}
		if _, err := repo.RecordSyncResult(ctx, c.ID, res); err != nil {
			t.Fatal(err)
		}
	}
	put("a", "b")
	put("b", "c")
	recs, err := repo.ListPartitions(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range recs {
		got = append(got, r.Name)
		if r.Attributes["nodes"] == nil {
			t.Fatalf("attributes not stored: %+v", r.Attributes)
		}
	}
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("want [b c], got %v", got)
	}
}

func TestAssignmentsRLS(t *testing.T) {
	// One shared DB: seed as superuser, read as the RLS-bound app role.
	dsn := dbtest.URL(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := clusterpg.New(pool)
	ctx := context.Background()

	c := mkCluster("rls")
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	ta := mkTenant(t, pool, "rls-a", tenants.StateActive)
	tb := mkTenant(t, pool, "rls-b", tenants.StateActive)

	ps := tenants.PlatformScope()
	if err := repo.UpsertAssignment(ctx, ps, clusters.Assignment{
		ClusterID: c.ID, TenantID: ta, Source: clusters.SourceManual,
		Defaults: clusters.AssignmentDefaults{
			DefaultAccountPrefix: "acct-",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertAssignment(ctx, ps, clusters.Assignment{
		ClusterID: c.ID, TenantID: tb, Source: clusters.SourceAuto,
	}); err != nil {
		t.Fatal(err)
	}

	// Platform scope sees both with sources + defaults preserved.
	as, _, err := repo.ListAssignments(ctx, ps, c.ID, clusters.Page{})
	if err != nil || len(as) != 2 {
		t.Fatalf("platform list: %v %d", err, len(as))
	}
	byTenant := map[uuid.UUID]clusters.Assignment{}
	for _, a := range as {
		byTenant[a.TenantID] = a
	}
	if byTenant[ta].Source != "manual" ||
		byTenant[ta].Defaults.DefaultAccountPrefix != "acct-" {
		t.Fatalf("manual row: %+v", byTenant[ta])
	}
	if byTenant[tb].Source != "auto" {
		t.Fatalf("auto row: %+v", byTenant[tb])
	}

	// Tenant-scoped (AppPool, non-superuser) sees only its own rows.
	app := dbtest.AppPoolOn(t, dsn)
	repoApp := clusterpg.New(app)
	visible, _, err := repoApp.ListAssignments(ctx,
		tenants.ScopeFor(&tenants.TenantContext{
			Tenant: tenants.Tenant{ID: ta},
		}), c.ID, clusters.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].TenantID != ta {
		t.Fatalf("tenant scope: %+v", visible)
	}
	// Raw query under tenant B sees no A rows (RLS forced).
	var n int
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, tb); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM cluster_tenant_assignments
			WHERE tenant_id=$1`, ta).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("tenant B saw %d of A's rows", n)
	}
	// ListVisibleForTenant joins assignments -> clusters.
	cs, m, err := repo.ListVisibleForTenant(ctx, ps, ta)
	if err != nil || len(cs) != 1 || cs[0].Name != "rls" {
		t.Fatalf("visible: %v %+v", err, cs)
	}
	if m[c.ID].Source != "manual" {
		t.Fatalf("assignment defaults: %+v", m[c.ID])
	}

	// DeleteAssignment + manual untouched by scope.
	if err := repo.DeleteAssignment(ctx, ps, c.ID, tb); err != nil {
		t.Fatal(err)
	}
	as, _, _ = repo.ListAssignments(ctx, ps, c.ID, clusters.Page{})
	if len(as) != 1 || as[0].TenantID != ta {
		t.Fatalf("after delete: %+v", as)
	}
}

func TestAutoAssignTenants(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := clusterpg.New(pool)
	ctx := context.Background()

	c := mkCluster("auto")
	c.Visibility = clusters.VisibilityAllTenants
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	ta := mkTenant(t, pool, "aa-a", tenants.StateActive)
	tb := mkTenant(t, pool, "aa-b", tenants.StateActive)
	_ = mkTenant(t, pool, "aa-del", tenants.StateDeleting)

	assign, drop, err := repo.AutoAssignTenants(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assign) != 2 || len(drop) != 0 {
		t.Fatalf("assign=%v drop=%v", assign, drop)
	}
	// Once assigned + one tenant leaves the qualifying states, flip.
	ps := tenants.PlatformScope()
	for _, tid := range assign {
		if err := repo.UpsertAssignment(ctx, ps, clusters.Assignment{
			ClusterID: c.ID, TenantID: tid, Source: clusters.SourceAuto,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Add a manual row on another tenant + delete B.
	td := mkTenant(t, pool, "aa-man", tenants.StateDeleting)
	if err := repo.UpsertAssignment(ctx, ps, clusters.Assignment{
		ClusterID: c.ID, TenantID: td, Source: clusters.SourceManual,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE tenants SET state='deleting' WHERE id=$1`, tb); err != nil {
		t.Fatal(err)
	}
	assign, drop, err = repo.AutoAssignTenants(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assign) != 0 || len(drop) != 1 || drop[0] != tb {
		t.Fatalf("assign=%v drop=%v (manual %v must be untouched)",
			assign, drop, td)
	}
	_ = ta
}
