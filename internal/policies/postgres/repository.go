// Package postgres implements policies.Repository on PostgreSQL.
package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/policies"
	"github.com/Exonical/custos/internal/tenants"
)

// Repository is the PostgreSQL policies.Repository.
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

// Upsert implements policies.Repository (insert or replace by scope key).
func (r *Repository) Upsert(ctx context.Context, scope tenants.Scope, p policies.Policy) error {
	body, err := json.Marshal(p.Policy)
	if err != nil {
		return err
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tid := p.TenantID
		if id, ok := scope.TenantID(); ok {
			tid = id
		}
		var projectID any
		if p.ProjectID != nil {
			projectID = *p.ProjectID
		}
		var conflict string
		switch p.Scope {
		case policies.ScopeTenant:
			conflict = "(tenant_id) WHERE scope='tenant'"
		case policies.ScopeProject:
			conflict = "(project_id) WHERE scope='project'"
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO resource_policies (id, tenant_id, scope, project_id, policy)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT `+conflict+` DO UPDATE SET policy=$5, updated_at=now(),
				version=resource_policies.version+1`,
			p.ID, tid, string(p.Scope), projectID, body)
		return db.MapError(err)
	})
}

func (r *Repository) get(ctx context.Context, scope tenants.Scope,
	where string, arg uuid.UUID) (policies.Policy, error) {
	var p policies.Policy
	var body []byte
	var scopeStr string
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT id, tenant_id, scope, project_id, policy, version, created_at, updated_at
			FROM resource_policies WHERE `+where, arg).
			Scan(&p.ID, &p.TenantID, &scopeStr, &p.ProjectID, &body,
				&p.Version, &p.CreatedAt, &p.UpdatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return policies.Policy{}, errNotFound
	}
	if err != nil {
		return policies.Policy{}, err
	}
	p.Scope = policies.Scope(scopeStr)
	if err := json.Unmarshal(body, &p.Policy); err != nil {
		return policies.Policy{}, err
	}
	return p, nil
}

// GetTenant implements policies.Repository.
func (r *Repository) GetTenant(ctx context.Context, scope tenants.Scope,
	tenantID uuid.UUID) (policies.Policy, error) {
	tid := tenantID
	if id, ok := scope.TenantID(); ok {
		tid = id
	}
	return r.get(ctx, scope, `tenant_id=$1 AND scope='tenant'`, tid)
}

// GetProject implements policies.Repository.
func (r *Repository) GetProject(ctx context.Context, scope tenants.Scope,
	projectID uuid.UUID) (policies.Policy, error) {
	return r.get(ctx, scope, `project_id=$1 AND scope='project'`, projectID)
}
