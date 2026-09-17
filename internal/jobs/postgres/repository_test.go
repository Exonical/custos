package postgres_test

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/jobs"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
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

func mkCluster(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	c := clusters.Cluster{
		ID: uuid.Must(uuid.NewV7()), Name: name, DisplayName: name,
		BaseURL: "https://slurm.example:6820", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "slurm/token"},
		Visibility: clusters.VisibilityAssigned, State: clusters.StateActive,
		Version: 1,
	}
	if err := clusterpg.New(pool).Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func mkProject(t *testing.T, pool *pgxpool.Pool, tid uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO projects (id, tenant_id, slug, name, state)
		VALUES ($1,$2,$3,$3,'active')`, id, tid, "p-"+id.String()[:8]); err != nil {
		t.Fatal(err)
	}
	return id
}

func specFor(t *testing.T, j jobs.Job) admission.ExecutionSpec {
	t.Helper()
	s := admission.ExecutionSpec{
		ID: j.ID, TenantID: j.TenantID, ProjectID: j.ProjectID,
		PrincipalID: j.CreatedBy, TaskName: "adhoc", Attempt: 1,
		Cluster: admission.ClusterRef{ID: j.ClusterID, Name: "c"},
		Account: "acct", Partition: "gpu",
		Payload: admission.PayloadRef{ScriptID: j.ID,
			Digest: j.ScriptDigest, Language: j.ScriptLanguage,
			Interpreter: admission.InterpreterBash},
		Security: admission.SecurityContext{SlurmUser: "custos",
			ImpersonationMode: "service"},
	}
	if err := s.Freeze(); err != nil {
		t.Fatal(err)
	}
	return s
}

func mkJob(t *testing.T, tid, pid, cid, uid uuid.UUID) jobs.Job {
	t.Helper()
	now := time.Now().UTC()
	j := jobs.Job{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: pid,
		ClusterID: cid, CreatedBy: uid, Name: "j",
		State: jobs.StateSubmitting,
		ResourceRequest: workflowspec.Resources{
			Nodes: 1, Walltime: workflowspec.Duration(time.Hour)},
		ScriptDigest:   validation.DigestOf([]byte("echo 1")),
		ScriptLanguage: workflowspec.LanguageBash,
		Version:        1, CreatedAt: now, UpdatedAt: now,
	}
	j.ExecutionSpec = specFor(t, j)
	j.ExecutionSpecDigest = j.ExecutionSpec.Digest
	return j
}

func TestCreateGetTransition(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := jobpg.New(pool)
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "job-a"), mkUser(t, pool, "job-u")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "job-c")
	sc := tenants.TenantScope(tid)

	j := mkJob(t, tid, pid, cid, uid)
	enqueued := false
	res, err := repo.CreateWithIdempotency(ctx, sc, j, jobs.IdemRecord{
		Key: "k1", RequestHash: sha256.Sum256([]byte("body")),
		Status: 202, ResourceID: j.ID, ExpiresAt: time.Now().Add(time.Hour),
	}, func(_ jobs.Execer) error { enqueued = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.Replayed || res.Job.ID != j.ID || !enqueued {
		t.Fatalf("create: %+v enqueued=%v", res, enqueued)
	}

	got, err := repo.Get(ctx, sc, j.ID)
	if err != nil || got.State != jobs.StateSubmitting ||
		got.ExecutionSpec.Digest != j.ExecutionSpecDigest {
		t.Fatalf("get: %v %+v", err, got)
	}

	// Idempotent replay: same key + same hash returns the stored job.
	res2, err := repo.CreateWithIdempotency(ctx, sc, mkJob(t, tid, pid, cid, uid),
		jobs.IdemRecord{Key: "k1",
			RequestHash: sha256.Sum256([]byte("body")), Status: 202,
			ResourceID: uuid.Must(uuid.NewV7()),
			ExpiresAt:  time.Now().Add(time.Hour)}, nil)
	if err != nil || !res2.Replayed || res2.Job.ID != j.ID {
		t.Fatalf("replay: %v %+v", err, res2)
	}
	// Same key, different body -> 409 IDEMPOTENCY_MISMATCH.
	_, err = repo.CreateWithIdempotency(ctx, sc, mkJob(t, tid, pid, cid, uid),
		jobs.IdemRecord{Key: "k1",
			RequestHash: sha256.Sum256([]byte("other")), Status: 202,
			ResourceID: uuid.Must(uuid.NewV7()),
			ExpiresAt:  time.Now().Add(time.Hour)}, nil)
	if !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want conflict, got %v", err)
	}

	// Guarded transition.
	st := jobs.StateQueued
	sid := int64(4242)
	out, err := repo.Transition(ctx, sc, j.ID, j.Version, jobs.Patch{
		State: &st, SlurmJobID: &sid, SlurmState: strp("PENDING")})
	if err != nil || out.State != jobs.StateQueued ||
		out.SlurmJobID == nil || *out.SlurmJobID != 4242 || out.Version != 2 {
		t.Fatalf("transition: %v %+v", err, out)
	}
	// Stale version -> Conflict.
	if _, err := repo.Transition(ctx, sc, j.ID, j.Version, jobs.Patch{
		State: &st}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want version conflict, got %v", err)
	}
}

// TestSpecImmutability: the trigger rejects execution_spec /
// execution_spec_digest / script_digest changes.
func TestSpecImmutability(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := jobpg.New(pool)
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "job-b"), mkUser(t, pool, "job-u2")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "job-c2")
	j := mkJob(t, tid, pid, cid, uid)
	if _, err := repo.CreateWithIdempotency(ctx, tenants.TenantScope(tid), j,
		jobs.IdemRecord{Key: "k", RequestHash: sha256.Sum256([]byte("b")),
			Status: 202, ResourceID: j.ID,
			ExpiresAt: time.Now().Add(time.Hour)}, nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE jobs SET execution_spec='{}'::jsonb WHERE id=$1`,
		`UPDATE jobs SET execution_spec_digest='\x00'::bytea WHERE id=$1`,
		`UPDATE jobs SET script_digest='\x00'::bytea WHERE id=$1`,
	} {
		if _, err := pool.Exec(ctx, q, j.ID); err == nil {
			t.Fatalf("%s: expected trigger error", q)
		}
	}
	// Ordinary updates still work.
	if _, err := pool.Exec(ctx,
		`UPDATE jobs SET state_reason='x' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
}

func TestListAndActive(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := jobpg.New(pool)
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "job-c"), mkUser(t, pool, "job-u3")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "job-c3")
	sc := tenants.TenantScope(tid)
	for i, st := range []jobs.State{jobs.StateSubmitting, jobs.StateQueued,
		jobs.StateCompleted} {
		j := mkJob(t, tid, pid, cid, uid)
		j.State = st
		j.CreatedAt = time.Now().UTC().Add(time.Duration(i) * time.Second)
		if _, err := repo.CreateWithIdempotency(ctx, sc, j, jobs.IdemRecord{
			Key: "l" + j.ID.String(), RequestHash: sha256.Sum256([]byte(j.ID.String())),
			Status: 202, ResourceID: j.ID,
			ExpiresAt: time.Now().Add(time.Hour)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	all, _, err := repo.List(ctx, sc, jobs.Filter{ProjectID: &pid},
		tenants.Page{Limit: 10})
	if err != nil || len(all) != 3 {
		t.Fatalf("list: %v n=%d", err, len(all))
	}
	queued, _, err := repo.List(ctx, sc, jobs.Filter{
		States: []jobs.State{jobs.StateQueued}}, tenants.Page{})
	if err != nil || len(queued) != 1 {
		t.Fatalf("state filter: %v n=%d", err, len(queued))
	}
	active, err := repo.ListActiveByCluster(ctx, cid)
	if err != nil || len(active) != 2 {
		t.Fatalf("active: %v n=%d", err, len(active))
	}
	counts, err := repo.CountActiveByState(ctx)
	if err != nil || counts[jobs.StateSubmitting]+counts[jobs.StateQueued] < 2 {
		t.Fatalf("counts: %v %v", err, counts)
	}
}

// TestJobsRLS: tenant B cannot see tenant A's jobs under the app role.
func TestJobsRLS(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ta, tb := mkTenant(t, admin, "jrls-a"), mkTenant(t, admin, "jrls-b")
	uid := mkUser(t, admin, "jrls-u")
	pa, ca := mkProject(t, admin, ta), mkCluster(t, admin, "jrls-c")
	repo := jobpg.New(admin)
	j := mkJob(t, ta, pa, ca, uid)
	if _, err := repo.CreateWithIdempotency(ctx, tenants.PlatformScope(), j,
		jobs.IdemRecord{Key: "r", RequestHash: sha256.Sum256([]byte("r")),
			Status: 202, ResourceID: j.ID,
			ExpiresAt: time.Now().Add(time.Hour)}, nil); err != nil {
		t.Fatal(err)
	}
	app := dbtest.AppPoolOn(t, dsn)
	if err := db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, tb); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM jobs WHERE tenant_id=$1`, ta).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatalf("tenant B saw %d of A's jobs", n)
		}
		appRepo := jobpg.New(app)
		if _, err := appRepo.Get(ctx, tenants.TenantScope(tb), j.ID); !apperr.Is(err, apperr.NotFound) {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func strp(s string) *string { return &s }
