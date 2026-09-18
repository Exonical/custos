// Package postgres implements workflows.Repository on PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/workflows"
)

var errNotFound = apperr.New(apperr.NotFound, "NOT_FOUND", "not found")

func applyScope(ctx context.Context, tx pgx.Tx, s tenants.Scope) error {
	if s.IsPlatform() {
		return db.SetPlatformScope(ctx, tx)
	}
	id, _ := s.TenantID()
	return db.SetTenant(ctx, tx, id)
}

func versionConflict() error {
	return apperr.New(apperr.Conflict, "VERSION_CONFLICT",
		"stale version; reload and retry")
}

// Repository is the PostgreSQL workflows store.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository on pool.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const wfCols = `id, tenant_id, project_id, name, description, state,
	latest_published_version_id, created_by, created_at, updated_at, version`

func scanWf(row pgx.Row) (workflows.Workflow, error) {
	var w workflows.Workflow
	err := row.Scan(&w.ID, &w.TenantID, &w.ProjectID, &w.Name,
		&w.Description, &w.State, &w.LatestPublishedVersion,
		&w.CreatedBy, &w.CreatedAt, &w.UpdatedAt, &w.Version)
	return w, err
}

// Create inserts the workflow record.
func (r *Repository) Create(ctx context.Context, scope tenants.Scope,
	w workflows.Workflow) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO workflows
			  (id, tenant_id, project_id, name, description, state,
			   created_by, created_at, updated_at, version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8,1)`,
			w.ID, w.TenantID, w.ProjectID, w.Name, w.Description,
			string(w.State), w.CreatedBy, w.CreatedAt)
		return db.MapError(err)
	})
}

// Get returns one workflow by id.
func (r *Repository) Get(ctx context.Context, scope tenants.Scope,
	tenantID, id uuid.UUID) (workflows.Workflow, error) {
	var w workflows.Workflow
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		w, err = scanWf(tx.QueryRow(ctx, `
			SELECT `+wfCols+` FROM workflows
			WHERE tenant_id=$1 AND id=$2`, tenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return workflows.Workflow{}, errNotFound
	}
	return w, err
}

// List returns workflows, optionally filtered by project.
func (r *Repository) List(ctx context.Context, scope tenants.Scope,
	tenantID uuid.UUID, projectID *uuid.UUID) ([]workflows.Workflow, error) {
	var out []workflows.Workflow
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		q := `SELECT ` + wfCols + ` FROM workflows WHERE tenant_id=$1`
		args := []any{tenantID}
		if projectID != nil {
			q += ` AND project_id=$2`
			args = append(args, *projectID)
		}
		q += ` ORDER BY created_at, id`
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			w, err := scanWf(rows)
			if err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

// Update applies name/description/state changes with optimistic
// locking.
func (r *Repository) Update(ctx context.Context, scope tenants.Scope,
	w workflows.Workflow, expectVersion int64) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE workflows
			SET name=$3, description=$4, state=$5, updated_at=$6,
			    version=version+1
			WHERE tenant_id=$1 AND id=$2 AND version=$7`,
			w.TenantID, w.ID, w.Name, w.Description, string(w.State),
			w.UpdatedAt, expectVersion)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return versionConflict()
		}
		return nil
	})
}

const verCols = `id, workflow_id, tenant_id, number, state,
	schema_version, spec, spec_hash, layout, created_by, created_at,
	published_at, version`

func scanVer(row pgx.Row) (workflows.Version, error) {
	var v workflows.Version
	var state string
	var hash []byte
	err := row.Scan(&v.ID, &v.WorkflowID, &v.TenantID, &v.Number,
		&state, &v.SchemaVersion, &v.Spec, &hash, &v.Layout,
		&v.CreatedBy, &v.CreatedAt, &v.PublishedAt, &v.Version)
	v.State = workflows.VersionState(state)
	copy(v.SpecHash[:], hash)
	return v, err
}

// CreateVersion inserts a draft version.
func (r *Repository) CreateVersion(ctx context.Context,
	scope tenants.Scope, v workflows.Version) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO workflow_versions
			  (id, workflow_id, tenant_id, number, state, schema_version,
			   spec, spec_hash, layout, created_by, created_at, version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,1)`,
			v.ID, v.WorkflowID, v.TenantID, v.Number, string(v.State),
			v.SchemaVersion, v.Spec, v.SpecHash[:], v.Layout,
			v.CreatedBy, v.CreatedAt)
		return db.MapError(err)
	})
}

// GetVersion returns one version by id.
func (r *Repository) GetVersion(ctx context.Context, scope tenants.Scope,
	tenantID, workflowID, versionID uuid.UUID) (workflows.Version, error) {
	var v workflows.Version
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		v, err = scanVer(tx.QueryRow(ctx, `
			SELECT `+verCols+` FROM workflow_versions
			WHERE tenant_id=$1 AND workflow_id=$2 AND id=$3`,
			tenantID, workflowID, versionID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return workflows.Version{}, errNotFound
	}
	return v, err
}

// ListVersions returns all versions of a workflow, newest first.
func (r *Repository) ListVersions(ctx context.Context,
	scope tenants.Scope, tenantID, workflowID uuid.UUID) ([]workflows.Version, error) {
	var out []workflows.Version
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT `+verCols+` FROM workflow_versions
			WHERE tenant_id=$1 AND workflow_id=$2
			ORDER BY number DESC`, tenantID, workflowID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVer(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateDraftSpec replaces a draft's canonical spec and hash.
func (r *Repository) UpdateDraftSpec(ctx context.Context,
	scope tenants.Scope, v workflows.Version, expectVersion int64) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE workflow_versions
			SET spec=$4, spec_hash=$5, schema_version=$6,
			    version=version+1
			WHERE tenant_id=$1 AND workflow_id=$2 AND id=$3
			  AND (version=$7 OR $7=0) AND state='draft'`,
			v.TenantID, v.WorkflowID, v.ID, v.Spec, v.SpecHash[:],
			v.SchemaVersion, expectVersion)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return versionConflict()
		}
		return nil
	})
}

// UpdateLayout replaces editor layout on any non-deprecated version.
func (r *Repository) UpdateLayout(ctx context.Context,
	scope tenants.Scope, v workflows.Version, expectVersion int64) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE workflow_versions
			SET layout=$4, version=version+1
			WHERE tenant_id=$1 AND workflow_id=$2 AND id=$3
			  AND (version=$5 OR $5=0) AND state<>'deprecated'`,
			v.TenantID, v.WorkflowID, v.ID, v.Layout, expectVersion)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return versionConflict()
		}
		return nil
	})
}

// SetVersionState moves a version forward; publish additionally
// stamps published_at and updates the workflow's
// latest_published_version_id atomically.
func (r *Repository) SetVersionState(ctx context.Context,
	scope tenants.Scope, v workflows.Version, to workflows.VersionState,
	expectVersion int64) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var pub *time.Time
		if to == workflows.VersionPublished {
			now := time.Now().UTC()
			pub = &now
		}
		tag, err := tx.Exec(ctx, `
			UPDATE workflow_versions
			SET state=$4, published_at=COALESCE($5, published_at),
			    version=version+1
			WHERE tenant_id=$1 AND workflow_id=$2 AND id=$3
			  AND (version=$6 OR $6=0)`,
			v.TenantID, v.WorkflowID, v.ID, string(to), pub,
			expectVersion)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return versionConflict()
		}
		if to == workflows.VersionPublished {
			_, err = tx.Exec(ctx, `
				UPDATE workflows
				SET latest_published_version_id=$3,
				    updated_at=$4, version=version+1
				WHERE tenant_id=$1 AND id=$2`,
				v.TenantID, v.WorkflowID, v.ID, *pub)
			return db.MapError(err)
		}
		return nil
	})
}

// NextVersionNumber returns max(number)+1 for the workflow.
func (r *Repository) NextVersionNumber(ctx context.Context,
	scope tenants.Scope, workflowID uuid.UUID) (int, error) {
	var n int
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(number),0)+1 FROM workflow_versions
			WHERE workflow_id=$1`, workflowID).Scan(&n)
	})
	return n, err
}
