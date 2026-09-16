package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/users"
	userpg "github.com/Exonical/custos/internal/users/postgres"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestUpsertAndRoles(t *testing.T) {
	repo := userpg.New(dbtest.Pool(t))
	ctx := context.Background()

	u := users.User{
		Issuer: "https://idp", Subject: "s1",
		Kind: "user", Email: "a@x.io", DisplayName: "A",
	}
	got, created, err := repo.UpsertByIdentity(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if !created || got.ID == uuid.Nil || got.Email != "a@x.io" {
		t.Fatalf("first upsert: %+v created=%v", got, created)
	}
	firstSeen := got.LastSeenAt

	// Same identity with a changed name updates; created=false.
	u.DisplayName = "A2"
	got2, created, err := repo.UpsertByIdentity(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if created || got2.DisplayName != "A2" || got2.ID != got.ID {
		t.Fatalf("second upsert: %+v created=%v", got2, created)
	}
	_ = firstSeen // may legitimately bump once (was NULL on first row)

	// Roles.
	if err := repo.GrantPlatformRole(ctx, got.ID, "platform-auditor", nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.GrantPlatformRole(ctx, got.ID, "platform-auditor", nil); err != nil {
		t.Fatalf("idempotent grant: %v", err)
	}
	roles, err := repo.PlatformRoles(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 || roles[0] != "platform-auditor" {
		t.Fatalf("roles = %v", roles)
	}
	bs, err := repo.ListPlatformRoleBindings(ctx)
	if err != nil || len(bs) != 1 {
		t.Fatalf("bindings: %v %v", bs, err)
	}
	if err := repo.RevokePlatformRole(ctx, got.ID, "platform-auditor"); err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokePlatformRole(ctx, got.ID, "platform-auditor"); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want NotFound, got %v", err)
	}

	// FindByEmail is case-insensitive.
	found, err := repo.FindByEmail(ctx, "A@X.IO")
	if err != nil || len(found) != 1 || found[0].ID != got.ID {
		t.Fatalf("find: %v %v", found, err)
	}
	if _, err := repo.GetByID(ctx, uuid.Must(uuid.NewV7())); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want NotFound, got %v", err)
	}
}
