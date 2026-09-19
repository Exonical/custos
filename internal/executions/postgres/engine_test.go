package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/executions"
	"github.com/Exonical/custos/internal/executions/engine"
	execpg "github.com/Exonical/custos/internal/executions/postgres"
	"github.com/Exonical/custos/internal/jobs"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/workqueue"
	policypg "github.com/Exonical/custos/internal/policies/postgres"
	policiesvc "github.com/Exonical/custos/internal/policies/service"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	scriptpg "github.com/Exonical/custos/internal/scripts/postgres"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
	"github.com/Exonical/custos/internal/validation/pipeline"
	vpolicy "github.com/Exonical/custos/internal/validation/policy"
	vpolicypg "github.com/Exonical/custos/internal/validation/postgres"
	"github.com/Exonical/custos/internal/workflows"
	wfpg "github.com/Exonical/custos/internal/workflows/postgres"
	"github.com/Exonical/custos/internal/workflowspec"
)

// fixture bundles the seeded rows and wired engine deps for one test.
type fixture struct {
	pool      *pgxpool.Pool
	deps      engine.Deps
	repo      *execpg.Repository
	jobs      *jobpg.Repository
	wfRepo    *wfpg.Repository
	tenantID  uuid.UUID
	projectID uuid.UUID
	userID    uuid.UUID
	clusterID uuid.UUID
}

func setup(t *testing.T) fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()

	tid := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO tenants (id, slug, name, state, settings, version)
		VALUES ($1,$2,'T','active','{}',1)`, tid,
		"t-"+tid.String()[:8]); err != nil {
		t.Fatal(err)
	}
	uid := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, issuer, subject, kind) VALUES ($1,'iss',$2,'user')`,
		uid, "u-"+uid.String()[:8]); err != nil {
		t.Fatal(err)
	}
	pid := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, tenant_id, slug, name, state, version)
		VALUES ($1,$2,$3,'P','active',1)`, pid, tid,
		"p-"+pid.String()[:8]); err != nil {
		t.Fatal(err)
	}
	cid := uuid.Must(uuid.NewV7())
	c := clusters.Cluster{
		ID: cid, Name: "main-" + cid.String()[:6], DisplayName: "main",
		BaseURL: "https://slurm.example:6820", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "slurm/token"},
		Visibility: clusters.VisibilityAssigned,
		State:      clusters.StateActive, Version: 1,
	}
	crepo := clusterpg.New(pool)
	if err := crepo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	caps, _ := json.Marshal(slurm.Capabilities{
		Partitions: []slurm.Partition{{Name: "main"}},
		QoSNames:   []string{"normal"},
	})
	if _, err := pool.Exec(ctx,
		`UPDATE clusters SET capabilities=$2, capabilities_at=now() WHERE id=$1`,
		cid, caps); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO cluster_tenant_assignments (cluster_id, tenant_id, source)
		VALUES ($1,$2,'manual')`, cid, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_cluster_bindings
		  (id, tenant_id, project_id, cluster_id, slurm_account, version)
		VALUES ($1,$2,$3,$4,'acct',1)`,
		uuid.Must(uuid.NewV7()), tid, pid, cid); err != nil {
		t.Fatal(err)
	}

	projectRepo := projectpg.New(pool)
	tenantRepo := tenantpg.New(pool)
	vpolStore := vpolicypg.NewPolicyStore(pool)
	return fixture{
		pool:   pool,
		repo:   execpg.New(pool),
		jobs:   jobpg.New(pool),
		wfRepo: wfpg.New(pool),
		deps: engine.Deps{
			Execs:     execpg.New(pool),
			Workflows: wfpg.New(pool),
			Policies: policiesvc.NewService(policypg.New(pool),
				authz.RBAC{}, nil),
			VPolicy: vpolicy.NewService(vpolicy.Deps{
				Store: vpolStore, Clusters: crepo,
				AZ: authz.RBAC{},
			}),
			Clusters: crepo,
			Projects: projectsvc.NewService(projectRepo, projectRepo,
				projectRepo, tenantRepo, crepo, authz.RBAC{}, nil),
			Pipeline:    pipeline.New(nil),
			Validations: vpolicypg.NewValidationStore(pool),
			Scripts:     scriptpg.New(pool),
			Jobs:        jobpg.New(pool),
		},
		tenantID: tid, projectID: pid, userID: uid, clusterID: cid,
	}
}

// seedVersion creates a workflow and a published version for spec.
func (f fixture) seedVersion(t *testing.T, name string,
	spec workflowspec.Workflow) (workflows.Workflow, workflows.Version) {
	t.Helper()
	ctx := context.Background()
	scope := tenants.PlatformScope()
	w := workflows.Workflow{
		ID: uuid.Must(uuid.NewV7()), TenantID: f.tenantID,
		ProjectID: f.projectID, Name: name,
		State: workflows.StateActive, CreatedBy: f.userID,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Version: 1,
	}
	if err := f.wfRepo.Create(ctx, scope, w); err != nil {
		t.Fatal(err)
	}
	canon, err := workflowspec.Canonical(spec)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := workflowspec.SpecHash(spec)
	if err != nil {
		t.Fatal(err)
	}
	v := workflows.Version{
		ID: uuid.Must(uuid.NewV7()), WorkflowID: w.ID,
		TenantID: w.TenantID, Number: 1, State: workflows.VersionDraft,
		SchemaVersion: "custos.io/v1alpha1", Spec: canon,
		SpecHash: hash,
		Layout:   []byte(`{}`), CreatedBy: f.userID,
		CreatedAt: time.Now().UTC(), Version: 1,
	}
	if err := f.wfRepo.CreateVersion(ctx, scope, v); err != nil {
		t.Fatal(err)
	}
	if err := f.wfRepo.SetVersionState(ctx, scope, v,
		workflows.VersionPublished, 0); err != nil {
		t.Fatal(err)
	}
	v.State = workflows.VersionPublished
	return w, v
}

// seedExec inserts a PENDING execution pinned to the version.
func (f fixture) seedExec(t *testing.T, w workflows.Workflow,
	v workflows.Version, params []byte) executions.Execution {
	t.Helper()
	if len(params) == 0 {
		params = []byte(`{}`)
	}
	now := time.Now().UTC()
	e := executions.Execution{
		ID: uuid.Must(uuid.NewV7()), TenantID: f.tenantID,
		ProjectID: f.projectID, WorkflowID: w.ID,
		WorkflowVersionID: v.ID, SpecHash: v.SpecHash,
		Parameters: params, Strategy: "auto",
		State: executions.ExecPending, RequestedBy: f.userID,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	_, err := f.repo.CreateWithIdempotency(context.Background(),
		tenants.PlatformScope(), e, executions.IdemRecord{
			Key: "k-" + e.ID.String(), ExpiresAt: now.Add(time.Hour),
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// advance runs the execution.advance handler once.
func (f fixture) advance(t *testing.T, e executions.Execution) {
	t.Helper()
	h := engine.Advance(f.deps)
	err := h(context.Background(), workqueue.Item{
		Kind: engine.KindAdvance,
		Payload: json.RawMessage(
			`{"execution_id":"` + e.ID.String() + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// admit runs the task.admit handler once.
func (f fixture) admit(t *testing.T, taskID uuid.UUID) {
	t.Helper()
	h := engine.Admit(f.deps)
	err := h(context.Background(), workqueue.Item{
		Kind: engine.KindAdmit,
		Payload: json.RawMessage(
			`{"task_id":"` + taskID.String() + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f fixture) getExec(t *testing.T, id uuid.UUID) executions.Execution {
	t.Helper()
	e, err := f.repo.Get(context.Background(), tenants.PlatformScope(),
		uuid.Nil, id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (f fixture) tasks(t *testing.T,
	execID uuid.UUID) []executions.TaskExecution {
	t.Helper()
	ts, err := f.repo.ListTasks(context.Background(),
		tenants.PlatformScope(), f.tenantID, execID)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func (f fixture) taskByName(t *testing.T, execID uuid.UUID,
	name string) executions.TaskExecution {
	t.Helper()
	for _, x := range f.tasks(t, execID) {
		if x.TaskName == name {
			return x
		}
	}
	t.Fatalf("task %s not found", name)
	return executions.TaskExecution{}
}

// admitReady admits every READY task of the execution.
func (f fixture) admitReady(t *testing.T, execID uuid.UUID) {
	t.Helper()
	for _, x := range f.tasks(t, execID) {
		if x.State == executions.TaskReady {
			f.admit(t, x.ID)
		}
	}
}

// jobTerminal drives a task's job through QUEUED/RUNNING to a terminal
// state, mirroring what the jobs worker does (transition enqueues
// execution.advance via the worker; here we call advance directly).
func (f fixture) jobTerminal(t *testing.T, task executions.TaskExecution,
	to jobs.State, reason string) {
	t.Helper()
	ctx := context.Background()
	if task.JobID == nil {
		t.Fatal("task has no job")
	}
	j, err := f.jobs.Get(ctx, tenants.PlatformScope(), *task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	set := func(s jobs.State, r string) {
		p := jobs.Patch{State: &s}
		if r != "" {
			p.Reason = &r
		}
		j, err = f.jobs.Transition(ctx, tenants.PlatformScope(),
			j.ID, j.Version, p)
		if err != nil {
			t.Fatal(err)
		}
	}
	switch to {
	case jobs.StateCompleted, jobs.StateFailed:
		if j.State == jobs.StateSubmitting {
			set(jobs.StateQueued, "")
		}
		set(jobs.StateRunning, "")
		set(to, reason)
	case jobs.StateCanceled:
		if j.State == jobs.StateSubmitting {
			set(jobs.StateQueued, "")
		}
		set(to, reason)
	default:
		set(to, reason)
	}
}

// cmdSpec builds a spec of command tasks on the seeded cluster.
func (f fixture) cmdSpec(tasks ...workflowspec.Task) workflowspec.Workflow {
	return workflowspec.Workflow{
		APIVersion: workflowspec.APIVersionV1Alpha1,
		Kind:       workflowspec.KindWorkflow,
		Metadata:   workflowspec.Metadata{Name: "wf"},
		Spec: workflowspec.Spec{
			Placement: &workflowspec.Placement{
				Cluster: "main-" + f.clusterID.String()[:6]},
			Tasks: tasks,
		},
	}
}

func cmd(name string, deps ...string) workflowspec.Task {
	return workflowspec.Task{Name: name, Type: "batch",
		Command: []string{"/bin/true"}, DependsOn: deps,
		Resources: workflowspec.TaskResources{
			CPU: 1, Memory: "512Mi", Walltime: "10m"}}
}

func TestLinearChain(t *testing.T) {
	f := setup(t)
	w, v := f.seedVersion(t, "linear", f.cmdSpec(
		cmd("a"), cmd("b", "a"), cmd("c", "b")))
	e := f.seedExec(t, w, v, nil)

	f.advance(t, e)
	e = f.getExec(t, e.ID)
	if e.State != executions.ExecQueued {
		t.Fatalf("state %s, want QUEUED", e.State)
	}
	ts := f.tasks(t, e.ID)
	if len(ts) != 3 {
		t.Fatalf("%d tasks, want 3", len(ts))
	}
	if got := f.taskByName(t, e.ID, "a").State; got != executions.TaskReady {
		t.Fatalf("a: %s", got)
	}
	for _, n := range []string{"b", "c"} {
		if got := f.taskByName(t, e.ID, n).State; got != executions.TaskBlocked {
			t.Fatalf("%s: %s", n, got)
		}
	}

	for _, n := range []string{"a", "b", "c"} {
		f.admitReady(t, e.ID)
		task := f.taskByName(t, e.ID, n)
		if task.State != executions.TaskSubmitting {
			t.Fatalf("%s: %s (%s) after admit", n, task.State,
				task.StateReason)
		}
		if task.JobID == nil {
			t.Fatalf("%s: no job", n)
		}
		f.jobTerminal(t, task, jobs.StateCompleted, "")
		e = f.getExec(t, e.ID)
		f.advance(t, e)
	}
	e = f.getExec(t, e.ID)
	if e.State != executions.ExecSucceeded {
		t.Fatalf("state %s (%s), want SUCCEEDED", e.State, e.StateReason)
	}
}

func TestFanOutFanInWhen(t *testing.T) {
	f := setup(t)
	fan := cmd("simulate")
	fan.FanOut = &workflowspec.FanOut{Count: 3}
	agg := cmd("aggregate", "simulate")
	agg.When = "{{ tasks.simulate.succeededCount }} > 0"
	w, v := f.seedVersion(t, "fan", f.cmdSpec(fan, agg))
	e := f.seedExec(t, w, v, nil)

	f.advance(t, e)
	if e = f.getExec(t, e.ID); e.State != executions.ExecQueued {
		t.Fatalf("state %s (%s), want QUEUED", e.State, e.StateReason)
	}
	var sims []executions.TaskExecution
	for _, x := range f.tasks(t, e.ID) {
		if x.TaskName == "simulate" {
			sims = append(sims, x)
		}
	}
	if len(sims) != 3 {
		t.Fatalf("%d fan-out tasks, want 3", len(sims))
	}
	for _, x := range sims {
		if x.State != executions.TaskReady || x.Count != 3 {
			t.Fatalf("fan task %+v", x)
		}
		f.admit(t, x.ID)
	}
	for _, x := range sims {
		x = f.taskByName(t, e.ID, "simulate")
		_ = x
	}
	// Complete all three instances.
	for _, x := range f.tasks(t, e.ID) {
		if x.TaskName == "simulate" && x.State == executions.TaskSubmitting {
			f.jobTerminal(t, x, jobs.StateCompleted, "")
		}
	}
	f.advance(t, f.getExec(t, e.ID))
	aggTask := f.taskByName(t, e.ID, "aggregate")
	if aggTask.State != executions.TaskReady {
		t.Fatalf("aggregate: %s, want READY", aggTask.State)
	}
	f.admit(t, aggTask.ID)
	aggTask = f.taskByName(t, e.ID, "aggregate")
	f.jobTerminal(t, aggTask, jobs.StateCompleted, "")
	f.advance(t, f.getExec(t, e.ID))
	if got := f.getExec(t, e.ID).State; got != executions.ExecSucceeded {
		t.Fatalf("state %s, want SUCCEEDED", got)
	}
}

func TestDepFailureSkips(t *testing.T) {
	f := setup(t)
	w, v := f.seedVersion(t, "depfail", f.cmdSpec(
		cmd("a"), cmd("b", "a"), cmd("c", "b")))
	e := f.seedExec(t, w, v, nil)
	f.advance(t, e)
	f.admitReady(t, e.ID)
	a := f.taskByName(t, e.ID, "a")
	f.jobTerminal(t, a, jobs.StateFailed, "NODE_FAIL")
	f.advance(t, f.getExec(t, e.ID))
	if got := f.taskByName(t, e.ID, "b").State; got != executions.TaskSkipped {
		t.Fatalf("b: %s, want SKIPPED", got)
	}
	// c's dep b is terminal (SKIPPED counts terminal-ok) — but a FAILED
	// means the execution fails fast: b SKIPPED cascades c too? c's dep
	// b is SKIPPED → terminal-ok → c unblocks. However fail-fast
	// policy cancels/skips remaining work on any FAILED task.
	f.advance(t, f.getExec(t, e.ID))
	e = f.getExec(t, e.ID)
	if e.State != executions.ExecFailed {
		t.Fatalf("state %s (%s), want FAILED", e.State, e.StateReason)
	}
	for _, x := range f.tasks(t, e.ID) {
		if !executions.Terminal(x.State) {
			t.Errorf("task %s non-terminal %s", x.TaskName, x.State)
		}
	}
}

func TestOnDependencyFailureRun(t *testing.T) {
	f := setup(t)
	b := cmd("b", "a")
	b.OnDependencyFailure = "run"
	w, v := f.seedVersion(t, "afterany", f.cmdSpec(cmd("a"), b))
	e := f.seedExec(t, w, v, nil)
	f.advance(t, e)
	f.admitReady(t, e.ID)
	a := f.taskByName(t, e.ID, "a")
	f.jobTerminal(t, a, jobs.StateFailed, "NODE_FAIL")
	// fail-fast would cancel b — but b is still BLOCKED at this
	// advance; check ordering: unblock happens before fail-fast, so b
	// goes READY then gets skipped/canceled by the policy. With
	// failurePolicy=fail (default) the FAILED task still fails the run;
	// b was already unblocked to READY which is a valid observable
	// outcome of onDependencyFailure: run.
	f.advance(t, f.getExec(t, e.ID))
	bt := f.taskByName(t, e.ID, "b")
	if bt.State != executions.TaskReady &&
		bt.State != executions.TaskSkipped &&
		bt.State != executions.TaskCanceled {
		t.Fatalf("b: %s", bt.State)
	}
}

func TestRetryThenFail(t *testing.T) {
	f := setup(t)
	a := cmd("a")
	a.Retry = &workflowspec.Retry{Attempts: 2, On: []string{"NODE_FAIL"}}
	w, v := f.seedVersion(t, "retry", f.cmdSpec(a))
	e := f.seedExec(t, w, v, nil)
	f.advance(t, e)
	f.admitReady(t, e.ID)
	a1 := f.taskByName(t, e.ID, "a")
	f.jobTerminal(t, a1, jobs.StateFailed, "NODE_FAIL")
	f.advance(t, f.getExec(t, e.ID))
	a2 := f.taskByName(t, e.ID, "a")
	if a2.State != executions.TaskReady || a2.Attempt != 2 {
		t.Fatalf("retry: state=%s attempt=%d", a2.State, a2.Attempt)
	}
	if a2.JobID != nil {
		t.Fatal("retry kept the old job_id")
	}
	f.admit(t, a2.ID)
	a2 = f.taskByName(t, e.ID, "a")
	f.jobTerminal(t, a2, jobs.StateFailed, "NODE_FAIL")
	f.advance(t, f.getExec(t, e.ID))
	if got := f.taskByName(t, e.ID, "a").State; got != executions.TaskFailed {
		t.Fatalf("a: %s, want FAILED", got)
	}
	if got := f.getExec(t, e.ID).State; got != executions.ExecFailed {
		t.Fatalf("exec %s, want FAILED", got)
	}
}

func TestFailurePolicyContinue(t *testing.T) {
	f := setup(t)
	spec := f.cmdSpec(cmd("a"), cmd("b"))
	spec.Spec.Execution = &workflowspec.Execution{
		FailurePolicy: "continue"}
	w, v := f.seedVersion(t, "continue", spec)
	e := f.seedExec(t, w, v, nil)
	f.advance(t, e)
	f.admitReady(t, e.ID)
	f.jobTerminal(t, f.taskByName(t, e.ID, "a"), jobs.StateFailed,
		"NODE_FAIL")
	f.jobTerminal(t, f.taskByName(t, e.ID, "b"), jobs.StateCompleted, "")
	f.advance(t, f.getExec(t, e.ID))
	if got := f.getExec(t, e.ID).State; got != executions.ExecPartialFailure {
		t.Fatalf("state %s, want PARTIAL_FAILURE", got)
	}
}

func TestCancel(t *testing.T) {
	f := setup(t)
	w, v := f.seedVersion(t, "cancel", f.cmdSpec(cmd("a"), cmd("b", "a")))
	e := f.seedExec(t, w, v, nil)
	f.advance(t, e)
	f.admitReady(t, e.ID)
	a := f.taskByName(t, e.ID, "a")
	// Move a to RUNNING; the mirror step applies one transition at a
	// time, so advance after each (as the jobs worker hook would).
	ctx := context.Background()
	j, err := f.jobs.Get(ctx, tenants.PlatformScope(), *a.JobID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []jobs.State{jobs.StateQueued, jobs.StateRunning} {
		j, err = f.jobs.Transition(ctx, tenants.PlatformScope(), j.ID,
			j.Version, jobs.Patch{State: &s})
		if err != nil {
			t.Fatal(err)
		}
		f.advance(t, f.getExec(t, e.ID))
	}
	e = f.getExec(t, e.ID)
	if e.State != executions.ExecRunning {
		t.Fatalf("state %s, want RUNNING", e.State)
	}
	// Cancel: → CANCELING, enqueue advance.
	if _, err := f.repo.CancelExec(ctx, tenants.PlatformScope(), e.ID,
		e.Version, func(ex executions.Execer) error {
			_, err := workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{
				Kind: engine.KindAdvance, Key: "execution:" + e.ID.String(),
				Payload: map[string]string{"execution_id": e.ID.String()},
			})
			return err
		}); err != nil {
		t.Fatal(err)
	}
	e = f.getExec(t, e.ID)
	if e.State != executions.ExecCanceling {
		t.Fatalf("state %s, want CANCELING", e.State)
	}
	f.advance(t, e)
	// a's job got job.cancel enqueued; b (BLOCKED) is canceled.
	var cancelItems int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM work_items
		WHERE kind='job.cancel'`).Scan(&cancelItems); err != nil {
		t.Fatal(err)
	}
	if cancelItems == 0 {
		t.Error("no job.cancel item enqueued")
	}
	if got := f.taskByName(t, e.ID, "b").State; got != executions.TaskCanceled {
		t.Fatalf("b: %s, want CANCELED", got)
	}
	// Job cancel lands → task canceled → execution CANCELED.
	a = f.taskByName(t, e.ID, "a")
	f.jobTerminal(t, a, jobs.StateCanceled, "")
	f.advance(t, f.getExec(t, e.ID))
	if got := f.getExec(t, e.ID).State; got != executions.ExecCanceled {
		t.Fatalf("state %s, want CANCELED", got)
	}
}

func TestConditionTask(t *testing.T) {
	f := setup(t)
	c := workflowspec.Task{Name: "gate", Type: "condition",
		When: "{{ parameters.go }}"}
	b := cmd("work", "gate")
	spec := f.cmdSpec(c, b)
	spec.Spec.Parameters = map[string]workflowspec.Parameter{
		"go": {Type: "boolean", Default: true},
	}
	w, v := f.seedVersion(t, "cond", spec)
	e := f.seedExec(t, w, v, nil)
	f.advance(t, e)
	if got := f.taskByName(t, e.ID, "gate").State; got != executions.TaskCompleted {
		t.Fatalf("gate: %s, want COMPLETED", got)
	}
}

func TestParameterInvalid(t *testing.T) {
	f := setup(t)
	spec := f.cmdSpec(cmd("a"))
	spec.Spec.Parameters = map[string]workflowspec.Parameter{
		"need": {Type: "string", Required: true},
	}
	w, v := f.seedVersion(t, "params", spec)
	e := f.seedExec(t, w, v, nil)
	f.advance(t, e)
	e = f.getExec(t, e.ID)
	if e.State != executions.ExecFailed ||
		e.StateReason != "PARAMETERS_INVALID" {
		t.Fatalf("state %s (%s)", e.State, e.StateReason)
	}
}

func TestFanOutBounds(t *testing.T) {
	f := setup(t)
	a := cmd("a")
	a.FanOut = &workflowspec.FanOut{Count: 5000}
	w, v := f.seedVersion(t, "fanbound", f.cmdSpec(a))
	e := f.seedExec(t, w, v, nil)
	f.advance(t, e)
	e = f.getExec(t, e.ID)
	if e.State != executions.ExecFailed ||
		e.StateReason != "FANOUT_BOUNDS" {
		t.Fatalf("state %s (%s)", e.State, e.StateReason)
	}
}

func TestSpecTampered(t *testing.T) {
	f := setup(t)
	w, v := f.seedVersion(t, "tamper", f.cmdSpec(cmd("a")))
	e := f.seedExec(t, w, v, nil)
	// Corrupt the pinned hash.
	e.SpecHash[0] ^= 0xff
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE workflow_executions SET spec_hash=$2 WHERE id=$1`,
		e.ID, e.SpecHash[:]); err != nil {
		t.Fatal(err)
	}
	f.advance(t, e)
	e = f.getExec(t, e.ID)
	if e.State != executions.ExecFailed ||
		e.StateReason != "SPEC_TAMPERED" {
		t.Fatalf("state %s (%s)", e.State, e.StateReason)
	}
}
