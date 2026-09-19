// Package postgres implements secretrefs.Repository on PostgreSQL.
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
	"github.com/Exonical/custos/internal/secretrefs"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/tenants"
)

// Repository persists tenant connectors and references in PostgreSQL.
type Repository struct{ pool *pgxpool.Pool }

// New returns a secret-reference repository on pool.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func scope(ctx context.Context, tx pgx.Tx, s tenants.Scope) error {
	if s.IsPlatform() {
		return db.SetPlatformScope(ctx, tx)
	}
	id, _ := s.TenantID()
	return db.SetTenant(ctx, tx, id)
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) CreateConnector(ctx context.Context, s tenants.Scope, c secretrefs.Connector) error {
	cfg, err := json.Marshal(c.Config)
	if err != nil {
		return err
	}
	var cred any
	if c.CredentialRef != nil {
		b, e := json.Marshal(c.CredentialRef)
		if e != nil {
			return e
		}
		cred = b
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO secret_connectors
			(id,tenant_id,name,kind,state,config,credential_ref,created_by)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, c.ID, c.TenantID, c.Name, c.Kind, c.State, cfg, cred, c.CreatedBy)
		return db.MapError(err)
	})
}

const connectorCols = `id,tenant_id,name,kind,state,config,credential_ref,
	created_by,created_at,updated_at,version`

func scanConnector(row pgx.Row) (secretrefs.Connector, error) {
	var c secretrefs.Connector
	var cfg []byte
	var cred []byte
	err := row.Scan(&c.ID, &c.TenantID, &c.Name, &c.Kind, &c.State, &cfg, &cred,
		&c.CreatedBy, &c.CreatedAt, &c.UpdatedAt, &c.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, apperr.New(apperr.NotFound, "NOT_FOUND", "not found")
	}
	if err != nil {
		return c, db.MapError(err)
	}
	if err := json.Unmarshal(cfg, &c.Config); err != nil {
		return c, err
	}
	if len(cred) > 0 {
		var ref secrets.Reference
		if err := json.Unmarshal(cred, &ref); err != nil {
			return c, err
		}
		c.CredentialRef = &ref
	}
	return c, nil
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) GetConnector(ctx context.Context, s tenants.Scope, tenantID uuid.UUID, ref string) (secretrefs.Connector, error) {
	return getConnectorTx(ctx, r.pool, s, tenantID, ref)
}
func getConnectorTx(ctx context.Context, pool *pgxpool.Pool, s tenants.Scope, tenantID uuid.UUID, ref string) (secretrefs.Connector, error) {
	var out secretrefs.Connector
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		var row pgx.Row
		if id, err := uuid.Parse(ref); err == nil {
			row = tx.QueryRow(ctx, `SELECT `+connectorCols+` FROM secret_connectors WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		} else {
			row = tx.QueryRow(ctx, `SELECT `+connectorCols+` FROM secret_connectors WHERE tenant_id=$1 AND name=$2`, tenantID, ref)
		}
		var e error
		out, e = scanConnector(row)
		return e
	})
	return out, err
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) ListConnectors(ctx context.Context, s tenants.Scope, tenantID uuid.UUID) ([]secretrefs.Connector, error) {
	var out []secretrefs.Connector
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+connectorCols+` FROM secret_connectors WHERE tenant_id=$1 ORDER BY name`, tenantID)
		if err != nil {
			return db.MapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			c, e := scanConnector(rows)
			if e != nil {
				return e
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) UpdateConnector(ctx context.Context, s tenants.Scope, c secretrefs.Connector) error {
	cfg, err := json.Marshal(c.Config)
	if err != nil {
		return err
	}
	var cred any
	if c.CredentialRef != nil {
		b, e := json.Marshal(c.CredentialRef)
		if e != nil {
			return e
		}
		cred = b
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE secret_connectors SET name=$3,state=$4,config=$5,credential_ref=$6,version=version+1,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND version=$7`, c.TenantID, c.ID, c.Name, c.State, cfg, cred, c.Version)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.Conflict, "VERSION_CONFLICT", "connector version conflict")
		}
		return nil
	})
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) DeleteConnector(ctx context.Context, s tenants.Scope, tenantID, id uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM secret_connectors WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.NotFound, "NOT_FOUND", "not found")
		}
		return nil
	})
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) ConnectorReferenceCount(ctx context.Context, s tenants.Scope, id uuid.UUID) (int, error) {
	var n int
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM secret_references WHERE connector_id=$1`, id).Scan(&n)
	})
	return n, err
}

const refCols = `id,tenant_id,owner_id,project_id,name,connector_id,namespace,mount,path,key,
	secret_version,kind,allowed_uses,created_by,created_at,updated_at,version`

func scanRef(row pgx.Row) (secretrefs.Reference, error) {
	var x secretrefs.Reference
	err := row.Scan(&x.ID, &x.TenantID, &x.OwnerID, &x.ProjectID, &x.Name, &x.ConnectorID, &x.Namespace, &x.Mount, &x.Path, &x.Key, &x.SecretVersion, &x.Kind, &x.AllowedUses, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt, &x.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, apperr.New(apperr.NotFound, "NOT_FOUND", "not found")
	}
	return x, db.MapError(err)
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) CreateReference(ctx context.Context, s tenants.Scope, x secretrefs.Reference) error {
	if x.AllowedUses == nil {
		x.AllowedUses = []string{}
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO secret_references(id,tenant_id,owner_id,project_id,name,connector_id,namespace,mount,path,key,secret_version,kind,allowed_uses,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, x.ID, x.TenantID, x.OwnerID, x.ProjectID, x.Name, x.ConnectorID, x.Namespace, x.Mount, x.Path, x.Key, x.SecretVersion, x.Kind, x.AllowedUses, x.CreatedBy)
		return db.MapError(err)
	})
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) GetReference(ctx context.Context, s tenants.Scope, tenantID uuid.UUID, ref string) (secretrefs.Reference, error) {
	var x secretrefs.Reference
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		var row pgx.Row
		if id, e := uuid.Parse(ref); e == nil {
			row = tx.QueryRow(ctx, `SELECT `+refCols+` FROM secret_references WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		} else {
			row = tx.QueryRow(ctx, `SELECT `+refCols+` FROM secret_references WHERE tenant_id=$1 AND name=$2`, tenantID, ref)
		}
		var e error
		x, e = scanRef(row)
		return e
	})
	return x, err
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) GetReferenceByName(ctx context.Context, s tenants.Scope, tenantID uuid.UUID, name string) (secretrefs.Reference, error) {
	return r.GetReference(ctx, s, tenantID, name)
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) ListReferences(ctx context.Context, s tenants.Scope, tenantID uuid.UUID) ([]secretrefs.Reference, error) {
	var out []secretrefs.Reference
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+refCols+` FROM secret_references WHERE tenant_id=$1 ORDER BY name`, tenantID)
		if err != nil {
			return db.MapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			x, e := scanRef(rows)
			if e != nil {
				return e
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) UpdateReference(ctx context.Context, s tenants.Scope, x secretrefs.Reference) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE secret_references SET name=$3,owner_id=$4,project_id=$5,path=$6,key=$7,secret_version=$8,kind=$9,allowed_uses=$10,version=version+1,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND version=$11`, x.TenantID, x.ID, x.Name, x.OwnerID, x.ProjectID, x.Path, x.Key, x.SecretVersion, x.Kind, x.AllowedUses, x.Version)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.Conflict, "VERSION_CONFLICT", "reference version conflict")
		}
		return nil
	})
}

//nolint:revive // Methods implement the secretrefs.Repository port.
func (r *Repository) DeleteReference(ctx context.Context, s tenants.Scope, tenantID, id uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, s); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM secret_references WHERE tenant_id=$1 AND id=$2`, tenantID, id)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.NotFound, "NOT_FOUND", "not found")
		}
		return nil
	})
}
