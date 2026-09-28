package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/accounting"
	accountpg "github.com/Exonical/custos/internal/accounting/postgres"
	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/jobs"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
func seed(t *testing.T, p *pgxpool.Pool, slug string) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tid, uid, pid := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, e := p.Exec(ctx, `INSERT INTO users(id,issuer,subject,kind) VALUES($1,'i',$2,'user')`, uid, uid.String()); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Exec(ctx, `INSERT INTO tenants(id,slug,name,state,settings) VALUES($1,$2,'t','active','{}')`, tid, slug); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Exec(ctx, `INSERT INTO projects(id,tenant_id,slug,name,state,settings) VALUES($1,$2,$3,'p','active','{}')`, pid, tid, "p-"+slug); e != nil {
		t.Fatal(e)
	}
	return tid, uid, pid
}
func TestAttributionAggregateAndRLS(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	ta, ua, pa := seed(t, pool, "acct-a")
	tb, _, pb := seed(t, pool, "acct-b")
	pc := uuid.Must(uuid.NewV7())
	if _, e = pool.Exec(ctx, `INSERT INTO projects(id,tenant_id,slug,name,state,settings) VALUES($1,$2,'p-shared','p','active','{}')`, pc, ta); e != nil {
		t.Fatal(e)
	}
	c := clusters.Cluster{ID: uuid.Must(uuid.NewV7()), Name: "acct-c", DisplayName: "c", BaseURL: "https://example", APIVersion: "v0.0.45", IdentityMode: clusters.IdentityService, ServiceUser: "custos", TokenRef: secrets.Reference{Provider: "file", Path: "x"}, Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1}
	if e = clusterpg.New(pool).Create(ctx, c); e != nil {
		t.Fatal(e)
	}
	for _, tid := range []uuid.UUID{ta, tb} {
		if _, e = pool.Exec(ctx, `INSERT INTO cluster_tenant_assignments(cluster_id,tenant_id,source,defaults) VALUES($1,$2,'manual','{}')`, c.ID, tid); e != nil {
			t.Fatal(e)
		}
	}
	for _, x := range []struct {
		tid, pid uuid.UUID
		account  string
	}{{ta, pa, "unique"}, {ta, pc, "shared"}, {tb, pb, "shared"}} {
		if _, e = pool.Exec(ctx, `INSERT INTO project_cluster_bindings(id,tenant_id,project_id,cluster_id,slurm_account,enabled) VALUES($1,$2,$3,$4,$5,true)`, uuid.Must(uuid.NewV7()), x.tid, x.pid, c.ID, x.account); e != nil {
			t.Fatal(e)
		}
	}
	sid := int64(101)
	jobID := uuid.Must(uuid.NewV7())
	j := jobs.Job{ID: jobID, TenantID: ta, ProjectID: pa, ClusterID: c.ID, CreatedBy: ua, Name: "custos-" + jobID.String(), State: jobs.StateCompleted, SlurmJobID: &sid, ResourceRequest: workflowspec.Resources{}, ExecutionSpec: admission.ExecutionSpec{ID: uuid.New(), TenantID: ta}, ExecutionSpecDigest: validation.DigestOf([]byte("s")), Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if e = jobpg.New(pool).Create(ctx, tenants.PlatformScope(), j, nil); e != nil {
		t.Fatal(e)
	}
	end := time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC)
	records := []accounting.Record{{ID: uuid.Must(uuid.NewV7()), SlurmJobID: 101, SlurmJobName: "custos-" + jobID.String(), Account: "none", EndTime: end, ElapsedSeconds: 60, CPUSeconds: 120, NodeSeconds: 60, CollectedAt: time.Now(), State: "COMPLETED"}, {ID: uuid.Must(uuid.NewV7()), SlurmJobID: 102, SlurmJobName: "external", Account: "unique", EndTime: end, ElapsedSeconds: 30, CPUSeconds: 30, NodeSeconds: 30, CollectedAt: time.Now(), State: "FAILED", WaitSeconds: ptr(10)}, {ID: uuid.Must(uuid.NewV7()), SlurmJobID: 103, SlurmJobName: "amb", Account: "shared", EndTime: end, ElapsedSeconds: 10, CollectedAt: time.Now(), State: "COMPLETED"}}
	repo := accountpg.New(pool)
	res, e := repo.Store(ctx, c.ID, records, end)
	if e != nil {
		t.Fatal(e)
	}
	if res.Inserted != 3 || res.UnattributedAmbiguous != 1 {
		t.Fatalf("store=%+v", res)
	}
	var correlated, unique, amb bool
	if e = pool.QueryRow(ctx, `SELECT bool_or(job_id=$1),bool_or(project_id=$2 AND job_id IS NULL),bool_or(tenant_id IS NULL) FROM usage_records`, j.ID, pa).Scan(&correlated, &unique, &amb); e != nil || !correlated || !unique || !amb {
		t.Fatalf("attribution %v %v %v %v", correlated, unique, amb, e)
	}
	if n, e := repo.AggregateDirty(ctx, 10); e != nil || n != 1 {
		t.Fatalf("aggregate %d %v", n, e)
	}
	var jobsN, cpu int64
	if e = pool.QueryRow(ctx, `SELECT sum(jobs),sum(cpu_seconds) FROM usage_daily WHERE tenant_id=$1`, ta).Scan(&jobsN, &cpu); e != nil || jobsN != 2 || cpu != 150 {
		t.Fatalf("daily %d %d %v", jobsN, cpu, e)
	}
	app := dbtest.AppPoolOn(t, dsn)
	defer app.Close()
	rows, e := repoOn(app).ListUsage(ctx, tenants.TenantScope(ta), accounting.UsageQuery{TenantID: ta, From: end.Add(-time.Hour), To: end.Add(24 * time.Hour), TenantWide: true})
	if e != nil || len(rows) == 0 {
		t.Fatalf("tenant rows %d %v", len(rows), e)
	}
	other, e := repoOn(app).ListUsage(ctx, tenants.TenantScope(ta), accounting.UsageQuery{TenantID: tb, From: end.Add(-time.Hour), To: end.Add(24 * time.Hour), TenantWide: true})
	if e != nil || len(other) != 0 {
		t.Fatalf("cross tenant rows %d %v", len(other), e)
	}
}
func TestAttributionUsesNameWhenSlurmIDIsReused(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	tenantID, userID, projectID := seed(t, pool, "reused-slurm-id")
	cluster := clusters.Cluster{ID: uuid.Must(uuid.NewV7()), Name: "reused-slurm-id", DisplayName: "reused Slurm ID", BaseURL: "https://example", APIVersion: "v0.0.45", IdentityMode: clusters.IdentityService, ServiceUser: "custos", TokenRef: secrets.Reference{Provider: "file", Path: "x"}, Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1}
	if err := clusterpg.New(pool).Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	submits := []time.Time{now.Add(-6 * time.Minute), now.Add(-2 * time.Minute)}
	ends := []time.Time{now.Add(-4 * time.Minute), now.Add(-time.Minute)}
	jobIDs := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	sid := int64(808)
	for i, id := range jobIDs {
		j := jobs.Job{ID: id, TenantID: tenantID, ProjectID: projectID, ClusterID: cluster.ID, CreatedBy: userID, Name: "custos-" + id.String(), State: jobs.StateCompleted, SlurmJobID: &sid, ResourceRequest: workflowspec.Resources{}, ExecutionSpec: admission.ExecutionSpec{ID: uuid.New(), TenantID: tenantID}, ExecutionSpecDigest: validation.DigestOf([]byte(id.String())), SubmittedAt: &submits[i], EndedAt: &ends[i], Version: 1, CreatedAt: submits[i], UpdatedAt: ends[i]}
		if err := jobpg.New(pool).Create(ctx, tenants.PlatformScope(), j, nil); err != nil {
			t.Fatal(err)
		}
	}
	records := []accounting.Record{
		{ID: uuid.Must(uuid.NewV7()), SlurmJobID: sid, SlurmJobName: "custos-" + jobIDs[0].String(), SubmitTime: submits[0], EndTime: ends[0], CollectedAt: now, State: "COMPLETED"},
		{ID: uuid.Must(uuid.NewV7()), SlurmJobID: sid, SlurmJobName: "custos-" + jobIDs[1].String(), SubmitTime: submits[1], EndTime: ends[1], CollectedAt: now, State: "COMPLETED"},
	}
	if _, err := accountpg.New(pool).Store(ctx, cluster.ID, records, now); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT slurm_job_name,job_id FROM usage_records WHERE cluster_id=$1 AND slurm_job_id=$2 ORDER BY end_time`, cluster.ID, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i := range jobIDs {
		if !rows.Next() {
			t.Fatalf("missing usage record %d", i)
		}
		var name string
		var jobID *uuid.UUID
		if err := rows.Scan(&name, &jobID); err != nil {
			t.Fatal(err)
		}
		if jobID == nil || *jobID != jobIDs[i] || name != "custos-"+jobIDs[i].String() {
			t.Fatalf("record %q attributed to %v, want %s", name, jobID, jobIDs[i])
		}
	}
	if rows.Next() {
		t.Fatal("unexpected extra usage record")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func repoOn(p *pgxpool.Pool) *accountpg.Repository { return accountpg.New(p) }
func ptr(v int64) *int64                           { return &v }
func TestAggregateFailedMatchesRecordFailed(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	cluster := clusters.Cluster{ID: uuid.Must(uuid.NewV7()), Name: "failed-rule", DisplayName: "failed rule", BaseURL: "https://example", APIVersion: "v0.0.45", IdentityMode: clusters.IdentityService, ServiceUser: "custos", TokenRef: secrets.Reference{Provider: "file", Path: "x"}, Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1}
	if err := clusterpg.New(pool).Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	states := []slurm.JobState{slurm.JobPending, slurm.JobRunning, slurm.JobSuspended, slurm.JobCompleting, slurm.JobCompleted, slurm.JobFailed, slurm.JobCancelled, slurm.JobTimeout, slurm.JobNodeFail, slurm.JobPreempted, slurm.JobBootFail, slurm.JobDeadline, slurm.JobOutOfMemory, slurm.JobUnknown}
	zero, one := 0, 1
	end := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	var records []accounting.Record
	var wantFailed int64
	for _, state := range states {
		for _, exit := range []*int{nil, &zero, &one} {
			if accounting.Failed(state, exit) {
				wantFailed++
			}
			records = append(records, accounting.Record{ID: uuid.Must(uuid.NewV7()), SlurmJobID: int64(len(records) + 1), SlurmJobName: "external", Account: "none", State: string(state), ExitCode: exit, EndTime: end, CollectedAt: time.Now()})
		}
	}
	repo := accountpg.New(pool)
	if _, err := repo.Store(ctx, cluster.ID, records, end); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AggregateDirty(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var jobsN, failed int64
	if err := pool.QueryRow(ctx, `SELECT sum(jobs),sum(failed) FROM usage_daily WHERE cluster_id=$1`, cluster.ID).Scan(&jobsN, &failed); err != nil {
		t.Fatal(err)
	}
	if jobsN != int64(len(records)) || failed != wantFailed {
		t.Fatalf("jobs=%d failed=%d, want jobs=%d failed=%d", jobsN, failed, len(records), wantFailed)
	}
}
