package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/workflows"
	wfpg "github.com/Exonical/custos/internal/workflows/postgres"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func mkTenant(t *testing.T, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO tenants (id, slug, name, state, settings, version)
		VALUES ($1,$2,'T','active','{}',1)`, id, slug); err != nil {
		t.Fatal(err)
	}
	return id
}

func mkUser(t *testing.T, pool *pgxpool.Pool, sub string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, issuer, subject, kind) VALUES ($1,'iss',$2,'user')`,
		id, sub); err != nil {
		t.Fatal(err)
	}
	return id
}

func mkProject(t *testing.T, pool *pgxpool.Pool,
	tid uuid.UUID, slug string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO projects (id, tenant_id, slug, name, state, version)
		VALUES ($1,$2,$3,'P','active',1)`, id, tid, slug); err != nil {
		t.Fatal(err)
	}
	return id
}

func mkWorkflow(t *testing.T, repo *wfpg.Repository, scope tenants.Scope,
	tid, pid, uid uuid.UUID, name string) workflows.Workflow {
	t.Helper()
	w := workflows.Workflow{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: pid,
		Name: name, State: workflows.StateActive, CreatedBy: uid,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Version: 1,
	}
	if err := repo.Create(context.Background(), scope, w); err != nil {
		t.Fatal(err)
	}
	return w
}

func mkDraft(t *testing.T, repo *wfpg.Repository, scope tenants.Scope,
	w workflows.Workflow, uid uuid.UUID, n int) workflows.Version {
	t.Helper()
	v := workflows.Version{
		ID: uuid.Must(uuid.NewV7()), WorkflowID: w.ID, TenantID: w.TenantID,
		Number: n, State: workflows.VersionDraft,
		SchemaVersion: "custos.io/v1alpha1",
		Spec:          []byte(`{"apiVersion":"custos.io/v1alpha1"}`),
		SpecHash:      [32]byte{byte(n)}, Layout: []byte(`{}`),
		CreatedBy: uid, CreatedAt: time.Now().UTC(), Version: 1,
	}
	if err := repo.CreateVersion(context.Background(), scope, v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWorkflowCRUD(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	tid := mkTenant(t, pool, "wf-a")
	uid := mkUser(t, pool, "wf-u")
	pid := mkProject(t, pool, tid, "wf-p")
	repo := wfpg.New(pool)
	ps := tenants.PlatformScope()

	w := mkWorkflow(t, repo, ps, tid, pid, uid, "pipe")
	got, err := repo.Get(ctx, ps, tid, w.ID)
	if err != nil || got.Name != "pipe" {
		t.Fatalf("get: %v %+v", err, got)
	}

	// List with project filter.
	pid2 := mkProject(t, pool, tid, "wf-p2")
	mkWorkflow(t, repo, ps, tid, pid2, uid, "other")
	all, err := repo.List(ctx, ps, tid, nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %v %d", err, len(all))
	}
	one, err := repo.List(ctx, ps, tid, &pid)
	if err != nil || len(one) != 1 || one[0].ID != w.ID {
		t.Fatalf("filtered list: %v %+v", err, one)
	}

	// Optimistic update + archive.
	w.Description = "d"
	if err := repo.Update(ctx, ps, w, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(ctx, ps, w, 1); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("stale update: %v", err)
	}
	w.State = workflows.StateArchived
	if err := repo.Update(ctx, ps, w, 2); err != nil {
		t.Fatal(err)
	}
}

func TestVersionLifecycle(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	tid := mkTenant(t, pool, "wv-a")
	uid := mkUser(t, pool, "wv-u")
	pid := mkProject(t, pool, tid, "wv-p")
	repo := wfpg.New(pool)
	ps := tenants.PlatformScope()
	w := mkWorkflow(t, repo, ps, tid, pid, uid, "pipe")

	v := mkDraft(t, repo, ps, w, uid, 1)
	if n, err := repo.NextVersionNumber(ctx, ps, w.ID); err != nil || n != 2 {
		t.Fatalf("next number: %v %d", err, n)
	}

	// Draft spec edits allowed.
	v.Spec = []byte(`{"apiVersion":"custos.io/v1alpha1","x":1}`)
	if err := repo.UpdateDraftSpec(ctx, ps, v, 1); err != nil {
		t.Fatal(err)
	}
	// Publish: state + workflow latest pointer atomically.
	if err := repo.SetVersionState(ctx, ps, v,
		workflows.VersionPublished, 2); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetVersion(ctx, ps, tid, w.ID, v.ID)
	if err != nil || got.State != workflows.VersionPublished ||
		got.PublishedAt == nil {
		t.Fatalf("published: %v %+v", err, got)
	}
	wfGot, err := repo.Get(ctx, ps, tid, w.ID)
	if err != nil || wfGot.LatestPublishedVersion == nil ||
		*wfGot.LatestPublishedVersion != v.ID {
		t.Fatalf("latest pointer: %v %+v", err, wfGot)
	}
	// Deprecate.
	if err := repo.SetVersionState(ctx, ps, v,
		workflows.VersionDeprecated, 3); err != nil {
		t.Fatal(err)
	}
}

// TestVersionImmutability exercises the trigger directly under the
// non-superuser app role.
func TestVersionImmutability(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	tid := mkTenant(t, admin, "wi-a")
	uid := mkUser(t, admin, "wi-u")
	pid := mkProject(t, admin, tid, "wi-p")
	repo := wfpg.New(admin)
	ps := tenants.PlatformScope()
	w := mkWorkflow(t, repo, ps, tid, pid, uid, "pipe")
	v := mkDraft(t, repo, ps, w, uid, 1)
	if err := repo.SetVersionState(ctx, ps, v,
		workflows.VersionPublished, 1); err != nil {
		t.Fatal(err)
	}

	app := dbtest.AppPoolOn(t, dsn)
	asTenant := func(fn func(tx pgx.Tx) error) error {
		return db.WithTx(ctx, app, func(tx pgx.Tx) error {
			if err := db.SetTenant(ctx, tx, tid); err != nil {
				return err
			}
			return fn(tx)
		})
	}

	// Spec update on a published version is rejected.
	err = asTenant(func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `
			UPDATE workflow_versions SET spec='{}'::jsonb WHERE id=$1`, v.ID)
		return e
	})
	if err == nil {
		t.Fatal("published spec update was allowed")
	}

	// Layout update on a published version is allowed.
	err = asTenant(func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `
			UPDATE workflow_versions SET layout='{"x":1}'::jsonb WHERE id=$1`, v.ID)
		return e
	})
	if err != nil {
		t.Fatalf("layout update rejected: %v", err)
	}

	// Published -> deprecated ok; deprecated -> published rejected.
	err = asTenant(func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `
			UPDATE workflow_versions SET state='deprecated' WHERE id=$1`, v.ID)
		return e
	})
	if err != nil {
		t.Fatalf("deprecate rejected: %v", err)
	}
	err = asTenant(func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `
			UPDATE workflow_versions SET state='published' WHERE id=$1`, v.ID)
		return e
	})
	if err == nil {
		t.Fatal("deprecated->published was allowed")
	}

	// Draft delete ok; published delete rejected.
	d := mkDraft(t, repo, ps, w, uid, 2)
	err = asTenant(func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`DELETE FROM workflow_versions WHERE id=$1`, d.ID)
		return e
	})
	if err != nil {
		t.Fatalf("draft delete rejected: %v", err)
	}
	err = asTenant(func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`DELETE FROM workflow_versions WHERE id=$1`, v.ID)
		return e
	})
	if err == nil {
		t.Fatal("published delete was allowed")
	}
}

// TestWorkflowRLS proves tenant sessions see only their own rows.
func TestWorkflowRLS(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ta, tb := mkTenant(t, admin, "wr-a"), mkTenant(t, admin, "wr-b")
	uid := mkUser(t, admin, "wr-u")
	pa, pb := mkProject(t, admin, ta, "wr-pa"), mkProject(t, admin, tb, "wr-pb")
	repo := wfpg.New(admin)
	ps := tenants.PlatformScope()
	wa := mkWorkflow(t, repo, ps, ta, pa, uid, "a")
	wb := mkWorkflow(t, repo, ps, tb, pb, uid, "b")
	mkDraft(t, repo, ps, wa, uid, 1)
	mkDraft(t, repo, ps, wb, uid, 1)

	app := dbtest.AppPoolOn(t, dsn)
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, ta); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM workflows`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return errors.New("tenant A should see exactly its workflow")
		}
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM workflow_versions`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return errors.New("tenant A should see exactly its version")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Cross-tenant insert rejected (own tx: a failed statement aborts
	// the transaction).
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, ta); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO workflows (id, tenant_id, project_id, name,
				state, created_by)
			VALUES ($1,$2,$3,'x','active',$4)`,
			uuid.Must(uuid.NewV7()), tb, pb, uid)
		return err
	})
	if err == nil {
		t.Fatal("cross-tenant workflow insert allowed")
	}
}

// TestWorkflowTenantProjectTrigger proves the project-tenant FK
// trigger rejects mismatched tenant/project pairs.
func TestWorkflowTenantProjectTrigger(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	ta, tb := mkTenant(t, pool, "wt-a"), mkTenant(t, pool, "wt-b")
	uid := mkUser(t, pool, "wt-u")
	pb := mkProject(t, pool, tb, "wt-pb")
	repo := wfpg.New(pool)
	err := repo.Create(ctx, tenants.PlatformScope(), workflows.Workflow{
		ID: uuid.Must(uuid.NewV7()), TenantID: ta, ProjectID: pb,
		Name: "x", State: workflows.StateActive, CreatedBy: uid,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("cross-tenant project binding was allowed")
	}
}
