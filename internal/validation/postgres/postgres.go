// Package postgres implements validation policy and ScriptValidation
// persistence on PostgreSQL.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/policy"
)

var errNotFound = apperr.New(apperr.NotFound, "NOT_FOUND", "not found")

func applyScope(ctx context.Context, tx pgx.Tx, s tenants.Scope) error {
	if s.IsPlatform() {
		return db.SetPlatformScope(ctx, tx)
	}
	id, _ := s.TenantID()
	return db.SetTenant(ctx, tx, id)
}

// PolicyStore is the PostgreSQL policy.Store.
type PolicyStore struct {
	pool *pgxpool.Pool
}

// NewPolicyStore returns a PolicyStore on pool.
func NewPolicyStore(pool *pgxpool.Pool) *PolicyStore {
	return &PolicyStore{pool: pool}
}

// Get implements policy.Store.
func (s *PolicyStore) Get(ctx context.Context, scope tenants.Scope,
	kind string, id uuid.UUID) (policy.Scoped, error) {
	var out policy.Scoped
	var body []byte
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var updBy *uuid.UUID
		err := tx.QueryRow(ctx, `
			SELECT scope_kind, scope_id, version, body, updated_by, updated_at
			FROM validation_policies
			WHERE scope_kind=$1 AND scope_id=$2`, kind, id).
			Scan(&out.ScopeKind, &out.ScopeID, &out.Version, &body,
				&updBy, &out.UpdatedAt)
		if updBy != nil {
			out.UpdatedBy = *updBy
		}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return policy.Scoped{}, errNotFound
	}
	if err != nil {
		return policy.Scoped{}, err
	}
	if err := json.Unmarshal(body, &out.Body); err != nil {
		return policy.Scoped{}, err
	}
	return out, nil
}

// Put implements policy.Store: optimistic version check
// (expectVersion 0 = create).
func (s *PolicyStore) Put(ctx context.Context, scope tenants.Scope,
	in policy.Scoped, expectVersion int64) (policy.Scoped, error) {
	body, err := json.Marshal(in.Body)
	if err != nil {
		return policy.Scoped{}, err
	}
	var updBy *uuid.UUID
	if in.UpdatedBy != uuid.Nil {
		updBy = &in.UpdatedBy
	}
	var out policy.Scoped
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var cur int64
		row := tx.QueryRow(ctx, `
			SELECT version FROM validation_policies
			WHERE scope_kind=$1 AND scope_id=$2`, in.ScopeKind, in.ScopeID)
		err := row.Scan(&cur)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if expectVersion != 0 {
				return apperr.New(apperr.Conflict, "VERSION_CONFLICT",
					"policy does not exist; expected version 0")
			}
			out = in
			out.Version = 1
			if out.UpdatedAt.IsZero() {
				out.UpdatedAt = time.Now().UTC()
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO validation_policies
				  (id, scope_kind, scope_id, version, body, updated_by, updated_at)
				VALUES ($1,$2,$3,1,$4,$5,$6)`,
				uuid.Must(uuid.NewV7()), in.ScopeKind, in.ScopeID, body,
				updBy, out.UpdatedAt)
			return db.MapError(err)
		case err != nil:
			return err
		}
		if cur != expectVersion {
			return apperr.New(apperr.Conflict, "VERSION_CONFLICT",
				"policy version mismatch")
		}
		out = in
		out.Version = cur + 1
		if out.UpdatedAt.IsZero() {
			out.UpdatedAt = time.Now().UTC()
		}
		_, err = tx.Exec(ctx, `
			UPDATE validation_policies
			SET version=$3, body=$4, updated_by=$5, updated_at=$6
			WHERE scope_kind=$1 AND scope_id=$2 AND version=$3-1`,
			in.ScopeKind, in.ScopeID, out.Version, body, updBy,
			out.UpdatedAt)
		return db.MapError(err)
	})
	if err != nil {
		return policy.Scoped{}, err
	}
	out.Body = in.Body
	return out, nil
}

// ValidationStore is the PostgreSQL ScriptValidation store.
type ValidationStore struct {
	pool *pgxpool.Pool
}

// NewValidationStore returns a ValidationStore on pool.
func NewValidationStore(pool *pgxpool.Pool) *ValidationStore {
	return &ValidationStore{pool: pool}
}

// Put persists one ScriptValidation.
func (s *ValidationStore) Put(ctx context.Context, scope tenants.Scope,
	sv validation.ScriptValidation) error {
	diags, err := json.Marshal(sv.Diagnostics)
	if err != nil {
		return err
	}
	tools, err := json.Marshal(sv.ToolVersions)
	if err != nil {
		return err
	}
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		// Validations are immutable content; the pipeline cache can hand
		// the same ID to several submissions, so re-put is a no-op.
		_, err := tx.Exec(ctx, `
			INSERT INTO script_validations
			  (id, tenant_id, workflow_version_id, task_name, script_digest,
			   language, valid, diagnostics, tool_versions, policy_version,
			   validated_at, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (id) DO NOTHING`,
			sv.ID, sv.TenantID, sv.WorkflowVersionID, sv.TaskName,
			sv.ScriptDigest[:], string(sv.Language), sv.Valid, diags, tools,
			sv.PolicyVersion, sv.ValidatedAt, sv.ExpiresAt)
		return db.MapError(err)
	})
}

func scanSV(row pgx.Row) (validation.ScriptValidation, error) {
	var sv validation.ScriptValidation
	var diags, tools, digest []byte
	var lang string
	err := row.Scan(&sv.ID, &sv.TenantID, &sv.WorkflowVersionID,
		&sv.TaskName, &digest, &lang, &sv.Valid, &diags,
		&tools, &sv.PolicyVersion, &sv.ValidatedAt, &sv.ExpiresAt)
	if err != nil {
		return validation.ScriptValidation{}, err
	}
	copy(sv.ScriptDigest[:], digest)
	sv.Language = validation.Language(lang)
	if err := json.Unmarshal(diags, &sv.Diagnostics); err != nil {
		return validation.ScriptValidation{}, err
	}
	if err := json.Unmarshal(tools, &sv.ToolVersions); err != nil {
		return validation.ScriptValidation{}, err
	}
	return sv, nil
}

const svCols = `id, tenant_id, workflow_version_id, task_name, script_digest,
	language, valid, diagnostics, tool_versions, policy_version,
	validated_at, expires_at`

// Latest returns the newest ScriptValidation for (digest,
// policyVersion), or NotFound.
func (s *ValidationStore) Latest(ctx context.Context, scope tenants.Scope,
	tenantID uuid.UUID, digest validation.Digest,
	policyVersion int64) (validation.ScriptValidation, error) {
	var sv validation.ScriptValidation
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		sv, err = scanSV(tx.QueryRow(ctx, `
			SELECT `+svCols+` FROM script_validations
			WHERE tenant_id=$1 AND script_digest=$2 AND policy_version=$3
			ORDER BY validated_at DESC, id DESC LIMIT 1`,
			tenantID, digest[:], policyVersion))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return validation.ScriptValidation{}, errNotFound
	}
	return sv, err
}

// ListByWorkflowVersion returns validations for one workflow version,
// newest first.
func (s *ValidationStore) ListByWorkflowVersion(ctx context.Context,
	scope tenants.Scope, tenantID, wfvID uuid.UUID) ([]validation.ScriptValidation, error) {
	var out []validation.ScriptValidation
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT `+svCols+` FROM script_validations
			WHERE tenant_id=$1 AND workflow_version_id=$2
			ORDER BY validated_at DESC, id DESC`, tenantID, wfvID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sv, err := scanSV(rows)
			if err != nil {
				return err
			}
			out = append(out, sv)
		}
		return rows.Err()
	})
	return out, err
}
