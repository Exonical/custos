package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/jobs"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	worker "github.com/Exonical/custos/internal/jobs/worker"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/fake"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
)

type cancelDuringSubmitCluster struct {
	slurm.Cluster
	beforeSubmit func(context.Context) error
}

func (c *cancelDuringSubmitCluster) SubmitJob(ctx context.Context,
	req slurm.JobSubmission) (slurm.JobRef, error) {
	if c.beforeSubmit != nil {
		beforeSubmit := c.beforeSubmit
		c.beforeSubmit = nil
		if err := beforeSubmit(ctx); err != nil {
			return slurm.JobRef{}, err
		}
	}
	return c.Cluster.SubmitJob(ctx, req)
}

type cancelDuringSubmitFactory struct {
	cluster    *cancelDuringSubmitCluster
	accounting slurm.Accounting
}

func (f cancelDuringSubmitFactory) Open(context.Context, slurm.ClusterConfig) (
	slurm.Cluster, slurm.Accounting, error,
) {
	return f.cluster, f.accounting, nil
}

func commandJob(t *testing.T, tid, pid, cid, uid uuid.UUID) jobs.Job {
	t.Helper()
	j := mkJob(t, tid, pid, cid, uid)
	j.ScriptDigest = validation.Digest{}
	j.ScriptLanguage = ""
	j.ExecutionSpec.Payload = admission.PayloadRef{}
	j.ExecutionSpec.Argv = []admission.ArgvElement{{Literal: "true"}}
	if err := j.ExecutionSpec.Freeze(); err != nil {
		t.Fatal(err)
	}
	j.ExecutionSpecDigest = j.ExecutionSpec.Digest
	return j
}

func TestSubmittingCancelRacingSubmitCancelsOrphan(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	repo := jobpg.New(pool)
	tid, uid := mkTenant(t, pool, "orphan-race-a"), mkUser(t, pool, "orphan-race-u")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "orphan-race-c")
	j := commandJob(t, tid, pid, cid, uid)
	if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
		t.Fatal(err)
	}
	fc := fake.New()
	recorder := &captureAudit{}
	cancelDeps := worker.Deps{Jobs: repo, Exec: pool, Audit: recorder}
	cluster := &cancelDuringSubmitCluster{
		Cluster: fc,
		beforeSubmit: func(ctx context.Context) error {
			return worker.Cancel(cancelDeps)(ctx, workqueue.Item{
				Kind: "job.cancel", Key: "job:" + j.ID.String(),
				Payload: json.RawMessage(`{"job_id":"` + j.ID.String() + `"}`),
			})
		},
	}
	deps := worker.Deps{
		Jobs: repo, Clusters: clusterpg.New(pool),
		Factory: cancelDuringSubmitFactory{cluster: cluster, accounting: fc},
		Exec:    pool, Audit: recorder, Metrics: worker.NewMetrics(nil, repo),
	}
	err := worker.Submit(deps)(ctx, workqueue.Item{
		Kind: "job.submit", Key: "job:" + j.ID.String(),
		Payload: json.RawMessage(`{"job_id":"` + j.ID.String() + `"}`),
	})
	if err != nil {
		t.Fatalf("submit with racing cancel: %v", err)
	}
	got, err := repo.Get(ctx, tenants.TenantScope(tid), j.ID)
	if err != nil || got.State != jobs.StateCanceled {
		t.Fatalf("Custos job after race = %+v err=%v, want CANCELED", got, err)
	}
	slurmJob, err := fc.GetJob(ctx, slurm.JobID{ID: 1})
	if err != nil || slurmJob.State != slurm.JobCancelled {
		t.Fatalf("orphan Slurm job after race = %+v err=%v, want CANCELED", slurmJob, err)
	}
	if len(recorder.events) != 1 ||
		recorder.events[0].Action != "job.orphan_canceled" ||
		recorder.events[0].Details["slurm_job_id"] != uint32(1) {
		t.Fatalf("orphan cancellation audit = %+v", recorder.events)
	}
	var sweeps int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM work_items
		WHERE kind=$1 AND key LIKE $2`,
		worker.KindSweepCanceled, "job:"+j.ID.String()+":orphan:%").Scan(&sweeps); err != nil {
		t.Fatal(err)
	}
	if sweeps != 2 {
		t.Fatalf("scheduled orphan sweeps = %d, want two", sweeps)
	}
}

func TestSweepCanceledFindsAndCancelsLiveOrphan(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	repo := jobpg.New(pool)
	tid, uid := mkTenant(t, pool, "orphan-sweep-a"), mkUser(t, pool, "orphan-sweep-u")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "orphan-sweep-c")
	j := commandJob(t, tid, pid, cid, uid)
	if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
		t.Fatal(err)
	}
	canceled, reason, endedAt := jobs.StateCanceled, "CANCELED", time.Now().UTC()
	j, err := repo.Transition(ctx, tenants.TenantScope(tid), j.ID, j.Version,
		jobs.Patch{State: &canceled, Reason: &reason, EndedAt: &endedAt})
	if err != nil {
		t.Fatal(err)
	}
	fc := fake.New()
	ref, err := fc.SubmitJob(ctx, slurm.JobSubmission{
		Name:    "custos-" + j.ID.String(),
		Comment: "custos:" + j.ID.String() + "/" + j.ExecutionSpec.TaskName,
	})
	if err != nil {
		t.Fatal(err)
	}
	fc.Advance(ref.ID.ID, slurm.JobRunning)
	recorder := &captureAudit{}
	deps := worker.Deps{
		Jobs: repo, Clusters: clusterpg.New(pool), Factory: fakeFactory{fc},
		Exec: pool, Audit: recorder, Metrics: worker.NewMetrics(nil, repo),
	}
	err = worker.SweepCanceled(deps)(ctx, workqueue.Item{
		Kind: worker.KindSweepCanceled, Key: "job:" + j.ID.String() + ":orphan:30s",
		Payload: json.RawMessage(`{"job_id":"` + j.ID.String() + `"}`),
	})
	if err != nil {
		t.Fatalf("sweep canceled orphan: %v", err)
	}
	got, err := fc.GetJob(ctx, ref.ID)
	if err != nil || got.State != slurm.JobCancelled {
		t.Fatalf("swept Slurm job = %+v err=%v, want CANCELED", got, err)
	}
	if len(recorder.events) != 1 ||
		recorder.events[0].Action != "job.orphan_canceled" ||
		recorder.events[0].Details["slurm_job_id"] != ref.ID.ID {
		t.Fatalf("orphan sweep audit = %+v", recorder.events)
	}
}

func TestSweepCanceledIsNoopForMissingOrNonCanceledJobs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		slug      string
		seedSlurm bool
	}{
		{name: "not found", slug: "not-found"},
		{name: "job not canceled", slug: "not-canceled", seedSlurm: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := dbtest.Pool(t)
			ctx := context.Background()
			repo := jobpg.New(pool)
			tid, uid := mkTenant(t, pool, "orphan-noop-"+tc.slug), mkUser(t, pool, "orphan-noop-u-"+tc.slug)
			pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "orphan-noop-c-"+tc.slug)
			j := commandJob(t, tid, pid, cid, uid)
			var fc *fake.Cluster
			var slurmID uint32
			if tc.seedSlurm {
				fc = fake.New()
				ref, err := fc.SubmitJob(ctx, slurm.JobSubmission{
					Name:    "custos-" + j.ID.String(),
					Comment: "custos:" + j.ID.String() + "/" + j.ExecutionSpec.TaskName,
				})
				if err != nil {
					t.Fatal(err)
				}
				slurmID = ref.ID.ID
				fc.Advance(ref.ID.ID, slurm.JobRunning)
				sid := int64(ref.ID.ID)
				state, slurmState, submittedAt := jobs.StateQueued,
					string(slurm.JobRunning), time.Now().UTC()
				if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.Transition(ctx, tenants.TenantScope(tid),
					j.ID, j.Version, jobs.Patch{
						State: &state, SlurmJobID: &sid,
						SlurmState: &slurmState, SubmittedAt: &submittedAt,
					}); err != nil {
					t.Fatal(err)
				}
			} else {
				fc = fake.New()
				if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
					t.Fatal(err)
				}
				canceled, reason, endedAt := jobs.StateCanceled,
					"CANCELED", time.Now().UTC()
				if _, err := repo.Transition(ctx, tenants.TenantScope(tid),
					j.ID, j.Version, jobs.Patch{
						State: &canceled, Reason: &reason, EndedAt: &endedAt,
					}); err != nil {
					t.Fatal(err)
				}
			}
			recorder := &captureAudit{}
			err := worker.SweepCanceled(worker.Deps{
				Jobs: repo, Clusters: clusterpg.New(pool),
				Factory: fakeFactory{fc}, Exec: pool, Audit: recorder,
				Metrics: worker.NewMetrics(nil, repo),
			})(ctx, workqueue.Item{
				Kind:    worker.KindSweepCanceled,
				Key:     "job:" + j.ID.String() + ":orphan:5m",
				Payload: json.RawMessage(`{"job_id":"` + j.ID.String() + `"}`),
			})
			if err != nil {
				t.Fatalf("sweep no-op: %v", err)
			}
			if len(recorder.events) != 0 {
				t.Fatalf("unexpected orphan audit events: %+v", recorder.events)
			}
			if tc.seedSlurm {
				got, err := fc.GetJob(ctx, slurm.JobID{ID: slurmID})
				if err != nil || got.State != slurm.JobRunning {
					t.Fatalf("non-canceled Job changed: %+v err=%v", got, err)
				}
			}
		})
	}
}
