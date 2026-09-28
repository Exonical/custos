// Package postgres implements allocation persistence on PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/Exonical/custos/internal/allocations"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository implements allocations.Repository on PostgreSQL.
type Repository struct{ pool *pgxpool.Pool }

// New returns a Repository backed by p.
func New(p *pgxpool.Pool) *Repository { return &Repository{p} }

const cols = `id,tenant_id,project_id,binding_id,name,unit,limit_amount,period_start,period_end,enforcement,consumed_amount,consumed_as_of,version,created_at,updated_at,created_by`

func scan(row pgx.Row) (allocations.Allocation, error) {
	var a allocations.Allocation
	err := row.Scan(&a.ID, &a.TenantID, &a.ProjectID, &a.BindingID, &a.Name, &a.Unit, &a.LimitAmount, &a.PeriodStart, &a.PeriodEnd, &a.Enforcement, &a.ConsumedAmount, &a.ConsumedAsOf, &a.Version, &a.CreatedAt, &a.UpdatedAt, &a.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, db.ErrNotFound
	}
	return a, db.MapError(err)
}

// Create inserts an allocation within the supplied RLS scope.
func (r *Repository) Create(ctx context.Context, s tenants.Scope, a allocations.Allocation) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.ApplyScope(ctx, tx, s); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO allocations(id,tenant_id,project_id,binding_id,name,unit,limit_amount,period_start,period_end,enforcement,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, a.ID, a.TenantID, a.ProjectID, a.BindingID, a.Name, a.Unit, a.LimitAmount, a.PeriodStart, a.PeriodEnd, a.Enforcement, a.CreatedBy)
		return db.MapError(err)
	})
}

// Get loads an allocation in a project.
func (r *Repository) Get(ctx context.Context, s tenants.Scope, pid, id uuid.UUID) (allocations.Allocation, error) {
	var a allocations.Allocation
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.ApplyScope(ctx, tx, s); err != nil {
			return err
		}
		var e error
		a, e = scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM allocations WHERE project_id=$1 AND id=$2`, pid, id))
		return e
	})
	return a, err
}

// List loads a project's allocations.
func (r *Repository) List(ctx context.Context, s tenants.Scope, pid uuid.UUID) ([]allocations.Allocation, error) {
	var out []allocations.Allocation
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.ApplyScope(ctx, tx, s); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+cols+` FROM allocations WHERE project_id=$1 ORDER BY name,id`, pid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, e := scan(rows)
			if e != nil {
				return e
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// ListTenant loads allocations in a tenant.
func (r *Repository) ListTenant(ctx context.Context, s tenants.Scope, tenantID uuid.UUID) ([]allocations.Allocation, error) {
	var out []allocations.Allocation
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.ApplyScope(ctx, tx, s); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+cols+` FROM allocations WHERE tenant_id=$1 ORDER BY project_id,name,id`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, e := scan(rows)
			if e != nil {
				return e
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// Update applies an optimistic allocation update.
func (r *Repository) Update(ctx context.Context, s tenants.Scope, a allocations.Allocation) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.ApplyScope(ctx, tx, s); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE allocations SET name=$3,unit=$4,limit_amount=$5,period_start=$6,period_end=$7,enforcement=$8,version=version+1,updated_at=now() WHERE id=$1 AND tenant_id=$2 AND version=$9`, a.ID, a.TenantID, a.Name, a.Unit, a.LimitAmount, a.PeriodStart, a.PeriodEnd, a.Enforcement, a.Version)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.Conflict, "VERSION_CONFLICT", "allocation version conflict")
		}
		return nil
	})
}

// Delete removes an allocation from a project.
func (r *Repository) Delete(ctx context.Context, s tenants.Scope, pid, id uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.ApplyScope(ctx, tx, s); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM allocations WHERE project_id=$1 AND id=$2`, pid, id)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return db.ErrNotFound
		}
		return nil
	})
}

// Active returns current allocations on a binding.
func (r *Repository) Active(ctx context.Context, s tenants.Scope, bid uuid.UUID, now time.Time) ([]allocations.Allocation, error) {
	var out []allocations.Allocation
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.ApplyScope(ctx, tx, s); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+cols+` FROM allocations WHERE binding_id=$1 AND period_start <= $2 AND period_end > $2`, bid, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, e := scan(rows)
			if e != nil {
				return e
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// InFlight sums estimated costs for non-terminal binding jobs.
func (r *Repository) InFlight(ctx context.Context, s tenants.Scope, bid uuid.UUID) (map[string]float64, error) {
	out := map[string]float64{"cpu_hours": 0, "gpu_hours": 0, "node_hours": 0}
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.ApplyScope(ctx, tx, s); err != nil {
			return err
		}
		var cpu, gpu, node float64
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum((estimated_cost->>'cpu_hours')::numeric),0),coalesce(sum((estimated_cost->>'gpu_hours')::numeric),0),coalesce(sum((estimated_cost->>'node_hours')::numeric),0) FROM jobs WHERE binding_id=$1 AND state IN('SUBMITTING','QUEUED','RUNNING')`, bid).Scan(&cpu, &gpu, &node); err != nil {
			return db.MapError(err)
		}
		out["cpu_hours"], out["gpu_hours"], out["node_hours"] = cpu, gpu, node
		return nil
	})
	return out, err
}

// CheckInTx serializes hard-budget checks for a binding and recomputes the
// active allocations and reservations using the caller's persistence transaction.
func (r *Repository) CheckInTx(ctx context.Context, transaction any, bindingID uuid.UUID, estimate map[string]float64) (allocations.CheckResult, error) {
	tx, ok := transaction.(pgx.Tx)
	if !ok {
		return allocations.CheckResult{}, apperr.New(apperr.Internal, "ALLOCATION_TRANSACTION_INVALID", "allocation check requires a PostgreSQL transaction")
	}
	var hasHard bool
	now := time.Now().UTC()
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM allocations WHERE binding_id=$1 AND enforcement='hard' AND period_start <= $2 AND period_end > $2)`, bindingID, now).Scan(&hasHard); err != nil {
		return allocations.CheckResult{}, db.MapError(err)
	}
	if hasHard {
		const lockNamespace int32 = 0x43555354
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1,hashtext($2))`, lockNamespace, bindingID.String()); err != nil {
			return allocations.CheckResult{}, db.MapError(err)
		}
	}
	now = time.Now().UTC()
	rows, err := tx.Query(ctx, `SELECT `+cols+` FROM allocations WHERE binding_id=$1 AND period_start <= $2 AND period_end > $2`, bindingID, now)
	if err != nil {
		return allocations.CheckResult{}, db.MapError(err)
	}
	var active []allocations.Allocation
	for rows.Next() {
		a, e := scan(rows)
		if e != nil {
			rows.Close()
			return allocations.CheckResult{}, e
		}
		active = append(active, a)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return allocations.CheckResult{}, err
	}
	rows.Close()
	var cpu, gpu, node float64
	if err = tx.QueryRow(ctx, `SELECT coalesce(sum((estimated_cost->>'cpu_hours')::numeric),0),coalesce(sum((estimated_cost->>'gpu_hours')::numeric),0),coalesce(sum((estimated_cost->>'node_hours')::numeric),0) FROM jobs WHERE binding_id=$1 AND state IN('SUBMITTING','QUEUED','RUNNING')`, bindingID).Scan(&cpu, &gpu, &node); err != nil {
		return allocations.CheckResult{}, db.MapError(err)
	}
	reserved := map[string]float64{"cpu_hours": cpu, "gpu_hours": gpu, "node_hours": node}
	return allocations.Check(active, reserved, estimate), nil
}

// Refresh recomputes allocation consumption, optionally for one cluster.
func (r *Repository) Refresh(ctx context.Context, clusterID *uuid.UUID) (int, error) {
	n := 0
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		sql := `SELECT a.id,a.unit,a.period_start,a.period_end,b.project_id,b.cluster_id,b.slurm_account FROM allocations a JOIN project_cluster_bindings b ON b.id=a.binding_id`
		var args []any
		if clusterID != nil {
			sql += ` WHERE b.cluster_id=$1`
			args = append(args, *clusterID)
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		type row struct {
			id         uuid.UUID
			unit       string
			start, end time.Time
			pid, cid   uuid.UUID
			account    string
		}
		var all []row
		for rows.Next() {
			var x row
			if err = rows.Scan(&x.id, &x.unit, &x.start, &x.end, &x.pid, &x.cid, &x.account); err != nil {
				return err
			}
			all = append(all, x)
		}
		for _, x := range all {
			col := map[string]string{"cpu_hours": "cpu_seconds", "gpu_hours": "gpu_seconds", "node_hours": "node_seconds"}[x.unit]
			var consumed float64
			q := `SELECT coalesce(sum(` + col + `),0)/3600.0 FROM usage_daily WHERE project_id=$1 AND cluster_id=$2 AND account=$3 AND day >= $4::timestamptz::date AND day <= ($5::timestamptz - interval '1 microsecond')::date`
			if err = tx.QueryRow(ctx, q, x.pid, x.cid, x.account, x.start, x.end).Scan(&consumed); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE allocations SET consumed_amount=$2,consumed_as_of=now(),updated_at=now() WHERE id=$1`, x.id, consumed); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
