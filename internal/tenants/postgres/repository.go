// Package postgres implements tenants.Repository on PostgreSQL. Every
// method opens a transaction, applies the RLS scope (app.tenant_id), and
// carries explicit WHERE predicates — RLS is defense in depth.
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
	"github.com/Exonical/custos/internal/tenants"
)

// Repository is the PostgreSQL tenants.Repository.
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

// tenantFilter returns the extra predicate fragment and args scoping a
// tenant query to s. args continues numbering from nextArg.
func tenantFilter(s tenants.Scope, nextArg int) (string, []any) {
	if s.IsPlatform() {
		return "", nil
	}
	id, _ := s.TenantID()
	return " AND id = $" + itoa(nextArg), []any{id}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

func scanTenant(row pgx.Row) (tenants.Tenant, error) {
	var t tenants.Tenant
	var settings []byte
	var ns *string
	err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.State, &settings, &ns,
		&t.Version, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return tenants.Tenant{}, err
	}
	t.Settings = map[string]any{}
	if len(settings) > 0 {
		if err := json.Unmarshal(settings, &t.Settings); err != nil {
			return tenants.Tenant{}, err
		}
	}
	if ns != nil {
		t.OpenBaoNamespace = *ns
	}
	return t, nil
}

const tenantCols = `id, slug, name, state, settings, openbao_namespace,
	version, created_at, updated_at`

// Create implements tenants.Repository.
func (r *Repository) Create(ctx context.Context, t tenants.Tenant) error {
	settings, err := json.Marshal(t.Settings)
	if err != nil {
		return err
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, tenants.PlatformScope()); err != nil {
			return err
		}
		var ns *string
		if t.OpenBaoNamespace != "" {
			ns = &t.OpenBaoNamespace
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO tenants (id, slug, name, state, settings, openbao_namespace)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			t.ID, t.Slug, t.Name, string(t.State), settings, ns)
		return db.MapError(err)
	})
}

// GetBySlugOrID implements tenants.Repository. ref is a slug or uuid.
func (r *Repository) GetBySlugOrID(ctx context.Context, scope tenants.Scope, ref string) (tenants.Tenant, error) {
	var t tenants.Tenant
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		filter, args := tenantFilter(scope, 2)
		row := tx.QueryRow(ctx,
			`SELECT `+tenantCols+` FROM tenants
			 WHERE (slug = $1 OR id::text = $1)`+filter,
			append([]any{ref}, args...)...)
		var err error
		t, err = scanTenant(row)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return tenants.Tenant{}, errNotFound
	}
	return t, err
}

// List implements tenants.Repository: keyset on (created_at, id).
func (r *Repository) List(ctx context.Context, scope tenants.Scope, page tenants.Page) ([]tenants.Tenant, string, error) {
	page = page.Normalize(50, 200)
	var ks *db.Keyset
	if page.Cursor != "" {
		k, err := db.DecodeCursor(page.Cursor)
		if err != nil {
			return nil, "", err
		}
		ks = &k
	}
	var out []tenants.Tenant
	var next string
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		where := ""
		args := []any{}
		if ks != nil {
			where = " WHERE (created_at, id) > ($1, $2)"
			args = append(args, ks.CreatedAt, ks.ID)
		}
		filter, fargs := tenantFilter(scope, len(args)+1)
		if filter != "" && where == "" {
			where = " WHERE true"
		}
		args = append(args, fargs...)
		rows, err := tx.Query(ctx,
			`SELECT `+tenantCols+` FROM tenants`+where+filter+
				` ORDER BY created_at, id LIMIT `+itoa(page.Limit+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTenant(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
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

// Update implements tenants.Repository (optimistic concurrency).
func (r *Repository) Update(ctx context.Context, scope tenants.Scope, t tenants.Tenant) error {
	settings, err := json.Marshal(t.Settings)
	if err != nil {
		return err
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var ns *string
		if t.OpenBaoNamespace != "" {
			ns = &t.OpenBaoNamespace
		}
		tag, err := tx.Exec(ctx, `
			UPDATE tenants SET name=$2, settings=$3, state=$4,
			       openbao_namespace=$5,
			       updated_at=now(), version=version+1
			WHERE id=$1 AND version=$6`,
			t.ID, t.Name, settings, string(t.State), ns, t.Version)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var exists int
		filter, args := tenantFilter(scope, 2)
		err = tx.QueryRow(ctx,
			`SELECT 1 FROM tenants WHERE id=$1`+filter,
			append([]any{t.ID}, args...)...).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return apperr.New(apperr.Conflict, "VERSION_CONFLICT", "tenant version conflict")
	})
}

func scanMembership(row pgx.Row) (tenants.Membership, error) {
	var m tenants.Membership
	err := row.Scan(&m.TenantID, &m.UserID, &m.Roles, &m.Source,
		&m.CreatedAt, &m.UpdatedAt)
	return m, err
}

const memberCols = `tenant_id, user_id, roles, source, created_at, updated_at`

// GetMembership implements tenants.Repository.
func (r *Repository) GetMembership(ctx context.Context, scope tenants.Scope, tenantID, userID uuid.UUID) (tenants.Membership, error) {
	var m tenants.Membership
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		m, err = scanMembership(tx.QueryRow(ctx,
			`SELECT `+memberCols+` FROM tenant_memberships
			 WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return tenants.Membership{}, errNotFound
	}
	return m, err
}

// ListMemberships implements tenants.Repository: keyset on
// (created_at, user_id).
func (r *Repository) ListMemberships(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID, page tenants.Page) ([]tenants.Membership, string, error) {
	page = page.Normalize(50, 200)
	var ks *db.Keyset
	if page.Cursor != "" {
		k, err := db.DecodeCursor(page.Cursor)
		if err != nil {
			return nil, "", err
		}
		ks = &k
	}
	var out []tenants.Membership
	var next string
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		where := " WHERE tenant_id = $1"
		args := []any{tenantID}
		if ks != nil {
			where += " AND (created_at, user_id) > ($2, $3)"
			args = append(args, ks.CreatedAt, ks.ID)
		}
		rows, err := tx.Query(ctx,
			`SELECT `+memberCols+` FROM tenant_memberships`+where+
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

// UpsertMembership implements tenants.Repository.
func (r *Repository) UpsertMembership(ctx context.Context, scope tenants.Scope, m tenants.Membership) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO tenant_memberships (tenant_id, user_id, roles, source)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (tenant_id, user_id)
			DO UPDATE SET roles=EXCLUDED.roles, source=EXCLUDED.source,
			              updated_at=now()`,
			m.TenantID, m.UserID, m.Roles, string(m.Source))
		return db.MapError(err)
	})
}

// DeleteMembership implements tenants.Repository.
func (r *Repository) DeleteMembership(ctx context.Context, scope tenants.Scope, tenantID, userID uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM tenant_memberships WHERE tenant_id=$1 AND user_id=$2`,
			tenantID, userID)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return nil
	})
}

// ListMembershipsForUser implements tenants.Repository. Reads platform
// scope and joins tenants for the /me projection.
func (r *Repository) ListMembershipsForUser(ctx context.Context, userID uuid.UUID) ([]tenants.Membership, error) {
	var out []tenants.Membership
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, tenants.PlatformScope()); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT m.tenant_id, m.user_id, m.roles, m.source,
			       m.created_at, m.updated_at, t.slug, t.name
			FROM tenant_memberships m JOIN tenants t ON t.id = m.tenant_id
			WHERE m.user_id = $1 ORDER BY m.created_at, m.tenant_id`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m tenants.Membership
			if err := rows.Scan(&m.TenantID, &m.UserID, &m.Roles, &m.Source,
				&m.CreatedAt, &m.UpdatedAt, &m.TenantSlug, &m.TenantName); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// CountTenantAdmins implements tenants.Repository.
func (r *Repository) CountTenantAdmins(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID) (int, error) {
	var n int
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM tenant_memberships
			 WHERE tenant_id=$1 AND 'tenant-admin' = ANY(roles)`,
			tenantID).Scan(&n)
	})
	return n, err
}
