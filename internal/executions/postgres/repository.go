// Package postgres implements executions.Repository on PostgreSQL.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/executions"
	"github.com/Exonical/custos/internal/jobs"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
)

var errNotFound = apperr.New(apperr.NotFound, "NOT_FOUND", "not found")

func applyScope(ctx context.Context, tx pgx.Tx, s tenants.Scope) error {
	if s.IsPlatform() {
		return db.SetPlatformScope(ctx, tx)
	}
	id, _ := s.TenantID()
	return db.SetTenant(ctx, tx, id)
}

// scopeTenant returns the RLS tenant when scoped; uuid.Nil under the
// platform scope (callers pass the row's tenant_id explicitly then).
func scopeTenant(s tenants.Scope, tenantID uuid.UUID) uuid.UUID {
	if id, ok := s.TenantID(); ok {
		return id
	}
	return tenantID
}

// Repository is the PostgreSQL executions store.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository on pool.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const execCols = `id, tenant_id, project_id, workflow_id,
	workflow_version_id, spec_hash, parameters, strategy, state,
	state_reason, requested_by, created_at, started_at, ended_at,
	updated_at, version`

func scanExec(row pgx.Row) (executions.Execution, error) {
	var e executions.Execution
	var hash []byte
	var state, strategy string
	var reason *string
	err := row.Scan(&e.ID, &e.TenantID, &e.ProjectID, &e.WorkflowID,
		&e.WorkflowVersionID, &hash, &e.Parameters, &strategy, &state,
		&reason, &e.RequestedBy, &e.CreatedAt, &e.StartedAt, &e.EndedAt,
		&e.UpdatedAt, &e.Version)
	if err != nil {
		return e, db.MapError(err)
	}
	copy(e.SpecHash[:], hash)
	e.State = executions.ExecutionState(state)
	e.Strategy = strategy
	if reason != nil {
		e.StateReason = *reason
	}
	return e, nil
}

func nilStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// CreateWithIdempotency implements executions.Repository.
func (r *Repository) CreateWithIdempotency(ctx context.Context,
	scope tenants.Scope, e executions.Execution, idem executions.IdemRecord,
	enqueue executions.EnqueueFunc) (executions.CreateResult, error) {
	var res executions.CreateResult
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tid := scopeTenant(scope, e.TenantID)
		var inserted uuid.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO idempotency_keys
			  (tenant_id, key, request_hash, response_status,
			   response_body, resource_id, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (tenant_id, key) DO NOTHING
			RETURNING resource_id`,
			tid, idem.Key, idem.RequestHash[:], idem.Status,
			idem.Body, idem.ResourceID, idem.ExpiresAt).Scan(&inserted)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return db.MapError(err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			var hash []byte
			var status int
			var body []byte
			var rid *uuid.UUID
			if err := tx.QueryRow(ctx, `
				SELECT request_hash, response_status, response_body, resource_id
				FROM idempotency_keys WHERE tenant_id=$1 AND key=$2`,
				tid, idem.Key).Scan(&hash, &status, &body, &rid); err != nil {
				return db.MapError(err)
			}
			if !bytes.Equal(hash, idem.RequestHash[:]) {
				return apperr.New(apperr.Conflict, "IDEMPOTENCY_MISMATCH",
					"Idempotency-Key was used with a different request body")
			}
			res = executions.CreateResult{Replayed: true,
				Status: status, Body: body}
			if rid != nil {
				re, rerr := scanExec(tx.QueryRow(ctx,
					`SELECT `+execCols+` FROM workflow_executions WHERE id=$1`,
					*rid))
				if rerr != nil {
					return rerr
				}
				res.Execution = re
			}
			return nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO workflow_executions (`+execCols+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			e.ID, tid, e.ProjectID, e.WorkflowID, e.WorkflowVersionID,
			e.SpecHash[:], e.Parameters, e.Strategy, string(e.State),
			nilStr(e.StateReason), e.RequestedBy, e.CreatedAt,
			e.StartedAt, e.EndedAt, e.UpdatedAt, e.Version); err != nil {
			return db.MapError(err)
		}
		if enqueue != nil {
			if err := enqueue(tx); err != nil {
				return err
			}
		}
		res = executions.CreateResult{Execution: e}
		return nil
	})
	return res, err
}

// Get implements executions.Repository.
func (r *Repository) Get(ctx context.Context, scope tenants.Scope,
	_, id uuid.UUID) (executions.Execution, error) {
	var e executions.Execution
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		e, err = scanExec(tx.QueryRow(ctx,
			`SELECT `+execCols+` FROM workflow_executions WHERE id=$1`, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return e, errNotFound
	}
	return e, err
}

// List implements executions.Repository (keyset on created_at DESC, id).
func (r *Repository) List(ctx context.Context, scope tenants.Scope,
	tenantID uuid.UUID, f executions.ExecFilter,
	page tenants.Page) ([]executions.Execution, string, error) {
	page = page.Normalize(50, 200)
	var out []executions.Execution
	var next string
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		where := []string{"tenant_id = $1"}
		args := []any{scopeTenant(scope, tenantID)}
		add := func(clause string, v any) {
			args = append(args, v)
			where = append(where, fmt.Sprintf(clause, len(args)))
		}
		if f.WorkflowID != nil {
			add("workflow_id = $%d", *f.WorkflowID)
		}
		if f.RequestedBy != nil {
			add("requested_by = $%d", *f.RequestedBy)
		}
		if len(f.States) > 0 {
			ss := make([]string, len(f.States))
			for i, s := range f.States {
				ss[i] = string(s)
			}
			add("state = ANY($%d)", ss)
		}
		if page.Cursor != "" {
			ks, err := db.DecodeCursor(page.Cursor)
			if err != nil {
				return err
			}
			args = append(args, ks.CreatedAt, ks.ID)
			where = append(where, fmt.Sprintf(
				"(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
		}
		args = append(args, page.Limit+1)
		rows, err := tx.Query(ctx,
			`SELECT `+execCols+` FROM workflow_executions WHERE `+
				strings.Join(where, " AND ")+
				` ORDER BY created_at DESC, id DESC LIMIT $`+
				fmt.Sprint(len(args)), args...)
		if err != nil {
			return db.MapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanExec(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			return db.MapError(err)
		}
		if len(out) > page.Limit {
			last := out[page.Limit-1]
			next = db.EncodeCursor(db.Keyset{
				CreatedAt: last.CreatedAt, ID: last.ID})
			out = out[:page.Limit]
		}
		return nil
	})
	return out, next, err
}

// TransitionExec implements executions.Repository: a guarded
// UPDATE ... WHERE id AND state AND version; zero rows → stale.
func (r *Repository) TransitionExec(ctx context.Context,
	scope tenants.Scope, id uuid.UUID, fromState executions.ExecutionState,
	fromVersion int64, p executions.ExecPatch,
	enqueue executions.EnqueueFunc) (executions.Execution, error) {
	var e executions.Execution
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		set := []string{"version = version + 1", "updated_at = now()"}
		args := []any{}
		add := func(clause string, v any) {
			args = append(args, v)
			set = append(set, fmt.Sprintf(clause, len(args)))
		}
		if p.State != nil {
			add("state = $%d", string(*p.State))
		}
		if p.Reason != nil {
			add("state_reason = $%d", *p.Reason)
		}
		if p.StartedAt != nil {
			add("started_at = $%d", *p.StartedAt)
		}
		if p.EndedAt != nil {
			add("ended_at = $%d", *p.EndedAt)
		}
		args = append(args, id, string(fromState), fromVersion)
		n := len(args)
		tag, err := tx.Exec(ctx,
			`UPDATE workflow_executions SET `+strings.Join(set, ", ")+
				fmt.Sprintf(` WHERE id=$%d AND state=$%d AND version=$%d`,
					n-2, n-1, n), args...)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return executions.ErrTransitionStale
		}
		if enqueue != nil {
			if err := enqueue(tx); err != nil {
				return err
			}
		}
		e, err = scanExec(tx.QueryRow(ctx,
			`SELECT `+execCols+` FROM workflow_executions WHERE id=$1`, id))
		return err
	})
	return e, err
}

const taskCols = `id, execution_id, tenant_id, task_name, index, count,
	attempt, state, state_reason, job_id, execution_spec,
	execution_spec_digest, script_validation_id, created_at, updated_at,
	version`

func scanTask(row pgx.Row) (executions.TaskExecution, error) {
	var t executions.TaskExecution
	var state string
	var reason *string
	var specJSON, specDigest []byte
	err := row.Scan(&t.ID, &t.ExecutionID, &t.TenantID, &t.TaskName,
		&t.Index, &t.Count, &t.Attempt, &state, &reason, &t.JobID,
		&specJSON, &specDigest, &t.ValidationID, &t.CreatedAt,
		&t.UpdatedAt, &t.Version)
	if err != nil {
		return t, db.MapError(err)
	}
	t.State = executions.TaskState(state)
	if reason != nil {
		t.StateReason = *reason
	}
	if specJSON != nil {
		var spec admission.ExecutionSpec
		if err := json.Unmarshal(specJSON, &spec); err != nil {
			return t, fmt.Errorf("task_executions: spec: %w", err)
		}
		t.Spec = &spec
	}
	if specDigest != nil {
		var d validation.Digest
		copy(d[:], specDigest)
		t.SpecDigest = &d
	}
	return t, nil
}

// Materialize implements executions.Repository: inserts the
// materialized tasks and applies the guarded execution transition +
// enqueue in one transaction.
func (r *Repository) Materialize(ctx context.Context,
	scope tenants.Scope, execID uuid.UUID,
	fromState executions.ExecutionState, fromVersion int64,
	tasks []executions.TaskExecution, p executions.ExecPatch,
	enqueue executions.EnqueueFunc) (executions.Execution, error) {
	var e executions.Execution
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		for _, t := range tasks {
			if _, err := tx.Exec(ctx, `
				INSERT INTO task_executions (`+taskCols+`)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
				t.ID, t.ExecutionID, scopeTenant(scope, t.TenantID),
				t.TaskName, t.Index, t.Count, t.Attempt, string(t.State),
				nilStr(t.StateReason), t.JobID, nil, nil, t.ValidationID,
				t.CreatedAt, t.UpdatedAt, t.Version); err != nil {
				return db.MapError(err)
			}
		}
		set := []string{"version = version + 1", "updated_at = now()"}
		args := []any{}
		add := func(clause string, v any) {
			args = append(args, v)
			set = append(set, fmt.Sprintf(clause, len(args)))
		}
		if p.State != nil {
			add("state = $%d", string(*p.State))
		}
		if p.Reason != nil {
			add("state_reason = $%d", *p.Reason)
		}
		if p.StartedAt != nil {
			add("started_at = $%d", *p.StartedAt)
		}
		args = append(args, execID, string(fromState), fromVersion)
		n := len(args)
		tag, err := tx.Exec(ctx,
			`UPDATE workflow_executions SET `+strings.Join(set, ", ")+
				fmt.Sprintf(` WHERE id=$%d AND state=$%d AND version=$%d`,
					n-2, n-1, n), args...)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return executions.ErrTransitionStale
		}
		if enqueue != nil {
			if err := enqueue(tx); err != nil {
				return err
			}
		}
		e, err = scanExec(tx.QueryRow(ctx,
			`SELECT `+execCols+` FROM workflow_executions WHERE id=$1`,
			execID))
		return err
	})
	return e, err
}

// CancelExec implements executions.Repository: → CANCELING from any
// cancelable state, guarded by version.
func (r *Repository) CancelExec(ctx context.Context,
	scope tenants.Scope, id uuid.UUID, fromVersion int64,
	enqueue executions.EnqueueFunc) (executions.Execution, error) {
	var e executions.Execution
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE workflow_executions
			SET state='CANCELING', version=version+1, updated_at=now()
			WHERE id=$1 AND version=$2
			  AND state IN ('PENDING','QUEUED','RUNNING')`,
			id, fromVersion)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return executions.ErrTransitionStale
		}
		if enqueue != nil {
			if err := enqueue(tx); err != nil {
				return err
			}
		}
		e, err = scanExec(tx.QueryRow(ctx,
			`SELECT `+execCols+` FROM workflow_executions WHERE id=$1`, id))
		return err
	})
	return e, err
}

// ListTasks implements executions.Repository.
func (r *Repository) ListTasks(ctx context.Context, scope tenants.Scope,
	_, executionID uuid.UUID) ([]executions.TaskExecution, error) {
	var out []executions.TaskExecution
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT `+taskCols+` FROM task_executions
			 WHERE execution_id=$1
			 ORDER BY task_name, index`, executionID)
		if err != nil {
			return db.MapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTask(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// GetTask implements executions.Repository.
func (r *Repository) GetTask(ctx context.Context, scope tenants.Scope,
	_, id uuid.UUID) (executions.TaskExecution, error) {
	var t executions.TaskExecution
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		t, err = scanTask(tx.QueryRow(ctx,
			`SELECT `+taskCols+` FROM task_executions WHERE id=$1`, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return t, errNotFound
	}
	return t, err
}

// GetTaskByJob implements executions.Repository.
func (r *Repository) GetTaskByJob(ctx context.Context,
	scope tenants.Scope, jobID uuid.UUID) (executions.TaskExecution, error) {
	var t executions.TaskExecution
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		t, err = scanTask(tx.QueryRow(ctx,
			`SELECT `+taskCols+` FROM task_executions WHERE job_id=$1`,
			jobID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return t, errNotFound
	}
	return t, err
}

// TransitionTask implements executions.Repository: guarded UPDATE;
// zero rows → stale. The spec columns are write-once (DB trigger).
func (r *Repository) TransitionTask(ctx context.Context,
	scope tenants.Scope, id uuid.UUID, fromState executions.TaskState,
	fromVersion int64, p executions.TaskPatch,
	enqueue executions.EnqueueFunc) (executions.TaskExecution, error) {
	var t executions.TaskExecution
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		set := []string{"version = version + 1", "updated_at = now()"}
		args := []any{}
		add := func(clause string, v any) {
			args = append(args, v)
			set = append(set, fmt.Sprintf(clause, len(args)))
		}
		if p.State != nil {
			add("state = $%d", string(*p.State))
		}
		if p.Reason != nil {
			add("state_reason = $%d", *p.Reason)
		}
		if p.JobID != nil {
			add("job_id = $%d", *p.JobID)
		}
		if p.ClearJob {
			set = append(set, "job_id = NULL")
		}
		if p.ClearSpec {
			set = append(set, "execution_spec = NULL",
				"execution_spec_digest = NULL")
		}
		if p.Attempt != nil {
			add("attempt = $%d", *p.Attempt)
		}
		if p.Spec != nil {
			specJSON, err := json.Marshal(*p.Spec)
			if err != nil {
				return fmt.Errorf("task_executions: spec: %w", err)
			}
			add("execution_spec = $%d", specJSON)
		}
		if p.SpecDigest != nil {
			add("execution_spec_digest = $%d", (*p.SpecDigest)[:])
		}
		if p.ValidationID != nil {
			add("script_validation_id = $%d", *p.ValidationID)
		}
		args = append(args, id, string(fromState), fromVersion)
		n := len(args)
		tag, err := tx.Exec(ctx,
			`UPDATE task_executions SET `+strings.Join(set, ", ")+
				fmt.Sprintf(` WHERE id=$%d AND state=$%d AND version=$%d`,
					n-2, n-1, n), args...)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return executions.ErrTransitionStale
		}
		if enqueue != nil {
			if err := enqueue(tx); err != nil {
				return err
			}
		}
		t, err = scanTask(tx.QueryRow(ctx,
			`SELECT `+taskCols+` FROM task_executions WHERE id=$1`, id))
		return err
	})
	return t, err
}

const admitJobCols = `id, tenant_id, project_id, cluster_id, created_by,
	name, state, state_reason, slurm_job_id, slurm_state, exit_code,
	exit_signal, resource_request, execution_spec,
	execution_spec_digest, script_digest, script_language,
	script_validation_id, task_execution_id, submitted_at, started_at,
	ended_at, last_reconciled_at, version, created_at, updated_at`

// AdmitTask implements executions.Repository: freezes the spec,
// transitions ADMITTING → SUBMITTING, inserts the job and runs
// enqueue (the job.submit item) in one transaction.
func (r *Repository) AdmitTask(ctx context.Context,
	scope tenants.Scope, taskID uuid.UUID,
	fromState executions.TaskState, fromVersion int64,
	p executions.TaskPatch, j jobs.Job,
	enqueue executions.EnqueueFunc) (executions.TaskExecution, error) {
	var t executions.TaskExecution
	specJSON, err := json.Marshal(j.ExecutionSpec)
	if err != nil {
		return t, fmt.Errorf("jobs: marshal execution_spec: %w", err)
	}
	reqJSON, err := json.Marshal(j.ResourceRequest)
	if err != nil {
		return t, fmt.Errorf("jobs: marshal resource_request: %w", err)
	}
	err = db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		// The job row must exist before task_executions.job_id can
		// reference it (the FK is checked immediately).
		var scriptDigest []byte
		if j.ScriptDigest != (validation.Digest{}) {
			scriptDigest = j.ScriptDigest[:]
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO jobs (`+admitJobCols+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
			        $16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`,
			j.ID, scopeTenant(scope, j.TenantID), j.ProjectID, j.ClusterID,
			j.CreatedBy, j.Name, string(j.State), nilStr(j.StateReason),
			j.SlurmJobID, nilStr(j.SlurmState), j.ExitCode, j.ExitSignal,
			reqJSON, specJSON, j.ExecutionSpecDigest[:], scriptDigest,
			string(j.ScriptLanguage), j.ScriptValidationID,
			j.TaskExecutionID, j.SubmittedAt, j.StartedAt, j.EndedAt,
			j.LastReconciledAt, j.Version, j.CreatedAt, j.UpdatedAt); err != nil {
			return db.MapError(err)
		}
		set := []string{"version = version + 1", "updated_at = now()"}
		args := []any{}
		add := func(clause string, v any) {
			args = append(args, v)
			set = append(set, fmt.Sprintf(clause, len(args)))
		}
		if p.State != nil {
			add("state = $%d", string(*p.State))
		}
		if p.Reason != nil {
			add("state_reason = $%d", *p.Reason)
		}
		if p.JobID != nil {
			add("job_id = $%d", *p.JobID)
		}
		if p.Attempt != nil {
			add("attempt = $%d", *p.Attempt)
		}
		if p.Spec != nil {
			tSpec, err := json.Marshal(*p.Spec)
			if err != nil {
				return fmt.Errorf("task_executions: spec: %w", err)
			}
			add("execution_spec = $%d", tSpec)
		}
		if p.SpecDigest != nil {
			add("execution_spec_digest = $%d", (*p.SpecDigest)[:])
		}
		if p.ValidationID != nil {
			add("script_validation_id = $%d", *p.ValidationID)
		}
		args = append(args, taskID, string(fromState), fromVersion)
		n := len(args)
		tag, err := tx.Exec(ctx,
			`UPDATE task_executions SET `+strings.Join(set, ", ")+
				fmt.Sprintf(` WHERE id=$%d AND state=$%d AND version=$%d`,
					n-2, n-1, n), args...)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return executions.ErrTransitionStale
		}
		if enqueue != nil {
			if err := enqueue(tx); err != nil {
				return err
			}
		}
		t, err = scanTask(tx.QueryRow(ctx,
			`SELECT `+taskCols+` FROM task_executions WHERE id=$1`,
			taskID))
		return err
	})
	return t, err
}
