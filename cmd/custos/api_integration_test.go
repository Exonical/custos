package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/api"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authn/authntest"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/health"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
	tenantsvc "github.com/Exonical/custos/internal/tenants/service"
	"github.com/Exonical/custos/internal/users"
	userpg "github.com/Exonical/custos/internal/users/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testEnv(dsn string) map[string]string {
	return map[string]string{
		"CUSTOS_DEV_MODE":           "true",
		"CUSTOS_SERVER__TLS__MODE":  "disabled",
		"CUSTOS_METRICS__TLS__MODE": "disabled",
		"CUSTOS_DATABASE__URL":      dsn,
		"CUSTOS_DATABASE__SSL_MODE": "disable",
	}
}

func grantPlatformRole(t *testing.T, dsn, issuer, subject, role string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	env := testEnv(dsn)
	code := run(context.Background(),
		[]string{"admin", "platform-role", "grant",
			"--issuer", issuer, "--subject", subject, "--role", role},
		&out, &errBuf, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if code != 0 {
		t.Fatalf("admin grant exit %d: %s", code, errBuf.String())
	}
}

// TestAPIIntegration runs the full auth path against a real database
// and test IdP: CLI bootstrap of the first platform-admin, tenant
// creation, membership, and the tenant-isolation matrix rows.
func TestAPIIntegration(t *testing.T) {
	dsn := dbtest.URL(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	idp := authntest.New(t)

	// Bootstrap the first platform-admin through the CLI.
	grantPlatformRole(t, dsn, idp.Issuer, "admin-sub", "platform-admin")

	verifier, err := authn.NewOIDCVerifier(context.Background(), config.OIDC{
		Issuer:                 idp.Issuer,
		Audiences:              []string{"custos"},
		AllowedAlgorithms:      []string{"RS256", "ES256"},
		Discovery:              true,
		JWKSCacheTTL:           time.Hour,
		JWKSRefreshMinInterval: 30 * time.Second,
		ClockSkew:              time.Minute,
		AcceptedTokenTypes:     []string{"at+jwt", "JWT", ""},
		MaxTokenLifetime:       24 * time.Hour,
		Claims: config.OIDCClaims{
			Subject: "sub", Email: "email", Name: "name", Groups: "groups",
		},
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(verifier.Close)

	rec := audit.Multi{pgaudit.New(pool)}
	urepo := userpg.New(pool)
	trepo := tenantpg.New(pool)
	mux := http.NewServeMux()
	api.Mount(mux, api.Deps{
		Health:      health.NewRegistry(),
		ReadyBudget: time.Second,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier:    verifier,
		Provisioner: users.NewService(urepo, rec),
		Audit:       rec,
		Tenants:     tenantsvc.NewService(trepo, urepo, authz.RBAC{}, rec),
		TenantRepo:  trepo,
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	token := func(sub string) string {
		c := idp.Claims()
		c["sub"] = sub
		return idp.Token(t, c)
	}
	call := func(tok, method, path string, body any) (int, map[string]any) {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, srv.URL+path, rdr)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}

	adminTok := token("admin-sub")

	// /me for the admin: provisioned with platform-admin.
	code, me := call(adminTok, "GET", "/api/v1/me", nil)
	if code != 200 {
		t.Fatalf("me: %d %v", code, me)
	}
	if pr, ok := me["platform_roles"].([]any); !ok || len(pr) != 1 || pr[0] != "platform-admin" {
		t.Fatalf("platform_roles: %v", me)
	}

	// Create tenants A and B.
	var slugA, slugB string
	for _, slug := range []string{"tenant-a", "tenant-b"} {
		code, tresp := call(adminTok, "POST", "/api/v1/tenants",
			map[string]any{"slug": slug, "name": slug})
		if code != 201 {
			t.Fatalf("create %s: %d %v", slug, code, tresp)
		}
		if slug == "tenant-a" {
			slugA = slug
		} else {
			slugB = slug
		}
	}

	// Provision users a and b via /me, capture user ids.
	userID := func(sub string) string {
		code, m := call(token(sub), "GET", "/api/v1/me", nil)
		if code != 200 {
			t.Fatalf("me %s: %d %v", sub, code, m)
		}
		id, _ := m["user_id"].(string)
		return id
	}
	ua, ub := userID("user-a"), userID("user-b")

	// a joins A (viewer), b joins B.
	code, resp := call(adminTok, "POST", "/api/v1/tenants/"+slugA+"/members",
		map[string]any{"user_id": ua, "roles": []string{"viewer"}})
	if code != 201 {
		t.Fatalf("add a to A: %d %v", code, resp)
	}
	code, resp = call(adminTok, "POST", "/api/v1/tenants/"+slugB+"/members",
		map[string]any{"user_id": ub, "roles": []string{"researcher"}})
	if code != 201 {
		t.Fatalf("add b to B: %d %v", code, resp)
	}

	tokA := token("user-a")

	// Isolation matrix (tenant rows): a listing B's tenant → 404.
	if code, _ := call(tokA, "GET", "/api/v1/tenants/"+slugB, nil); code != 404 {
		t.Fatalf("a GET B: %d", code)
	}
	// a has no platform role → tenant list is 403.
	if code, _ := call(tokA, "GET", "/api/v1/tenants", nil); code != 403 {
		t.Fatalf("a list tenants: %d", code)
	}
	// a reads A fine.
	if code, _ := call(tokA, "GET", "/api/v1/tenants/"+slugA, nil); code != 200 {
		t.Fatalf("a GET A: %d", code)
	}
	// /me for a includes membership in A.
	code, meA := call(tokA, "GET", "/api/v1/me", nil)
	if code != 200 {
		t.Fatalf("me a: %d", code)
	}
	ms, _ := meA["memberships"].([]any)
	if len(ms) != 1 || ms[0].(map[string]any)["slug"] != slugA {
		t.Fatalf("memberships: %v", meA["memberships"])
	}

	// platform-auditor reads B but cannot mutate it.
	grantPlatformRole(t, dsn, idp.Issuer, "aud-sub", "platform-auditor")
	tokAud := token("aud-sub")
	if code, _ := call(tokAud, "GET", "/api/v1/tenants/"+slugB, nil); code != 200 {
		t.Fatalf("auditor GET B: %d", code)
	}
	code, resp = call(tokAud, "PATCH", "/api/v1/tenants/"+slugB,
		map[string]any{"name": "x", "version": 1})
	if code != 403 {
		t.Fatalf("auditor PATCH B: %d %v", code, resp)
	}

	// Audit rows exist for the bootstrap grant.
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action IN
		 ('platform_role.granted','tenant.created','membership.granted')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 3 {
		t.Fatalf("audit events = %d", n)
	}
}
