package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/tenants"
)

// RequestDeletion implements tenants.Repository. The state flip and the
// work item commit atomically — the queue row is the hand-off to the
// worker, so it must live in the same tx (outbox pattern).
func (r *Repository) RequestDeletion(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE tenants SET state='deleting', updated_at=now(),
			       version=version+1
			WHERE id=$1 AND state IN ('provisioning','active','suspended')`,
			tenantID)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.Conflict, "TENANT_STATE",
				"tenant is already being deleted or deleted")
		}
		_, err = workqueue.Enqueue(ctx, tx, workqueue.EnqueueRequest{
			Kind:     "tenant.delete",
			Key:      "tenant:" + tenantID.String(),
			TenantID: &tenantID,
		})
		return err
	})
}

// PurgeTenantData implements tenants.Repository: removes all tenant-owned
// rows and flips the tombstone to 'deleted'. The tenants row itself (and
// its slug) stays — slugs are never reused. Idempotent.
func (r *Repository) PurgeTenantData(ctx context.Context, tenantID uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, tenants.PlatformScope()); err != nil {
			return err
		}
		var state string
		if err := tx.QueryRow(ctx,
			`SELECT state FROM tenants WHERE id=$1`, tenantID).Scan(&state); err != nil {
			return db.MapError(err)
		}
		if state == string(tenants.StateDeleted) {
			return nil
		}
		for _, q := range []string{
			`DELETE FROM group_memberships WHERE tenant_id=$1`,
			`DELETE FROM groups WHERE tenant_id=$1`,
			`DELETE FROM claim_mapping_rules WHERE tenant_id=$1`,
			`DELETE FROM tenant_memberships WHERE tenant_id=$1`,
		} {
			if _, err := tx.Exec(ctx, q, tenantID); err != nil {
				return db.MapError(err)
			}
		}
		_, err := tx.Exec(ctx,
			`UPDATE tenants SET state='deleted', updated_at=now(),
			       version=version+1 WHERE id=$1`, tenantID)
		return db.MapError(err)
	})
}
