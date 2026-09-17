// Package postgres implements clusters.Repository.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/tenants"
)

const clusterCols = `id, name, display_name, base_url, api_version,
 ca_bundle_pem, identity_mode, service_user, token_ref, client_cert_ref,
 visibility, state, consecutive_failures, consecutive_successes,
 last_sync_at, last_error, capabilities, capabilities_at,
 version, created_at, updated_at`

// clusterColsQualified is clusterCols with the c. table alias, for joins.
var clusterColsQualified = "c." + strings.ReplaceAll(
	strings.ReplaceAll(clusterCols, "\n", ""), ", ", ", c.")

// Repository implements clusters.Repository.
type Repository struct{ pool *pgxpool.Pool }

// New returns a Repository on pool.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func applyScope(ctx context.Context, tx pgx.Tx, s tenants.Scope) error {
	if s.IsPlatform() {
		return db.SetPlatformScope(ctx, tx)
	}
	id, _ := s.TenantID()
	return db.SetTenant(ctx, tx, id)
}

func scanCluster(row pgx.Row) (clusters.Cluster, error) {
	var c clusters.Cluster
	var caBundle, lastErr *string
	var tokenRef, certRef []byte
	var capJSON []byte
	err := row.Scan(&c.ID, &c.Name, &c.DisplayName, &c.BaseURL,
		&c.APIVersion, &caBundle, &c.IdentityMode, &c.ServiceUser,
		&tokenRef, &certRef, &c.Visibility, &c.State,
		&c.ConsecFailures, &c.ConsecSuccesses,
		&c.LastSyncAt, &lastErr, &capJSON, &c.CapabilitiesAt,
		&c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, db.MapError(err)
	}
	if caBundle != nil {
		c.CABundlePEM = *caBundle
	}
	if lastErr != nil {
		c.LastError = *lastErr
	}
	if err := json.Unmarshal(tokenRef, &c.TokenRef); err != nil {
		return c, err
	}
	if certRef != nil {
		var r secrets.Reference
		if err := json.Unmarshal(certRef, &r); err != nil {
			return c, err
		}
		c.ClientCertRef = &r
	}
	if capJSON != nil {
		var caps slurm.Capabilities
		if err := json.Unmarshal(capJSON, &caps); err == nil {
			c.Capabilities = &caps
		}
	}
	return c, nil
}

func (r *Repository) get(ctx context.Context, q string, args ...any) (clusters.Cluster, error) {
	var c clusters.Cluster
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		var err error
		c, err = scanCluster(tx.QueryRow(ctx, q, args...))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.New(apperr.NotFound, "NOT_FOUND", "cluster not found")
		}
		return err
	})
	return c, err
}

// Create implements clusters.Repository.
func (r *Repository) Create(ctx context.Context, c clusters.Cluster) error {
	tok, _ := json.Marshal(c.TokenRef)
	var cert []byte
	if c.ClientCertRef != nil {
		cert, _ = json.Marshal(*c.ClientCertRef)
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO clusters (`+clusterColsNoTS+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
			c.ID, c.Name, c.DisplayName, c.BaseURL, c.APIVersion,
			nullStr(c.CABundlePEM), c.IdentityMode, c.ServiceUser,
			tok, cert, c.Visibility, c.State,
			c.ConsecFailures, c.ConsecSuccesses,
			c.LastSyncAt, nullStr(c.LastError), nil, nil,
			c.Version)
		return db.MapError(err)
	})
}

const clusterColsNoTS = `id, name, display_name, base_url, api_version,
 ca_bundle_pem, identity_mode, service_user, token_ref, client_cert_ref,
 visibility, state, consecutive_failures, consecutive_successes,
 last_sync_at, last_error, capabilities, capabilities_at, version`

// GetByNameOrID implements clusters.Repository.
func (r *Repository) GetByNameOrID(ctx context.Context, ref string) (clusters.Cluster, error) {
	var id uuid.UUID
	if u, err := uuid.Parse(ref); err == nil {
		id = u
		return r.get(ctx, `SELECT `+clusterCols+` FROM clusters WHERE id=$1`, id)
	}
	return r.get(ctx, `SELECT `+clusterCols+` FROM clusters WHERE name=$1`, ref)
}

// List implements clusters.Repository (keyset on created_at,id).
func (r *Repository) List(ctx context.Context, page clusters.Page) ([]clusters.Cluster, string, error) {
	page = page.Normalize(50, 200)
	var out []clusters.Cluster
	var next string
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		q := `SELECT ` + clusterCols + ` FROM clusters`
		args := []any{}
		if page.Cursor != "" {
			k, err := db.DecodeCursor(page.Cursor)
			if err != nil {
				return err
			}
			q += ` WHERE (created_at, id) > ($1, $2)`
			args = append(args, k.CreatedAt, k.ID)
		}
		q += ` ORDER BY created_at, id LIMIT ` + itoa(page.Limit+1)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCluster(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	if len(out) > page.Limit {
		last := out[page.Limit-1]
		next = db.EncodeCursor(db.Keyset{CreatedAt: last.CreatedAt, ID: last.ID})
		out = out[:page.Limit]
	}
	return out, next, nil
}

// Update implements clusters.Repository (optimistic version check).
func (r *Repository) Update(ctx context.Context, c clusters.Cluster) error {
	tok, _ := json.Marshal(c.TokenRef)
	var cert []byte
	if c.ClientCertRef != nil {
		cert, _ = json.Marshal(*c.ClientCertRef)
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE clusters SET
			 display_name=$3, base_url=$4, api_version=$5, ca_bundle_pem=$6,
			 identity_mode=$7, service_user=$8, token_ref=$9,
			 client_cert_ref=$10, visibility=$11,
			 version=version+1, updated_at=now()
			WHERE id=$1 AND version=$2`,
			c.ID, c.Version, c.DisplayName, c.BaseURL, c.APIVersion,
			nullStr(c.CABundlePEM), c.IdentityMode, c.ServiceUser,
			tok, cert, c.Visibility)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.Conflict, "VERSION_CONFLICT",
				"cluster version conflict")
		}
		return nil
	})
}

// SetState implements clusters.Repository.
func (r *Repository) SetState(ctx context.Context, id uuid.UUID,
	s clusters.State) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE clusters SET state=$2, updated_at=now() WHERE id=$1`,
			id, s)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.NotFound, "NOT_FOUND", "cluster not found")
		}
		return nil
	})
}

// tokenScrub removes anything resembling an X-SLURM-USER-TOKEN value from
// error text before it is persisted (defense in depth — adapters never
// include it).
var tokenScrub = regexp.MustCompile(
	`(?i)(x-slurm-user-token|authorization|token|jwt)[=:]\s*\S+`)

func scrubErr(s string) string {
	const maxLen = 1024
	s = tokenScrub.ReplaceAllString(s, "$1=[REDACTED]")
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
}

// RecordSyncResult implements clusters.Repository. One transaction:
// hysteresis UPDATE, capabilities snapshot, partition replace.
func (r *Repository) RecordSyncResult(ctx context.Context, id uuid.UUID,
	res clusters.SyncResult) (clusters.State, error) {
	var capJSON []byte
	if res.Capabilities != nil {
		capJSON, _ = json.Marshal(res.Capabilities)
	}
	var state clusters.State
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			UPDATE clusters SET
			 consecutive_failures = CASE WHEN $2 THEN 0
			      ELSE consecutive_failures + 1 END,
			 consecutive_successes = CASE WHEN $2 THEN
			      consecutive_successes + 1 ELSE 0 END,
			 state = CASE WHEN state='disabled' THEN 'disabled'
			      WHEN $2 AND ((consecutive_successes + 1) >= 2
			           OR state = 'active') THEN 'active'
			      WHEN $2 THEN 'degraded'
			      WHEN (consecutive_failures + 1) >= 3 THEN 'unreachable'
			      ELSE 'degraded' END,
			 last_sync_at = $3,
			 last_error = CASE WHEN $2 THEN NULL ELSE $4 END,
			 capabilities = COALESCE($5, capabilities),
			 capabilities_at = CASE WHEN $5 IS NULL THEN capabilities_at
			      ELSE $3 END,
			 updated_at = now()
			WHERE id = $1
			RETURNING state`,
			id, res.OK, res.At, nullStr(scrubErr(res.Err)), capJSON).
			Scan(&state)
		if err != nil {
			return db.MapError(err)
		}
		if res.OK && res.Partitions != nil {
			if _, err := tx.Exec(ctx,
				`DELETE FROM cluster_partitions WHERE cluster_id=$1`, id); err != nil {
				return db.MapError(err)
			}
			for _, p := range res.Partitions {
				attrs, _ := json.Marshal(p)
				if _, err := tx.Exec(ctx, `
					INSERT INTO cluster_partitions (cluster_id, name, attributes, synced_at)
					VALUES ($1,$2,$3,$4)`, id, p.Name, attrs, res.At); err != nil {
					return db.MapError(err)
				}
			}
		}
		return nil
	})
	return state, err
}

// --- assignments ---------------------------------------------------------

func scanAssignment(row pgx.Row) (clusters.Assignment, error) {
	var a clusters.Assignment
	var def []byte
	err := row.Scan(&a.ClusterID, &a.TenantID, &a.Source, &def,
		&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return a, db.MapError(err)
	}
	if len(def) > 0 {
		_ = json.Unmarshal(def, &a.Defaults)
	}
	return a, nil
}

// GetAssignment implements clusters.Repository.
func (r *Repository) GetAssignment(ctx context.Context, scope tenants.Scope,
	clusterID, tenantID uuid.UUID) (clusters.Assignment, error) {
	var a clusters.Assignment
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		a, err = scanAssignment(tx.QueryRow(ctx, `
			SELECT cluster_id, tenant_id, source, defaults, created_at, updated_at
			FROM cluster_tenant_assignments
			WHERE cluster_id=$1 AND tenant_id=$2`, clusterID, tenantID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return clusters.Assignment{}, apperr.New(apperr.NotFound, "NOT_FOUND", "assignment not found")
	}
	return a, err
}

// UpsertAssignment implements clusters.Repository.
func (r *Repository) UpsertAssignment(ctx context.Context, scope tenants.Scope,
	a clusters.Assignment) error {
	def, _ := json.Marshal(a.Defaults)
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO cluster_tenant_assignments
			 (cluster_id, tenant_id, source, defaults)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (cluster_id, tenant_id) DO UPDATE
			  SET defaults=EXCLUDED.defaults, updated_at=now()`,
			a.ClusterID, a.TenantID, a.Source, def)
		return db.MapError(err)
	})
}

// DeleteAssignment implements clusters.Repository.
func (r *Repository) DeleteAssignment(ctx context.Context, scope tenants.Scope,
	clusterID, tenantID uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			DELETE FROM cluster_tenant_assignments
			WHERE cluster_id=$1 AND tenant_id=$2`, clusterID, tenantID)
		return db.MapError(err)
	})
}

// ListAssignments implements clusters.Repository.
func (r *Repository) ListAssignments(ctx context.Context, scope tenants.Scope,
	clusterID uuid.UUID, page clusters.Page) ([]clusters.Assignment, string, error) {
	page = page.Normalize(50, 200)
	var out []clusters.Assignment
	var next string
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		q := `SELECT cluster_id, tenant_id, source, defaults, created_at, updated_at
		      FROM cluster_tenant_assignments WHERE cluster_id=$1`
		args := []any{clusterID}
		if page.Cursor != "" {
			k, err := db.DecodeCursor(page.Cursor)
			if err != nil {
				return err
			}
			q += ` AND (created_at, tenant_id) > ($2, $3)`
			args = append(args, k.CreatedAt, k.ID)
		}
		q += ` ORDER BY created_at, tenant_id LIMIT ` + itoa(page.Limit+1)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAssignment(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	if len(out) > page.Limit {
		last := out[page.Limit-1]
		next = db.EncodeCursor(db.Keyset{CreatedAt: last.CreatedAt, ID: last.TenantID})
		out = out[:page.Limit]
	}
	return out, next, nil
}

// ListVisibleForTenant implements clusters.Repository.
func (r *Repository) ListVisibleForTenant(ctx context.Context, scope tenants.Scope,
	tenantID uuid.UUID) ([]clusters.Cluster, map[uuid.UUID]clusters.Assignment, error) {
	var out []clusters.Cluster
	m := map[uuid.UUID]clusters.Assignment{}
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT `+clusterColsQualified+`, a.source, a.defaults
			FROM clusters c
			JOIN cluster_tenant_assignments a
			  ON a.cluster_id = c.id AND a.tenant_id = $1
			ORDER BY c.name`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c clusters.Cluster
			var caBundle, lastErr *string
			var tokenRef, certRef, capJSON, def []byte
			var src string
			var a clusters.Assignment
			if err := rows.Scan(&c.ID, &c.Name, &c.DisplayName, &c.BaseURL,
				&c.APIVersion, &caBundle, &c.IdentityMode, &c.ServiceUser,
				&tokenRef, &certRef, &c.Visibility, &c.State,
				&c.ConsecFailures, &c.ConsecSuccesses,
				&c.LastSyncAt, &lastErr, &capJSON, &c.CapabilitiesAt,
				&c.Version, &c.CreatedAt, &c.UpdatedAt,
				&src, &def); err != nil {
				return err
			}
			if caBundle != nil {
				c.CABundlePEM = *caBundle
			}
			if lastErr != nil {
				c.LastError = *lastErr
			}
			_ = json.Unmarshal(tokenRef, &c.TokenRef)
			if certRef != nil {
				var rr secrets.Reference
				if err := json.Unmarshal(certRef, &rr); err == nil {
					c.ClientCertRef = &rr
				}
			}
			if capJSON != nil {
				var cc slurm.Capabilities
				if err := json.Unmarshal(capJSON, &cc); err == nil {
					c.Capabilities = &cc
				}
			}
			a.ClusterID, a.TenantID, a.Source = c.ID, tenantID, src
			_ = json.Unmarshal(def, &a.Defaults)
			m[c.ID] = a
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, m, err
}

// AutoAssignTenants implements clusters.Repository: returns the tenant
// ids that need an auto row (active/suspended tenants lacking one) and
// the auto rows to drop (tenants no longer active/suspended).
func (r *Repository) AutoAssignTenants(ctx context.Context, clusterID uuid.UUID) (
	assign, drop []uuid.UUID, err error) {
	err = db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT t.id FROM tenants t
			WHERE t.state IN ('active','suspended')
			  AND NOT EXISTS (SELECT 1 FROM cluster_tenant_assignments a
			    WHERE a.cluster_id=$1 AND a.tenant_id=t.id)`, clusterID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			assign = append(assign, id)
		}
		rows.Close()
		rows2, err := tx.Query(ctx, `
			SELECT a.tenant_id FROM cluster_tenant_assignments a
			JOIN tenants t ON t.id = a.tenant_id
			WHERE a.cluster_id=$1 AND a.source='auto'
			  AND t.state NOT IN ('active','suspended')`, clusterID)
		if err != nil {
			return err
		}
		for rows2.Next() {
			var id uuid.UUID
			if err := rows2.Scan(&id); err != nil {
				rows2.Close()
				return err
			}
			drop = append(drop, id)
		}
		rows2.Close()
		return rows2.Err()
	})
	return assign, drop, err
}

// ListPartitions implements clusters.Repository.
func (r *Repository) ListPartitions(ctx context.Context, clusterID uuid.UUID) (
	[]clusters.PartitionRecord, error) {
	var out []clusters.PartitionRecord
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT cluster_id, name, attributes, synced_at
			FROM cluster_partitions WHERE cluster_id=$1 ORDER BY name`, clusterID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p clusters.PartitionRecord
			var attrs []byte
			if err := rows.Scan(&p.ClusterID, &p.Name, &attrs, &p.SyncedAt); err != nil {
				return err
			}
			_ = json.Unmarshal(attrs, &p.Attributes)
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// ListAll implements clusters.Repository (sync bootstrap).
func (r *Repository) ListAll(ctx context.Context) ([]clusters.Cluster, error) {
	all, _, err := r.List(ctx, clusters.Page{Limit: 10000})
	return all, err
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
