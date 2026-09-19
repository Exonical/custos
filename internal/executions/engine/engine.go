// Package engine implements the workflow execution engine's durable
// work handlers (docs/workflows.md §Engine strategy, docs/workers.md):
// execution.advance validates/materializes a PENDING execution and
// then mirrors job progress, unblocks dependents, applies retry and
// failure policy, and settles the terminal state; task.admit runs the
// per-task validation/admission/freeze/submit step.
//
// Every state change is a guarded (state, version) UPDATE inside one
// transaction with its follow-up enqueue; a zero-row transition means
// another worker advanced the row — the handler reloads or simply
// finishes, since the item key dedupe guarantees a pending advance.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/executions"
	"github.com/Exonical/custos/internal/jobs"
	jobssvc "github.com/Exonical/custos/internal/jobs/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/workqueue"
	policiessvc "github.com/Exonical/custos/internal/policies/service"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	"github.com/Exonical/custos/internal/scripts"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/pipeline"
	vpolicy "github.com/Exonical/custos/internal/validation/policy"
	"github.com/Exonical/custos/internal/workflows"
	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/expr"
	wfvalidate "github.com/Exonical/custos/internal/workflowspec/validate"
)

// Work kinds registered on the queue.
const (
	KindAdvance = "execution.advance"
	KindAdmit   = "task.admit"
)

// maxFanOut bounds a fan-out count (docs/workflows.md).
const maxFanOut = 1024

// ValidationStore is the validation persistence port task.admit needs
// (same surface the workflows service uses).
type ValidationStore interface {
	Put(ctx context.Context, scope tenants.Scope,
		sv validation.ScriptValidation) error
	Latest(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID,
		digest validation.Digest, policyVersion int64,
		inputHash uint64) (validation.ScriptValidation, error)
}

// JobStore reads jobs for the mirror step (platform scope).
type JobStore interface {
	Get(ctx context.Context, scope tenants.Scope, id uuid.UUID) (jobs.Job, error)
}

// Deps wires the engine handlers.
type Deps struct {
	Execs       executions.Repository
	Workflows   workflows.Repository
	Policies    *policiessvc.Service
	VPolicy     *vpolicy.Service
	Clusters    clusters.Repository
	Projects    *projectsvc.Service
	Pipeline    *pipeline.Pipeline
	Validations ValidationStore
	Scripts     scripts.Store
	Jobs        JobStore
	Audit       audit.Recorder // may be nil
	Metrics     *pipeline.Metrics
	Now         func() time.Time // tests may override; nil → time.Now
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

func (d Deps) record(ctx context.Context, ev audit.Event) {
	if d.Audit != nil {
		_ = d.Audit.Record(ctx, ev)
	}
}

// Advance returns the execution.advance handler. Item key:
// execution:<id>.
func Advance(d Deps) workqueue.Handler {
	return func(ctx context.Context, it workqueue.Item) error {
		var p struct {
			ExecutionID string `json:"execution_id"`
		}
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			return workqueue.Perm(fmt.Errorf("%s payload: %w", it.Kind, err))
		}
		id, err := uuid.Parse(p.ExecutionID)
		if err != nil {
			return workqueue.Perm(fmt.Errorf("%s execution_id: %w",
				it.Kind, err))
		}
		e, err := d.Execs.Get(ctx, tenants.PlatformScope(), uuid.Nil, id)
		if err != nil {
			if apperr.Is(err, apperr.NotFound) {
				return nil // deleted between enqueue and lease
			}
			return err
		}
		switch e.State {
		case executions.ExecPending, executions.ExecValidating:
			return validateStage(ctx, d, e)
		case executions.ExecQueued, executions.ExecRunning,
			executions.ExecCanceling:
			return advanceStage(ctx, d, e)
		default:
			return nil // terminal
		}
	}
}

// --- VALIDATING --------------------------------------------------------------

// validateStage runs PENDING → VALIDATING → QUEUED: re-validate the
// pinned spec, resolve parameters, materialize task rows, and enqueue
// task.admit for the initially-READY tasks — the materialization and
// enqueue land in one transaction.
func validateStage(ctx context.Context, d Deps,
	e executions.Execution) error {
	scope := tenants.PlatformScope()
	if e.State == executions.ExecPending {
		to := executions.ExecValidating
		var err error
		e, err = d.Execs.TransitionExec(ctx, scope, e.ID,
			executions.ExecPending, e.Version,
			executions.ExecPatch{State: &to}, nil)
		if err != nil {
			if executions.IsStale(err) {
				return nil // another worker owns it; its enqueue stands
			}
			return err
		}
	}
	fail := func(reason string) error {
		to := executions.ExecFailed
		end := d.now()
		_, err := d.Execs.TransitionExec(ctx, scope, e.ID,
			executions.ExecValidating, e.Version, executions.ExecPatch{
				State: &to, Reason: &reason, EndedAt: &end,
			}, nil)
		if err != nil && !executions.IsStale(err) {
			return err
		}
		return nil
	}

	wf, err := d.Workflows.Get(ctx, scope, e.TenantID, e.WorkflowID)
	if err != nil {
		return err
	}
	v, err := d.Workflows.GetVersion(ctx, scope, e.TenantID,
		e.WorkflowID, e.WorkflowVersionID)
	if err != nil {
		return err
	}
	if v.State != workflows.VersionPublished ||
		v.SpecHash != e.SpecHash {
		d.record(ctx, audit.Event{
			Actor:    audit.Actor{Type: audit.ActorSystem, ID: "custos"},
			Action:   "workflow.execution.spec_tampered",
			Result:   audit.ResultError,
			Target:   audit.Target{Type: "workflow_execution", ID: e.ID.String()},
			TenantID: &e.TenantID,
			Details: map[string]any{
				"version":       v.ID.String(),
				"version_state": string(v.State),
				"expected_hash": fmt.Sprintf("%x", e.SpecHash),
				"actual_hash":   fmt.Sprintf("%x", v.SpecHash),
			},
		})
		return fail("SPEC_TAMPERED")
	}
	spec, err := workflowspec.Decode(v.Spec, "application/json")
	if err != nil {
		return fail("SPEC_DECODE")
	}
	if errs := wfvalidate.Static(spec); len(errs) > 0 {
		return fail(errs[0].Code)
	}
	vc, err := buildContext(ctx, d, e)
	if err != nil {
		return err
	}
	if errs := wfvalidate.Contextual(spec, vc); len(errs) > 0 {
		return fail(errs[0].Code)
	}
	params, perrs := wfvalidate.ResolveParameters(
		spec.Spec.Parameters, e.Parameters)
	if len(perrs) > 0 {
		return fail("PARAMETERS_INVALID")
	}

	// Materialize: one row per (task, index); fan-out counts are
	// evaluated over the resolved parameters.
	tasks := make([]executions.TaskExecution, 0, len(spec.Spec.Tasks))
	now := d.now()
	for _, t := range spec.Spec.Tasks {
		count := 1
		if t.FanOut != nil && t.FanOut.Count != nil {
			n, err := evalFanOut(t.FanOut.Count, params)
			if err != nil || n < 1 || n > maxFanOut {
				return fail("FANOUT_BOUNDS")
			}
			count = n
		}
		st := executions.TaskReady
		var reason *string
		if len(t.DependsOn) > 0 {
			st = executions.TaskBlocked
		} else {
			// Root tasks unblock trivially: evaluate `when` now, and
			// condition tasks complete immediately without admit.
			if t.When != "" && !evalWhen(t.When, spec, wf, v, e,
				params, nil) {
				st = executions.TaskSkipped
				r := "WHEN_FALSE"
				reason = &r
			} else if t.Type == "condition" {
				st = executions.TaskCompleted
			}
		}
		for i := 0; i < count; i++ {
			tasks = append(tasks, executions.TaskExecution{
				ID: uuid.Must(uuid.NewV7()), ExecutionID: e.ID,
				TenantID: e.TenantID, TaskName: t.Name, Index: i,
				Count: count, Attempt: 1, State: st,
				StateReason: deref(reason),
				CreatedAt:   now, UpdatedAt: now, Version: 1,
			})
		}
	}
	to := executions.ExecQueued
	start := now
	_, err = d.Execs.Materialize(ctx, scope, e.ID,
		executions.ExecValidating, e.Version, tasks,
		executions.ExecPatch{State: &to, StartedAt: &start},
		func(ex executions.Execer) error {
			for _, t := range tasks {
				if t.State != executions.TaskReady {
					continue
				}
				if _, err := enqueueAdmit(ctx, ex, e, t.ID); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		if executions.IsStale(err) {
			return nil
		}
		return err
	}
	d.record(ctx, audit.Event{
		Actor:    audit.Actor{Type: audit.ActorSystem, ID: "custos"},
		Action:   "workflow.execution.materialized",
		Result:   audit.ResultAllow,
		Target:   audit.Target{Type: "workflow_execution", ID: e.ID.String()},
		TenantID: &e.TenantID,
		Details: map[string]any{
			"workflow": wf.Name, "version": v.Number,
			"tasks": len(tasks),
		},
	})
	return nil
}

// evalFanOut resolves fanOut.count (integer or expression over
// parameters) to a bounded int.
func evalFanOut(count any, params map[string]any) (int, error) {
	switch c := count.(type) {
	case float64:
		return int(c), nil
	case int:
		return c, nil
	case string:
		e, err := expr.BareExpr(c)
		if err != nil {
			return 0, err
		}
		v, err := e.Eval(paramScope(params))
		if err != nil || v.Kind != expr.Int {
			return 0, fmt.Errorf("fanOut.count is not an integer")
		}
		if v.Int < 1 || v.Int > maxFanOut {
			return int(v.Int), nil // out of bounds; caller fails
		}
		return int(v.Int), nil
	default:
		return 0, fmt.Errorf("fanOut.count has unsupported type")
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// paramScope is the expression scope over resolved parameters.
type paramScope map[string]any

func (s paramScope) Lookup(path []string) (expr.Value, bool) {
	if len(path) != 2 || path[0] != "parameters" {
		return expr.Value{}, false
	}
	return paramValue(s[path[1]])
}

func paramValue(v any) (expr.Value, bool) {
	switch x := v.(type) {
	case string:
		return expr.Value{Kind: expr.String, Str: x}, true
	case bool:
		return expr.Value{Kind: expr.Bool, Bool: x}, true
	case float64:
		return expr.Value{Kind: expr.Int, Int: int64(x)}, true
	case int64:
		return expr.Value{Kind: expr.Int, Int: x}, true
	case int:
		return expr.Value{Kind: expr.Int, Int: int64(x)}, true
	}
	return expr.Value{}, false
}

// buildContext is the validate.Contextual context for the execution's
// project (same wiring as workflows/service.buildContext).
func buildContext(ctx context.Context, d Deps,
	e executions.Execution) (wfvalidate.Context, error) {
	scope := tenants.PlatformScope()
	pol, err := d.Policies.Effective(ctx, scope, e.TenantID, e.ProjectID)
	if err != nil {
		return wfvalidate.Context{}, err
	}
	vpol, _, err := d.VPolicy.Effective(ctx, scope, e.TenantID, nil)
	if err != nil {
		return wfvalidate.Context{}, err
	}
	return wfvalidate.Context{
		ShellAllowed:   vpol.AllowShellTasks,
		ResourcePolicy: pol,
		Cluster: func(name string) (admission.Binding,
			validation.ClusterSnapshot, bool) {
			c, err := d.Clusters.GetByNameOrID(ctx, name)
			if err != nil {
				return admission.Binding{},
					validation.ClusterSnapshot{}, false
			}
			b, err := d.Projects.ResolveBinding(ctx, scope,
				e.ProjectID, c.ID)
			if err != nil {
				return admission.Binding{},
					validation.ClusterSnapshot{}, false
			}
			s := workflows.ClusterSnapshot(&c)
			return b, s, true
		},
	}, nil
}

// --- QUEUED / RUNNING / CANCELING -------------------------------------------

// advanceStage mirrors job progress into tasks, applies retry,
// unblocks dependents, enforces the failure policy and settles the
// terminal state. Each guarded transition is a no-op when another
// worker raced it.
func advanceStage(ctx context.Context, d Deps,
	e executions.Execution) error {
	scope := tenants.PlatformScope()
	tasks, err := d.Execs.ListTasks(ctx, scope, e.TenantID, e.ID)
	if err != nil {
		return err
	}
	wf, v, spec, err := d.loadSpec(ctx, e)
	if err != nil {
		return err
	}
	params, _ := wfvalidate.ResolveParameters(
		spec.Spec.Parameters, e.Parameters)

	// 1. Mirror job states into job-linked tasks.
	for _, t := range tasks {
		if t.JobID == nil || executions.Terminal(t.State) {
			continue
		}
		j, err := d.Jobs.Get(ctx, scope, *t.JobID)
		if err != nil {
			if apperr.Is(err, apperr.NotFound) {
				continue
			}
			return err
		}
		to, reason, ok := mirrorTaskState(j)
		if !ok || to == t.State {
			continue
		}
		reasonCopy := reason
		_, err = d.Execs.TransitionTask(ctx, scope, t.ID, t.State,
			t.Version, executions.TaskPatch{
				State: &to, Reason: &reasonCopy,
			}, nil)
		if err != nil && !executions.IsStale(err) {
			return err
		}
		if (to == executions.TaskQueued ||
			to == executions.TaskRunning) &&
			e.State == executions.ExecQueued {
			e = transitionExec(ctx, d, e, executions.ExecRunning, "")
		}
	}

	// Reload after mirroring.
	tasks, err = d.Execs.ListTasks(ctx, scope, e.TenantID, e.ID)
	if err != nil {
		return err
	}
	byName := map[string][]executions.TaskExecution{}
	for _, t := range tasks {
		byName[t.TaskName] = append(byName[t.TaskName], t)
	}

	// 2. Retry failed tasks whose policy allows another attempt.
	for _, t := range tasks {
		if t.State != executions.TaskFailed {
			continue
		}
		st := findTaskSpec(spec, t.TaskName)
		if st == nil || st.Retry == nil ||
			t.Attempt >= st.Retry.Attempts ||
			!retryOn(st.Retry, t.StateReason) {
			continue
		}
		to := executions.TaskReady
		attempt := t.Attempt + 1
		reason := "RETRY"
		nt, err := d.Execs.TransitionTask(ctx, scope, t.ID, t.State,
			t.Version, executions.TaskPatch{
				State: &to, Reason: &reason, ClearJob: true,
				ClearSpec: true,
				Attempt:   &attempt,
			}, func(ex executions.Execer) error {
				_, err := enqueueAdmit(ctx, ex, e, t.ID)
				return err
			})
		if err != nil {
			if executions.IsStale(err) {
				continue
			}
			return err
		}
		byName[t.TaskName] = replaceTask(byName[t.TaskName], nt)
	}

	// 3. Unblock BLOCKED tasks; evaluate `when`; condition tasks
	//    complete immediately.
	for _, t := range tasks {
		if t.State != executions.TaskBlocked {
			continue
		}
		st := findTaskSpec(spec, t.TaskName)
		if st == nil {
			continue
		}
		depOK, depDone := depsState(st, byName)
		var to executions.TaskState
		var reason string
		switch {
		case !depDone:
			continue
		case depOK || st.OnDependencyFailure == "run":
			if st.When != "" && !evalWhen(st.When, spec, wf, v, e,
				params, tasks) {
				to, reason = executions.TaskSkipped, "WHEN_FALSE"
				break
			}
			if st.Type == "condition" {
				to = executions.TaskCompleted
				break
			}
			to = executions.TaskReady
		default:
			to, reason = executions.TaskSkipped, "DEP_FAILED"
		}
		var enq executions.EnqueueFunc
		if to == executions.TaskReady {
			taskID := t.ID
			enq = func(ex executions.Execer) error {
				_, err := enqueueAdmit(ctx, ex, e, taskID)
				return err
			}
		}
		var rp *string
		if reason != "" {
			rp = &reason
		}
		nt, err := d.Execs.TransitionTask(ctx, scope, t.ID, t.State,
			t.Version, executions.TaskPatch{State: &to, Reason: rp}, enq)
		if err != nil {
			if executions.IsStale(err) {
				continue
			}
			return err
		}
		byName[t.TaskName] = replaceTask(byName[t.TaskName], nt)
	}

	// Refresh once more for terminal accounting.
	tasks, err = d.Execs.ListTasks(ctx, scope, e.TenantID, e.ID)
	if err != nil {
		return err
	}

	// 4. Cancel: drive job cancels and settle CANCELED.
	if e.State == executions.ExecCanceling {
		return settleCancel(ctx, d, e, tasks)
	}

	// 5. Failure policy: fail-fast cancels/skips the rest.
	policy := "fail"
	if spec.Spec.Execution != nil && spec.Spec.Execution.FailurePolicy != "" {
		policy = spec.Spec.Execution.FailurePolicy
	}
	if policy == "fail" && anyFailed(tasks) && anyRunning(tasks) {
		if err := cancelRemaining(ctx, d, e, tasks); err != nil {
			return err
		}
		tasks, err = d.Execs.ListTasks(ctx, scope, e.TenantID, e.ID)
		if err != nil {
			return err
		}
	}

	// 6. Settle when every task is terminal.
	if !allTerminal(tasks) {
		return nil
	}
	failed, okCount := false, true
	for _, t := range tasks {
		switch t.State {
		case executions.TaskFailed:
			failed = true
		case executions.TaskCompleted, executions.TaskSkipped:
		default:
			okCount = false
		}
	}
	var to executions.ExecutionState
	var reason string
	switch {
	case !failed && okCount:
		to = executions.ExecSucceeded
	case failed && policy == "continue":
		to, reason = executions.ExecPartialFailure, "TASK_FAILURES"
	default:
		to, reason = executions.ExecFailed, "TASK_FAILED"
	}
	e = transitionExec(ctx, d, e, to, reason)
	return nil
}

// mirrorTaskState maps a job state to the task transition it implies;
// ok=false means the job state maps to nothing.
func mirrorTaskState(j jobs.Job) (executions.TaskState, string, bool) {
	switch j.State {
	case jobs.StateQueued:
		return executions.TaskQueued, "", true
	case jobs.StateRunning:
		return executions.TaskRunning, "", true
	case jobs.StateCompleted:
		return executions.TaskCompleted, "", true
	case jobs.StateFailed:
		reason := j.StateReason
		if reason == "" {
			reason = "JOB_FAILED"
		}
		return executions.TaskFailed, reason, true
	case jobs.StateCanceled:
		return executions.TaskCanceled, "JOB_CANCELED", true
	}
	return "", "", false
}

// depsState reports (all-deps-terminal-ok, all-deps-terminal) for a
// task spec across every fan-out index of each dependency.
func depsState(st *workflowspec.Task,
	byName map[string][]executions.TaskExecution) (ok, done bool) {
	ok, done = true, true
	for _, dep := range st.DependsOn {
		depTasks := byName[dep]
		if len(depTasks) == 0 {
			return false, false
		}
		for _, dt := range depTasks {
			if !executions.Terminal(dt.State) {
				done = false
				continue
			}
			if !executions.TerminalOK(dt.State) {
				ok = false
			}
		}
	}
	return ok, done
}

// evalWhen evaluates a `when` expression; an evaluation error is
// treated as false (the task is skipped rather than run).
func evalWhen(src string, spec workflowspec.Workflow,
	wf workflows.Workflow, v workflows.Version,
	e executions.Execution, params map[string]any,
	tasks []executions.TaskExecution) bool {
	ex, err := expr.BareExpr(src)
	if err != nil {
		return false
	}
	val, err := ex.Eval(admitScope{
		spec: spec, workflow: wf.Name, version: v.Number,
		exec: e, params: params, tasks: tasks,
	})
	if err != nil || val.Kind != expr.Bool {
		return false
	}
	return val.Bool
}

// settleCancel finishes a CANCELING execution: job.cancel is enqueued
// for every still-active job task (dedupe makes repeats cheap) and
// non-job tasks are canceled directly; all-terminal → CANCELED.
func settleCancel(ctx context.Context, d Deps, e executions.Execution,
	tasks []executions.TaskExecution) error {
	scope := tenants.PlatformScope()
	for _, t := range tasks {
		if executions.Terminal(t.State) {
			continue
		}
		if t.JobID == nil {
			to := executions.TaskCanceled
			reason := "CANCELED"
			if _, err := d.Execs.TransitionTask(ctx, scope, t.ID,
				t.State, t.Version, executions.TaskPatch{
					State: &to, Reason: &reason,
				}, nil); err != nil && !executions.IsStale(err) {
				return err
			}
			continue
		}
		if _, err := d.Execs.TransitionTask(ctx, scope, t.ID, t.State,
			t.Version, executions.TaskPatch{}, // version bump only
			func(ex executions.Execer) error {
				_, err := enqueueJobCancel(ctx, ex, e.TenantID, *t.JobID)
				return err
			}); err != nil && !executions.IsStale(err) {
			return err
		}
	}
	tasks, err := d.Execs.ListTasks(ctx, scope, e.TenantID, e.ID)
	if err != nil {
		return err
	}
	if allTerminal(tasks) {
		transitionExec(ctx, d, e, executions.ExecCanceled, "")
	}
	return nil
}

// cancelRemaining applies failurePolicy=fail: non-terminal tasks are
// canceled (their jobs get job.cancel) or skipped.
func cancelRemaining(ctx context.Context, d Deps, e executions.Execution,
	tasks []executions.TaskExecution) error {
	scope := tenants.PlatformScope()
	for _, t := range tasks {
		if executions.Terminal(t.State) {
			continue
		}
		to := executions.TaskCanceled
		reason := "FAIL_FAST"
		if t.State == executions.TaskBlocked ||
			t.State == executions.TaskReady ||
			t.State == executions.TaskAdmitting && t.JobID == nil {
			to = executions.TaskSkipped
		}
		var enq executions.EnqueueFunc
		if t.JobID != nil {
			jobID := *t.JobID
			enq = func(ex executions.Execer) error {
				_, err := enqueueJobCancel(ctx, ex, e.TenantID, jobID)
				return err
			}
		}
		if _, err := d.Execs.TransitionTask(ctx, scope, t.ID, t.State,
			t.Version, executions.TaskPatch{State: &to, Reason: &reason},
			enq); err != nil && !executions.IsStale(err) {
			return err
		}
	}
	return nil
}

// transitionExec applies a guarded exec transition ignoring staleness
// (the losing worker's update stands).
func transitionExec(ctx context.Context, d Deps, e executions.Execution,
	to executions.ExecutionState, reason string) executions.Execution {
	p := executions.ExecPatch{State: &to}
	if reason != "" {
		p.Reason = &reason
	}
	if execTerminal(to) {
		end := d.now()
		p.EndedAt = &end
	}
	out, err := d.Execs.TransitionExec(ctx, tenants.PlatformScope(), e.ID,
		e.State, e.Version, p, nil)
	if err != nil {
		return e
	}
	return out
}

// ExecTerminal reports whether s is a terminal execution state.
func execTerminal(s executions.ExecutionState) bool {
	switch s {
	case executions.ExecSucceeded, executions.ExecFailed,
		executions.ExecPartialFailure, executions.ExecCanceled:
		return true
	}
	return false
}

func anyFailed(ts []executions.TaskExecution) bool {
	for _, t := range ts {
		if t.State == executions.TaskFailed {
			return true
		}
	}
	return false
}

func anyRunning(ts []executions.TaskExecution) bool {
	for _, t := range ts {
		if !executions.Terminal(t.State) {
			return true
		}
	}
	return false
}

func allTerminal(ts []executions.TaskExecution) bool {
	for _, t := range ts {
		if !executions.Terminal(t.State) {
			return false
		}
	}
	return true
}

func replaceTask(list []executions.TaskExecution,
	t executions.TaskExecution) []executions.TaskExecution {
	for i, x := range list {
		if x.ID == t.ID {
			list[i] = t
			return list
		}
	}
	return append(list, t)
}

func findTaskSpec(spec workflowspec.Workflow,
	name string) *workflowspec.Task {
	for i := range spec.Spec.Tasks {
		if spec.Spec.Tasks[i].Name == name {
			return &spec.Spec.Tasks[i]
		}
	}
	return nil
}

// retryOn reports whether the failure reason is in retry.on (empty →
// any reason).
func retryOn(r *workflowspec.Retry, reason string) bool {
	if len(r.On) == 0 {
		return true
	}
	for _, s := range r.On {
		if s == reason {
			return true
		}
	}
	return false
}

// loadSpec decodes the pinned spec for an execution and returns the
// workflow and version records it was pinned to.
func (d Deps) loadSpec(ctx context.Context,
	e executions.Execution) (workflows.Workflow, workflows.Version,
	workflowspec.Workflow, error) {
	wf, err := d.Workflows.Get(ctx, tenants.PlatformScope(),
		e.TenantID, e.WorkflowID)
	if err != nil {
		return workflows.Workflow{}, workflows.Version{},
			workflowspec.Workflow{}, err
	}
	v, err := d.Workflows.GetVersion(ctx, tenants.PlatformScope(),
		e.TenantID, e.WorkflowID, e.WorkflowVersionID)
	if err != nil {
		return workflows.Workflow{}, workflows.Version{},
			workflowspec.Workflow{}, err
	}
	spec, err := workflowspec.Decode(v.Spec, "application/json")
	return wf, v, spec, err
}

// enqueueAdmit queues task.admit for a task inside a transaction.
func enqueueAdmit(ctx context.Context, ex executions.Execer,
	e executions.Execution, taskID uuid.UUID) (bool, error) {
	return workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{
		Kind:     KindAdmit,
		Key:      "task:" + taskID.String(),
		TenantID: &e.TenantID,
		Payload:  map[string]string{"task_id": taskID.String()},
	})
}

// enqueueJobCancel queues job.cancel inside a transaction.
func enqueueJobCancel(ctx context.Context, ex executions.Execer,
	tenantID, jobID uuid.UUID) (bool, error) {
	return workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{
		Kind:     jobssvc.KindCancel,
		Key:      "job:" + jobID.String(),
		TenantID: &tenantID,
		Payload:  map[string]string{"job_id": jobID.String()},
	})
}

// admitScope is the template/expression scope available at admit time
// (docs/workflows.md §Templating): parameters, run.*, task.resources.*,
// item.*, tasks.<dep>.* and array.taskId (a runtime value).
type admitScope struct {
	spec     workflowspec.Workflow
	workflow string // workflow name (run.workflow)
	version  int    // workflow version number (run.version)
	exec     executions.Execution
	params   map[string]any
	tasks    []executions.TaskExecution
	self     *executions.TaskExecution // item.index/item.count source
	res      workflowspec.Resources
}

func (s admitScope) Lookup(path []string) (expr.Value, bool) {
	if len(path) < 2 {
		return expr.Value{}, false
	}
	switch path[0] {
	case "parameters":
		if len(path) != 2 {
			return expr.Value{}, false
		}
		return paramValue(s.params[path[1]])
	case "run":
		if len(path) != 2 {
			return expr.Value{}, false
		}
		switch path[1] {
		case "id":
			return expr.Value{Kind: expr.String,
				Str: s.exec.ID.String()}, true
		case "workflow":
			return expr.Value{Kind: expr.String,
				Str: s.workflow}, true
		case "version":
			return expr.Value{Kind: expr.Int,
				Int: int64(s.version)}, true
		}
	case "task":
		if len(path) == 3 && path[1] == "resources" {
			return s.resourceValue(path[2])
		}
	case "item":
		if s.self == nil || len(path) != 2 {
			return expr.Value{}, false
		}
		switch path[1] {
		case "index":
			return expr.Value{Kind: expr.Int,
				Int: int64(s.self.Index)}, true
		case "count":
			return expr.Value{Kind: expr.Int,
				Int: int64(s.self.Count)}, true
		}
	case "array":
		if len(path) == 2 && path[1] == "taskId" {
			return expr.Value{Kind: expr.Runtime,
				Runtime: admission.RuntimeSlurmArrayTaskID}, true
		}
	case "tasks":
		if len(path) != 3 {
			return expr.Value{}, false
		}
		return s.taskDepValue(path[1], path[2])
	}
	return expr.Value{}, false
}

func (s admitScope) resourceValue(field string) (expr.Value, bool) {
	r := s.res
	switch field {
	case "cpu":
		return expr.Value{Kind: expr.Int, Int: int64(r.CPUsPerTask)}, true
	case "nodes":
		return expr.Value{Kind: expr.Int, Int: int64(r.Nodes)}, true
	case "tasksPerNode":
		return expr.Value{Kind: expr.Int, Int: int64(r.TasksPerNode)}, true
	case "cpusPerTask":
		return expr.Value{Kind: expr.Int, Int: int64(r.CPUsPerTask)}, true
	case "memoryMiB":
		return expr.Value{Kind: expr.Int, Int: r.MemoryPerNodeMiB}, true
	case "walltimeSeconds":
		return expr.Value{Kind: expr.Int,
			Int: int64(time.Duration(r.Walltime) / time.Second)}, true
	}
	return expr.Value{}, false
}

func (s admitScope) taskDepValue(name, field string) (expr.Value, bool) {
	var succ, faild int64
	var states []executions.TaskState
	for _, t := range s.tasks {
		if t.TaskName != name {
			continue
		}
		states = append(states, t.State)
		switch t.State {
		case executions.TaskCompleted:
			succ++
		case executions.TaskFailed:
			faild++
		}
	}
	if len(states) == 0 {
		return expr.Value{}, false
	}
	switch field {
	case "succeededCount":
		return expr.Value{Kind: expr.Int, Int: succ}, true
	case "failedCount":
		return expr.Value{Kind: expr.Int, Int: faild}, true
	case "state":
		// Multi-instance deps expose the first instance's state.
		return expr.Value{Kind: expr.String, Str: string(states[0])}, true
	}
	return expr.Value{}, false
}
