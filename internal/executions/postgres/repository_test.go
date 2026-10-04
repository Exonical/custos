package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/executions"
	execpg "github.com/Exonical/custos/internal/executions/postgres"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/workflows"
	wfpg "github.com/Exonical/custos/internal/workflows/postgres"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func mkTenant(t *testing.T, p *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := p.Exec(context.Background(), `
		INSERT INTO tenants (id, slug, name, state, settings, version)
		VALUES ($1,$2,'T','active','{}',1)`, id, slug)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedExec builds tenant/project/workflow/published-version and a
// PENDING execution; returns the execution row.
func seedExec(t *testing.T, p *pgxpool.Pool, tid uuid.UUID,
	prefix string) executions.Execution {
	t.Helper()
	ctx := context.Background()
	uid := uuid.Must(uuid.NewV7())
	if _, err := p.Exec(ctx,
		`INSERT INTO users (id, issuer, subject, kind) VALUES ($1,'i',$2,'user')`,
		uid, prefix+"-u"); err != nil {
		t.Fatal(err)
	}
	pid := uuid.Must(uuid.NewV7())
	if _, err := p.Exec(ctx, `
		INSERT INTO projects (id, tenant_id, slug, name, state, version)
		VALUES ($1,$2,$3,'P','active',1)`, pid, tid, prefix+"-p"); err != nil {
		t.Fatal(err)
	}
	wrepo := wfpg.New(p)
	ps := tenants.PlatformScope()
	w := workflows.Workflow{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: pid,
		Name: prefix + "-w", State: workflows.StateActive,
		CreatedBy: uid, CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(), Version: 1,
	}
	if err := wrepo.Create(ctx, ps, w); err != nil {
		t.Fatal(err)
	}
	v := workflows.Version{
		ID: uuid.Must(uuid.NewV7()), WorkflowID: w.ID, TenantID: tid,
		Number: 1, State: workflows.VersionPublished,
		SchemaVersion: "custos.io/v1alpha1",
		Spec:          []byte(`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow","metadata":{"name":"x"},"spec":{"tasks":[]}}`),
		SpecHash:      [32]byte{1}, Layout: []byte(`{}`),
		CreatedBy: uid, CreatedAt: time.Now().UTC(), Version: 1,
	}
	if err := wrepo.CreateVersion(ctx, ps, v); err != nil {
		t.Fatal(err)
	}
	repo := execpg.New(p)
	now := time.Now().UTC()
	e := executions.Execution{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: pid,
		WorkflowID: w.ID, WorkflowVersionID: v.ID, SpecHash: [32]byte{1},
		Parameters: []byte(`{}`), Strategy: "auto",
		State: executions.ExecPending, RequestedBy: uid,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	out, err := repo.CreateWithIdempotency(ctx, ps, e,
		executions.IdemRecord{
			Key: "k-" + e.ID.String(), ExpiresAt: now.Add(time.Hour),
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out.Execution
}

// TestExecutionRLS proves the app role sees only its tenant's rows on
// both tables.
func TestExecutionRLS(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	tA := mkTenant(t, admin, "er-a")
	tB := mkTenant(t, admin, "er-b")
	eA := seedExec(t, admin, tA, "a")
	_ = seedExec(t, admin, tB, "b")

	app := dbtest.AppPoolOn(t, dsn)
	asTenant := func(tid uuid.UUID, fn func(pgx.Tx) error) error {
		return db.WithTx(ctx, app, func(tx pgx.Tx) error {
			if err := db.SetTenant(ctx, tx, tid); err != nil {
				return err
			}
			return fn(tx)
		})
	}

	var n int
	err = asTenant(tA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM workflow_executions WHERE id=$1`,
			eA.ID).Scan(&n)
	})
	if err != nil || n != 1 {
		t.Fatalf("tenant A read own execution: n=%d err=%v", n, err)
	}
	err = asTenant(tB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM workflow_executions WHERE id=$1`,
			eA.ID).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("tenant B saw A's execution: n=%d err=%v", n, err)
	}
	// Cross-tenant insert is rejected by the WITH CHECK.
	err = asTenant(tB, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `
			INSERT INTO workflow_executions
			  (id, tenant_id, project_id, workflow_id,
			   workflow_version_id, spec_hash, parameters, strategy,
			   state, requested_by, created_at, updated_at, version)
			VALUES ($1,$2,$3,$4,$5,$6,'{}','auto','PENDING',$7,
			        now(),now(),1)`,
			uuid.Must(uuid.NewV7()), tA, eA.ProjectID, eA.WorkflowID,
			eA.WorkflowVersionID, eA.SpecHash[:], eA.RequestedBy)
		return e
	})
	if err == nil {
		t.Fatal("cross-tenant insert was allowed")
	}

	// task_executions: seed one row as platform, check visibility.
	if _, err := admin.Exec(ctx, `
		INSERT INTO task_executions
		  (id, execution_id, tenant_id, task_name, index, state,
		   created_at, updated_at, version)
		VALUES ($1,$2,$3,'t',0,'READY',now(),now(),1)`,
		uuid.Must(uuid.NewV7()), eA.ID, tA); err != nil {
		t.Fatal(err)
	}
	err = asTenant(tB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM task_executions`).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("tenant B saw A's tasks: n=%d err=%v", n, err)
	}
}

// TestStaleTransitionGuard proves a guarded update losing the version
// race is a no-op (ErrTransitionStale), not a corrupting write.
func TestStaleTransitionGuard(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	tid := mkTenant(t, admin, "st-a")
	e := seedExec(t, admin, tid, "s")
	repo := execpg.New(admin)
	ps := tenants.PlatformScope()

	// Two writers race PENDING -> VALIDATING with the same version.
	to := executions.ExecValidating
	won, err := repo.TransitionExec(ctx, ps, e.ID,
		executions.ExecPending, e.Version,
		executions.ExecPatch{State: &to}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if won.State != executions.ExecValidating {
		t.Fatalf("winner state %s", won.State)
	}
	_, err = repo.TransitionExec(ctx, ps, e.ID,
		executions.ExecPending, e.Version,
		executions.ExecPatch{State: &to}, nil)
	if !executions.IsStale(err) {
		t.Fatalf("loser err=%v, want stale", err)
	}
	// The losing write did not apply.
	cur, err := repo.Get(ctx, ps, tid, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.State != executions.ExecValidating ||
		cur.Version != won.Version {
		t.Fatalf("state %s v%d after stale write", cur.State, cur.Version)
	}
}

// TestSpecImmutability exercises the frozen-spec trigger under the app
// role: same-attempt change rejected, new-attempt re-freeze allowed.
func TestSpecImmutability(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	tid := mkTenant(t, admin, "si-a")
	e := seedExec(t, admin, tid, "s")
	taskID := uuid.Must(uuid.NewV7())
	if _, err := admin.Exec(ctx, `
		INSERT INTO task_executions
		  (id, execution_id, tenant_id, task_name, index, state,
		   execution_spec, execution_spec_digest,
		   created_at, updated_at, version)
		VALUES ($1,$2,$3,'t',0,'SUBMITTING','{"k":1}',$4,
		        now(),now(),1)`,
		taskID, e.ID, tid, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	app := dbtest.AppPoolOn(t, dsn)
	asTenant := func(fn func(pgx.Tx) error) error {
		return db.WithTx(ctx, app, func(tx pgx.Tx) error {
			if err := db.SetTenant(ctx, tx, tid); err != nil {
				return err
			}
			return fn(tx)
		})
	}
	// Same attempt: spec change rejected.
	err = asTenant(func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `
			UPDATE task_executions SET execution_spec='{"k":2}'
			WHERE id=$1`, taskID)
		return e
	})
	if err == nil {
		t.Fatal("same-attempt spec update was allowed")
	}
	// New attempt: clear + re-freeze allowed.
	err = asTenant(func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `
			UPDATE task_executions SET attempt=2, execution_spec=NULL,
			       execution_spec_digest=NULL WHERE id=$1`, taskID)
		return e
	})
	if err != nil {
		t.Fatalf("attempt bump + clear rejected: %v", err)
	}
	err = asTenant(func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `
			UPDATE task_executions SET execution_spec='{"k":3}',
			       execution_spec_digest=$2 WHERE id=$1`,
			taskID, make([]byte, 32))
		return e
	})
	if err != nil {
		t.Fatalf("re-freeze on new attempt rejected: %v", err)
	}
}

func TestDraftTestInsertAndDraftUpdateSerializeOnVersionLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := dbtest.Pool(t)
	tid := mkTenant(t, pool, "test-lock-a")
	uid := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, issuer, subject, kind) VALUES ($1,'i',$2,'user')`,
		uid, "test-lock-u"); err != nil {
		t.Fatal(err)
	}
	pid := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, tenant_id, slug, name, state, version)
		VALUES ($1,$2,'test-lock-p','P','active',1)`, pid, tid); err != nil {
		t.Fatal(err)
	}
	wrepo := wfpg.New(pool)
	scope := tenants.PlatformScope()
	w := workflows.Workflow{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: pid,
		Name: "test-lock-w", State: workflows.StateActive,
		CreatedBy: uid, CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(), Version: 1,
	}
	if err := wrepo.Create(ctx, scope, w); err != nil {
		t.Fatal(err)
	}
	v := workflows.Version{
		ID: uuid.Must(uuid.NewV7()), WorkflowID: w.ID, TenantID: tid,
		Number: 1, State: workflows.VersionDraft,
		SchemaVersion: "custos.io/v1alpha1",
		Spec:          []byte(`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow"}`),
		SpecHash:      [32]byte{1}, Layout: []byte(`{}`),
		CreatedBy: uid, CreatedAt: time.Now().UTC(), Version: 1,
	}
	if err := wrepo.CreateVersion(ctx, scope, v); err != nil {
		t.Fatal(err)
	}
	repo := execpg.New(pool)
	now := time.Now().UTC()
	e := executions.Execution{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: pid,
		WorkflowID: w.ID, WorkflowVersionID: v.ID, SpecHash: v.SpecHash,
		IsTest: true, Parameters: []byte(`{}`), Strategy: "auto",
		State: executions.ExecPending, RequestedBy: uid,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	createErr := make(chan error, 1)
	go func() {
		_, err := repo.CreateWithIdempotency(ctx, scope, e,
			executions.IdemRecord{
				Key: "test-" + e.ID.String(), Status: 202,
				ResourceID: e.ID, ExpiresAt: now.Add(time.Hour),
			}, func(_ executions.Execer) error {
				close(entered)
				<-release
				return nil
			})
		createErr <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("test execution did not reach its insert transaction")
	}

	updated := v
	updated.Spec = []byte(`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow","metadata":{"name":"new"}}`)
	updated.SpecHash = [32]byte{2}
	updateStarted := make(chan struct{})
	updateErr := make(chan error, 1)
	go func() {
		close(updateStarted)
		updateErr <- wrepo.UpdateDraftSpec(ctx, scope, updated, 1)
	}()
	<-updateStarted
	waiting := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE pid <> pg_backend_pid()
			  AND datname = current_database()
			  AND wait_event_type = 'Lock'
			  AND query ILIKE '%SELECT state FROM workflow_versions%'`).Scan(&count); err != nil {
			close(release)
			t.Fatalf("inspect draft row-lock wait: %v", err)
		}
		if count > 0 {
			waiting = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		close(release)
		t.Fatal("draft update did not wait for the test execution's version lock")
	}
	close(release)
	if err := <-createErr; err != nil {
		t.Fatalf("test execution insert failed: %v", err)
	}
	testOnly := true
	listed, _, err := repo.List(ctx, scope, tid, executions.ExecFilter{
		WorkflowID: &w.ID, Test: &testOnly,
	}, tenants.Page{})
	if err != nil || len(listed) != 1 || !listed[0].IsTest {
		t.Fatalf("test execution filter = %+v err=%v", listed, err)
	}
	testOnly = false
	listed, _, err = repo.List(ctx, scope, tid, executions.ExecFilter{
		WorkflowID: &w.ID, Test: &testOnly,
	}, tenants.Page{})
	if err != nil || len(listed) != 0 {
		t.Fatalf("normal execution filter = %+v err=%v, want empty", listed, err)
	}
	err = <-updateErr
	var domainErr *apperr.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "DRAFT_LOCKED" ||
		!strings.Contains(domainErr.Message, e.ID.String()) {
		t.Fatalf("draft update after test insert = %v, want DRAFT_LOCKED naming %s", err, e.ID)
	}
}

func TestDraftTestInsertRejectsSpecChangedAfterHashCapture(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	tid := mkTenant(t, pool, "test-stale-a")
	uid := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, issuer, subject, kind) VALUES ($1,'i',$2,'user')`,
		uid, "test-stale-u"); err != nil {
		t.Fatal(err)
	}
	pid := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, tenant_id, slug, name, state, version)
		VALUES ($1,$2,'test-stale-p','P','active',1)`, pid, tid); err != nil {
		t.Fatal(err)
	}
	scope := tenants.PlatformScope()
	wrepo := wfpg.New(pool)
	w := workflows.Workflow{
		ID: uuid.New(), TenantID: tid, ProjectID: pid, Name: "test-stale-w",
		State: workflows.StateActive, CreatedBy: uid,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Version: 1,
	}
	if err := wrepo.Create(ctx, scope, w); err != nil {
		t.Fatal(err)
	}
	oldHash := [32]byte{1}
	v := workflows.Version{
		ID: uuid.New(), WorkflowID: w.ID, TenantID: tid,
		Number: 1, State: workflows.VersionDraft,
		SchemaVersion: "custos.io/v1alpha1",
		Spec:          []byte(`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow"}`),
		SpecHash:      oldHash, Layout: []byte(`{}`),
		CreatedBy: uid, CreatedAt: time.Now().UTC(), Version: 1,
	}
	if err := wrepo.CreateVersion(ctx, scope, v); err != nil {
		t.Fatal(err)
	}
	updated := v
	updated.Spec = []byte(`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow","metadata":{"name":"updated"}}`)
	updated.SpecHash = [32]byte{2}
	if err := wrepo.UpdateDraftSpec(ctx, scope, updated, 1); err != nil {
		t.Fatalf("draft update before test insert: %v", err)
	}

	now := time.Now().UTC()
	testRun := executions.Execution{
		ID: uuid.New(), TenantID: tid, ProjectID: pid,
		WorkflowID: w.ID, WorkflowVersionID: v.ID, SpecHash: oldHash,
		IsTest: true, Parameters: []byte(`{}`), Strategy: "auto",
		State: executions.ExecPending, RequestedBy: uid,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	_, err := execpg.New(pool).CreateWithIdempotency(ctx, scope, testRun,
		executions.IdemRecord{
			Key: "test-stale-" + testRun.ID.String(), Status: 202,
			ResourceID: testRun.ID, ExpiresAt: now.Add(time.Hour),
		}, nil)
	var domainErr *apperr.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "VERSION_CONFLICT" {
		t.Fatalf("stale test insert = %v, want VERSION_CONFLICT", err)
	}
}
