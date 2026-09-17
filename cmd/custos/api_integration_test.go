package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/api"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authn/authntest"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	clustersvc "github.com/Exonical/custos/internal/clusters/service"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/health"
	"github.com/Exonical/custos/internal/platform/workqueue"
	policypg "github.com/Exonical/custos/internal/policies/postgres"
	policiesvc "github.com/Exonical/custos/internal/policies/service"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm/httpclient"
	"github.com/Exonical/custos/internal/slurm/slinky"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
	tenantsvc "github.com/Exonical/custos/internal/tenants/service"
	"github.com/Exonical/custos/internal/users"
	userpg "github.com/Exonical/custos/internal/users/postgres"
	"github.com/google/uuid"
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
		Provisioner: users.NewService(urepo, rec,
			users.WithGroupsClaim("groups")),
		Audit:      rec,
		Tenants:    tenantsvc.NewService(trepo, trepo, trepo, urepo, authz.RBAC{}, rec),
		TenantRepo: trepo,
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

	// --- Claim reconciliation -----------------------------------------
	// Rule in A: IdP group "hpc-a" -> viewer.
	code, resp = call(adminTok, "POST", "/api/v1/tenants/"+slugA+"/claim-rules",
		map[string]any{"claim": "groups", "match_value": "hpc-a",
			"roles": []string{"viewer"}})
	if code != 201 {
		t.Fatalf("create claim rule: %d %v", code, resp)
	}

	// Token carrying the group grants membership on first request.
	c := idp.Claims()
	c["sub"] = "user-c"
	c["groups"] = []string{"hpc-a"}
	code, meC := call(idp.Token(t, c), "GET", "/api/v1/me", nil)
	if code != 200 {
		t.Fatalf("me c: %d %v", code, meC)
	}
	ms, _ = meC["memberships"].([]any)
	if len(ms) != 1 {
		t.Fatalf("c memberships: %v", meC["memberships"])
	}
	m0, _ := ms[0].(map[string]any)
	roles, _ := m0["roles"].([]any)
	if m0["slug"] != slugA || len(roles) != 1 || roles[0] != "viewer" {
		t.Fatalf("c membership: %v", m0)
	}
	var tenantAID string
	tenantAID, _ = m0["tenant_id"].(string)

	// Same user without the group: claim absent -> idp membership
	// revoked on next sync (hash differs -> resync runs).
	delete(c, "groups")
	code, meC = call(idp.Token(t, c), "GET", "/api/v1/me", nil)
	if code != 200 {
		t.Fatalf("me c (no group): %d", code)
	}
	if ms, _ := meC["memberships"].([]any); len(ms) != 0 {
		t.Fatalf("c memberships after claim removal: %v", ms)
	}
	_ = tenantAID

	// --- Tenant deletion ----------------------------------------------
	// Capture B's id before deletion for the worker item key.
	code, tB := call(tokAud, "GET", "/api/v1/tenants/"+slugB, nil)
	if code != 200 {
		t.Fatalf("get B: %d", code)
	}
	bid, _ := tB["id"].(string)

	tokB := token("user-b")
	if code, _ := call(adminTok, "DELETE", "/api/v1/tenants/"+slugB, nil); code != 202 {
		t.Fatalf("delete B: %d", code)
	}
	// Deleting is invisible to members, visible to platform roles.
	if code, _ := call(tokB, "GET", "/api/v1/tenants/"+slugB, nil); code != 404 {
		t.Fatalf("b GET deleting B: %d", code)
	}
	code, tB = call(adminTok, "GET", "/api/v1/tenants/"+slugB, nil)
	if code != 200 || tB["state"] != "deleting" {
		t.Fatalf("admin GET deleting B: %d %v", code, tB)
	}

	// Run the worker handler directly on the enqueued key.
	if err := tenantDeleteHandler(pool, rec)(context.Background(),
		workqueue.Item{Key: "tenant:" + bid}); err != nil {
		t.Fatal(err)
	}
	code, tB = call(adminTok, "GET", "/api/v1/tenants/"+slugB, nil)
	if code != 200 || tB["state"] != "deleted" {
		t.Fatalf("admin GET deleted B: %d %v", code, tB)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tenant_memberships WHERE tenant_id=$1`,
		bid).Scan(&n); err != nil || n != 0 {
		t.Fatalf("B memberships after purge: n=%d err=%v", n, err)
	}
}

// TestAPIClusters covers the M3-B cluster registry API: admin registers a
// cluster against a TLS v0045 stub, tests the connection, assigns it to
// tenant A, and verifies tenant-visible summaries and SSRF rejection.
func TestAPIClusters(t *testing.T) {
	dsn := dbtest.URL(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	idp := authntest.New(t)

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
	clusterRepo := clusterpg.New(pool)

	// Secret resolver: a real file provider rooted at a temp dir.
	secretRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(secretRoot, "slurm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretRoot, "slurm", "token"),
		[]byte("tok123"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileResolver, err := secrets.NewFileResolver(secretRoot)
	if err != nil {
		t.Fatal(err)
	}
	resolver := secrets.Multi{"file": fileResolver}
	policy := httpclient.DialPolicy{AllowLoopback: true}
	factory := slinky.NewFactory(resolver, policy)
	clusterSvc := clustersvc.New(clustersvc.Deps{
		Repository: clusterRepo, Tenants: trepo, Factory: factory,
		Authorizer: authz.RBAC{}, Recorder: rec,
		DialPolicy: policy, Resolver: resolver, Enqueuer: pool,
	})

	mux := http.NewServeMux()
	api.Mount(mux, api.Deps{
		Health:      health.NewRegistry(),
		ReadyBudget: time.Second,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier:    verifier,
		Provisioner: users.NewService(urepo, rec, users.WithGroupsClaim("groups")),
		Audit:       rec,
		Tenants:     tenantsvc.NewService(trepo, trepo, trepo, urepo, authz.RBAC{}, rec),
		TenantRepo:  trepo,
		Clusters:    clusterSvc,
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// TLS v0045 slurmrestd stub serving the conformance fixtures.
	fixtureDir := filepath.Join("..", "..", "internal", "slurm", "slinky", "v0045", "testdata")
	stubMux := map[string]string{
		"/slurm/v0.0.45/ping/":         "ping.json",
		"/slurm/v0.0.45/partitions/":   "partitions.json",
		"/slurm/v0.0.45/nodes/":        "nodes.json",
		"/slurm/v0.0.45/reservations/": "reservations.json",
		"/slurm/v0.0.45/diag/":         "diag.json",
	}
	slurmStub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx, ok := stubMux[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join(fixtureDir, fx))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	defer slurmStub.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: slurmStub.Certificate().Raw}))

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

	// Tenants A/B and users a/b (a joins A).
	for _, slug := range []string{"c-tenant-a", "c-tenant-b"} {
		code, resp := call(adminTok, "POST", "/api/v1/tenants",
			map[string]any{"slug": slug, "name": slug})
		if code != 201 {
			t.Fatalf("create %s: %d %v", slug, code, resp)
		}
	}
	userID := func(sub string) string {
		code, m := call(token(sub), "GET", "/api/v1/me", nil)
		if code != 200 {
			t.Fatalf("me %s: %d %v", sub, code, m)
		}
		id, _ := m["user_id"].(string)
		return id
	}
	ua, ub := userID("user-a"), userID("user-b")
	code, resp := call(adminTok, "POST", "/api/v1/tenants/c-tenant-a/members",
		map[string]any{"user_id": ua, "roles": []string{"viewer"}})
	if code != 201 {
		t.Fatalf("add a: %d %v", code, resp)
	}
	code, resp = call(adminTok, "POST", "/api/v1/tenants/c-tenant-b/members",
		map[string]any{"user_id": ub, "roles": []string{"viewer"}})
	if code != 201 {
		t.Fatalf("add b: %d %v", code, resp)
	}
	tokA, tokB := token("user-a"), token("user-b")

	// Register the cluster against the TLS stub. token_ref.path is
	// absolute — the file resolver requires paths inside a root.
	tokenPath := filepath.Join(secretRoot, "slurm", "token")
	createBody := map[string]any{
		"name": "main", "display_name": "Main Cluster",
		"base_url": slurmStub.URL, "api_version": "v0.0.45",
		"ca_bundle_pem": caPEM,
		"identity_mode": "service", "service_user": "custos",
		"token_ref":  map[string]any{"provider": "file", "path": tokenPath},
		"visibility": "assigned",
	}
	code, created := call(adminTok, "POST", "/api/v1/clusters", createBody)
	if code != 201 {
		t.Fatalf("register: %d %v", code, created)
	}
	// platform DTO exposes token_ref (a reference), never a value
	tr, _ := created["token_ref"].(map[string]any)
	if tr["provider"] != "file" || tr["path"] != tokenPath {
		t.Fatalf("token_ref: %v", tr)
	}
	for k := range created {
		if strings.Contains(strings.ToLower(k), "token") && k != "token_ref" {
			t.Fatalf("secret-looking key in DTO: %s", k)
		}
	}

	// Test connection: opens the adapter against the stub.
	code, conn := call(adminTok, "POST", "/api/v1/clusters/main/test-connection", nil)
	if code != 200 {
		t.Fatalf("test-connection: %d %v", code, conn)
	}
	if conn["ok"] != true {
		t.Fatalf("connection not ok: %v", conn)
	}
	parts, _ := conn["partitions"].([]any)
	if len(parts) == 0 {
		t.Fatalf("no partitions: %v", conn)
	}

	// Assign to tenant A.
	code, resp = call(adminTok, "PUT", "/api/v1/clusters/main/tenants/c-tenant-a",
		map[string]any{"defaults": map[string]any{}})
	if code != 204 {
		t.Fatalf("assign: %d %v", code, resp)
	}

	// Tenant views: a sees 1 summary (no platform config keys), b sees 0.
	code, listA := call(tokA, "GET", "/api/v1/tenants/c-tenant-a/clusters", nil)
	if code != 200 {
		t.Fatalf("a list clusters: %d %v", code, listA)
	}
	items, _ := listA["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("a sees %d clusters, want 1", len(items))
	}
	sum, _ := items[0].(map[string]any)
	for _, k := range []string{"base_url", "token_ref", "ca_bundle_pem",
		"client_cert_ref", "service_user", "identity_mode"} {
		if _, ok := sum[k]; ok {
			t.Fatalf("tenant summary leaks platform key %q: %v", k, sum)
		}
	}
	code, listB := call(tokB, "GET", "/api/v1/tenants/c-tenant-b/clusters", nil)
	if code != 200 {
		t.Fatalf("b list clusters: %d %v", code, listB)
	}
	if n, _ := listB["items"].([]any); len(n) != 0 {
		t.Fatalf("b sees %d clusters, want 0", len(n))
	}

	// Partitions visible to A.
	code, pl := call(tokA, "GET",
		"/api/v1/tenants/c-tenant-a/clusters/main/partitions", nil)
	if code != 200 {
		t.Fatalf("a partitions: %d %v", code, pl)
	}
	// Empty until the first cluster.sync run — the endpoint must answer.
	if _, ok := pl["items"]; !ok {
		t.Fatalf("partitions response: %v", pl)
	}

	// SSRF: metadata endpoint and plaintext http are rejected with 422.
	code, e := call(adminTok, "POST", "/api/v1/clusters", map[string]any{
		"name": "evil", "display_name": "x", "base_url": "https://169.254.169.254/",
		"api_version": "v0.0.45", "identity_mode": "service",
		"service_user": "custos",
		"token_ref":    map[string]any{"provider": "file", "path": tokenPath},
		"visibility":   "assigned",
	})
	if code != 422 {
		t.Fatalf("metadata ip: %d %v", code, e)
	}
	if ec, _ := e["error"].(map[string]any); ec["code"] != "slurm.dial_denied" {
		t.Fatalf("want slurm.dial_denied, got %v", e)
	}
	code, e = call(adminTok, "POST", "/api/v1/clusters", map[string]any{
		"name": "evil2", "display_name": "x", "base_url": "http://203.0.113.10/",
		"api_version": "v0.0.45", "identity_mode": "service",
		"service_user": "custos",
		"token_ref":    map[string]any{"provider": "file", "path": tokenPath},
		"visibility":   "assigned",
	})
	if code != 422 {
		t.Fatalf("http endpoint: %d %v", code, e)
	}
}

// TestAPIProjects exercises the project/member/binding/policy API: a
// tenant-admin creates a project, adds a project member, creates a
// cluster binding under assignment constraints, and resource policies
// intersect tenant ∩ project.
func TestAPIProjects(t *testing.T) {
	dsn := dbtest.URL(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	idp := authntest.New(t)
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
	clusterRepo := clusterpg.New(pool)
	projectRepo := projectpg.New(pool)

	mux := http.NewServeMux()
	api.Mount(mux, api.Deps{
		Health:      health.NewRegistry(),
		ReadyBudget: time.Second,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier:    verifier,
		Provisioner: users.NewService(urepo, rec, users.WithGroupsClaim("groups")),
		Audit:       rec,
		Tenants:     tenantsvc.NewService(trepo, trepo, trepo, urepo, authz.RBAC{}, rec),
		TenantRepo:  trepo,
		Projects: projectsvc.NewService(projectRepo, projectRepo, projectRepo,
			trepo, clusterRepo, authz.RBAC{}, rec),
		ProjectRepo:    projectRepo,
		ProjectMembers: projectRepo,
		Policies:       policiesvc.NewService(policypg.New(pool), authz.RBAC{}, rec),
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
	for _, slug := range []string{"p-tenant-a", "p-tenant-b"} {
		code, resp := call(adminTok, "POST", "/api/v1/tenants",
			map[string]any{"slug": slug, "name": slug})
		if code != 201 {
			t.Fatalf("create %s: %d %v", slug, code, resp)
		}
	}
	userID := func(sub string) string {
		code, m := call(token(sub), "GET", "/api/v1/me", nil)
		if code != 200 {
			t.Fatalf("me %s: %d %v", sub, code, m)
		}
		id, _ := m["user_id"].(string)
		return id
	}
	ua, ur, ub := userID("user-a"), userID("user-r"), userID("user-b")
	for slug, m := range map[string]map[string]any{
		"p-tenant-a": {"user_id": ua, "roles": []string{"tenant-admin"}},
		"p-tenant-b": {"user_id": ub, "roles": []string{"viewer"}},
	} {
		if code, resp := call(adminTok, "POST", "/api/v1/tenants/"+slug+"/members", m); code != 201 {
			t.Fatalf("member %s: %d %v", slug, code, resp)
		}
	}
	if code, resp := call(adminTok, "POST", "/api/v1/tenants/p-tenant-a/members",
		map[string]any{"user_id": ur, "roles": []string{"researcher"}}); code != 201 {
		t.Fatalf("member r: %d %v", code, resp)
	}
	tokA, tokR, tokB := token("user-a"), token("user-r"), token("user-b")

	// Cluster assigned to tenant A with partition + account-prefix
	// constraints (created via the repo — registration API is covered
	// by TestAPIClusters).
	cid := uuid.Must(uuid.NewV7())
	if err := clusterRepo.Create(context.Background(), clusters.Cluster{
		ID: cid, Name: "pc1", DisplayName: "pc1",
		BaseURL: "https://203.0.113.10/", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "tok"},
		Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	var tidA uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM tenants WHERE slug='p-tenant-a'`).Scan(&tidA); err != nil {
		t.Fatal(err)
	}
	if err := clusterRepo.UpsertAssignment(context.Background(), tenants.PlatformScope(),
		clusters.Assignment{ClusterID: cid, TenantID: tidA, Source: clusters.SourceManual,
			Defaults: clusters.AssignmentDefaults{
				AllowedPartitions:    []string{"gpu", "batch"},
				DefaultAccountPrefix: "acct-",
			}}); err != nil {
		t.Fatal(err)
	}

	// Project lifecycle.
	code, pr := call(tokA, "POST", "/api/v1/tenants/p-tenant-a/projects",
		map[string]any{"slug": "proj1", "name": "Project 1"})
	if code != 201 {
		t.Fatalf("create project: %d %v", code, pr)
	}
	pid, _ := pr["id"].(string)
	if pr["slug"] != "proj1" || pr["state"] != "active" {
		t.Fatalf("project dto: %v", pr)
	}
	// Creator is a project-admin member (auto-membership).
	code, ml := call(tokA, "GET", "/api/v1/tenants/p-tenant-a/projects/proj1/members", nil)
	if code != 200 || len(ml["items"].([]any)) != 1 {
		t.Fatalf("members: %d %v", code, ml)
	}

	// Add researcher r as project-member.
	code, mr := call(tokA, "POST", "/api/v1/tenants/p-tenant-a/projects/proj1/members",
		map[string]any{"user_id": ur, "roles": []string{"project-member"}})
	if code != 201 {
		t.Fatalf("add member: %d %v", code, mr)
	}
	// r sees exactly one project; r's /me shows the membership.
	code, pl := call(tokR, "GET", "/api/v1/tenants/p-tenant-a/projects", nil)
	if code != 200 || len(pl["items"].([]any)) != 1 {
		t.Fatalf("r list: %d %v", code, pl)
	}
	code, me := call(tokR, "GET", "/api/v1/me", nil)
	if code != 200 {
		t.Fatalf("r me: %d", code)
	}
	pms, _ := me["project_memberships"].([]any)
	if len(pms) != 1 {
		t.Fatalf("project_memberships: %v", me)
	}

	// Bindings under assignment constraints.
	code, br := call(tokA, "POST",
		"/api/v1/tenants/p-tenant-a/projects/proj1/cluster-bindings",
		map[string]any{"cluster_id": cid.String(), "slurm_account": "acct-p",
			"allowed_partitions": []string{"gpu"}, "default_partition": "gpu"})
	if code != 201 {
		t.Fatalf("create binding: %d %v", code, br)
	}
	code, er := call(tokA, "POST",
		"/api/v1/tenants/p-tenant-a/projects/proj1/cluster-bindings",
		map[string]any{"cluster_id": uuid.Must(uuid.NewV7()).String(),
			"slurm_account": "acct-x"})
	if code != 422 {
		t.Fatalf("unassigned cluster: %d %v", code, er)
	}
	cid2 := uuid.Must(uuid.NewV7())
	if err := clusterRepo.Create(context.Background(), clusters.Cluster{
		ID: cid2, Name: "pc2", DisplayName: "pc2",
		BaseURL: "https://203.0.113.11/", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "tok"},
		Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := clusterRepo.UpsertAssignment(context.Background(), tenants.PlatformScope(),
		clusters.Assignment{ClusterID: cid2, TenantID: tidA, Source: clusters.SourceManual,
			Defaults: clusters.AssignmentDefaults{AllowedPartitions: []string{"batch"},
				DefaultAccountPrefix: "acct-"}}); err != nil {
		t.Fatal(err)
	}
	code, er = call(tokA, "POST",
		"/api/v1/tenants/p-tenant-a/projects/proj1/cluster-bindings",
		map[string]any{"cluster_id": cid2.String(), "slurm_account": "acct-x",
			"allowed_partitions": []string{"restricted"}})
	if code != 422 {
		t.Fatalf("restricted partition: %d %v", code, er)
	}
	code, er = call(tokA, "POST",
		"/api/v1/tenants/p-tenant-a/projects/proj1/cluster-bindings",
		map[string]any{"cluster_id": cid2.String(), "slurm_account": "other-x"})
	if code != 422 {
		t.Fatalf("account prefix: %d %v", code, er)
	}

	// Cross-tenant isolation: b (tenant B) gets 404 for A's project.
	if code, _ := call(tokB, "GET", "/api/v1/tenants/p-tenant-a/projects/proj1", nil); code != 404 {
		t.Fatalf("b GET A project: %d", code)
	}

	// Resource policies: tenant max_gpus 4, project max_gpus 8 →
	// effective 4.
	code, tp := call(tokA, "PUT", "/api/v1/tenants/p-tenant-a/policies/resource",
		map[string]any{"policy": map[string]any{"max_gpus_per_job": 4}})
	if code != 200 {
		t.Fatalf("tenant policy: %d %v", code, tp)
	}
	code, pp := call(tokA, "PUT",
		"/api/v1/tenants/p-tenant-a/projects/proj1/policies/resource",
		map[string]any{"policy": map[string]any{"max_gpus_per_job": 8}})
	if code != 200 {
		t.Fatalf("project policy: %d %v", code, pp)
	}
	code, gp := call(tokA, "GET",
		"/api/v1/tenants/p-tenant-a/projects/proj1/policies/resource", nil)
	if code != 200 {
		t.Fatalf("get project policy: %d %v", code, gp)
	}
	eff, _ := gp["effective"].(map[string]any)
	if eff["max_gpus_per_job"] != float64(4) {
		t.Fatalf("effective: %v", gp)
	}
	// A project-member can read the project but not set policy.
	if code, _ := call(tokR, "PUT",
		"/api/v1/tenants/p-tenant-a/projects/proj1/policies/resource",
		map[string]any{"policy": map[string]any{}}); code != 403 {
		t.Fatalf("member set policy: %d", code)
	}
	// The project id also resolves by uuid.
	if code, _ := call(tokR, "GET", "/api/v1/tenants/p-tenant-a/projects/"+pid, nil); code != 200 {
		t.Fatalf("get by uuid: %d", code)
	}
}
