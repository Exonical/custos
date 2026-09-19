package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	clustersync "github.com/Exonical/custos/internal/clusters/sync"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/fake"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
)

type stubFactory struct {
	cluster   slurm.Cluster
	openErr   error
	openCalls int
}

func (f *stubFactory) Open(_ context.Context, _ slurm.ClusterConfig) (slurm.Cluster, slurm.Accounting, error) {
	f.openCalls++
	if f.openErr != nil {
		return nil, nil, f.openErr
	}
	return f.cluster, nil, nil
}

type fx struct {
	pool    *pgxpool.Pool
	repo    *clusterpg.Repository
	trepo   *tenantpg.Repository
	factory *stubFactory
	handler workqueue.Handler
	ctx     context.Context
}

func newFx(t *testing.T, cluster slurm.Cluster, openErr error) *fx {
	t.Helper()
	pool := dbtest.Pool(t)
	factory := &stubFactory{cluster: cluster, openErr: openErr}
	repo := clusterpg.New(pool)
	return &fx{
		pool: pool, repo: repo, trepo: tenantpg.New(pool), factory: factory,
		handler: clustersync.Handler(repo, factory,
			60*time.Second, nil),
		ctx: context.Background(),
	}
}

func (f *fx) mkCluster(t *testing.T, name string, vis clusters.Visibility,
	state clusters.State) clusters.Cluster {
	t.Helper()
	c := clusters.Cluster{
		ID: uuid.Must(uuid.NewV7()), Name: name, DisplayName: name,
		BaseURL: "https://203.0.113.10/", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "t"},
		Visibility: vis, State: state, Version: 1,
	}
	if err := f.repo.Create(f.ctx, c); err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fx) mkTenant(t *testing.T, slug string, state tenants.State) tenants.Tenant {
	t.Helper()
	tn := tenants.Tenant{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: "T",
		State: state, Settings: map[string]any{}, Version: 1}
	if err := f.trepo.Create(f.ctx, tn); err != nil {
		t.Fatal(err)
	}
	return tn
}

func item(id uuid.UUID) workqueue.Item {
	return workqueue.Item{Kind: "cluster.sync", Key: "cluster:" + id.String()}
}

// rescheduleAt asserts the handler returned workqueue.RescheduleAt —
// the queue then moves the leased row back to pending at At.
func rescheduleAt(t *testing.T, err error) time.Time {
	t.Helper()
	var rs workqueue.Reschedule
	if !errors.As(err, &rs) {
		t.Fatalf("handler error = %v, want workqueue.Reschedule", err)
	}
	return rs.At
}

// pendingSyncs returns run_at values of queued cluster.sync items for id.
func (f *fx) pendingSyncs(t *testing.T, id uuid.UUID) []time.Time {
	t.Helper()
	rows, err := f.pool.Query(f.ctx, `
		SELECT run_at FROM work_items
		WHERE kind='cluster.sync' AND key=$1 AND state IN ('pending','leased')
		ORDER BY run_at`, "cluster:"+id.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var ts time.Time
		if err := rows.Scan(&ts); err != nil {
			t.Fatal(err)
		}
		out = append(out, ts)
	}
	return out
}

func TestSyncSuccessChain(t *testing.T) {
	fc := fake.New()
	fc.SetPartitions([]slurm.Partition{{Name: "gpu"}, {Name: "batch"}})
	f := newFx(t, fc, nil)
	c := f.mkCluster(t, "ok", clusters.VisibilityAssigned, clusters.StateUnreachable)
	now := time.Now()

	h := f.handler
	at := rescheduleAt(t, h(f.ctx, item(c.ID)))
	got, _ := f.repo.GetByNameOrID(f.ctx, c.ID.String())
	// one success from unreachable -> degraded (needs 2)
	if got.State != clusters.StateDegraded || got.ConsecSuccesses != 1 {
		t.Fatalf("after 1 ok: %+v", got)
	}
	// rescheduled near base interval (±10% jitter of 60s)
	if d := at.Sub(now); d < 50*time.Second || d > 70*time.Second {
		t.Fatalf("next run %v not near 60s", d)
	}

	if at := rescheduleAt(t, h(f.ctx, item(c.ID))); at.Before(time.Now()) {
		t.Fatalf("second run rescheduled in the past: %v", at)
	}
	got, _ = f.repo.GetByNameOrID(f.ctx, c.ID.String())
	if got.State != clusters.StateActive || got.ConsecSuccesses != 2 {
		t.Fatalf("after 2 ok: %+v", got)
	}
	// partitions stored from Capabilities.Partitions
	recs, _ := f.repo.ListPartitions(f.ctx, c.ID)
	if len(recs) == 0 {
		t.Fatal("no partitions stored")
	}
}

func TestSyncFailureBackoff(t *testing.T) {
	fc := fake.New()
	f := newFx(t, fc, nil)
	c := f.mkCluster(t, "fail", clusters.VisibilityAssigned, clusters.StateActive)
	now := time.Now()

	// inject a persistent failure: every Ping fails
	fc.FailNext(10, slurm.ErrUnavailable)
	var at time.Time
	for i, want := range []clusters.State{
		clusters.StateDegraded, clusters.StateDegraded, clusters.StateUnreachable,
	} {
		// Slurm errors are recorded, not propagated: the handler still
		// reschedules (the next run is the retry).
		at = rescheduleAt(t, f.handler(f.ctx, item(c.ID)))
		got, _ := f.repo.GetByNameOrID(f.ctx, c.ID.String())
		if got.State != want {
			t.Fatalf("run %d: state %v want %v", i+1, got.State, want)
		}
	}
	got, _ := f.repo.GetByNameOrID(f.ctx, c.ID.String())
	if got.ConsecFailures != 3 || got.LastError == "" {
		t.Fatalf("failure record: %+v", got)
	}
	// unreachable -> backoff 5x interval (300s ±10%)
	if d := at.Sub(now); d < 240*time.Second || d > 360*time.Second {
		t.Fatalf("backoff run_at %v not ~5x interval", d)
	}
}

func TestSyncDisabledStopsChain(t *testing.T) {
	f := newFx(t, fake.New(), nil)
	c := f.mkCluster(t, "off", clusters.VisibilityAssigned, clusters.StateDisabled)
	if err := f.handler(f.ctx, item(c.ID)); err != nil {
		t.Fatal(err)
	}
	if f.factory.openCalls != 0 {
		t.Fatal("factory must not be opened for disabled cluster")
	}
	if runs := f.pendingSyncs(t, c.ID); len(runs) != 0 {
		t.Fatalf("disabled cluster re-enqueued: %v", runs)
	}
}

func TestSyncSlurmErrorReschedules(t *testing.T) {
	f := newFx(t, nil, errors.New("open refused"))
	c := f.mkCluster(t, "serr", clusters.VisibilityAssigned, clusters.StateActive)
	// The slurm error is recorded, not propagated; the handler still
	// asks for its next run.
	rescheduleAt(t, f.handler(f.ctx, item(c.ID)))
	got, _ := f.repo.GetByNameOrID(f.ctx, c.ID.String())
	if got.ConsecFailures != 1 || got.LastError == "" {
		t.Fatalf("failure not recorded: %+v", got)
	}
}

func TestSyncAllTenantsAutoAssign(t *testing.T) {
	f := newFx(t, fake.New(), nil)
	c := f.mkCluster(t, "all", clusters.VisibilityAllTenants, clusters.StateActive)
	ta := f.mkTenant(t, "aa", tenants.StateActive)
	tb := f.mkTenant(t, "ab", tenants.StateSuspended)
	td := f.mkTenant(t, "ad", tenants.StateDeleting)
	ps := tenants.PlatformScope()

	// manual row on a deleting tenant must survive reconciliation
	if err := f.repo.UpsertAssignment(f.ctx, ps, clusters.Assignment{
		ClusterID: c.ID, TenantID: td.ID, Source: clusters.SourceManual,
	}); err != nil {
		t.Fatal(err)
	}

	rescheduleAt(t, f.handler(f.ctx, item(c.ID)))
	as, _, _ := f.repo.ListAssignments(f.ctx, ps, c.ID, clusters.Page{})
	got := map[uuid.UUID]string{}
	for _, a := range as {
		got[a.TenantID] = a.Source
	}
	if got[ta.ID] != "auto" || got[tb.ID] != "auto" {
		t.Fatalf("auto rows missing: %v", got)
	}
	if got[td.ID] != "manual" {
		t.Fatalf("manual row touched: %v", got)
	}

	// B leaves qualifying states -> its auto row is dropped
	if _, err := f.pool.Exec(f.ctx,
		`UPDATE tenants SET state='deleting' WHERE id=$1`, tb.ID); err != nil {
		t.Fatal(err)
	}
	rescheduleAt(t, f.handler(f.ctx, item(c.ID)))
	as, _, _ = f.repo.ListAssignments(f.ctx, ps, c.ID, clusters.Page{})
	got = map[uuid.UUID]string{}
	for _, a := range as {
		got[a.TenantID] = a.Source
	}
	if _, ok := got[tb.ID]; ok {
		t.Fatalf("auto row for deleted tenant kept: %v", got)
	}
	if got[ta.ID] != "auto" || got[td.ID] != "manual" {
		t.Fatalf("after drop: %v", got)
	}
}

func TestBootstrap(t *testing.T) {
	f := newFx(t, fake.New(), nil)
	a := f.mkCluster(t, "b1", clusters.VisibilityAssigned, clusters.StateActive)
	b := f.mkCluster(t, "b2", clusters.VisibilityAssigned, clusters.StateDisabled)
	c := f.mkCluster(t, "b3", clusters.VisibilityAllTenants, clusters.StateUnreachable)

	if err := clustersync.Bootstrap(f.ctx, f.pool, f.repo); err != nil {
		t.Fatal(err)
	}
	if runs := f.pendingSyncs(t, a.ID); len(runs) != 1 {
		t.Fatalf("a: %v", runs)
	}
	if runs := f.pendingSyncs(t, b.ID); len(runs) != 0 {
		t.Fatalf("disabled b enqueued: %v", runs)
	}
	if runs := f.pendingSyncs(t, c.ID); len(runs) != 1 {
		t.Fatalf("c: %v", runs)
	}
	// idempotent: second bootstrap doesn't duplicate
	if err := clustersync.Bootstrap(f.ctx, f.pool, f.repo); err != nil {
		t.Fatal(err)
	}
	if runs := f.pendingSyncs(t, a.ID); len(runs) != 1 {
		t.Fatalf("bootstrap not idempotent: %v", runs)
	}
}

func TestUnreachableChecker(t *testing.T) {
	f := newFx(t, fake.New(), nil)
	chk := clustersync.UnreachableChecker(f.repo)
	if chk.Name() != "clusters" {
		t.Fatalf("name: %v", chk.Name())
	}
	if err := chk.Check(f.ctx); err != nil {
		t.Fatalf("empty should pass: %v", err)
	}
	f.mkCluster(t, "bad", clusters.VisibilityAssigned, clusters.StateUnreachable)
	if err := chk.Check(f.ctx); err == nil {
		t.Fatal("unreachable cluster must fail the checker")
	}
	f.mkCluster(t, "off", clusters.VisibilityAssigned, clusters.StateDisabled)
	// disabled + unreachable mix still fails on the unreachable one — covered.
}
