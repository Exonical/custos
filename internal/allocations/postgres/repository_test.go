package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/allocations"
	allocationpg "github.com/Exonical/custos/internal/allocations/postgres"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/jobs"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestAllocationRepositoryReservationsAndRLS(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	tid, uid, pid, bid, cid := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, e = pool.Exec(ctx, `INSERT INTO users(id,issuer,subject,kind) VALUES($1,'alloc',$2,'user')`, uid, uid.String()); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO tenants(id,slug,name,state,settings) VALUES($1,$2,'t','active','{}')`, tid, "allocation-"+uid.String()[:8]); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO projects(id,tenant_id,slug,name,state,settings) VALUES($1,$2,$3,'p','active','{}')`, pid, tid, "p-"+uid.String()[:8]); e != nil {
		t.Fatal(e)
	}
	c := clusters.Cluster{ID: cid, Name: "alloc-" + uid.String()[:8], DisplayName: "alloc", BaseURL: "https://example", APIVersion: "v0.0.45", IdentityMode: clusters.IdentityService, ServiceUser: "custos", TokenRef: secrets.Reference{Provider: "file", Path: "x"}, Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1}
	if e = clusterpg.New(pool).Create(ctx, c); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO cluster_tenant_assignments(cluster_id,tenant_id,source,defaults) VALUES($1,$2,'manual','{}')`, cid, tid); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO project_cluster_bindings(id,tenant_id,project_id,cluster_id,slurm_account,enabled) VALUES($1,$2,$3,$4,'alloc-acct',true)`, bid, tid, pid, cid); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	a := allocations.Allocation{ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: pid, BindingID: bid, Name: "cpu", Unit: "cpu_hours", LimitAmount: 10, PeriodStart: now.Add(-time.Hour), PeriodEnd: now.Add(time.Hour), Enforcement: "hard", CreatedBy: uid}
	repo := allocationpg.New(pool)
	if e = repo.Create(ctx, tenants.PlatformScope(), a); e != nil {
		t.Fatal(e)
	}
	active, e := repo.Active(ctx, tenants.PlatformScope(), bid, now)
	if e != nil || len(active) != 1 {
		t.Fatalf("active allocations=%d err=%v", len(active), e)
	}
	inactive, e := repo.Active(ctx, tenants.PlatformScope(), bid, a.PeriodEnd.Add(time.Second))
	if e != nil || len(inactive) != 0 {
		t.Fatalf("inactive allocations=%d err=%v", len(inactive), e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO usage_daily(id,tenant_id,project_id,user_id,cluster_id,account,partition,day,jobs,failed,cpu_seconds,gpu_seconds,node_seconds,mem_gb_seconds,wait_seconds_sum,run_seconds_sum) VALUES($1,$2,$3,NULL,$4,'alloc-acct','debug',$5,1,0,7200,0,3600,0,0,3600)`, uuid.Must(uuid.NewV7()), tid, pid, cid, now.Format("2006-01-02")); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Refresh(ctx, nil); e != nil {
		t.Fatal(e)
	}
	var consumed float64
	if e = pool.QueryRow(ctx, `SELECT consumed_amount FROM allocations WHERE id=$1`, a.ID).Scan(&consumed); e != nil || consumed != 2 {
		t.Fatalf("refreshed consumption=%v err=%v", consumed, e)
	}
	for i, x := range []struct {
		state jobs.State
		cpu   float64
	}{{jobs.StateSubmitting, 1.25}, {jobs.StateRunning, 2.5}, {jobs.StateCompleted, 90}} {
		binding := bid
		j := jobs.Job{ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: pid, ClusterID: cid, BindingID: &binding, CreatedBy: uid, Name: "reserve" + string(rune('a'+i)), State: x.state, ResourceRequest: workflowspec.Resources{}, ExecutionSpec: admission.ExecutionSpec{ID: uuid.New(), TenantID: tid}, ExecutionSpecDigest: validation.DigestOf([]byte{byte(i + 1)}), EstimatedCost: map[string]float64{"cpu_hours": x.cpu}, Version: 1, CreatedAt: now, UpdatedAt: now}
		if e = jobpg.New(pool).Create(ctx, tenants.PlatformScope(), j, nil); e != nil {
			t.Fatal(e)
		}
	}
	app := dbtest.AppPoolOn(t, dsn)
	defer app.Close()
	appRepo := allocationpg.New(app)
	reserved, e := appRepo.InFlight(ctx, tenants.TenantScope(tid), bid)
	if e != nil {
		t.Fatal(e)
	}
	if reserved["cpu_hours"] != 3.75 {
		t.Fatalf("reserved=%v", reserved)
	}
	visible, e := appRepo.ListTenant(ctx, tenants.TenantScope(tid), tid)
	if e != nil || len(visible) != 1 {
		t.Fatalf("tenant allocations=%d err=%v", len(visible), e)
	}
	other, e := appRepo.ListTenant(ctx, tenants.TenantScope(uuid.New()), tid)
	if e != nil || len(other) != 0 {
		t.Fatalf("cross-tenant allocations=%d err=%v", len(other), e)
	}
}

func TestConcurrentAdmissionReservesExactlyOneJob(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := uuid.Must(uuid.NewV7()).String()[:8]
	tid, uid, pid, bid, cid := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,issuer,subject,kind) VALUES($1,'alloc-race',$2,'user')`, uid, uid.String()); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tenants(id,slug,name,state,settings) VALUES($1,$2,'t','active','{}')`, tid, "alloc-race-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO projects(id,tenant_id,slug,name,state,settings) VALUES($1,$2,$3,'p','active','{}')`, pid, tid, "p-race-"+suffix); err != nil {
		t.Fatal(err)
	}
	cluster := clusters.Cluster{ID: cid, Name: "alloc-race-" + suffix, DisplayName: "race", BaseURL: "https://example", APIVersion: "v0.0.45", IdentityMode: clusters.IdentityService, ServiceUser: "custos", TokenRef: secrets.Reference{Provider: "file", Path: "x"}, Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1}
	if err = clusterpg.New(pool).Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO cluster_tenant_assignments(cluster_id,tenant_id,source,defaults) VALUES($1,$2,'manual','{}')`, cid, tid); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO project_cluster_bindings(id,tenant_id,project_id,cluster_id,slurm_account,enabled) VALUES($1,$2,$3,$4,'alloc-race',true)`, bid, tid, pid, cid); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	budget := allocations.Allocation{ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: pid, BindingID: bid, Name: "single-job", Unit: "cpu_hours", LimitAmount: 1, PeriodStart: now.Add(-time.Hour), PeriodEnd: now.Add(time.Hour), Enforcement: "hard", CreatedBy: uid}
	allocationRepo := allocationpg.New(pool)
	if err = allocationRepo.Create(ctx, tenants.PlatformScope(), budget); err != nil {
		t.Fatal(err)
	}
	jobRepo := jobpg.New(pool)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id := uuid.Must(uuid.NewV7())
			binding := bid
			j := jobs.Job{ID: id, TenantID: tid, ProjectID: pid, ClusterID: cid, BindingID: &binding, CreatedBy: uid, Name: "race-" + id.String(), State: jobs.StateSubmitting, ResourceRequest: workflowspec.Resources{}, ExecutionSpec: admission.ExecutionSpec{ID: id, TenantID: tid, ProjectID: pid}, ExecutionSpecDigest: validation.DigestOf([]byte(id.String())), EstimatedCost: map[string]float64{"cpu_hours": 1}, ScriptLanguage: workflowspec.LanguageBash, Version: 1, CreatedAt: now, UpdatedAt: now}
			idem := jobs.IdemRecord{Key: "race-" + id.String(), RequestHash: sha256.Sum256([]byte(id.String())), Status: 202, Body: []byte(`{}`), ResourceID: id, ExpiresAt: now.Add(time.Hour)}
			_, e := jobRepo.CreateWithIdempotencyChecked(ctx, tenants.PlatformScope(), j, idem, func(guardCtx context.Context, tx any) error {
				checked, checkErr := allocationRepo.CheckInTx(guardCtx, tx, bid, j.EstimatedCost)
				if checkErr != nil {
					return checkErr
				}
				if checked.Denial != nil {
					return apperr.New(apperr.Validation, checked.Denial.Code, checked.Denial.Message)
				}
				return nil
			}, nil)
			results <- e
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	admitted, denied := 0, 0
	for e := range results {
		if e == nil {
			admitted++
			continue
		}
		var ae *apperr.Error
		if errors.As(e, &ae) && ae.Code == "ALLOCATION_EXHAUSTED" {
			denied++
			continue
		}
		t.Fatalf("submission error: %v", e)
	}
	if admitted != 1 || denied != 1 {
		t.Fatalf("admitted=%d denied=%d", admitted, denied)
	}
	var active int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE binding_id=$1 AND state IN('SUBMITTING','QUEUED','RUNNING')`, bid).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active jobs=%d err=%v", active, err)
	}
}
