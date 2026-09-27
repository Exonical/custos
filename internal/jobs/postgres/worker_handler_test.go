package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/jobs"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	worker "github.com/Exonical/custos/internal/jobs/worker"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/fake"
	"github.com/Exonical/custos/internal/tenants"
)

// job.reconcile is a self-cadencing handler: while the leased item is
// still 'leased', an Enqueue with the same (kind,key) is deduped to
// nothing. It must return workqueue.RescheduleAt instead.
func TestReconcileReschedulesWhenSubmitInFlight(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := jobpg.New(pool)
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "rc-a"), mkUser(t, pool, "rc-u")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "rc-c")

	j := mkJob(t, tid, pid, cid, uid) // SUBMITTING, no slurm_job_id yet
	if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
		t.Fatal(err)
	}

	d := worker.Deps{Jobs: repo, Exec: pool}
	h := worker.Reconcile(d)
	before := time.Now()
	err := h(ctx, workqueue.Item{
		Kind: worker.KindReconcile, Key: "job:" + j.ID.String()})
	var rs workqueue.Reschedule
	if !errors.As(err, &rs) {
		t.Fatalf("reconcile error = %v, want workqueue.Reschedule", err)
	}
	if lo, hi := before.Add(25*time.Second), time.Now().Add(35*time.Second); rs.At.Before(lo) || rs.At.After(hi) {
		t.Fatalf("reschedule at %v, want ~now+30s", rs.At)
	}
}

// The observed-state path reschedules the leased reconcile item itself
// with the age-based cadence (5s for a fresh job).
func TestReconcileReschedulesObserved(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := jobpg.New(pool)
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "ro-a"), mkUser(t, pool, "ro-u")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "ro-c")

	fc := fake.New()
	ref, err := fc.SubmitJob(ctx, slurm.JobSubmission{Name: "custos-x"})
	if err != nil {
		t.Fatal(err)
	}
	fc.Advance(ref.ID.ID, slurm.JobRunning)

	j := mkJob(t, tid, pid, cid, uid)
	j.State = jobs.StateQueued
	sid := int64(ref.ID.ID)
	j.SlurmJobID = &sid
	if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
		t.Fatal(err)
	}

	d := worker.Deps{
		Jobs: repo, Clusters: clusterpg.New(pool),
		Factory: fakeFactory{fc}, Exec: pool,
	}
	h := worker.Reconcile(d)
	before := time.Now()
	err = h(ctx, workqueue.Item{
		Kind: worker.KindReconcile, Key: "job:" + j.ID.String()})
	var rs workqueue.Reschedule
	if !errors.As(err, &rs) {
		t.Fatalf("reconcile error = %v, want workqueue.Reschedule", err)
	}
	if lo, hi := before.Add(3*time.Second), time.Now().Add(8*time.Second); rs.At.Before(lo) || rs.At.After(hi) {
		t.Fatalf("reschedule at %v, want ~now+5s", rs.At)
	}
}

func TestSweepPersistsDiscoveredSlurmIDForCompletedJob(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := jobpg.New(pool)
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "sweep-adopt-a"), mkUser(t, pool, "sweep-adopt-u")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "sweep-adopt-c")
	j := mkJob(t, tid, pid, cid, uid)
	j.State = jobs.StateSubmitting
	j.SlurmJobID = nil
	if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
		t.Fatal(err)
	}

	fc := fake.New()
	ref, err := fc.SubmitJob(ctx, slurm.JobSubmission{Name: "custos-" + j.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	fc.Advance(ref.ID.ID, slurm.JobCompleted)
	d := worker.Deps{Jobs: repo, Clusters: clusterpg.New(pool), Factory: fakeFactory{fc}, Exec: pool}
	if err := worker.Sweep(d)(ctx, workqueue.Item{Kind: worker.KindSweep, Key: "sweep:" + cid.String()}); err == nil {
		t.Fatal("sweep did not reschedule")
	} else {
		var rs workqueue.Reschedule
		if !errors.As(err, &rs) {
			t.Fatalf("sweep result=%v, want reschedule", err)
		}
	}
	got, err := repo.Get(ctx, tenants.TenantScope(tid), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != jobs.StateCompleted || got.SlurmJobID == nil || *got.SlurmJobID != int64(ref.ID.ID) {
		t.Fatalf("adopted job=%+v", got)
	}
}

func TestSubmitAdoptsCompletedAccountingRecord(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := jobpg.New(pool)
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "submit-adopt-a"), mkUser(t, pool, "submit-adopt-u")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "submit-adopt-c")
	j := mkJob(t, tid, pid, cid, uid)
	j.State = jobs.StateSubmitting
	j.SlurmJobID = nil
	if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
		t.Fatal(err)
	}
	fc := fake.New()
	sid := uint32(91)
	fc.SetJobRecords([]slurm.JobRecord{{ID: slurm.JobID{ID: sid}, Name: "custos-" + j.ID.String(), State: slurm.JobCompleted, EndTime: time.Now().UTC()}})
	d := worker.Deps{Jobs: repo, Clusters: clusterpg.New(pool), Factory: fakeFactory{fc}, Exec: pool, Metrics: worker.NewMetrics(nil, repo)}
	payload := []byte(fmt.Sprintf(`{"job_id":%q}`, j.ID.String()))
	if err := worker.Submit(d)(ctx, workqueue.Item{Kind: "job.submit", Key: "job:" + j.ID.String(), Payload: payload}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, tenants.TenantScope(tid), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != jobs.StateCompleted || got.SlurmJobID == nil || *got.SlurmJobID != int64(sid) {
		t.Fatalf("adopted job=%+v", got)
	}
}
