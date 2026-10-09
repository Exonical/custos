// Package postgres implements nodehooks.Repository.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/nodehooks"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
)

// Repository implements nodehooks.Repository.
type Repository struct{ pool *pgxpool.Pool }

var _ nodehooks.Repository = (*Repository)(nil)

// New returns a Repository on pool.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// mapErr maps a missing row to NotFound and defers to db.MapError otherwise.
func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ErrNotFound
	}
	return db.MapError(err)
}

func versionConflict() error {
	return apperr.New(apperr.Conflict, "VERSION_CONFLICT", "node config version conflict")
}

const configCols = `cluster_id, config, revision, content_sha256, version, updated_at, updated_by`

func scanConfig(row pgx.Row) (nodehooks.Stored, error) {
	var s nodehooks.Stored
	var raw []byte
	if err := row.Scan(&s.ClusterID, &raw, &s.Revision, &s.ContentSHA256,
		&s.Version, &s.UpdatedAt, &s.UpdatedBy); err != nil {
		return s, mapErr(err)
	}
	if err := json.Unmarshal(raw, &s.Config); err != nil {
		return s, err
	}
	return s, nil
}

// GetConfig implements nodehooks.Repository.
func (r *Repository) GetConfig(ctx context.Context, clusterID uuid.UUID) (nodehooks.Stored, error) {
	return scanConfig(r.pool.QueryRow(ctx,
		`SELECT `+configCols+` FROM cluster_node_config WHERE cluster_id=$1`, clusterID))
}

// PutConfig implements nodehooks.Repository.
func (r *Repository) PutConfig(ctx context.Context, clusterID uuid.UUID,
	cfg nodehooks.Config, contentSHA256 string, expectedVersion int,
	by *uuid.UUID) (nodehooks.Stored, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nodehooks.Stored{}, err
	}
	var out nodehooks.Stored
	err = db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var row pgx.Row
		if expectedVersion == 0 {
			row = tx.QueryRow(ctx, `
				INSERT INTO cluster_node_config
				 (cluster_id, config, revision, content_sha256, version, updated_by)
				VALUES ($1,$2,1,$3,1,$4)
				ON CONFLICT (cluster_id) DO NOTHING
				RETURNING `+configCols, clusterID, raw, contentSHA256, by)
		} else {
			row = tx.QueryRow(ctx, `
				UPDATE cluster_node_config SET config=$2, revision=revision+1,
				 content_sha256=$3, version=version+1, updated_at=now(), updated_by=$4
				WHERE cluster_id=$1 AND version=$5
				RETURNING `+configCols, clusterID, raw, contentSHA256, by, expectedVersion)
		}
		s, err := scanConfig(row)
		if err != nil {
			if apperr.Is(err, apperr.NotFound) {
				return versionConflict()
			}
			return err
		}
		out = s
		return nil
	})
	return out, err
}

const ntCols = `id, cluster_id, name, created_at, created_by, last_used_at, revoked_at`

func scanToken(row pgx.Row) (nodehooks.Token, error) {
	var t nodehooks.Token
	err := row.Scan(&t.ID, &t.ClusterID, &t.Name, &t.CreatedAt, &t.CreatedBy,
		&t.LastUsedAt, &t.RevokedAt)
	return t, mapErr(err)
}

// CreateToken implements nodehooks.Repository.
func (r *Repository) CreateToken(ctx context.Context, t nodehooks.Token, tokenSHA256 string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO cluster_node_tokens (id, cluster_id, name, token_sha256, created_by)
		VALUES ($1,$2,$3,$4,$5)`, t.ID, t.ClusterID, t.Name, tokenSHA256, t.CreatedBy)
	return db.MapError(err)
}

// ListTokens implements nodehooks.Repository.
func (r *Repository) ListTokens(ctx context.Context, clusterID uuid.UUID) ([]nodehooks.Token, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+ntCols+`
		FROM cluster_node_tokens WHERE cluster_id=$1 ORDER BY created_at, id`, clusterID)
	if err != nil {
		return nil, db.MapError(err)
	}
	defer rows.Close()
	out := []nodehooks.Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, db.MapError(rows.Err())
}

// RevokeToken implements nodehooks.Repository.
func (r *Repository) RevokeToken(ctx context.Context, clusterID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cluster_node_tokens SET revoked_at=COALESCE(revoked_at, now())
		WHERE id=$1 AND cluster_id=$2`, id, clusterID)
	if err != nil {
		return db.MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return db.ErrNotFound
	}
	return nil
}

// LookupToken implements nodehooks.Repository.
func (r *Repository) LookupToken(ctx context.Context, tokenSHA256 string) (nodehooks.Token, error) {
	return scanToken(r.pool.QueryRow(ctx, `SELECT `+ntCols+`
		FROM cluster_node_tokens WHERE token_sha256=$1 AND revoked_at IS NULL`, tokenSHA256))
}

// TouchToken implements nodehooks.Repository.
func (r *Repository) TouchToken(ctx context.Context, id uuid.UUID, minInterval time.Duration) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE cluster_node_tokens SET last_used_at=now()
		WHERE id=$1 AND (last_used_at IS NULL OR last_used_at < now() - $2 * interval '1 second')`,
		id, minInterval.Seconds())
	return db.MapError(err)
}

// UpsertStatus implements nodehooks.Repository.
func (r *Repository) UpsertStatus(ctx context.Context, s nodehooks.NodeStatus, minInterval time.Duration) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO cluster_node_status (cluster_id, node_name, token_id, revision, bundle_sha256, fetched_at)
		VALUES ($1,$2,$3,$4,$5,now())
		ON CONFLICT (cluster_id, node_name) DO UPDATE
		 SET token_id=EXCLUDED.token_id, revision=EXCLUDED.revision,
		     bundle_sha256=EXCLUDED.bundle_sha256, fetched_at=now()
		 WHERE cluster_node_status.revision <> EXCLUDED.revision
		    OR cluster_node_status.bundle_sha256 <> EXCLUDED.bundle_sha256
		    OR cluster_node_status.token_id IS DISTINCT FROM EXCLUDED.token_id
		    OR cluster_node_status.fetched_at < now() - $6 * interval '1 second'`,
		s.ClusterID, s.NodeName, s.TokenID, s.Revision, s.BundleSHA256, minInterval.Seconds())
	return db.MapError(err)
}

// ListStatus implements nodehooks.Repository.
func (r *Repository) ListStatus(ctx context.Context, clusterID uuid.UUID) ([]nodehooks.NodeStatus, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT cluster_id, node_name, token_id, revision, bundle_sha256, fetched_at
		FROM cluster_node_status WHERE cluster_id=$1 ORDER BY node_name`, clusterID)
	if err != nil {
		return nil, db.MapError(err)
	}
	defer rows.Close()
	out := []nodehooks.NodeStatus{}
	for rows.Next() {
		var s nodehooks.NodeStatus
		if err := rows.Scan(&s.ClusterID, &s.NodeName, &s.TokenID, &s.Revision, &s.BundleSHA256, &s.FetchedAt); err != nil {
			return nil, db.MapError(err)
		}
		out = append(out, s)
	}
	return out, db.MapError(rows.Err())
}
