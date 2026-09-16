package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/tenants"
)

const groupCols = `id, tenant_id, name, description, source, version, created_at, updated_at`

func scanGroup(row pgx.Row) (tenants.Group, error) {
	var g tenants.Group
	var src string
	err := row.Scan(&g.ID, &g.TenantID, &g.Name, &g.Description, &src,
		&g.Version, &g.CreatedAt, &g.UpdatedAt)
	g.Source = tenants.MembershipSource(src)
	return g, err
}

// CreateGroup implements tenants.GroupRepository.
func (r *Repository) CreateGroup(ctx context.Context, scope tenants.Scope, g tenants.Group) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO groups (id, tenant_id, name, description, source)
			VALUES ($1,$2,$3,$4,$5)`,
			g.ID, g.TenantID, g.Name, g.Description, string(g.Source))
		return db.MapError(err)
	})
}

// GetGroup implements tenants.GroupRepository: ref is a uuid or name.
func (r *Repository) GetGroup(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID, ref string) (tenants.Group, error) {
	var g tenants.Group
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		g, err = scanGroup(tx.QueryRow(ctx,
			`SELECT `+groupCols+` FROM groups
			 WHERE tenant_id=$1 AND (id::text=$2 OR name=$2)`,
			tenantID, ref))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return tenants.Group{}, errNotFound
	}
	return g, err
}

// ListGroups implements tenants.GroupRepository: keyset on (created_at, id).
func (r *Repository) ListGroups(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID, page tenants.Page) ([]tenants.Group, string, error) {
	page = page.Normalize(50, 200)
	var ks *db.Keyset
	if page.Cursor != "" {
		k, err := db.DecodeCursor(page.Cursor)
		if err != nil {
			return nil, "", err
		}
		ks = &k
	}
	var out []tenants.Group
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		where := " WHERE tenant_id = $1"
		args := []any{tenantID}
		if ks != nil {
			where += " AND (created_at, id) > ($2, $3)"
			args = append(args, ks.CreatedAt, ks.ID)
		}
		rows, err := tx.Query(ctx,
			`SELECT `+groupCols+` FROM groups`+where+
				` ORDER BY created_at, id LIMIT `+itoa(page.Limit+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanGroup(rows)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	var next string
	if len(out) > page.Limit {
		last := out[page.Limit-1]
		next = db.EncodeCursor(db.Keyset{CreatedAt: last.CreatedAt, ID: last.ID})
		out = out[:page.Limit]
	}
	return out, next, nil
}

// UpdateGroup implements tenants.GroupRepository (optimistic version).
func (r *Repository) UpdateGroup(ctx context.Context, scope tenants.Scope, g tenants.Group) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE groups SET name=$3, description=$4, updated_at=now(),
			       version=version+1
			WHERE id=$1 AND tenant_id=$2 AND version=$5`,
			g.ID, g.TenantID, g.Name, g.Description, g.Version)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var exists int
		err = tx.QueryRow(ctx,
			`SELECT 1 FROM groups WHERE id=$1 AND tenant_id=$2`,
			g.ID, g.TenantID).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return apperr.New(apperr.Conflict, "VERSION_CONFLICT", "group version conflict")
	})
}

// DeleteGroup implements tenants.GroupRepository.
func (r *Repository) DeleteGroup(ctx context.Context, scope tenants.Scope, tenantID, groupID uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM groups WHERE id=$1 AND tenant_id=$2`, groupID, tenantID)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return nil
	})
}

const groupMemberCols = `tenant_id, group_id, user_id, source, created_at`

func scanGroupMembership(row pgx.Row) (tenants.GroupMembership, error) {
	var m tenants.GroupMembership
	var src string
	err := row.Scan(&m.TenantID, &m.GroupID, &m.UserID, &src, &m.CreatedAt)
	m.Source = tenants.MembershipSource(src)
	return m, err
}

// ListGroupMembers implements tenants.GroupRepository: keyset on
// (created_at, user_id).
func (r *Repository) ListGroupMembers(ctx context.Context, scope tenants.Scope, tenantID, groupID uuid.UUID, page tenants.Page) ([]tenants.GroupMembership, string, error) {
	page = page.Normalize(50, 200)
	var ks *db.Keyset
	if page.Cursor != "" {
		k, err := db.DecodeCursor(page.Cursor)
		if err != nil {
			return nil, "", err
		}
		ks = &k
	}
	var out []tenants.GroupMembership
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		where := " WHERE tenant_id = $1 AND group_id = $2"
		args := []any{tenantID, groupID}
		if ks != nil {
			where += " AND (created_at, user_id) > ($3, $4)"
			args = append(args, ks.CreatedAt, ks.ID)
		}
		rows, err := tx.Query(ctx,
			`SELECT `+groupMemberCols+` FROM group_memberships`+where+
				` ORDER BY created_at, user_id LIMIT `+itoa(page.Limit+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanGroupMembership(rows)
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
	var next string
	if len(out) > page.Limit {
		last := out[page.Limit-1]
		next = db.EncodeCursor(db.Keyset{CreatedAt: last.CreatedAt, ID: last.UserID})
		out = out[:page.Limit]
	}
	return out, next, nil
}

// UpsertGroupMember implements tenants.GroupRepository.
func (r *Repository) UpsertGroupMember(ctx context.Context, scope tenants.Scope, m tenants.GroupMembership) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO group_memberships (tenant_id, group_id, user_id, source)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (group_id, user_id)
			DO UPDATE SET source=EXCLUDED.source`,
			m.TenantID, m.GroupID, m.UserID, string(m.Source))
		return db.MapError(err)
	})
}

// DeleteGroupMember implements tenants.GroupRepository.
func (r *Repository) DeleteGroupMember(ctx context.Context, scope tenants.Scope, tenantID, groupID, userID uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM group_memberships
			 WHERE tenant_id=$1 AND group_id=$2 AND user_id=$3`,
			tenantID, groupID, userID)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return nil
	})
}

// ListIDPGroupMembershipsForUser implements tenants.GroupRepository.
// Platform scope; idp-sourced rows only.
func (r *Repository) ListIDPGroupMembershipsForUser(ctx context.Context, userID uuid.UUID) ([]tenants.GroupMembership, error) {
	var out []tenants.GroupMembership
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, tenants.PlatformScope()); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT `+groupMemberCols+` FROM group_memberships
			 WHERE user_id=$1 AND source='idp'`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanGroupMembership(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

const claimRuleCols = `id, tenant_id, claim, match_value, roles, group_id,
	enabled, version, created_at, updated_at`

func scanClaimRule(row pgx.Row) (tenants.ClaimRule, error) {
	var cr tenants.ClaimRule
	err := row.Scan(&cr.ID, &cr.TenantID, &cr.Claim, &cr.MatchValue, &cr.Roles,
		&cr.GroupID, &cr.Enabled, &cr.Version, &cr.CreatedAt, &cr.UpdatedAt)
	return cr, err
}

// CreateClaimRule implements tenants.ClaimRuleRepository.
func (r *Repository) CreateClaimRule(ctx context.Context, scope tenants.Scope, cr tenants.ClaimRule) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO claim_mapping_rules
			 (id, tenant_id, claim, match_value, roles, group_id, enabled)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			cr.ID, cr.TenantID, cr.Claim, cr.MatchValue, cr.Roles,
			cr.GroupID, cr.Enabled)
		return db.MapError(err)
	})
}

// GetClaimRule implements tenants.ClaimRuleRepository.
func (r *Repository) GetClaimRule(ctx context.Context, scope tenants.Scope, tenantID, ruleID uuid.UUID) (tenants.ClaimRule, error) {
	var cr tenants.ClaimRule
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		var err error
		cr, err = scanClaimRule(tx.QueryRow(ctx,
			`SELECT `+claimRuleCols+` FROM claim_mapping_rules
			 WHERE tenant_id=$1 AND id=$2`, tenantID, ruleID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return tenants.ClaimRule{}, errNotFound
	}
	return cr, err
}

// ListClaimRules implements tenants.ClaimRuleRepository: keyset on
// (created_at, id).
func (r *Repository) ListClaimRules(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID, page tenants.Page) ([]tenants.ClaimRule, string, error) {
	page = page.Normalize(50, 200)
	var ks *db.Keyset
	if page.Cursor != "" {
		k, err := db.DecodeCursor(page.Cursor)
		if err != nil {
			return nil, "", err
		}
		ks = &k
	}
	var out []tenants.ClaimRule
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		where := " WHERE tenant_id = $1"
		args := []any{tenantID}
		if ks != nil {
			where += " AND (created_at, id) > ($2, $3)"
			args = append(args, ks.CreatedAt, ks.ID)
		}
		rows, err := tx.Query(ctx,
			`SELECT `+claimRuleCols+` FROM claim_mapping_rules`+where+
				` ORDER BY created_at, id LIMIT `+itoa(page.Limit+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			cr, err := scanClaimRule(rows)
			if err != nil {
				return err
			}
			out = append(out, cr)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	var next string
	if len(out) > page.Limit {
		last := out[page.Limit-1]
		next = db.EncodeCursor(db.Keyset{CreatedAt: last.CreatedAt, ID: last.ID})
		out = out[:page.Limit]
	}
	return out, next, nil
}

// UpdateClaimRule implements tenants.ClaimRuleRepository (optimistic).
func (r *Repository) UpdateClaimRule(ctx context.Context, scope tenants.Scope, cr tenants.ClaimRule) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE claim_mapping_rules SET roles=$3, group_id=$4, enabled=$5,
			       updated_at=now(), version=version+1
			WHERE id=$1 AND tenant_id=$2 AND version=$6`,
			cr.ID, cr.TenantID, cr.Roles, cr.GroupID, cr.Enabled, cr.Version)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var exists int
		err = tx.QueryRow(ctx,
			`SELECT 1 FROM claim_mapping_rules WHERE id=$1 AND tenant_id=$2`,
			cr.ID, cr.TenantID).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return apperr.New(apperr.Conflict, "VERSION_CONFLICT", "claim rule version conflict")
	})
}

// DeleteClaimRule implements tenants.ClaimRuleRepository.
func (r *Repository) DeleteClaimRule(ctx context.Context, scope tenants.Scope, tenantID, ruleID uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM claim_mapping_rules WHERE id=$1 AND tenant_id=$2`,
			ruleID, tenantID)
		if err != nil {
			return db.MapError(err)
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return nil
	})
}

// maxClaimValues bounds the MatchRules input (claim pairs per call).
const maxClaimValues = 200

// MatchRules implements tenants.ClaimRuleRepository: platform scope,
// (claim, match_value) pairs joined via unnest so claim and value stay
// paired.
func (r *Repository) MatchRules(ctx context.Context, claims map[string][]string) ([]tenants.ClaimRule, error) {
	var names, values []string
	for claim, vals := range claims {
		for _, v := range vals {
			names = append(names, claim)
			values = append(values, v)
		}
	}
	if len(values) > maxClaimValues {
		return nil, apperr.New(apperr.Invalid, "CLAIMS_TOO_LARGE",
			fmt.Sprintf("more than %d claim values", maxClaimValues))
	}
	if len(values) == 0 {
		return nil, nil
	}
	var out []tenants.ClaimRule
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, tenants.PlatformScope()); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT `+claimRuleCols+`
			FROM claim_mapping_rules c
			JOIN (SELECT unnest($1::text[]) AS c_name,
			            unnest($2::text[]) AS c_value) v
			  ON v.c_name = c.claim AND v.c_value = c.match_value
			WHERE c.enabled
			ORDER BY c.tenant_id, c.created_at`,
			names, values)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			cr, err := scanClaimRule(rows)
			if err != nil {
				return err
			}
			out = append(out, cr)
		}
		return rows.Err()
	})
	return out, err
}

// ListIDPMembershipsForUser implements tenants.Repository: platform
// scope; idp-sourced tenant memberships only.
func (r *Repository) ListIDPMembershipsForUser(ctx context.Context, userID uuid.UUID) ([]tenants.Membership, error) {
	var out []tenants.Membership
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, tenants.PlatformScope()); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT `+memberCols+` FROM tenant_memberships
			 WHERE user_id=$1 AND source='idp'`, userID)
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

var (
	_ tenants.GroupRepository     = (*Repository)(nil)
	_ tenants.ClaimRuleRepository = (*Repository)(nil)
)
