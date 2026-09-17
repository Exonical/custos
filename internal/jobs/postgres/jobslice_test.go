package postgres_test

// End-to-end slice tests: service admission pipeline + worker handlers
// against real PostgreSQL and fake Slurm (docs/script-validation.md
// §Testing items 3, 4, 6).

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/jobs"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	jobssvc "github.com/Exonical/custos/internal/jobs/service"
	jobsworker "github.com/Exonical/custos/internal/jobs/worker"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/workqueue"
	policypg "github.com/Exonical/custos/internal/policies/postgres"
	policiessvc "github.com/Exonical/custos/internal/policies/service"
	"github.com/Exonical/custos/internal/projects"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	scriptpg "github.com/Exonical/custos/internal/scripts/postgres"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/fake"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/envcheck"
	"github.com/Exonical/custos/internal/validation/pipeline"
	vpolicy "github.com/Exonical/custos/internal/validation/policy"
	vpolicypg "github.com/Exonical/custos/internal/validation/postgres"
	"github.com/Exonical/custos/internal/validation/sbatchscan"
	"github.com/Exonical/custos/internal/validation/shsyntax"
	"github.com/Exonical/custos/internal/workflowspec"
)

type fakeFactory struct{ c *fake.Cluster }

func (f fakeFactory) Open(context.Context, slurm.ClusterConfig) (slurm.Cluster, slurm.Accounting, error) {
	return f.c, f.c, nil
}

func tenantMember(t *testing.T, pool *pgxpool.Pool, tenantID, userID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO tenant_memberships (tenant_id, user_id, roles, source)
		VALUES ($1,$2,'{researcher}','manual')`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
}

type fx struct {
	pool   *pgxpool.Pool
	ctx    context.Context
	p      authn.Principal
	tc     tenants.TenantContext
	pc     projects.ProjectContext
	svc    *jobssvc.Service
	jrepo  *jobpg.Repository
	fake   *fake.Cluster
	tenant uuid.UUID
	proj   uuid.UUID
	clus   uuid.UUID
	user   uuid.UUID
}

func newFx(t *testing.T, slug string) *fx {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	tid := mkTenant(t, pool, slug)
	uid := mkUser(t, pool, slug+"-u")
	tenantMember(t, pool, tid, uid)
	pid := mkProject(t, pool, tid)
	cid := mkCluster(t, pool, slug+"-c")

	ps := tenants.PlatformScope()
	crepo := clusterpg.New(pool)
	if err := crepo.UpsertAssignment(ctx, tenants.TenantScope(tid),
		clusters.Assignment{ClusterID: cid, TenantID: tid,
			Source: clusters.SourceManual}); err != nil {
		t.Fatal(err)
	}
	maxT := 8 * time.Hour
	if _, err := crepo.RecordSyncResult(ctx, cid, clusters.SyncResult{
		OK: true, At: time.Now(),
		Capabilities: &slurm.Capabilities{
			APIVersion: "v0.0.45",
			Partitions: []slurm.Partition{
				{Name: "gpu", MaxTime: &maxT, AllowedQoS: []string{"high"}},
				{Name: "batch"},
			},
			QoSNames:  []string{"high", "normal"},
			GRESTypes: []string{"gpu:h100"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	prepo := projectpg.New(pool)
	if err := prepo.UpsertMembership(ctx, ps, projects.Membership{
		TenantID: tid, ProjectID: pid, UserID: uid,
		Roles: []string{"project-member"}, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := prepo.CreateBinding(ctx, ps, projects.ClusterBinding{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid, ProjectID: pid,
		ClusterID: cid, SlurmAccount: "acct-p",
		DefaultPartition: "gpu", DefaultQoS: "high",
		AllowedPartitions: []string{"gpu", "batch"},
		AllowedQoS:        []string{"high"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	tenantRepo := tenantpg.New(pool)
	projectSvc := projectsvc.NewService(prepo, prepo, prepo,
		tenantRepo, crepo, authz.RBAC{}, audit.Multi{})
	policySvc := policiessvc.NewService(policypg.New(pool),
		authz.RBAC{}, audit.Multi{})
	jrepo := jobpg.New(pool)
	fc := fake.New()
	fx := &fx{
		pool: pool, fake: fc, jrepo: jrepo, tenant: tid,
		proj: pid, clus: cid, user: uid,
		p: authn.Principal{UserID: uid, Kind: authn.KindUser},
		tc: tenants.TenantContext{
			Tenant:     tenants.Tenant{ID: tid, Slug: slug, State: tenants.StateActive},
			Membership: &tenants.Membership{Roles: []string{"researcher"}},
		},
		pc: projects.ProjectContext{
			Project:    projects.Project{ID: pid, TenantID: tid, State: projects.StateActive},
			Membership: &projects.Membership{Roles: []string{"project-member"}},
		},
	}
	fx.ctx = tenants.WithTenantContext(ctx, fx.tc)
	fx.svc = jobssvc.New(jobssvc.Deps{
		Jobs: jrepo, Scripts: scriptpg.New(pool), Projects: projectSvc,
		Policies: policySvc, Clusters: crepo,
		Pipeline: pipeline.New([]validation.ScriptValidator{
			shsyntax.Validator{}, sbatchscan.Validator{},
			envcheck.Validator{}}),
		VPolicy: vpolicy.NewService(vpolicy.Deps{
			Store: vpolicypg.NewPolicyStore(pool), Clusters: crepo,
			AZ: authz.RBAC{}}),
		Validations: vpolicypg.NewValidationStore(pool),
		AZ:          authz.RBAC{}, Audit: audit.Multi{},
	})
	return fx
}

// withProjectPolicy sets the project resource policy row directly.
func (f *fx) setTenantPolicy(t *testing.T, policy string) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO resource_policies (id, tenant_id, scope, policy)
		VALUES ($1,$2,'tenant',$3::jsonb)
		ON CONFLICT (tenant_id) WHERE scope='tenant'
		DO UPDATE SET policy=$3::jsonb`,
		uuid.Must(uuid.NewV7()), f.tenant, policy); err != nil {
		t.Fatal(err)
	}
}

func submitIn(script string) jobssvc.SubmitInput {
	return jobssvc.SubmitInput{
		Cluster: "", // filled by caller
		Resources: workflowspec.Resources{
			Nodes: 2, Walltime: workflowspec.Duration(4 * time.Hour),
			GPU: &workflowspec.GPURequest{Type: "h100", Count: 4},
		},
		Script: jobssvc.ScriptInput{
			Language: workflowspec.LanguageBash, Body: []byte(script)},
	}
}

func (f *fx) submit(t *testing.T, in jobssvc.SubmitInput, key string) (jobssvc.Result, []validation.Diagnostic, error) {
	t.Helper()
	in.Cluster = f.clus.String()
	body, _ := json.Marshal(in)
	return f.svc.Submit(f.ctx, f.p, f.tc, f.pc, in, key, sha256.Sum256(body))
}

func (f *fx) wdeps() jobsworker.Deps {
	return jobsworker.Deps{
		Jobs: f.jrepo, Scripts: scriptpg.New(f.pool),
		Clusters: clusterpg.New(f.pool), Factory: fakeFactory{f.fake},
		Exec: f.pool, Audit: audit.Multi{},
	}
}

func runSubmit(f *fx, t *testing.T, id uuid.UUID) error {
	t.Helper()
	return jobsworker.Submit(f.wdeps())(f.ctx, workqueue.Item{
		Kind: jobssvc.KindSubmit, Key: "job:" + id.String(),
		Payload: json.RawMessage(
			`{"job_id":"` + id.String() + `"}`)})
}

func runReconcile(f *fx, t *testing.T, id uuid.UUID) error {
	t.Helper()
	return jobsworker.Reconcile(f.wdeps())(f.ctx, workqueue.Item{
		Kind: jobsworker.KindReconcile, Key: "job:" + id.String(),
		Payload: json.RawMessage(`{"job_id":"` + id.String() + `"}`)})
}

func jobOf(f *fx, t *testing.T, id uuid.UUID) jobs.Job {
	t.Helper()
	j, err := f.jrepo.Get(f.ctx, tenants.PlatformScope(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// --- the invariant (doc item 4) -----------------------------------------

func TestSubmitInvariant(t *testing.T) {
	f := newFx(t, "inv")
	in := submitIn("#!/bin/bash\nset -e\nsrun ./app\n")
	in.Partition = "gpu"
	in.Env = map[string]string{"FOO": "bar"}
	res, diags, err := f.submit(t, in, "inv-1")
	if err != nil || len(diags) != 0 {
		t.Fatalf("submit: %v %v", err, diags)
	}
	j := res.Job
	if j.State != jobs.StateSubmitting {
		t.Fatalf("state %s", j.State)
	}
	if err := runSubmit(f, t, j.ID); err != nil {
		t.Fatal(err)
	}
	subs := f.fake.Submissions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 slurm submission, got %d", len(subs))
	}
	s := subs[0]
	j = jobOf(f, t, j.ID)
	if j.State != jobs.StateQueued || j.SlurmJobID == nil {
		t.Fatalf("job %+v", j)
	}
	// Scheduler fields come only from the admitted spec.
	if s.Account != "acct-p" || s.Partition != "gpu" {
		t.Fatalf("account/partition: %+v", s)
	}
	if s.QoS != "high" {
		t.Fatalf("qos %q (binding default should apply? allowed=[high], spec qos '')", s.QoS)
	}
	if len(s.GRES) != 1 || s.GRES[0].Type != "h100" || s.GRES[0].Count != 4 {
		t.Fatalf("gres %+v", s.GRES)
	}
	if s.Walltime != 4*time.Hour || s.Nodes != 2 {
		t.Fatalf("resources %+v", s)
	}
	if s.Name != "custos-"+j.ID.String() ||
		s.Comment != "custos:"+j.ID.String()+"/adhoc" {
		t.Fatalf("name/comment: %q %q", s.Name, s.Comment)
	}
	// The wrapper carries the payload but no directives.
	if strings.Contains(s.Script, "#SBATCH") {
		t.Fatal("wrapper contains #SBATCH")
	}
	if !strings.Contains(s.Script, "echo") && !strings.Contains(s.Script, "base64") {
		t.Fatal("wrapper missing payload")
	}
	for k := range s.Environment {
		if strings.HasPrefix(k, "SLURM_") || strings.HasPrefix(k, "SBATCH_") {
			t.Fatalf("controlled env key reached Slurm: %s", k)
		}
	}
	if s.Environment["FOO"] != "bar" {
		t.Fatal("user env missing")
	}
}

// --- resource tampering (doc item 3) ------------------------------------

func TestSubmitRejectsDirectives(t *testing.T) {
	f := newFx(t, "tamp")
	f.setTenantPolicy(t, `{"max_gpus_per_job":4}`)
	cases := []struct {
		name, directive, field, code string
	}{
		{"gres", "#SBATCH --gres=gpu:8", "gres", "CUSTOS101"},
		{"qos", "#SBATCH --qos=high", "qos", "CUSTOS102"},
		{"account", "#SBATCH --account=other", "account", "CUSTOS103"},
		{"partition", "#SBATCH --partition=debug", "partition", "CUSTOS104"},
		{"reservation", "#SBATCH --reservation=r", "reservation", "CUSTOS105"},
		{"nodes", "#SBATCH --nodes=9", "nodes", "CUSTOS106"},
		{"memory", "#SBATCH --mem=999G", "memory", "CUSTOS107"},
		{"walltime", "#SBATCH --time=99:00:00", "walltime", "CUSTOS108"},
		{"licenses", "#SBATCH --licenses=lic:9", "licenses", "CUSTOS112"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := submitIn("#!/bin/bash\n" + tc.directive + "\necho hi\n")
			in.Resources.GPU.Count = 4 // within policy
			_, diags, err := f.submit(t, in, "k-"+tc.name)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if !apperr.Is(err, apperr.Validation) {
				t.Fatalf("kind: %v", err)
			}
			found := false
			for _, d := range diags {
				if d.Code == tc.code && d.Field == tc.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %s field=%s in %v", tc.code, tc.field, diags)
			}
			if n := len(f.fake.Submissions()); n != 0 {
				t.Fatalf("fake got %d submissions", n)
			}
		})
	}
}

func TestSubmitPolicyViolation(t *testing.T) {
	f := newFx(t, "polv")
	f.setTenantPolicy(t, `{"max_gpus_per_job":4}`)
	in := submitIn("#!/bin/bash\necho hi\n")
	in.Resources.GPU.Count = 8
	_, _, err := f.submit(t, in, "pol-1")
	ae, ok := err.(*apperr.Error)
	if !ok || ae.Code != "POLICY_VIOLATION" ||
		!strings.Contains(ae.Message, "gpu") {
		t.Fatalf("want POLICY_VIOLATION gpu, got %v", err)
	}
	if n := len(f.fake.Submissions()); n != 0 {
		t.Fatalf("fake got %d submissions", n)
	}
}

// --- integrity (doc item 6) ---------------------------------------------

func TestIntegrityFailure(t *testing.T) {
	f := newFx(t, "intg")
	res, _, err := f.submit(t, submitIn("#!/bin/bash\necho hi\n"), "i-1")
	if err != nil {
		t.Fatal(err)
	}
	// Tamper with the stored body, bypassing the trigger.
	if _, err := f.pool.Exec(f.ctx,
		`ALTER TABLE scripts DISABLE TRIGGER scripts_no_update`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.pool.Exec(f.ctx,
			`ALTER TABLE scripts ENABLE TRIGGER scripts_no_update`)
	}()
	if _, err := f.pool.Exec(f.ctx,
		`UPDATE scripts SET body=$2 WHERE tenant_id=$1 AND sha256=$3`,
		f.tenant, []byte("echo evil"), res.Job.ScriptDigest[:]); err != nil {
		t.Fatal(err)
	}
	if err := runSubmit(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	j := jobOf(f, t, res.Job.ID)
	if j.State != jobs.StateFailed || j.StateReason != "INTEGRITY" {
		t.Fatalf("job %+v", j)
	}
	if n := len(f.fake.Submissions()); n != 0 {
		t.Fatalf("fake got %d submissions", n)
	}
}

// --- lost submit ---------------------------------------------------------

func TestLostSubmitAdoption(t *testing.T) {
	f := newFx(t, "lost")
	res, _, err := f.submit(t, submitIn("#!/bin/bash\necho hi\n"), "l-1")
	if err != nil {
		t.Fatal(err)
	}
	f.fake.LoseNextSubmitResponse()
	if err := runSubmit(f, t, res.Job.ID); err == nil {
		t.Fatal("expected retryable error on lost submit")
	}
	// Second run must adopt the already-created job, not resubmit.
	if err := runSubmit(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	j := jobOf(f, t, res.Job.ID)
	if j.State != jobs.StateQueued || j.SlurmJobID == nil || *j.SlurmJobID != 1 {
		t.Fatalf("job %+v", j)
	}
	if n := len(f.fake.Submissions()); n != 1 {
		t.Fatalf("duplicate submissions: %d", n)
	}
}

// --- reconcile -----------------------------------------------------------

func TestReconcileLifecycle(t *testing.T) {
	f := newFx(t, "recon")
	res, _, err := f.submit(t, submitIn("#!/bin/bash\necho hi\n"), "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := runSubmit(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	sid := uint32(*jobOf(f, t, res.Job.ID).SlurmJobID)
	f.fake.Advance(sid, slurm.JobRunning)
	if err := runReconcile(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	j := jobOf(f, t, res.Job.ID)
	if j.State != jobs.StateRunning || j.StartedAt == nil {
		t.Fatalf("job %+v", j)
	}
	f.fake.Advance(sid, slurm.JobCompleted)
	if err := runReconcile(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	j = jobOf(f, t, res.Job.ID)
	if j.State != jobs.StateCompleted || j.EndedAt == nil {
		t.Fatalf("job %+v", j)
	}
}

func TestReconcileCancelled(t *testing.T) {
	f := newFx(t, "recc")
	res, _, err := f.submit(t, submitIn("#!/bin/bash\necho hi\n"), "rc-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := runSubmit(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	sid := uint32(*jobOf(f, t, res.Job.ID).SlurmJobID)
	f.fake.Advance(sid, slurm.JobCancelled)
	if err := runReconcile(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	if j := jobOf(f, t, res.Job.ID); j.State != jobs.StateCanceled {
		t.Fatalf("job %+v", j)
	}
}

func TestReconcileLost(t *testing.T) {
	f := newFx(t, "recl")
	res, _, err := f.submit(t, submitIn("#!/bin/bash\necho hi\n"), "rl-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := runSubmit(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	j := jobOf(f, t, res.Job.ID)
	// Age the job past the 10-minute window and point the reconcile at an
	// empty fake (the job vanished from the scheduler and accounting).
	old := time.Now().Add(-11 * time.Minute)
	if _, err := f.pool.Exec(f.ctx,
		`UPDATE jobs SET submitted_at=$2 WHERE id=$1`, j.ID, old); err != nil {
		t.Fatal(err)
	}
	d2 := f.wdeps()
	d2.Factory = fakeFactory{fake.New()} // no such job anywhere
	if err := jobsworker.Reconcile(d2)(f.ctx, workqueue.Item{
		Kind: jobsworker.KindReconcile, Key: "job:" + j.ID.String(),
		Payload: json.RawMessage(`{"job_id":"` + j.ID.String() + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	j = jobOf(f, t, j.ID)
	if j.State != jobs.StateFailed || j.StateReason != "LOST" {
		t.Fatalf("want FAILED/LOST, got %+v", j)
	}
}

// --- cancel --------------------------------------------------------------

func TestCancelSubmitting(t *testing.T) {
	f := newFx(t, "csub")
	res, _, err := f.submit(t, submitIn("#!/bin/bash\necho hi\n"), "c-1")
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.Cancel(f.ctx, f.p, f.tc, res.Job.ID,
		func(context.Context) error { return nil })
	if err != nil || out.State != jobs.StateCanceled {
		t.Fatalf("cancel: %v %+v", err, out)
	}
	// Submit handler must skip canceled jobs — zero submissions.
	if err := runSubmit(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(f.fake.Submissions()); n != 0 {
		t.Fatalf("fake got %d submissions", n)
	}
}

func TestCancelRunning(t *testing.T) {
	f := newFx(t, "crun")
	res, _, err := f.submit(t, submitIn("#!/bin/bash\necho hi\n"), "c-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := runSubmit(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	sid := uint32(*jobOf(f, t, res.Job.ID).SlurmJobID)
	f.fake.Advance(sid, slurm.JobRunning)
	if err := runReconcile(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Cancel(f.ctx, f.p, f.tc, res.Job.ID, func(ctx context.Context) error {
		_, err := workqueue.Enqueue(ctx, f.pool, workqueue.EnqueueRequest{
			Kind: jobssvc.KindCancel, Key: "jobcancel:" + res.Job.ID.String(),
			Payload: map[string]string{"job_id": res.Job.ID.String()},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := jobsworker.Cancel(f.wdeps())(f.ctx, workqueue.Item{
		Kind: jobssvc.KindCancel, Key: "jobcancel:" + res.Job.ID.String(),
		Payload: json.RawMessage(`{"job_id":"` + res.Job.ID.String() + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if sj, err := f.fake.GetJob(f.ctx, slurm.JobID{ID: sid}); err != nil ||
		sj.State != slurm.JobCancelled {
		t.Fatalf("fake job not cancelled: %v %+v", err, sj)
	}
	if err := runReconcile(f, t, res.Job.ID); err != nil {
		t.Fatal(err)
	}
	if j := jobOf(f, t, res.Job.ID); j.State != jobs.StateCanceled {
		t.Fatalf("job %+v", j)
	}
}

// --- idempotency + authz --------------------------------------------------

func TestIdempotentSubmit(t *testing.T) {
	f := newFx(t, "idem")
	in := submitIn("#!/bin/bash\necho hi\n")
	in.Cluster = f.clus.String()
	body, _ := json.Marshal(in)
	r1, _, err := f.svc.Submit(f.ctx, f.p, f.tc, f.pc, in, "key-1",
		sha256.Sum256(body))
	if err != nil {
		t.Fatal(err)
	}
	r2, _, err := f.svc.Submit(f.ctx, f.p, f.tc, f.pc, in, "key-1",
		sha256.Sum256(body))
	if err != nil || !r2.Replayed || r2.Job.ID != r1.Job.ID {
		t.Fatalf("replay: %v %+v", err, r2)
	}
	// Same key, different body -> 409.
	in2 := in
	in2.Name = "other"
	body2, _ := json.Marshal(in2)
	_, _, err = f.svc.Submit(f.ctx, f.p, f.tc, f.pc, in2, "key-1",
		sha256.Sum256(body2))
	if !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want 409, got %v", err)
	}
}

func TestAuthzMatrix(t *testing.T) {
	f := newFx(t, "authz")
	res, _, err := f.submit(t, submitIn("#!/bin/bash\necho hi\n"), "a-1")
	if err != nil {
		t.Fatal(err)
	}
	j := res.Job

	// Other owner: same-tenant researcher, not the creator — can submit
	// but cannot read or cancel j (self-scoped).
	other := mkUser(t, f.pool, "authz-o")
	tenantMember(t, f.pool, f.tenant, other)
	op := authn.Principal{UserID: other, Kind: authn.KindUser}
	if _, err := f.svc.Get(f.ctx, op, f.tc, j.ID); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("other-owner get: %v", err)
	}
	if _, err := f.svc.Cancel(f.ctx, op, f.tc, j.ID,
		func(context.Context) error { return nil }); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("other-owner cancel: %v", err)
	}

	// Project role: project-admin (member) gets job.read.project.
	adm := mkUser(t, f.pool, "authz-pa")
	tenantMember(t, f.pool, f.tenant, adm)
	prepo := projectpg.New(f.pool)
	if err := prepo.UpsertMembership(f.ctx, tenants.PlatformScope(),
		projects.Membership{TenantID: f.tenant, ProjectID: f.proj,
			UserID: adm, Roles: []string{"project-admin"}, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	padmin := authn.Principal{UserID: adm, Kind: authn.KindUser}
	pctx := authz.WithProjectRoles(f.ctx, f.proj.String(), []string{"project-admin"})
	if _, err := f.svc.Get(pctx, padmin, f.tc, j.ID); err != nil {
		t.Fatalf("project-admin get: %v", err)
	}
	// project-admin has no job.cancel.any.
	if _, err := f.svc.Cancel(pctx, padmin, f.tc, j.ID,
		func(context.Context) error { return nil }); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("project-admin cancel: %v", err)
	}

	// Tenant operator: job.read.tenant + job.cancel.any.
	opr := mkUser(t, f.pool, "authz-to")
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO tenant_memberships (tenant_id, user_id, roles, source)
		VALUES ($1,$2,'{tenant-operator}','manual')`, f.tenant, opr); err != nil {
		t.Fatal(err)
	}
	opp := authn.Principal{UserID: opr, Kind: authn.KindUser}
	otc := tenants.TenantContext{Tenant: f.tc.Tenant,
		Membership: &tenants.Membership{Roles: []string{"tenant-operator"}}}
	ocx := tenants.WithTenantContext(f.ctx, otc)
	if _, err := f.svc.Get(ocx, opp, otc, j.ID); err != nil {
		t.Fatalf("operator get: %v", err)
	}
	if _, err := f.svc.Cancel(ocx, opp, otc, j.ID,
		func(context.Context) error { return nil }); err != nil {
		t.Fatalf("operator cancel: %v", err)
	}

	// Tenant viewer (job.read.self only) cannot read another's job.
	vu := mkUser(t, f.pool, "authz-v")
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO tenant_memberships (tenant_id, user_id, roles, source)
		VALUES ($1,$2,'{viewer}','manual')`, f.tenant, vu); err != nil {
		t.Fatal(err)
	}
	vp := authn.Principal{UserID: vu, Kind: authn.KindUser}
	vtc := tenants.TenantContext{Tenant: f.tc.Tenant,
		Membership: &tenants.Membership{Roles: []string{"viewer"}}}
	vcx := tenants.WithTenantContext(f.ctx, vtc)
	if _, err := f.svc.Get(vcx, vp, vtc, j.ID); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("viewer get: %v", err)
	}
}
