package postgres

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/users"
)

// claimsSyncFreshness mirrors the service-side throttle: claims synced
// within this window are considered fresh.
const claimsSyncFreshness = 5 * time.Minute

// maxSyncClaimValues bounds the (claim, match_value) pairs per sync.
const maxSyncClaimValues = 200

// upsertIDPMembership inserts an idp-sourced tenant membership. A
// concurrent manual grant for the same (tenant, user) must win: the
// DO UPDATE only fires while the row is still idp-sourced.
const upsertIDPMembership = `
	INSERT INTO tenant_memberships (tenant_id, user_id, roles, source)
	VALUES ($1,$2,$3,'idp')
	ON CONFLICT (tenant_id, user_id) DO UPDATE
	  SET roles=EXCLUDED.roles, source=EXCLUDED.source, updated_at=now()
	  WHERE tenant_memberships.source='idp'`

// upsertIDPGroupMembership is the group-membership equivalent of
// upsertIDPMembership.
const upsertIDPGroupMembership = `
	INSERT INTO group_memberships (tenant_id, group_id, user_id, source)
	VALUES ($1,$2,$3,'idp')
	ON CONFLICT (group_id, user_id) DO UPDATE SET source=EXCLUDED.source
	  WHERE group_memberships.source='idp'`

// CountPlatformRoleHolders implements users.Repository.
func (r *Repository) CountPlatformRoleHolders(ctx context.Context, role string) (int, error) {
	var n int
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM platform_role_bindings WHERE role=$1`,
			role).Scan(&n)
	})
	return n, err
}

// SyncIDPClaims implements users.Repository. One transaction under
// platform scope: lock the user row, re-check freshness, diff the
// idp-sourced memberships against the rules matching the claims, write,
// and stamp the sync hash. Manual memberships are never touched, and a
// revocation that would strand a tenant without a tenant-admin is kept
// (revoke_blocked) rather than applied.
func (r *Repository) SyncIDPClaims(ctx context.Context, userID uuid.UUID,
	claims map[string][]string, hash []byte, now time.Time) (users.SyncOutcome, error) {
	var out users.SyncOutcome
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}

		// Lock first so a concurrent Provision serializes behind us and
		// sees the fresh sync stamp.
		var curHash []byte
		var syncedAt *time.Time
		if err := tx.QueryRow(ctx,
			`SELECT claims_hash, claims_synced_at FROM users
			 WHERE id=$1 FOR UPDATE`, userID).
			Scan(&curHash, &syncedAt); err != nil {
			return err
		}
		if bytes.Equal(curHash, hash) && syncedAt != nil &&
			now.Sub(*syncedAt) < claimsSyncFreshness {
			out.Skipped = true
			return nil
		}

		// Match enabled rules on exact (claim, match_value) pairs.
		var names, values []string
		for c, vs := range claims {
			for _, v := range vs {
				names = append(names, c)
				values = append(values, v)
			}
		}
		if len(values) > maxSyncClaimValues {
			values, names = values[:maxSyncClaimValues], names[:maxSyncClaimValues]
		}
		type rule struct {
			id       uuid.UUID
			tenantID uuid.UUID
			roles    []string
			groupID  *uuid.UUID
		}
		var rules []rule
		if len(values) > 0 {
			rows, err := tx.Query(ctx, `
				SELECT c.id, c.tenant_id, c.roles, c.group_id
				FROM claim_mapping_rules c
				JOIN (SELECT unnest($1::text[]) AS c_name,
				            unnest($2::text[]) AS c_value) v
				  ON v.c_name = c.claim AND v.c_value = c.match_value
				WHERE c.enabled`, names, values)
			if err != nil {
				return err
			}
			for rows.Next() {
				var rl rule
				if err := rows.Scan(&rl.id, &rl.tenantID, &rl.roles, &rl.groupID); err != nil {
					rows.Close()
					return err
				}
				rules = append(rules, rl)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}

		// Desired state per tenant; deleting/deleted tenants are skipped.
		type desired struct {
			roles   map[string]bool
			groups  map[uuid.UUID]bool
			ruleIDs []string
		}
		desiredByTenant := map[uuid.UUID]*desired{}
		for _, rl := range rules {
			var state string
			if err := tx.QueryRow(ctx,
				`SELECT state FROM tenants WHERE id=$1`, rl.tenantID).Scan(&state); err != nil {
				return err
			}
			if state == "deleting" || state == "deleted" {
				continue
			}
			d := desiredByTenant[rl.tenantID]
			if d == nil {
				d = &desired{roles: map[string]bool{}, groups: map[uuid.UUID]bool{}}
				desiredByTenant[rl.tenantID] = d
			}
			for _, ro := range rl.roles {
				d.roles[ro] = true
			}
			if rl.groupID != nil {
				d.groups[*rl.groupID] = true
			}
			d.ruleIDs = append(d.ruleIDs, rl.id.String())
		}

		// Current idp memberships (all tenants).
		type membership struct {
			tenantID uuid.UUID
			roles    []string
			source   string
		}
		var current []membership
		rows, err := tx.Query(ctx,
			`SELECT tenant_id, roles, source FROM tenant_memberships
			 WHERE user_id=$1`, userID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var m membership
			if err := rows.Scan(&m.tenantID, &m.roles, &m.source); err != nil {
				rows.Close()
				return err
			}
			current = append(current, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		currentByTenant := map[uuid.UUID]membership{}
		for _, m := range current {
			currentByTenant[m.tenantID] = m
		}

		event := func(action string, tenantID uuid.UUID, roles, ruleIDs []string, reason string) {
			out.Events = append(out.Events, users.SyncEvent{
				Action: action, TenantID: tenantID, UserID: userID,
				Roles: roles, RuleIDs: ruleIDs, Reason: reason,
			})
		}

		// Apply desired memberships (before group rows — the trigger
		// requires the tenant membership to exist).
		for tenantID, d := range desiredByTenant {
			roleList := slices.Sorted(maps.Keys(d.roles))
			cur, has := currentByTenant[tenantID]
			switch {
			case !has:
				tag, err := tx.Exec(ctx, upsertIDPMembership,
					tenantID, userID, roleList)
				if err != nil {
					return db.MapError(err)
				}
				if tag.RowsAffected() == 0 {
					// A manual membership raced in; it wins.
					continue
				}
				event("membership.granted", tenantID, roleList, d.ruleIDs, "")
			case cur.source == "manual":
				// manual wins; never reconcile over it.
			case !sameStrings(cur.roles, roleList):
				if _, err := tx.Exec(ctx,
					`UPDATE tenant_memberships SET roles=$3, updated_at=now()
					 WHERE tenant_id=$1 AND user_id=$2 AND source='idp'`,
					tenantID, userID, roleList); err != nil {
					return db.MapError(err)
				}
				event("membership.updated", tenantID, roleList, d.ruleIDs, "")
			}
		}

		// Revoke idp memberships no longer desired; manual untouched.
		for _, m := range current {
			if m.source != "idp" {
				continue
			}
			if _, ok := desiredByTenant[m.tenantID]; ok {
				continue
			}
			if slices.Contains(m.roles, "tenant-admin") {
				var n int
				if err := tx.QueryRow(ctx,
					`SELECT count(*) FROM tenant_memberships
					 WHERE tenant_id=$1 AND 'tenant-admin' = ANY(roles)`,
					m.tenantID).Scan(&n); err != nil {
					return err
				}
				if n <= 1 {
					event("membership.revoke_blocked", m.tenantID, m.roles, nil,
						"last_admin")
					continue
				}
			}
			if _, err := tx.Exec(ctx,
				`DELETE FROM tenant_memberships
				 WHERE tenant_id=$1 AND user_id=$2 AND source='idp'`,
				m.tenantID, userID); err != nil {
				return db.MapError(err)
			}
			event("membership.revoked", m.tenantID, m.roles, nil, "claim_absent")
		}

		// Group memberships: same add/remove logic, source=idp only.
		type gm struct {
			tenantID, groupID uuid.UUID
			source            string
		}
		var curGroups []gm
		grows, err := tx.Query(ctx,
			`SELECT tenant_id, group_id, source FROM group_memberships
			 WHERE user_id=$1`, userID)
		if err != nil {
			return err
		}
		for grows.Next() {
			var g gm
			if err := grows.Scan(&g.tenantID, &g.groupID, &g.source); err != nil {
				grows.Close()
				return err
			}
			curGroups = append(curGroups, g)
		}
		grows.Close()
		if err := grows.Err(); err != nil {
			return err
		}
		curGroupSet := map[[2]uuid.UUID]string{}
		for _, g := range curGroups {
			curGroupSet[[2]uuid.UUID{g.tenantID, g.groupID}] = g.source
		}
		desiredGroups := map[[2]uuid.UUID]bool{}
		for tenantID, d := range desiredByTenant {
			for gid := range d.groups {
				desiredGroups[[2]uuid.UUID{tenantID, gid}] = true
				key := [2]uuid.UUID{tenantID, gid}
				if src, ok := curGroupSet[key]; ok {
					if src == "manual" {
						continue // manual wins
					}
					continue // already idp member
				}
				gtag, err := tx.Exec(ctx, upsertIDPGroupMembership,
					tenantID, gid, userID)
				if err != nil {
					return db.MapError(err)
				}
				if gtag.RowsAffected() == 0 {
					continue // a manual group membership raced in; it wins
				}
				out.Events = append(out.Events, users.SyncEvent{
					Action: "group.member.added", TenantID: tenantID,
					UserID: userID, GroupID: gid,
				})
			}
		}
		for _, g := range curGroups {
			if g.source != "idp" {
				continue
			}
			key := [2]uuid.UUID{g.tenantID, g.groupID}
			if desiredGroups[key] {
				continue
			}
			if _, err := tx.Exec(ctx,
				`DELETE FROM group_memberships
				 WHERE group_id=$1 AND user_id=$2 AND source='idp'`,
				g.groupID, userID); err != nil {
				return db.MapError(err)
			}
			out.Events = append(out.Events, users.SyncEvent{
				Action: "group.member.removed", TenantID: g.tenantID,
				UserID: userID, GroupID: g.groupID, Reason: "claim_absent",
			})
		}

		_, err = tx.Exec(ctx,
			`UPDATE users SET claims_hash=$2, claims_synced_at=$3 WHERE id=$1`,
			userID, hash, now)
		return err
	})
	if err != nil {
		return users.SyncOutcome{}, err
	}
	return out, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := map[string]int{}, map[string]int{}
	for _, s := range a {
		sa[s]++
	}
	for _, s := range b {
		sb[s]++
	}
	for k, v := range sa {
		if sb[k] != v {
			return false
		}
	}
	return true
}
