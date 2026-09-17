// Package postgres implements the projects repositories on PostgreSQL.
// Every method opens a transaction, applies the RLS scope and carries
// explicit WHERE predicates — RLS is defense in depth.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/tenants"
)

// Repository implements projects.Repository, MembershipRepository and
// BindingRepository on pool.
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

func itoa(i int) string { return strconv.Itoa(i) }

// strs normalizes a nil slice to the empty array the NOT NULL columns
// require.
func strs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// tid returns the tenant id a tenant scope constrains to; platform
// scope takes the explicit tenantID argument instead.
func tid(s tenants.Scope, tenantID uuid.UUID) uuid.UUID {
	if id, ok := s.TenantID(); ok {
		return id
	}
	return tenantID
}

const projectCols = `id, tenant_id, slug, name, description, state,
	settings, version, created_at, updated_at`

func scanProject(row pgx.Row) (projects.Project, error) {
	var p projects.Project
	var settings []byte
	err := row.Scan(&p.ID, &p.TenantID, &p.Slug, &p.Name, &p.Description,
		&p.State, &settings, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return projects.Project{}, err
	}
	p.Settings = map[string]any{}
	if len(settings) > 0 {
		if err := json.Unmarshal(settings, &p.Settings); err != nil {
			return projects.Project{}, err
		}
	}
	return p, nil
}

// Create implements projects.Repository.
func (r *Repository) Create(ctx context.Context, scope tenants.Scope, p projects.Project) error {
	settings, err := json.Marshal(p.Settings)
	if err != nil {
		return err
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO projects (id, tenant_id, slug, name, description, state, settings)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			p.ID, tid(scope, p.TenantID), p.Slug, p.Name, p.Description,
			string(p.State), settings)
		return db.MapError(err)
	})
}

// GetBySlugOrID implements projects.Repository; ref is a slug or uuid
// inside the tenant.
func (r *Repository) GetBySlugOrID(ctx context.Context, scope tenants.Scope,
	tenantID uuid.UUID, ref string) (projects.Project, error) {
	var p projects.Project
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		p, err = scanProject(tx.QueryRow(ctx,
			`SELECT `+projectCols+` FROM projects
			 WHERE tenant_id = $1 AND (slug = $2 OR id::text = $2)`,
			tid(scope, tenantID), ref))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return projects.Project{}, errNotFound
	}
	return p, err
}

// List implements projects.Repository: keyset on (created_at, id).
func (r *Repository) List(ctx context.Context, scope tenants.Scope,
	tenantID uuid.UUID, page tenants.Page) ([]projects.Project, string, error) {
	page = page.Normalize(50, 200)
	var ks *db.Keyset
	if page.Cursor != "" {
		k, err := db.DecodeCursor(page.Cursor)
		if err != nil {
			return nil, "", err
		}
		ks = &k
	}
	var out []projects.Project
	var next string
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		where := " WHERE tenant_id = $1"
		args := []any{tid(scope, tenantID)}
		if ks != nil {
			where += " AND (created_at, id) > ($2, $3)"
			args = append(args, ks.CreatedAt, ks.ID)
		}
		rows, err := tx.Query(ctx,
			`SELECT `+projectCols+` FROM projects`+where+
				` ORDER BY created_at, id LIMIT `+itoa(page.Limit+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProject(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
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

// ListForUser implements projects.Repository.
func (r *Repository) ListForUser(ctx context.Context, scope tenants.Scope,
	tenantID, userID uuid.UUID) ([]projects.Project, error) {
	var out []projects.Project
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT `+projectCols+` FROM projects p
			 WHERE p.tenant_id = $1
			   AND EXISTS (SELECT 1 FROM project_memberships m
			               WHERE m.project_id = p.id AND m.user_id = $2)
			 ORDER BY p.created_at, p.id`, tid(scope, tenantID), userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProject(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// Update implements projects.Repository (optimistic concurrency).
func (r *Repository) Update(ctx context.Context, scope tenants.Scope, p projects.Project) error {
	settings, err := json.Marshal(p.Settings)
	if err != nil {
		return err
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE projects SET name=$3, description=$4, state=$5, settings=$6,
			       updated_at=now(), version=version+1
			WHERE id=$1 AND tenant_id=$2 AND version=$7`,
			p.ID, tid(scope, p.TenantID), p.Name, p.Description,
			string(p.State), settings, p.Version)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM projects WHERE id=$1 AND tenant_id=$2)`,
			p.ID, tid(scope, p.TenantID)).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errNotFound
		}
		return apperr.New(apperr.Conflict, "VERSION_CONFLICT", "version mismatch")
	})
}

const membershipCols = `tenant_id, project_id, user_id, roles, source,
	created_at, updated_at`

func scanMembership(row pgx.Row) (projects.Membership, error) {
	var m projects.Membership
	err := row.Scan(&m.TenantID, &m.ProjectID, &m.UserID, &m.Roles,
		&m.Source, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

// GetMembership implements projects.MembershipRepository.
func (r *Repository) GetMembership(ctx context.Context, scope tenants.Scope,
	projectID, userID uuid.UUID) (projects.Membership, error) {
	var m projects.Membership
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		m, err = scanMembership(tx.QueryRow(ctx,
			`SELECT `+membershipCols+` FROM project_memberships
			 WHERE project_id = $1 AND user_id = $2`, projectID, userID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return projects.Membership{}, errNotFound
	}
	return m, err
}

// ListMemberships implements projects.MembershipRepository (keyset on
// (created_at, user_id)).
func (r *Repository) ListMemberships(ctx context.Context, scope tenants.Scope,
	projectID uuid.UUID, page tenants.Page) ([]projects.Membership, string, error) {
	page = page.Normalize(50, 200)
	var ks *db.Keyset
	if page.Cursor != "" {
		k, err := db.DecodeCursor(page.Cursor)
		if err != nil {
			return nil, "", err
		}
		ks = &k
	}
	var out []projects.Membership
	var next string
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		where := " WHERE project_id = $1"
		args := []any{projectID}
		if ks != nil {
			where += " AND (created_at, user_id) > ($2, $3)"
			args = append(args, ks.CreatedAt, ks.ID)
		}
		rows, err := tx.Query(ctx,
			`SELECT `+membershipCols+` FROM project_memberships`+where+
				` ORDER BY created_at, user_id LIMIT `+itoa(page.Limit+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMembership(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	if len(out) > page.Limit {
		last := out[page.Limit-1]
		next = db.EncodeCursor(db.Keyset{CreatedAt: last.CreatedAt, ID: last.UserID})
		out = out[:page.Limit]
	}
	return out, next, nil
}

// UpsertMembership implements projects.MembershipRepository.
func (r *Repository) UpsertMembership(ctx context.Context, scope tenants.Scope,
	m projects.Membership) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO project_memberships (tenant_id, project_id, user_id, roles, source)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (project_id, user_id)
			DO UPDATE SET roles=$4, source=$5, updated_at=now()`,
			tid(scope, m.TenantID), m.ProjectID, m.UserID, m.Roles, m.Source)
		return db.MapError(err)
	})
}

// DeleteMembership implements projects.MembershipRepository.
func (r *Repository) DeleteMembership(ctx context.Context, scope tenants.Scope,
	projectID, userID uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM project_memberships WHERE project_id=$1 AND user_id=$2`,
			projectID, userID)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return nil
	})
}

// CountProjectAdmins implements projects.MembershipRepository.
func (r *Repository) CountProjectAdmins(ctx context.Context, scope tenants.Scope,
	projectID uuid.UUID) (int, error) {
	var n int
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM project_memberships
			WHERE project_id=$1 AND 'project-admin' = ANY(roles)`,
			projectID).Scan(&n)
	})
	return n, err
}

// ListMembershipsForUser implements projects.MembershipRepository.
func (r *Repository) ListMembershipsForUser(ctx context.Context, scope tenants.Scope,
	tenantID, userID uuid.UUID) ([]projects.Membership, error) {
	var out []projects.Membership
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT `+membershipCols+` FROM project_memberships
			 WHERE tenant_id = $1 AND user_id = $2
			 ORDER BY created_at, project_id`, tid(scope, tenantID), userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMembership(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

const bindingCols = `id, tenant_id, project_id, cluster_id, slurm_account,
	default_partition, allowed_partitions, default_qos, allowed_qos,
	enabled, version, created_at, updated_at`

func scanBinding(row pgx.Row) (projects.ClusterBinding, error) {
	var b projects.ClusterBinding
	err := row.Scan(&b.ID, &b.TenantID, &b.ProjectID, &b.ClusterID,
		&b.SlurmAccount, &b.DefaultPartition, &b.AllowedPartitions,
		&b.DefaultQoS, &b.AllowedQoS, &b.Enabled, &b.Version,
		&b.CreatedAt, &b.UpdatedAt)
	return b, err
}

// CreateBinding implements projects.BindingRepository.
func (r *Repository) CreateBinding(ctx context.Context, scope tenants.Scope,
	b projects.ClusterBinding) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO project_cluster_bindings
			  (id, tenant_id, project_id, cluster_id, slurm_account,
			   default_partition, allowed_partitions, default_qos, allowed_qos, enabled)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			b.ID, tid(scope, b.TenantID), b.ProjectID, b.ClusterID,
			b.SlurmAccount, b.DefaultPartition, strs(b.AllowedPartitions),
			b.DefaultQoS, strs(b.AllowedQoS), b.Enabled)
		return db.MapError(err)
	})
}

// GetBinding implements projects.BindingRepository.
func (r *Repository) GetBinding(ctx context.Context, scope tenants.Scope,
	projectID, bindingID uuid.UUID) (projects.ClusterBinding, error) {
	var b projects.ClusterBinding
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		b, err = scanBinding(tx.QueryRow(ctx,
			`SELECT `+bindingCols+` FROM project_cluster_bindings
			 WHERE project_id=$1 AND id=$2`, projectID, bindingID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return projects.ClusterBinding{}, errNotFound
	}
	return b, err
}

// GetBindingByCluster implements projects.BindingRepository.
func (r *Repository) GetBindingByCluster(ctx context.Context, scope tenants.Scope,
	projectID, clusterID uuid.UUID) (projects.ClusterBinding, error) {
	var b projects.ClusterBinding
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		b, err = scanBinding(tx.QueryRow(ctx,
			`SELECT `+bindingCols+` FROM project_cluster_bindings
			 WHERE project_id=$1 AND cluster_id=$2`, projectID, clusterID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return projects.ClusterBinding{}, errNotFound
	}
	return b, err
}

// ListBindings implements projects.BindingRepository.
func (r *Repository) ListBindings(ctx context.Context, scope tenants.Scope,
	projectID uuid.UUID) ([]projects.ClusterBinding, error) {
	var out []projects.ClusterBinding
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT `+bindingCols+` FROM project_cluster_bindings
			 WHERE project_id=$1 ORDER BY created_at, id`, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBinding(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateBinding implements projects.BindingRepository (optimistic
// concurrency).
func (r *Repository) UpdateBinding(ctx context.Context, scope tenants.Scope,
	b projects.ClusterBinding) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE project_cluster_bindings SET slurm_account=$3,
			       default_partition=$4, allowed_partitions=$5,
			       default_qos=$6, allowed_qos=$7, enabled=$8,
			       updated_at=now(), version=version+1
			WHERE id=$1 AND project_id=$2 AND version=$9`,
			b.ID, b.ProjectID, b.SlurmAccount, b.DefaultPartition,
			strs(b.AllowedPartitions), b.DefaultQoS, strs(b.AllowedQoS),
			b.Enabled, b.Version)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM project_cluster_bindings WHERE id=$1)`,
			b.ID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errNotFound
		}
		return apperr.New(apperr.Conflict, "VERSION_CONFLICT", "version mismatch")
	})
}

// DeleteBinding implements projects.BindingRepository.
func (r *Repository) DeleteBinding(ctx context.Context, scope tenants.Scope,
	projectID, bindingID uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM project_cluster_bindings WHERE project_id=$1 AND id=$2`,
			projectID, bindingID)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return nil
	})
}

// ListAllForUser implements projects.MembershipRepository (platform
// scope; /me).
func (r *Repository) ListAllForUser(ctx context.Context, userID uuid.UUID) ([]projects.MembershipRef, error) {
	var out []projects.MembershipRef
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, tenants.PlatformScope()); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT m.project_id, p.slug, m.tenant_id, m.roles
			FROM project_memberships m
			JOIN projects p ON p.id = m.project_id
			WHERE m.user_id = $1
			ORDER BY m.created_at, m.project_id`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m projects.MembershipRef
			if err := rows.Scan(&m.ProjectID, &m.ProjectSlug, &m.TenantID, &m.Roles); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}
