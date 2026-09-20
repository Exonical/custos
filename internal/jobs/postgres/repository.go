// Package postgres implements jobs.Repository on PostgreSQL.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/jobs"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// Repository is the PostgreSQL jobs.Repository.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository on pool.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

var errNotFound = apperr.New(apperr.NotFound, "NOT_FOUND", "not found")

func applyScope(ctx context.Context, tx pgx.Tx, s tenants.Scope) error {
	if s.IsPlatform() {
		return db.SetPlatformScope(ctx, tx)
	}
	id, _ := s.TenantID()
	return db.SetTenant(ctx, tx, id)
}

// scopeTenant returns the RLS tenant when scoped; uuid.Nil under the
// platform scope (callers pass the job's tenant_id explicitly then).
func scopeTenant(s tenants.Scope, tenantID uuid.UUID) uuid.UUID {
	if id, ok := s.TenantID(); ok {
		return id
	}
	return tenantID
}

const jobCols = `id, tenant_id, project_id, cluster_id, created_by, name,
	state, state_reason, slurm_job_id, slurm_state, exit_code, exit_signal,
	resource_request, execution_spec, execution_spec_digest, script_digest,
	script_language, script_validation_id, task_execution_id,
	submitted_at, started_at, ended_at, last_reconciled_at, version,
	created_at, updated_at, resource_usage`

// nilDigest returns nil for the zero digest so script_digest stores
// NULL on command (payload-less) jobs.
func nilDigest(d validation.Digest) []byte {
	if d == (validation.Digest{}) {
		return nil
	}
	return d[:]
}

func scanJob(row pgx.Row) (jobs.Job, error) {
	var j jobs.Job
	var reason, slurmState *string
	var state, lang string
	var specJSON, reqJSON, usageJSON []byte
	var specDigest, scriptDigest []byte
	err := row.Scan(&j.ID, &j.TenantID, &j.ProjectID, &j.ClusterID,
		&j.CreatedBy, &j.Name, &state, &reason, &j.SlurmJobID,
		&slurmState, &j.ExitCode, &j.ExitSignal, &reqJSON, &specJSON,
		&specDigest, &scriptDigest, &lang, &j.ScriptValidationID,
		&j.TaskExecutionID, &j.SubmittedAt,
		&j.StartedAt, &j.EndedAt, &j.LastReconciledAt, &j.Version,
		&j.CreatedAt, &j.UpdatedAt, &usageJSON)
	if err != nil {
		return j, db.MapError(err)
	}
	j.State = jobs.State(state)
	j.ScriptLanguage = workflowspec.Language(lang)
	if reason != nil {
		j.StateReason = *reason
	}
	if slurmState != nil {
		j.SlurmState = *slurmState
	}
	if err := json.Unmarshal(reqJSON, &j.ResourceRequest); err != nil {
		return j, fmt.Errorf("jobs: resource_request: %w", err)
	}
	if err := json.Unmarshal(specJSON, &j.ExecutionSpec); err != nil {
		return j, fmt.Errorf("jobs: execution_spec: %w", err)
	}
	copy(j.ExecutionSpecDigest[:], specDigest)
	copy(j.ScriptDigest[:], scriptDigest)
	if len(usageJSON) > 0 {
		if err := json.Unmarshal(usageJSON, &j.ResourceUsage); err != nil {
			return j, fmt.Errorf("jobs: resource_usage: %w", err)
		}
	}
	return j, nil
}

const insertJobSQL = `INSERT INTO jobs (` + jobCols + `)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,
	        $17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`

// Create inserts a job and runs enqueue in one transaction (workflow
// task admissions; ad-hoc submissions use CreateWithIdempotency).
func (r *Repository) Create(ctx context.Context, scope tenants.Scope,
	j jobs.Job, enqueue jobs.EnqueueFunc) error {
	specJSON, err := json.Marshal(j.ExecutionSpec)
	if err != nil {
		return fmt.Errorf("jobs: marshal execution_spec: %w", err)
	}
	reqJSON, err := json.Marshal(j.ResourceRequest)
	if err != nil {
		return fmt.Errorf("jobs: marshal resource_request: %w", err)
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, insertJobSQL,
			j.ID, scopeTenant(scope, j.TenantID), j.ProjectID, j.ClusterID,
			j.CreatedBy, j.Name, string(j.State), nilStr(j.StateReason),
			j.SlurmJobID, nilStr(j.SlurmState), j.ExitCode, j.ExitSignal,
			reqJSON, specJSON, j.ExecutionSpecDigest[:],
			nilDigest(j.ScriptDigest), string(j.ScriptLanguage),
			j.ScriptValidationID, j.TaskExecutionID, j.SubmittedAt,
			j.StartedAt, j.EndedAt, j.LastReconciledAt, j.Version,
			j.CreatedAt, j.UpdatedAt, nil); err != nil {
			return db.MapError(err)
		}
		if enqueue != nil {
			return enqueue(tx)
		}
		return nil
	})
}

// CreateWithIdempotency implements jobs.Repository.
func (r *Repository) CreateWithIdempotency(ctx context.Context,
	scope tenants.Scope, j jobs.Job, idem jobs.IdemRecord,
	enqueue jobs.EnqueueFunc) (jobs.CreateResult, error) {
	var res jobs.CreateResult
	specJSON, err := json.Marshal(j.ExecutionSpec)
	if err != nil {
		return res, fmt.Errorf("jobs: marshal execution_spec: %w", err)
	}
	reqJSON, err := json.Marshal(j.ResourceRequest)
	if err != nil {
		return res, fmt.Errorf("jobs: marshal resource_request: %w", err)
	}
	err = db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tid := scopeTenant(scope, j.TenantID)
		// Insert the idempotency key first: a racing duplicate key
		// blocks until the winner commits, then replays its response.
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
			res = jobs.CreateResult{Replayed: true, Status: status, Body: body}
			if rid != nil {
				rj, rerr := scanJob(tx.QueryRow(ctx,
					`SELECT `+jobCols+` FROM jobs WHERE id=$1`, *rid))
				if rerr != nil {
					return rerr
				}
				res.Job = rj
			}
			return nil
		}
		if _, err := tx.Exec(ctx, insertJobSQL,
			j.ID, tid, j.ProjectID, j.ClusterID, j.CreatedBy, j.Name,
			string(j.State), nilStr(j.StateReason), j.SlurmJobID,
			nilStr(j.SlurmState), j.ExitCode, j.ExitSignal, reqJSON,
			specJSON, j.ExecutionSpecDigest[:], nilDigest(j.ScriptDigest),
			string(j.ScriptLanguage), j.ScriptValidationID,
			j.TaskExecutionID, j.SubmittedAt,
			j.StartedAt, j.EndedAt, j.LastReconciledAt, j.Version,
			j.CreatedAt, j.UpdatedAt, nil); err != nil {
			return db.MapError(err)
		}
		if enqueue != nil {
			if err := enqueue(tx); err != nil {
				return err
			}
		}
		res = jobs.CreateResult{Job: j}
		return nil
	})
	return res, err
}

func nilStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Get implements jobs.Repository.
func (r *Repository) Get(ctx context.Context, scope tenants.Scope,
	id uuid.UUID) (jobs.Job, error) {
	var j jobs.Job
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		j, err = scanJob(tx.QueryRow(ctx,
			`SELECT `+jobCols+` FROM jobs WHERE id=$1`, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return j, errNotFound
	}
	return j, err
}

// List implements jobs.Repository (keyset on created_at DESC, id).
func (r *Repository) List(ctx context.Context, scope tenants.Scope,
	f jobs.Filter, page tenants.Page) ([]jobs.Job, string, error) {
	page = page.Normalize(50, 200)
	var out []jobs.Job
	var next string
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		where := []string{"true"}
		args := []any{}
		add := func(clause string, v any) {
			args = append(args, v)
			where = append(where, fmt.Sprintf(clause, len(args)))
		}
		if f.ProjectID != nil {
			add("project_id = $%d", *f.ProjectID)
		}
		if f.ClusterID != nil {
			add("cluster_id = $%d", *f.ClusterID)
		}
		if f.CreatedBy != nil {
			add("created_by = $%d", *f.CreatedBy)
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
			`SELECT `+jobCols+` FROM jobs WHERE `+strings.Join(where, " AND ")+
				` ORDER BY created_at DESC, id DESC LIMIT $`+
				fmt.Sprint(len(args)), args...)
		if err != nil {
			return db.MapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, j)
		}
		if err := rows.Err(); err != nil {
			return db.MapError(err)
		}
		if len(out) > page.Limit {
			last := out[page.Limit-1]
			next = db.EncodeCursor(db.Keyset{CreatedAt: last.CreatedAt, ID: last.ID})
			out = out[:page.Limit]
		}
		return nil
	})
	return out, next, err
}

// ListActiveByCluster implements jobs.Repository.
func (r *Repository) ListActiveByCluster(ctx context.Context,
	clusterID uuid.UUID) ([]jobs.Job, error) {
	var out []jobs.Job
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+jobCols+` FROM jobs
			WHERE cluster_id=$1 AND state IN ('SUBMITTING','QUEUED','RUNNING')`,
			clusterID)
		if err != nil {
			return db.MapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, j)
		}
		return db.MapError(rows.Err())
	})
	return out, err
}

// Transition implements jobs.Repository.
func (r *Repository) Transition(ctx context.Context, scope tenants.Scope,
	id uuid.UUID, fromVersion int, p jobs.Patch) (jobs.Job, error) {
	var out jobs.Job
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		sets := []string{"version = version + 1", "updated_at = now()"}
		args := []any{}
		add := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
		}
		if p.State != nil {
			add("state", string(*p.State))
		}
		if p.Reason != nil {
			add("state_reason", *p.Reason)
		}
		if p.SlurmJobID != nil {
			add("slurm_job_id", *p.SlurmJobID)
		}
		if p.SlurmState != nil {
			add("slurm_state", *p.SlurmState)
		}
		if p.ExitCode != nil {
			add("exit_code", *p.ExitCode)
		}
		if p.ExitSignal != nil {
			add("exit_signal", *p.ExitSignal)
		}
		if p.SubmittedAt != nil {
			add("submitted_at", *p.SubmittedAt)
		}
		if p.StartedAt != nil {
			add("started_at", *p.StartedAt)
		}
		if p.EndedAt != nil {
			add("ended_at", *p.EndedAt)
		}
		if p.LastReconciledAt != nil {
			add("last_reconciled_at", *p.LastReconciledAt)
		}
		args = append(args, id, fromVersion)
		tag, err := tx.Exec(ctx,
			`UPDATE jobs SET `+strings.Join(sets, ", ")+
				fmt.Sprintf(` WHERE id = $%d AND version = $%d`,
					len(args)-1, len(args)), args...)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM jobs WHERE id=$1)`,
				id).Scan(&exists); err != nil {
				return db.MapError(err)
			}
			if !exists {
				return errNotFound
			}
			return apperr.New(apperr.Conflict, "VERSION_CONFLICT",
				"job was modified concurrently")
		}
		var jerr error
		out, jerr = scanJob(tx.QueryRow(ctx,
			`SELECT `+jobCols+` FROM jobs WHERE id=$1`, id))
		return jerr
	})
	return out, err
}

// CountActiveByState implements jobs.Repository.
func (r *Repository) CountActiveByState(ctx context.Context) (map[jobs.State]int64, error) {
	out := map[jobs.State]int64{}
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT state, count(*) FROM jobs
			WHERE state IN ('SUBMITTING','QUEUED','RUNNING')
			GROUP BY state`)
		if err != nil {
			return db.MapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			var n int64
			if err := rows.Scan(&s, &n); err != nil {
				return err
			}
			out[jobs.State(s)] = n
		}
		return db.MapError(rows.Err())
	})
	return out, err
}

// ExpireIdempotency implements jobs.Repository.
func (r *Repository) ExpireIdempotency(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM idempotency_keys WHERE expires_at < $1`, now)
		if err != nil {
			return db.MapError(err)
		}
		n = tag.RowsAffected()
		return nil
	})
	return n, err
}
