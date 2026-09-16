package tenants_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func withPrincipal(p authn.Principal, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(authn.WithPrincipal(r.Context(), p)))
	})
}

func TestRequire404Identical(t *testing.T) {
	repo := tenantpg.New(dbtest.Pool(t))
	ctx := context.Background()

	tn := tenants.Tenant{
		ID: uuid.Must(uuid.NewV7()), Slug: "hideme",
		Name: "Hidden", State: tenants.StateActive, Settings: map[string]any{},
	}
	if err := repo.Create(ctx, tn); err != nil {
		t.Fatal(err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := tenants.Require(repo, discard, nil)

	// Non-member principal (no platform roles).
	p := authn.Principal{UserID: uuid.Must(uuid.NewV7())}
	mux := http.NewServeMux()
	mux.Handle("GET /t/{tenant}", withPrincipal(p, mw(handler)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func(path string) (int, []byte) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	codeB, bodyB := get("/t/hideme")         // exists, but p is not a member
	codeX, bodyX := get("/t/does-not-exist") // does not exist
	if codeB != 404 || codeX != 404 {
		t.Fatalf("codes %d %d", codeB, codeX)
	}
	// Bodies must be identical except request_id (both empty here — no
	// RequestID middleware in this mux).
	if string(bodyB) != string(bodyX) {
		t.Fatalf("non-member 404 differs from nonexistent 404:\n%s\n%s", bodyB, bodyX)
	}
}

func TestRequirePlatformWithoutMembership(t *testing.T) {
	repo := tenantpg.New(dbtest.Pool(t))
	ctx := context.Background()

	tn := tenants.Tenant{
		ID: uuid.Must(uuid.NewV7()), Slug: "plat",
		Name: "Plat", State: tenants.StateSuspended, Settings: map[string]any{},
	}
	if err := repo.Create(ctx, tn); err != nil {
		t.Fatal(err)
	}

	var got tenants.TenantContext
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = tenants.MustTenantContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	p := authn.Principal{UserID: uuid.Must(uuid.NewV7()), PlatformRoles: []string{"platform-auditor"}}
	mux := http.NewServeMux()
	mux.Handle("GET /t/{tenant}", withPrincipal(p, tenants.Require(repo, discard, nil)(handler)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/t/plat")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("platform-auditor without membership: %d", resp.StatusCode)
	}
	if got.Membership != nil || got.Tenant.ID != tn.ID || got.Tenant.State != tenants.StateSuspended {
		t.Fatalf("ctx = %+v", got)
	}
}
