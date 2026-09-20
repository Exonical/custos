// Package postgres implements accounting persistence and attribution.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/accounting"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/tenants"
)

// Repository persists accounting facts and aggregates.
type Repository struct{ pool *pgxpool.Pool }

// New returns an accounting repository.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

//nolint:revive // Methods implement the accounting.Repository port.
func (r *Repository) Watermark(ctx context.Context, clusterID uuid.UUID) (accounting.Watermark, error) {
	var w accounting.Watermark
	w.ClusterID = clusterID
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT cluster_id,watermark,last_collected_at,coalesce(last_error,'') FROM accounting_watermarks WHERE cluster_id=$1`, clusterID).Scan(&w.ClusterID, &w.Watermark, &w.LastCollectedAt, &w.LastError)
		if errors.Is(err, pgx.ErrNoRows) {
			w.Watermark = time.Unix(0, 0).UTC()
			return nil
		}
		return db.MapError(err)
	})
	return w, err
}

//nolint:revive // Methods implement the accounting.Repository port.
func (r *Repository) Store(ctx context.Context, clusterID uuid.UUID, records []accounting.Record, watermark time.Time) (accounting.StoreResult, error) {
	var result accounting.StoreResult
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		for i := range records {
			rec := &records[i]
			rec.ClusterID = clusterID
			var jobID, tenantID, projectID, userID uuid.UUID
			err := tx.QueryRow(ctx, `SELECT id,tenant_id,project_id,created_by FROM jobs WHERE cluster_id=$1 AND slurm_job_id=$2`, clusterID, rec.SlurmJobID).Scan(&jobID, &tenantID, &projectID, &userID)
			switch {
			case err == nil:
				rec.JobID = &jobID
				rec.TenantID = &tenantID
				rec.ProjectID = &projectID
				rec.UserID = &userID
				usage, _ := json.Marshal(map[string]any{"elapsed": rec.ElapsedSeconds, "cpu_time": rec.CPUSeconds, "node_count": rec.NodeCount, "tres_alloc": rec.TRESAlloc, "tres_usage": rec.TRESUsage, "wait_seconds": rec.WaitSeconds, "exit_code": rec.ExitCode, "failed": rec.Failed})
				if _, err = tx.Exec(ctx, `UPDATE jobs SET resource_usage=$2 WHERE id=$1`, jobID, usage); err != nil {
					return db.MapError(err)
				}
			case errors.Is(err, pgx.ErrNoRows):
				rows, e := tx.Query(ctx, `SELECT tenant_id,project_id FROM project_cluster_bindings WHERE cluster_id=$1 AND slurm_account=$2 AND enabled`, clusterID, rec.Account)
				if e != nil {
					return db.MapError(e)
				}
				var attrs [][2]uuid.UUID
				for rows.Next() {
					var a, b uuid.UUID
					if e = rows.Scan(&a, &b); e != nil {
						rows.Close()
						return e
					}
					attrs = append(attrs, [2]uuid.UUID{a, b})
				}
				rows.Close()
				if len(attrs) == 1 {
					rec.TenantID = &attrs[0][0]
					rec.ProjectID = &attrs[0][1]
				} else if len(attrs) > 1 {
					result.UnattributedAmbiguous++
				} else {
					result.UnattributedMissing++
				}
			default:
				return db.MapError(err)
			}
			tag, err := tx.Exec(ctx, `INSERT INTO usage_records(id,cluster_id,slurm_job_id,slurm_job_name,job_id,tenant_id,project_id,user_id,slurm_user,account,partition,state,exit_code,submit_time,eligible_time,start_time,end_time,elapsed_seconds,node_count,cpu_seconds,gpu_seconds,node_seconds,mem_gb_seconds,wait_seconds,energy_joules,tres_alloc,tres_usage,collected_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28) ON CONFLICT(cluster_id,slurm_job_id,end_time) DO NOTHING`, rec.ID, clusterID, rec.SlurmJobID, rec.SlurmJobName, rec.JobID, rec.TenantID, rec.ProjectID, rec.UserID, rec.SlurmUser, rec.Account, rec.Partition, rec.State, rec.ExitCode, nilTime(rec.SubmitTime), nilTime(rec.EligibleTime), nilTime(rec.StartTime), rec.EndTime, rec.ElapsedSeconds, rec.NodeCount, rec.CPUSeconds, rec.GPUSeconds, rec.NodeSeconds, rec.MemGBSeconds, rec.WaitSeconds, rec.EnergyJoules, jsonBytes(rec.TRESAlloc), jsonBytes(rec.TRESUsage), rec.CollectedAt)
			if err != nil {
				return db.MapError(err)
			}
			if tag.RowsAffected() > 0 {
				result.Inserted++
				if _, err = tx.Exec(ctx, `INSERT INTO usage_dirty_days(cluster_id,day) VALUES($1,$2) ON CONFLICT DO NOTHING`, clusterID, rec.EndTime.UTC().Format("2006-01-02")); err != nil {
					return db.MapError(err)
				}
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO accounting_watermarks(cluster_id,watermark,last_collected_at,last_error) VALUES($1,$2,now(),NULL) ON CONFLICT(cluster_id) DO UPDATE SET watermark=greatest(accounting_watermarks.watermark,excluded.watermark),last_collected_at=now(),last_error=NULL`, clusterID, watermark)
		return db.MapError(err)
	})
	return result, err
}
func nilTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
func jsonBytes(v map[string]int64) []byte {
	if v == nil {
		return []byte(`{}`)
	}
	b, _ := json.Marshal(v)
	return b
}

//nolint:revive // Methods implement the accounting.Repository port.
func (r *Repository) RecordError(ctx context.Context, id uuid.UUID, cause error) error {
	msg := cause.Error()
	if len(msg) > 1024 {
		msg = msg[:1024]
	}
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO accounting_watermarks(cluster_id,watermark,last_collected_at,last_error) VALUES($1,'1970-01-01',now(),$2) ON CONFLICT(cluster_id) DO UPDATE SET last_collected_at=now(),last_error=$2`, id, msg)
		return db.MapError(err)
	})
}

//nolint:revive // Methods implement the accounting.Repository port.
func (r *Repository) ListDirty(ctx context.Context, limit int) ([]accounting.DirtyDay, error) {
	var out []accounting.DirtyDay
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT cluster_id,day FROM usage_dirty_days ORDER BY day,cluster_id LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d accounting.DirtyDay
			if err = rows.Scan(&d.ClusterID, &d.Day); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

//nolint:revive // Methods implement the accounting.Repository port.
func (r *Repository) AggregateDirty(ctx context.Context, limit int) (int, error) {
	days, err := r.ListDirty(ctx, limit)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, d := range days {
		err = db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
			if err := db.SetPlatformScope(ctx, tx); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM usage_daily WHERE cluster_id=$1 AND day=$2`, d.ClusterID, d.Day); err != nil {
				return db.MapError(err)
			}
			_, err := tx.Exec(ctx, `INSERT INTO usage_daily(id,tenant_id,project_id,user_id,cluster_id,account,partition,day,jobs,failed,cpu_seconds,gpu_seconds,node_seconds,mem_gb_seconds,wait_seconds_sum,run_seconds_sum,energy_joules,wait_p50,wait_p90,wait_p99,run_p50,run_p90,run_p99)
SELECT gen_random_uuid(),tenant_id,project_id,user_id,cluster_id,account,partition,(end_time AT TIME ZONE 'UTC')::date,count(*),count(*) FILTER(WHERE state IN('FAILED','TIMEOUT','NODE_FAIL','OUT_OF_MEMORY','BOOT_FAIL','DEADLINE') OR (state='COMPLETED' AND coalesce(exit_code,0)<>0)),sum(cpu_seconds),sum(gpu_seconds),sum(node_seconds),sum(mem_gb_seconds),coalesce(sum(wait_seconds),0),sum(elapsed_seconds),sum(energy_joules),percentile_cont(.5) WITHIN GROUP(ORDER BY wait_seconds) FILTER(WHERE wait_seconds IS NOT NULL),percentile_cont(.9) WITHIN GROUP(ORDER BY wait_seconds) FILTER(WHERE wait_seconds IS NOT NULL),percentile_cont(.99) WITHIN GROUP(ORDER BY wait_seconds) FILTER(WHERE wait_seconds IS NOT NULL),percentile_cont(.5) WITHIN GROUP(ORDER BY elapsed_seconds),percentile_cont(.9) WITHIN GROUP(ORDER BY elapsed_seconds),percentile_cont(.99) WITHIN GROUP(ORDER BY elapsed_seconds) FROM usage_records WHERE cluster_id=$1 AND end_time >= $2::date AND end_time < $2::date+1 GROUP BY tenant_id,project_id,user_id,cluster_id,account,partition,(end_time AT TIME ZONE 'UTC')::date`, d.ClusterID, d.Day)
			if err != nil {
				return db.MapError(err)
			}
			_, err = tx.Exec(ctx, `DELETE FROM usage_dirty_days WHERE cluster_id=$1 AND day=$2`, d.ClusterID, d.Day)
			return db.MapError(err)
		})
		if err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

const dailyCols = `id,tenant_id,project_id,user_id,cluster_id,account,partition,day,jobs,failed,cpu_seconds,gpu_seconds,node_seconds,mem_gb_seconds,wait_seconds_sum,run_seconds_sum,energy_joules,wait_p50,wait_p90,wait_p99,run_p50,run_p90,run_p99`

//nolint:revive // Methods implement the accounting.Repository port.
func (r *Repository) ListUsage(ctx context.Context, scope tenants.Scope, q accounting.UsageQuery) ([]accounting.Daily, error) {
	var out []accounting.Daily
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if scope.IsPlatform() {
			if err := db.SetPlatformScope(ctx, tx); err != nil {
				return err
			}
		} else {
			if err := db.SetTenant(ctx, tx, q.TenantID); err != nil {
				return err
			}
		}
		sql := `SELECT ` + dailyCols + ` FROM usage_daily WHERE tenant_id=$1 AND day >= $2 AND day < $3`
		args := []any{q.TenantID, q.From, q.To}
		if q.ProjectID != nil {
			args = append(args, *q.ProjectID)
			sql += fmt.Sprintf(` AND project_id=$%d`, len(args))
		}
		if q.ClusterID != nil {
			args = append(args, *q.ClusterID)
			sql += fmt.Sprintf(` AND cluster_id=$%d`, len(args))
		}
		if !q.TenantWide {
			args = append(args, q.UserID, q.ProjectIDs)
			sql += fmt.Sprintf(` AND (user_id=$%d OR project_id=ANY($%d))`, len(args)-1, len(args))
		}
		sql += ` ORDER BY day,id`
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return db.MapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var d accounting.Daily
			if err = rows.Scan(&d.ID, &d.TenantID, &d.ProjectID, &d.UserID, &d.ClusterID, &d.Account, &d.Partition, &d.Day, &d.Jobs, &d.Failed, &d.CPUSeconds, &d.GPUSeconds, &d.NodeSeconds, &d.MemGBSeconds, &d.WaitSecondsSum, &d.RunSecondsSum, &d.EnergyJoules, &d.WaitP50, &d.WaitP90, &d.WaitP99, &d.RunP50, &d.RunP90, &d.RunP99); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

//nolint:revive // Methods implement the accounting.Repository port.
func (r *Repository) Top(context.Context, tenants.Scope, accounting.TopQuery) ([]accounting.TopRow, error) {
	return nil, apperr.New(apperr.Internal, "INTERNAL", "top is computed by service")
}

//nolint:revive // Methods implement the accounting.Repository port.
func (r *Repository) ClusterStatus(ctx context.Context, id uuid.UUID) (accounting.Watermark, int64, error) {
	w, err := r.Watermark(ctx, id)
	if err != nil {
		return w, 0, err
	}
	var n int64
	err = db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM usage_records WHERE cluster_id=$1 AND tenant_id IS NULL`, id).Scan(&n)
	})
	return w, n, err
}
