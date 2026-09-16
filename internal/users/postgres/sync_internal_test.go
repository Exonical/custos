package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/platform/db/dbtest"
)

// A manual membership must survive the idp upsert verbatim: a tenant
// admin's concurrent grant (taken without the user-row lock the sync
// holds) is never flipped to source='idp'.
func TestUpsertGuardsManualSource(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	userID, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, issuer, subject, kind)
		 VALUES ($1,'iss','sub','user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants (id, slug, name, state)
		 VALUES ($1,$2,'T','active')`,
		tenantID, "t-"+tenantID.String()[24:]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant_memberships (tenant_id, user_id, roles, source)
		 VALUES ($1,$2,'{tenant-admin}','manual')`,
		tenantID, userID); err != nil {
		t.Fatal(err)
	}

	// The exact statement SyncIDPClaims runs: conflict on the manual row
	// must be a no-op.
	tag, err := pool.Exec(ctx, upsertIDPMembership,
		tenantID, userID, []string{"viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatal("manual tenant membership was overwritten by idp upsert")
	}
	var source string
	var roles []string
	if err := pool.QueryRow(ctx,
		`SELECT source, roles FROM tenant_memberships
		 WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).
		Scan(&source, &roles); err != nil {
		t.Fatal(err)
	}
	if source != "manual" || len(roles) != 1 || roles[0] != "tenant-admin" {
		t.Fatalf("membership mutated: source=%s roles=%v", source, roles)
	}

	// Same guard for group memberships.
	groupID := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx,
		`INSERT INTO groups (id, tenant_id, name, source)
		 VALUES ($1,$2,'g','manual')`, groupID, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO group_memberships (tenant_id, group_id, user_id, source)
		 VALUES ($1,$2,$3,'manual')`, tenantID, groupID, userID); err != nil {
		t.Fatal(err)
	}
	tag, err = pool.Exec(ctx, upsertIDPGroupMembership, tenantID, groupID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatal("manual group membership was overwritten by idp upsert")
	}
	if err := pool.QueryRow(ctx,
		`SELECT source FROM group_memberships
		 WHERE group_id=$1 AND user_id=$2`, groupID, userID).
		Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "manual" {
		t.Fatalf("group membership source = %s", source)
	}
}
