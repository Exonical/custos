package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/jobs"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	worker "github.com/Exonical/custos/internal/jobs/worker"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/fake"
	"github.com/Exonical/custos/internal/tenants"
)

type optimisticConflictRepo struct {
	jobs.Repository
	attempts int
}

func (r *optimisticConflictRepo) Transition(context.Context, tenants.Scope, uuid.UUID, int, jobs.Patch) (jobs.Job, error) {
	return jobs.Job{}, apperr.New(apperr.Conflict, "VERSION_CONFLICT", "job was modified concurrently")
}

func (r *optimisticConflictRepo) RecordAdoptVersionConflict(context.Context, uuid.UUID) (int, error) {
	r.attempts++
	return r.attempts, nil
}

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

	j := mkJob(t, tid, pid, cid, uid)
	fc := fake.New()
	ref, err := fc.SubmitJob(ctx, slurm.JobSubmission{Name: "custos-" + j.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	fc.Advance(ref.ID.ID, slurm.JobRunning)

	j.State = jobs.StateQueued
	sid := int64(ref.ID.ID)
	j.SlurmJobID = &sid
	submitted := time.Now().UTC()
	j.SubmittedAt = &submitted
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

func TestReconcileDoesNotApplyReusedForeignSlurmJob(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := jobpg.New(pool)
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "foreign-id-tenant"), mkUser(t, pool, "foreign-id-user")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "foreign-id-cluster")
	fc := fake.New()
	ref, err := fc.SubmitJob(ctx, slurm.JobSubmission{Name: "external-job"})
	if err != nil {
		t.Fatal(err)
	}
	fc.Advance(ref.ID.ID, slurm.JobRunning)
	j := mkJob(t, tid, pid, cid, uid)
	j.State = jobs.StateQueued
	sid := int64(ref.ID.ID)
	j.SlurmJobID = &sid
	j.SlurmState = string(slurm.JobPending)
	now := time.Now().UTC()
	j.SubmittedAt = &now
	if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
		t.Fatal(err)
	}
	d := worker.Deps{Jobs: repo, Clusters: clusterpg.New(pool), Factory: fakeFactory{fc}, Exec: pool}
	err = worker.Reconcile(d)(ctx, workqueue.Item{Kind: worker.KindReconcile, Key: "job:" + j.ID.String()})
	var reschedule workqueue.Reschedule
	if !errors.As(err, &reschedule) {
		t.Fatalf("reconcile result=%v, want retry after foreign-name guard", err)
	}
	got, err := repo.Get(ctx, tenants.TenantScope(tid), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != jobs.StateQueued || got.SlurmState != string(slurm.JobPending) || got.StartedAt != nil || got.EndedAt != nil {
		t.Fatalf("foreign job state was applied: %+v", got)
	}
}

func TestSubmitUniqueSlurmIDConflictUsesNormalBackoff(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := jobpg.New(pool)
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "duplicate-adopt-tenant"), mkUser(t, pool, "duplicate-adopt-user")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "duplicate-adopt-cluster")
	sid := int64(1)
	active := mkJob(t, tid, pid, cid, uid)
	active.State = jobs.StateQueued
	active.SlurmJobID = &sid
	if err := repo.Create(ctx, tenants.TenantScope(tid), active, nil); err != nil {
		t.Fatal(err)
	}
	j := mkJob(t, tid, pid, cid, uid)
	j.State = jobs.StateSubmitting
	if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
		t.Fatal(err)
	}
	fc := fake.New()
	fc.SetJobRecords([]slurm.JobRecord{{ID: slurm.JobID{ID: uint32(sid)}, Name: "custos-" + j.ID.String(), State: slurm.JobPending, EndTime: time.Now().UTC()}})
	d := worker.Deps{Jobs: repo, Clusters: clusterpg.New(pool), Factory: fakeFactory{fc}, Exec: pool}
	payload := []byte(fmt.Sprintf(`{"job_id":%q}`, j.ID.String()))
	err := worker.Submit(d)(ctx, workqueue.Item{Kind: "job.submit", Key: "job:" + j.ID.String(), Payload: payload})
	got, getErr := repo.Get(ctx, tenants.TenantScope(tid), j.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	var reschedule workqueue.Reschedule
	if err == nil || errors.As(err, &reschedule) || !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("unique conflict result=%v submissions=%d job=%+v; want conflict error for normal queue backoff", err, len(fc.Submissions()), got)
	}
	if got.State != jobs.StateSubmitting || got.SlurmJobID != nil {
		t.Fatalf("failed adopt mutated job: %+v", got)
	}
}

func TestSubmitAdoptionVersionConflictCeiling(t *testing.T) {
	pool := dbtest.Pool(t)
	base := jobpg.New(pool)
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "adopt-ceiling-tenant"), mkUser(t, pool, "adopt-ceiling-user")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "adopt-ceiling-cluster")
	j := mkJob(t, tid, pid, cid, uid)
	j.State = jobs.StateSubmitting
	if err := base.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
		t.Fatal(err)
	}
	fc := fake.New()
	fc.SetJobRecords([]slurm.JobRecord{{ID: slurm.JobID{ID: 91}, Name: "custos-" + j.ID.String(), State: slurm.JobCompleted, EndTime: time.Now().UTC()}})
	conflicts := &optimisticConflictRepo{Repository: base}
	d := worker.Deps{Jobs: conflicts, Clusters: clusterpg.New(pool), Factory: fakeFactory{fc}, Exec: pool}
	h := worker.Submit(d)
	item := workqueue.Item{Kind: "job.submit", Key: "job:" + j.ID.String()}
	for i := 1; i <= 10; i++ {
		err := h(ctx, item)
		var reschedule workqueue.Reschedule
		if !errors.As(err, &reschedule) {
			t.Fatalf("optimistic attempt %d result=%v, want reschedule", i, err)
		}
	}
	err := h(ctx, item)
	var reschedule workqueue.Reschedule
	if err == nil || errors.As(err, &reschedule) || !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("attempt after retry ceiling=%v, want conflict for queue backoff", err)
	}
	if conflicts.attempts != 11 {
		t.Fatalf("recorded optimistic attempts=%d want 11", conflicts.attempts)
	}
}
