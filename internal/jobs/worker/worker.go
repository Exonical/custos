// Package worker implements the job work-queue handlers
// (docs/workers.md): job.submit (lost-submit safe), job.reconcile,
// job.cancel, jobs.sweep, and idempotency.expire. This package is the
// only place stored script bytes meet wrapper generation.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/executions"
	"github.com/Exonical/custos/internal/jobs"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/scripts"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/submission"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
)

// Work kinds.
const (
	KindReconcile   = "job.reconcile"
	KindSweep       = "jobs.sweep"
	KindIdemExpire  = "idempotency.expire"
	lostAfter       = 10 * time.Minute
	sweepInterval   = 60 * time.Second
	idemExpireEvery = time.Hour
)

// Deps wires the handlers.
type Deps struct {
	Jobs     jobs.Repository
	Scripts  scripts.Store
	Clusters clusters.Repository
	Factory  slurm.Factory
	Exec     workqueue.Execer // pool: out-of-transaction enqueues
	Execs    executions.Repository
	Audit    audit.Recorder
	Metrics  *Metrics // optional
}

func jobID(it workqueue.Item) (uuid.UUID, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if len(it.Payload) > 0 {
		if err := json.Unmarshal(it.Payload, &p); err == nil && p.JobID != "" {
			return uuid.Parse(p.JobID)
		}
	}
	if strings.HasPrefix(it.Key, "job:") {
		return uuid.Parse(it.Key[len("job:"):])
	}
	return uuid.Nil, fmt.Errorf("%s: no job id in payload or key", it.Kind)
}

// --- job.submit ---------------------------------------------------------

// Submit returns the job.submit handler.
func Submit(d Deps) workqueue.Handler {
	return func(ctx context.Context, it workqueue.Item) error {
		id, err := jobID(it)
		if err != nil {
			return err
		}
		j, err := d.Jobs.Get(ctx, tenants.PlatformScope(), id)
		if err != nil {
			return err // transient -> retry
		}
		if j.State != jobs.StateSubmitting {
			return nil // canceled or already progressed; idempotent
		}
		c, err := d.Clusters.GetByNameOrID(ctx, j.ClusterID.String())
		if err != nil {
			return err
		}
		cl, acct, err := d.Factory.Open(ctx, c.SlurmConfig())
		if err != nil {
			return err
		}
		wantName := "custos-" + j.ID.String()
		wantComment := "custos:" + j.ID.String() + "/" + j.ExecutionSpec.TaskName

		// Lost-submit safety: a prior attempt may have submitted but
		// lost the response. Check the scheduler by name first; the
		// comment is the second correlation key.
		if found, err := findByName(ctx, cl, acct, wantName); err != nil {
			return err // lookup failed -> retry
		} else if found != nil {
			return adopt(ctx, d, j, *found, wantComment)
		}

		// Payload-bearing specs fetch and verify the script bytes;
		// command (argv-only) specs have neither digest nor bytes.
		var payload []byte
		if j.ExecutionSpec.Payload.Digest != (validation.Digest{}) {
			if j.ScriptDigest != j.ExecutionSpec.Payload.Digest {
				return integrityFailure(ctx, d, j,
					fmt.Errorf("script digest does not match spec payload digest"))
			}
			payload, err = d.Scripts.Get(ctx,
				tenants.TenantScope(j.TenantID), j.TenantID, j.ScriptDigest)
			if err != nil {
				if apperr.Is(err, apperr.Internal) {
					return integrityFailure(ctx, d, j, err)
				}
				return err
			}
		} else if len(j.ExecutionSpec.Argv) == 0 {
			return integrityFailure(ctx, d, j,
				fmt.Errorf("spec has neither payload nor argv"))
		}
		wrapper, err := submission.Wrapper(j.ExecutionSpec, payload)
		if err != nil {
			return integrityFailure(ctx, d, j, err)
		}
		sub := submission.JobSubmission(j.ExecutionSpec, wrapper)
		ref, err := cl.SubmitJob(ctx, sub)
		if err != nil {
			if errors.Is(err, slurm.ErrUnavailable) {
				return err // lost response possible -> retry (adopt next run)
			}
			reason := "SUBMIT_REJECTED"
			if errors.Is(err, slurm.ErrUnauthorized) {
				reason = "SUBMIT_UNAUTHORIZED"
			}
			_, terr := transition(ctx, d, j, jobs.Patch{
				State:   ptr(jobs.StateFailed),
				Reason:  &reason,
				EndedAt: ptr(time.Now().UTC()),
			})
			auditJob(ctx, d, j, "job.submit_failed", audit.ResultError, reason)
			return terr
		}
		now := time.Now().UTC()
		sid := int64(ref.ID.ID)
		if _, err := transition(ctx, d, j, jobs.Patch{
			State:       ptr(jobs.StateQueued),
			SlurmJobID:  &sid,
			SlurmState:  ptr(string(ref.State)),
			SubmittedAt: &now,
		}); err != nil {
			return err
		}
		return enqueueReconcile(ctx, d, j.ID, now, 5*time.Second)
	}
}

// findByName looks up an existing scheduler job by the deterministic
// custos-<uuid> name, falling back to accounting records from the last
// 24h when the queue shows nothing.
func findByName(ctx context.Context, cl slurm.Cluster, acct slurm.Accounting,
	name string) (*slurm.Job, error) {
	list, err := cl.ListJobs(ctx, slurm.JobFilter{Names: []string{name}})
	if err != nil {
		return nil, err
	}
	for i := range list {
		return &list[i], nil
	}
	if acct == nil {
		return nil, nil
	}
	since := time.Now().Add(-24 * time.Hour)
	recs, err := acct.GetJobRecords(ctx, slurm.JobRecordFilter{
		Names: []string{name}, Since: &since})
	if err != nil {
		return nil, nil // accounting is advisory; never blocks submit
	}
	if len(recs) == 0 {
		return nil, nil
	}
	r := recs[0]
	return &slurm.Job{
		ID: r.ID, Name: r.Name, State: r.State, ExitCode: r.ExitCode,
		SubmitTime: r.StartTime, StartTime: r.StartTime, EndTime: r.EndTime,
	}, nil
}

// adopt records an existing scheduler job instead of resubmitting.
func adopt(ctx context.Context, d Deps, j jobs.Job, sj slurm.Job,
	wantComment string) error {
	state, _ := jobs.MapSlurmState(sj.State)
	now := time.Now().UTC()
	sid := int64(sj.ID.ID)
	p := jobs.Patch{
		State:            &state,
		SlurmJobID:       &sid,
		SlurmState:       ptr(string(sj.State)),
		SubmittedAt:      &now,
		LastReconciledAt: &now,
	}
	if sj.SubmitTime.After(time.Time{}) {
		p.SubmittedAt = &sj.SubmitTime
	}
	if state.Terminal() {
		p.EndedAt = &now
		applyExit(&p, sj)
	} else if state == jobs.StateRunning {
		p.StartedAt = &now
	}
	nj, err := transition(ctx, d, j, p)
	if err != nil {
		if apperr.Is(err, apperr.Conflict) {
			return nil // concurrent handler won; idempotent
		}
		return err
	}
	auditJob(ctx, d, nj, "job.slurm_adopted", audit.ResultAllow,
		fmt.Sprintf("adopted slurm job %d (comment match=%v)",
			sj.ID.ID, sj.Comment == wantComment))
	if state.Terminal() {
		d.Metrics.terminal(ctx, state)
		return nil
	}
	return enqueueReconcile(ctx, d, j.ID, now, 5*time.Second)
}

func integrityFailure(ctx context.Context, d Deps, j jobs.Job, cause error) error {
	reason := "INTEGRITY"
	_, err := transition(ctx, d, j, jobs.Patch{
		State:   ptr(jobs.StateFailed),
		Reason:  &reason,
		EndedAt: ptr(time.Now().UTC()),
	})
	d.Metrics.terminal(ctx, jobs.StateFailed)
	auditJob(ctx, d, j, "job.integrity_failure", audit.ResultDeny,
		"stored script failed digest re-verification")
	_ = cause // detail is never script bytes; the class is enough
	return err
}

// --- job.reconcile ------------------------------------------------------

// Reconcile returns the job.reconcile handler.
func Reconcile(d Deps) workqueue.Handler {
	return func(ctx context.Context, it workqueue.Item) error {
		id, err := jobID(it)
		if err != nil {
			return err
		}
		j, err := d.Jobs.Get(ctx, tenants.PlatformScope(), id)
		if err != nil {
			return err
		}
		if j.State.Terminal() {
			return nil
		}
		sid, ok := j.SlurmJobIDRef()
		if !ok {
			// Submit still in flight; poll again soon.
			return workqueue.RescheduleAt(time.Now().Add(30 * time.Second))
		}
		c, err := d.Clusters.GetByNameOrID(ctx, j.ClusterID.String())
		if err != nil {
			return err
		}
		cl, acct, err := d.Factory.Open(ctx, c.SlurmConfig())
		if err != nil {
			return err
		}
		sj, err := cl.GetJob(ctx, sid)
		if errors.Is(err, slurm.ErrNotFound) {
			return lostOrRetry(ctx, d, j, acct)
		}
		if err != nil {
			return err
		}
		// Self-reschedule: the leased item itself becomes the next run.
		return applyObserved(ctx, d, j, sj, func(d time.Duration) error {
			return workqueue.RescheduleAt(time.Now().Add(d))
		})
	}
}

// applyObserved maps an observed slurm.Job onto a guarded transition
// and reschedules while non-terminal. Shared by reconcile and sweep:
// resched requeues the job — reconcile reschedules its own leased item,
// sweep enqueues a separate job.reconcile item.
func applyObserved(ctx context.Context, d Deps, j jobs.Job, sj slurm.Job,
	resched func(time.Duration) error) error {
	state, ok := jobs.MapSlurmState(sj.State)
	reason := string(sj.State)
	if !ok {
		state = jobs.StateFailed
		reason = "UNKNOWN_SLURM_STATE:" + string(sj.State)
	}
	if sj.StateReason != "" {
		reason = sj.StateReason
	}
	now := time.Now().UTC()
	p := jobs.Patch{
		SlurmState:       ptr(string(sj.State)),
		LastReconciledAt: &now,
	}
	if state != j.State {
		p.State = &state
		p.Reason = &reason
	}
	if sj.StartTime.After(time.Time{}) && j.StartedAt == nil &&
		state != jobs.StateQueued {
		p.StartedAt = &sj.StartTime
	}
	if state.Terminal() {
		end := sj.EndTime
		if end.IsZero() {
			end = now
		}
		p.EndedAt = &end
		applyExit(&p, sj)
	}
	nj, err := transition(ctx, d, j, p)
	if err != nil {
		if apperr.Is(err, apperr.Conflict) {
			return nil // a fresher observer won
		}
		return err
	}
	if state.Terminal() {
		d.Metrics.terminal(ctx, state)
		return nil
	}
	return resched(reconcileInterval(nj))
}

// reconcileInterval grows from 5s to 60s based on job age.
func reconcileInterval(j jobs.Job) time.Duration {
	base := j.SubmittedAt
	if base == nil {
		b := j.CreatedAt
		base = &b
	}
	age := time.Since(*base)
	ivl := 5*time.Second + age/12
	if ivl > 60*time.Second {
		ivl = 60 * time.Second
	}
	return ivl
}

// lostOrRetry handles ErrNotFound from the scheduler: accounting may
// still know the job; after 10 minutes unknown, the job is LOST.
func lostOrRetry(ctx context.Context, d Deps, j jobs.Job,
	acct slurm.Accounting) error {
	name := "custos-" + j.ID.String()
	if acct != nil {
		since := time.Now().Add(-24 * time.Hour)
		if recs, err := acct.GetJobRecords(ctx, slurm.JobRecordFilter{
			Names: []string{name}, Since: &since}); err == nil && len(recs) > 0 {
			r := recs[0]
			state, _ := jobs.MapSlurmState(r.State)
			if !state.Terminal() {
				state = jobs.StateFailed
			}
			now := time.Now().UTC()
			p := jobs.Patch{State: &state,
				SlurmState:       ptr(string(r.State)),
				LastReconciledAt: &now, EndedAt: &now,
				Reason: ptr("accounting:" + string(r.State))}
			if r.ExitCode != nil {
				p.ExitCode = &r.ExitCode.Code
				p.ExitSignal = &r.ExitCode.Signal
			}
			if _, err := transition(ctx, d, j, p); err != nil {
				if !apperr.Is(err, apperr.Conflict) {
					return err
				}
			}
			d.Metrics.terminal(ctx, state)
			return nil
		}
	}
	base := j.SubmittedAt
	if base == nil {
		b := j.CreatedAt
		base = &b
	}
	if time.Since(*base) > lostAfter {
		now := time.Now().UTC()
		if _, err := transition(ctx, d, j, jobs.Patch{
			State:            ptr(jobs.StateFailed),
			Reason:           ptr("LOST"),
			LastReconciledAt: &now,
			EndedAt:          &now,
		}); err != nil && !apperr.Is(err, apperr.Conflict) {
			return err
		}
		d.Metrics.terminal(ctx, jobs.StateFailed)
		auditJob(ctx, d, j, "job.lost", audit.ResultError,
			"job vanished from scheduler for over 10 minutes")
		return nil
	}
	return workqueue.RescheduleAt(time.Now().Add(30 * time.Second))
}

// --- job.cancel ---------------------------------------------------------

// Cancel returns the job.cancel handler.
func Cancel(d Deps) workqueue.Handler {
	return func(ctx context.Context, it workqueue.Item) error {
		id, err := jobID(it)
		if err != nil {
			return err
		}
		j, err := d.Jobs.Get(ctx, tenants.PlatformScope(), id)
		if err != nil {
			return err
		}
		switch {
		case j.State.Terminal():
			return nil
		case j.State == jobs.StateSubmitting:
			// Race with job.submit: mark canceled; the submit handler
			// re-reads state before calling Slurm.
			now := time.Now().UTC()
			if _, err := transition(ctx, d, j, jobs.Patch{
				State: ptr(jobs.StateCanceled), Reason: ptr("CANCELED"),
				EndedAt: &now,
			}); err != nil && !apperr.Is(err, apperr.Conflict) {
				return err
			}
			return nil
		}
		sid, ok := j.SlurmJobIDRef()
		if !ok {
			return enqueueReconcile(ctx, d, j.ID, time.Now(), 2*time.Second)
		}
		c, err := d.Clusters.GetByNameOrID(ctx, j.ClusterID.String())
		if err != nil {
			return err
		}
		cl, _, err := d.Factory.Open(ctx, c.SlurmConfig())
		if err != nil {
			return err
		}
		if err := cl.CancelJob(ctx, sid, slurm.CancelOptions{}); err != nil &&
			!errors.Is(err, slurm.ErrNotFound) {
			return err
		}
		return enqueueReconcile(ctx, d, j.ID, time.Now(), 2*time.Second)
	}
}

// --- jobs.sweep ---------------------------------------------------------

// Sweep returns the jobs.sweep handler: one ListJobs per cluster,
// client-side name-prefix filter, bulk reconcile of every active Custos
// job on that cluster. Self-reschedules every 60s; the chain stops on
// disabled clusters.
func Sweep(d Deps) workqueue.Handler {
	return func(ctx context.Context, it workqueue.Item) error {
		id, err := uuid.Parse(strings.TrimPrefix(it.Key, "sweep:"))
		if err != nil {
			return fmt.Errorf("jobs.sweep key %q: %w", it.Key, err)
		}
		c, err := d.Clusters.GetByNameOrID(ctx, id.String())
		if err != nil {
			return err
		}
		if c.State == clusters.StateDisabled {
			return nil
		}
		cl, _, err := d.Factory.Open(ctx, c.SlurmConfig())
		if err != nil {
			return err
		}
		list, err := cl.ListJobs(ctx, slurm.JobFilter{})
		if err != nil {
			return err
		}
		byName := map[string]slurm.Job{}
		for _, sj := range list {
			if strings.HasPrefix(sj.Name, "custos-") {
				byName[sj.Name] = sj
			}
		}
		active, err := d.Jobs.ListActiveByCluster(ctx, c.ID)
		if err != nil {
			return err
		}
		for _, j := range active {
			sj, ok := byName["custos-"+j.ID.String()]
			if !ok {
				base := j.SubmittedAt
				if base == nil {
					b := j.CreatedAt
					base = &b
				}
				if j.SlurmJobID != nil && time.Since(*base) > lostAfter {
					now := time.Now().UTC()
					if _, err := transition(ctx, d, j, jobs.Patch{
						State: ptr(jobs.StateFailed), Reason: ptr("LOST"),
						LastReconciledAt: &now, EndedAt: &now,
					}); err == nil {
						d.Metrics.terminal(ctx, jobs.StateFailed)
					}
				}
				continue
			}
			if err := applyObserved(ctx, d, j, sj,
				func(delay time.Duration) error {
					return enqueueReconcile(ctx, d, j.ID, time.Now(), delay)
				}); err != nil {
				return err
			}
		}
		return workqueue.RescheduleAt(time.Now().Add(sweepInterval))
	}
}

// BootstrapSweep enqueues a jobs.sweep item for every non-disabled
// cluster (dedupe makes it idempotent), like cluster.sync.
func BootstrapSweep(ctx context.Context, ex workqueue.Execer,
	repo clusters.Repository) error {
	list, err := repo.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.State == clusters.StateDisabled {
			continue
		}
		if _, err := workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{
			Kind: KindSweep, Key: "sweep:" + c.ID.String(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// --- idempotency.expire -------------------------------------------------

// IdempotencyExpire returns the hourly self-rescheduling handler that
// deletes expired idempotency_keys rows.
func IdempotencyExpire(d Deps) workqueue.Handler {
	return func(ctx context.Context, _ workqueue.Item) error {
		if _, err := d.Jobs.ExpireIdempotency(ctx, time.Now()); err != nil {
			return err
		}
		return workqueue.RescheduleAt(time.Now().Add(idemExpireEvery))
	}
}

// --- helpers ------------------------------------------------------------

func transition(ctx context.Context, d Deps, j jobs.Job,
	p jobs.Patch) (jobs.Job, error) {
	if p.State != nil && !jobs.TransitionOK(j.State, *p.State) {
		return j, apperr.New(apperr.Conflict, "JOB_STATE",
			"invalid job state transition "+string(j.State)+" -> "+
				string(*p.State))
	}
	out, err := d.Jobs.Transition(ctx, tenants.PlatformScope(), j.ID,
		j.Version, p)
	if err != nil {
		return out, err
	}
	// Workflow task jobs drive their execution forward. The lookup must
	// go through the executions repository — task_executions is under
	// FORCE RLS, so a bare QueryRow on the pool sees zero rows.
	if j.TaskExecutionID != nil {
		task, qerr := d.Execs.GetTaskByJob(ctx, tenants.PlatformScope(),
			j.ID)
		if qerr != nil {
			return out, fmt.Errorf("job.task_execution link: %w", qerr)
		}
		execID := task.ExecutionID
		if _, qerr := workqueue.Enqueue(ctx, d.Exec, workqueue.EnqueueRequest{
			Kind:     "execution.advance",
			Key:      "execution:" + execID.String(),
			TenantID: &j.TenantID,
			Payload:  map[string]string{"execution_id": execID.String()},
		}); qerr != nil {
			return out, qerr
		}
	}
	return out, nil
}

func applyExit(p *jobs.Patch, sj slurm.Job) {
	if sj.ExitCode != nil {
		p.ExitCode = &sj.ExitCode.Code
		p.ExitSignal = &sj.ExitCode.Signal
	}
}

func enqueueReconcile(ctx context.Context, d Deps, id uuid.UUID,
	now time.Time, delay time.Duration) error {
	_, err := workqueue.Enqueue(ctx, d.Exec, workqueue.EnqueueRequest{
		Kind:    KindReconcile,
		Key:     "job:" + id.String(),
		Payload: map[string]string{"job_id": id.String()},
		RunAt:   now.Add(delay),
	})
	return err
}

func auditJob(ctx context.Context, d Deps, j jobs.Job, action, result,
	reason string) {
	if d.Audit == nil {
		return
	}
	_ = d.Audit.Record(ctx, audit.Event{
		Actor:    audit.Actor{Type: audit.ActorSystem, ID: "custos"},
		Action:   action,
		Target:   audit.Target{Type: "job", ID: j.ID.String()},
		Result:   result,
		Reason:   reason,
		TenantID: &j.TenantID,
	})
}

func ptr[T any](v T) *T { return &v }

// Metrics holds the job instruments: custos_jobs_total{result} and the
// custos_jobs_active{state} gauge (DB count cached 15s).
type Metrics struct {
	total metric.Int64Counter

	mu      sync.Mutex
	repo    jobs.Repository
	cache   map[jobs.State]int64
	cacheAt time.Time
}

// NewMetrics registers job instruments on mp (may be nil).
func NewMetrics(mp metric.MeterProvider, repo jobs.Repository) *Metrics {
	m := &Metrics{repo: repo}
	if mp == nil || repo == nil {
		return m
	}
	meter := mp.Meter("custos/jobs")
	m.total, _ = meter.Int64Counter("custos_jobs_total",
		metric.WithDescription("terminal job results"))
	g, _ := meter.Int64ObservableGauge("custos_jobs_active",
		metric.WithDescription("active jobs by state"))
	_, _ = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		counts, err := m.counts(ctx)
		if err != nil {
			return nil // gauge stays stale; never break collection
		}
		for _, st := range []jobs.State{jobs.StateSubmitting,
			jobs.StateQueued, jobs.StateRunning} {
			o.ObserveInt64(g, counts[st], metric.WithAttributes(
				attribute.String("state", string(st))))
		}
		return nil
	}, g)
	return m
}

func (m *Metrics) terminal(ctx context.Context, st jobs.State) {
	if m != nil && m.total != nil {
		m.total.Add(ctx, 1, metric.WithAttributes(
			attribute.String("result", st.Result())))
	}
}

// counts caches the active-job query for 15s.
func (m *Metrics) counts(ctx context.Context) (map[jobs.State]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.cacheAt) < 15*time.Second && m.cache != nil {
		return m.cache, nil
	}
	c, err := m.repo.CountActiveByState(ctx)
	if err != nil {
		return nil, err
	}
	m.cache, m.cacheAt = c, time.Now()
	return c, nil
}
