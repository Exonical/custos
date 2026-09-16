package users_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/users"
	userpg "github.com/Exonical/custos/internal/users/postgres"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type fakeRec struct {
	mu     sync.Mutex
	events []audit.Event
}

func (f *fakeRec) Record(_ context.Context, e audit.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func TestProvision(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := userpg.New(pool)
	rec := &fakeRec{}
	svc := users.NewService(repo, rec)
	ctx := context.Background()

	p := authn.Principal{
		Issuer: "https://idp", Subject: "s1", Kind: authn.KindUser,
		Email: "a@x.io", Name: "A",
	}
	out, err := svc.Provision(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if out.UserID == uuid.Nil {
		t.Fatal("UserID not filled")
	}
	if len(rec.events) != 1 || rec.events[0].Action != "user.provisioned" ||
		rec.events[0].Actor.ID != out.UserID.String() {
		t.Fatalf("audit: %+v", rec.events)
	}

	// Second provision: no new audit event.
	if _, err := svc.Provision(ctx, p); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("events = %d", len(rec.events))
	}

	// Platform roles propagate onto the principal.
	if err := repo.GrantPlatformRole(ctx, out.UserID, "platform-admin", nil); err != nil {
		t.Fatal(err)
	}
	out, err = svc.Provision(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.PlatformRoles) != 1 || out.PlatformRoles[0] != "platform-admin" {
		t.Fatalf("roles = %v", out.PlatformRoles)
	}
}
