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
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/policy"
	vpg "github.com/Exonical/custos/internal/validation/postgres"
	"github.com/Exonical/custos/internal/workflowspec"
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

func TestPolicyStoreRoundtrip(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	tid := mkTenant(t, pool, "vp-a")
	uid := mkUser(t, pool, "vp-u")
	store := vpg.NewPolicyStore(pool)
	ps := tenants.PlatformScope()

	// create
	out, err := store.Put(ctx, ps, policy.Scoped{
		ScopeKind: policy.ScopeTenant, ScopeID: tid,
		Body:      policy.Default(),
		UpdatedBy: uid,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != 1 {
		t.Fatalf("version %d", out.Version)
	}

	// optimistic update
	pol := policy.Default()
	pol.BlockAt = validation.SeverityWarning
	out, err = store.Put(ctx, ps, policy.Scoped{
		ScopeKind: policy.ScopeTenant, ScopeID: tid,
		Body: pol, UpdatedBy: uid,
	}, 1)
	if err != nil || out.Version != 2 {
		t.Fatalf("update: %v %+v", err, out)
	}

	// stale version conflicts
	if _, err := store.Put(ctx, ps, policy.Scoped{
		ScopeKind: policy.ScopeTenant, ScopeID: tid, Body: pol,
	}, 1); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("stale write: %v", err)
	}

	got, err := store.Get(ctx, ps, policy.ScopeTenant, tid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || got.Body.BlockAt != validation.SeverityWarning {
		t.Fatalf("get: %+v", got)
	}

	// unset scope -> NotFound
	if _, err := store.Get(ctx, ps, policy.ScopeCluster,
		uuid.New()); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("unset: %v", err)
	}
}

func TestPolicyRLS(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ta, tb := mkTenant(t, admin, "vprls-a"), mkTenant(t, admin, "vprls-b")
	cid := uuid.Must(uuid.NewV7())
	store := vpg.NewPolicyStore(admin)
	ps := tenants.PlatformScope()
	for _, sc := range []policy.Scoped{
		{ScopeKind: policy.ScopeTenant, ScopeID: ta, Body: policy.Default()},
		{ScopeKind: policy.ScopeCluster, ScopeID: cid, Body: policy.Default()},
	} {
		if _, err := store.Put(ctx, ps, sc, 0); err != nil {
			t.Fatal(err)
		}
	}
	app := dbtest.AppPoolOn(t, dsn)
	if err := db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, tb); err != nil {
			return err
		}
		var n int
		// tenant B sees no tenant-A policy but does see cluster rows
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM validation_policies
			WHERE scope_kind='tenant'`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatalf("tenant B saw %d tenant policies", n)
		}
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM validation_policies
			WHERE scope_kind='cluster'`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("cluster policy invisible to tenant scope: %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// tenant B cannot write a tenant-A row (its own tx; the failed
	// statement aborts it, which is the point).
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, tb); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO validation_policies
			(id, scope_kind, scope_id, version, body)
			VALUES ($1,'tenant',$2,1,'{}')`, uuid.New(), ta)
		if err == nil {
			return errors.New("tenant B wrote a tenant-A policy row")
		}
		return err
	})
	if err == nil {
		t.Fatal("RLS allowed cross-tenant policy insert")
	}
	// Tenant scope must not be able to write cluster-scope rows, even
	// though it can read them (cluster writes are platform-scope only).
	// Each forbidden statement runs in its own tx since the failure
	// aborts it.
	for name, q := range map[string]string{
		"insert cluster row": `INSERT INTO validation_policies
			(id, scope_kind, scope_id, version, body)
			VALUES ('` + uuid.NewString() + `','cluster','` + cid.String() + `',1,'{}')`,
		"update cluster row": `UPDATE validation_policies
			SET body='{"blockAt":"INFO"}' WHERE scope_kind='cluster'`,
	} {
		err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
			if err := db.SetTenant(ctx, tx, tb); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, q)
			if err == nil {
				return errors.New("tenant session wrote a cluster policy: " + name)
			}
			return err
		})
		if err == nil {
			t.Fatalf("RLS allowed tenant-scoped cluster write: %s", name)
		}
	}
	// Its own tenant-scope row is writable.
	if err := db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, tb); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO validation_policies
			(id, scope_kind, scope_id, version, body)
			VALUES ($1,'tenant',$2,1,'{}')`, uuid.New(), tb)
		return err
	}); err != nil {
		t.Fatalf("tenant could not write its own policy row: %v", err)
	}
}

func svFixture(tid uuid.UUID, body string) validation.ScriptValidation {
	return validation.ScriptValidation{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid,
		ScriptDigest: validation.DigestOf([]byte(body)),
		Language:     workflowspec.LanguageBash,
		Valid:        true,
		Diagnostics: []validation.Diagnostic{
			{Source: "shsyntax", Code: "CUSTOS010",
				Severity: validation.SeverityWarning, Line: 1},
		},
		ToolVersions:  map[string]string{"shsyntax": "v3.14.1"},
		PolicyVersion: 7,
		InputHash:     0x9e3779b97f4a7c15,
		ValidatedAt:   time.Now().UTC(),
		ExpiresAt:     time.Now().UTC().Add(24 * time.Hour),
	}
}

func TestValidationStore(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	tid := mkTenant(t, pool, "sv-a")
	store := vpg.NewValidationStore(pool)
	ps := tenants.PlatformScope()

	sv := svFixture(tid, "echo hi")
	if err := store.Put(ctx, ps, sv); err != nil {
		t.Fatal(err)
	}
	got, err := store.Latest(ctx, ps, tid, sv.ScriptDigest, 7, sv.InputHash)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputHash != sv.InputHash {
		t.Fatalf("input hash roundtrip: %d != %d", got.InputHash, sv.InputHash)
	}
	if got.ID != sv.ID || !got.Valid ||
		got.Diagnostics[0].Code != "CUSTOS010" ||
		got.ToolVersions["shsyntax"] != "v3.14.1" {
		t.Fatalf("roundtrip: %+v", got)
	}

	// different policy fingerprint or input context -> not found
	if _, err := store.Latest(ctx, ps, tid, sv.ScriptDigest, 8, sv.InputHash); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("currency: %v", err)
	}
	if _, err := store.Latest(ctx, ps, tid, sv.ScriptDigest, 7, sv.InputHash+1); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("input-hash currency: %v", err)
	}

	// workflow-version listing (script_validations.workflow_version_id
	// is a real FK, so seed the parent rows)
	uid := mkUser(t, pool, "sv-u")
	pid := uuid.Must(uuid.NewV7())
	wid := uuid.Must(uuid.NewV7())
	wfv := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, tenant_id, slug, name, state, version)
		VALUES ($1,$2,'sv-p','P','active',1)`, pid, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflows (id, tenant_id, project_id, name, created_by)
		VALUES ($1,$2,$3,'sv-wf',$4)`, wid, tid, pid, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_versions
		(id, workflow_id, tenant_id, number, schema_version, spec, spec_hash, created_by)
		VALUES ($1,$2,$3,1,'custos.io/v1alpha1','{}',$4,$5)`,
		wfv, wid, tid, make([]byte, 32), uid); err != nil {
		t.Fatal(err)
	}
	sv2 := svFixture(tid, "echo bye")
	sv2.WorkflowVersionID = &wfv
	if err := store.Put(ctx, ps, sv2); err != nil {
		t.Fatal(err)
	}
	lst, err := store.ListByWorkflowVersion(ctx, ps, tid, wfv)
	if err != nil || len(lst) != 1 || lst[0].ID != sv2.ID {
		t.Fatalf("list: %v %+v", err, lst)
	}
}
