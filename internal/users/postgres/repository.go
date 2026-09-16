// Package postgres implements users.Repository on PostgreSQL. users and
// platform_role_bindings are platform tables (no RLS); all queries run
// under platform scope.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/users"
)

// Repository is the PostgreSQL users.Repository.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository on pool.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

var errNotFound = apperr.New(apperr.NotFound, "NOT_FOUND", "not found")

func scanUser(row pgx.Row) (users.User, error) {
	var u users.User
	var email, name *string
	var kind string
	err := row.Scan(&u.ID, &u.Issuer, &u.Subject, &kind, &email, &name,
		&u.CreatedAt, &u.UpdatedAt, &u.LastSeenAt)
	if err != nil {
		return users.User{}, err
	}
	u.Kind = authn.Kind(kind)
	if email != nil {
		u.Email = *email
	}
	if name != nil {
		u.DisplayName = *name
	}
	return u, nil
}

const userCols = `id, issuer, subject, kind, email, display_name,
	created_at, updated_at, last_seen_at`

// UpsertByIdentity implements users.Repository. Identity is (issuer,
// subject); email/display_name refresh only when changed; last_seen_at
// bumps at most once per 5 minutes.
func (r *Repository) UpsertByIdentity(ctx context.Context, u users.User) (users.User, bool, error) {
	var out users.User
	var created bool
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		id := u.ID
		if id == uuid.Nil {
			id = uuid.Must(uuid.NewV7())
		}
		var email, name *string
		var kind string
		// xmax = 0 on a RETURNING row means INSERT (not conflict-update).
		// A conflicting row needing no changes fires no UPDATE and
		// returns no row — handled by the SELECT below.
		err := tx.QueryRow(ctx, `
			INSERT INTO users (id, issuer, subject, kind, email, display_name)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (issuer, subject) DO UPDATE SET
				email = EXCLUDED.email,
				display_name = EXCLUDED.display_name,
				updated_at = now()
			WHERE users.email IS DISTINCT FROM EXCLUDED.email
			   OR users.display_name IS DISTINCT FROM EXCLUDED.display_name
			RETURNING `+userCols+`, (xmax = 0)`,
			id, u.Issuer, u.Subject, string(u.Kind),
			nilStr(u.Email), nilStr(u.DisplayName)).
			Scan(&out.ID, &out.Issuer, &out.Subject, &kind, &email, &name,
				&out.CreatedAt, &out.UpdatedAt, &out.LastSeenAt, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx,
				`SELECT `+userCols+` FROM users WHERE issuer=$1 AND subject=$2`,
				u.Issuer, u.Subject).
				Scan(&out.ID, &out.Issuer, &out.Subject, &kind, &email, &name,
					&out.CreatedAt, &out.UpdatedAt, &out.LastSeenAt)
		}
		if err != nil {
			return err
		}
		out.Kind = authn.Kind(kind)
		if email != nil {
			out.Email = *email
		}
		if name != nil {
			out.DisplayName = *name
		}
		_, err = tx.Exec(ctx, `
			UPDATE users SET last_seen_at = now()
			WHERE id = $1
			  AND (last_seen_at IS NULL OR last_seen_at < now() - interval '5 minutes')`,
			out.ID)
		return err
	})
	if err != nil {
		return users.User{}, false, db.MapError(err)
	}
	return out, created, nil
}

func nilStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// GetByID implements users.Repository.
func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (users.User, error) {
	var u users.User
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		var err error
		u, err = scanUser(tx.QueryRow(ctx,
			`SELECT `+userCols+` FROM users WHERE id=$1`, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return users.User{}, errNotFound
	}
	return u, err
}

// FindByEmail implements users.Repository (case-insensitive).
func (r *Repository) FindByEmail(ctx context.Context, email string) ([]users.User, error) {
	var out []users.User
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT `+userCols+` FROM users WHERE lower(email) = lower($1)
			 ORDER BY created_at`, email)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				return err
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}

// PlatformRoles implements users.Repository.
func (r *Repository) PlatformRoles(ctx context.Context, userID uuid.UUID) ([]string, error) {
	var out []string
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT role FROM platform_role_bindings
			 WHERE user_id=$1 ORDER BY role`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var role string
			if err := rows.Scan(&role); err != nil {
				return err
			}
			out = append(out, role)
		}
		return rows.Err()
	})
	return out, err
}

// GrantPlatformRole implements users.Repository (idempotent).
func (r *Repository) GrantPlatformRole(ctx context.Context, userID uuid.UUID, role string, by *uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO platform_role_bindings (user_id, role, created_by)
			VALUES ($1,$2,$3)
			ON CONFLICT (user_id, role) DO NOTHING`,
			userID, role, by)
		return db.MapError(err)
	})
}

// RevokePlatformRole implements users.Repository.
func (r *Repository) RevokePlatformRole(ctx context.Context, userID uuid.UUID, role string) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM platform_role_bindings WHERE user_id=$1 AND role=$2`,
			userID, role)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return nil
	})
}

// ListPlatformRoleBindings implements users.Repository.
func (r *Repository) ListPlatformRoleBindings(ctx context.Context) ([]users.PlatformRoleBinding, error) {
	var out []users.PlatformRoleBinding
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT user_id, role, created_at, created_by
			FROM platform_role_bindings ORDER BY user_id, role`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b users.PlatformRoleBinding
			if err := rows.Scan(&b.UserID, &b.Role, &b.CreatedAt, &b.CreatedBy); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

var _ users.Repository = (*Repository)(nil)
