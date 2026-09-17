// Package postgres implements scripts.Store on PostgreSQL.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// Store is the PostgreSQL scripts.Store.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store on pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var errNotFound = apperr.New(apperr.NotFound, "NOT_FOUND", "not found")

func applyScope(ctx context.Context, tx pgx.Tx, s tenants.Scope) error {
	if s.IsPlatform() {
		return db.SetPlatformScope(ctx, tx)
	}
	id, _ := s.TenantID()
	return db.SetTenant(ctx, tx, id)
}

func tid(s tenants.Scope, tenantID uuid.UUID) uuid.UUID {
	if id, ok := s.TenantID(); ok {
		return id
	}
	return tenantID
}

// Put implements scripts.Store. Limit violations are returned as the
// validation diagnostics' first error via apperr.Validation.
func (s *Store) Put(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID,
	lang workflowspec.Language, body []byte,
	createdBy uuid.UUID) (validation.Digest, error) {
	if diags := validation.CheckLimits(body, validation.DefaultLimits); len(diags) > 0 {
		d := diags[0]
		return validation.Digest{}, apperr.New(apperr.Validation, "SCRIPT_LIMITS",
			d.Code+": "+d.Message)
	}
	digest := validation.DigestOf(body)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var cb *uuid.UUID
		if createdBy != uuid.Nil {
			cb = &createdBy
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO scripts (tenant_id, sha256, language, size, body, created_by)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (tenant_id, sha256) DO NOTHING`,
			tid(scope, tenantID), digest[:], string(lang), len(body),
			body, cb)
		return db.MapError(err)
	})
	return digest, err
}

// Get implements scripts.Store; the body's sha256 is re-verified — a
// DB-side tamper yields scripts.integrity, never bytes.
func (s *Store) Get(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID,
	digest validation.Digest) ([]byte, error) {
	var body []byte
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT body FROM scripts WHERE tenant_id=$1 AND sha256=$2`,
			tid(scope, tenantID), digest[:]).Scan(&body)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if validation.DigestOf(body) != digest {
		return nil, apperr.New(apperr.Internal, "scripts.integrity",
			"stored script fails digest re-verification")
	}
	return body, nil
}
