package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/api"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authn/authntest"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	clustersvc "github.com/Exonical/custos/internal/clusters/service"
	"github.com/Exonical/custos/internal/nodehooks"
	nodehookspg "github.com/Exonical/custos/internal/nodehooks/postgres"
	nodehookssvc "github.com/Exonical/custos/internal/nodehooks/service"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/health"
	"github.com/Exonical/custos/internal/platform/httpx"
	policypg "github.com/Exonical/custos/internal/policies/postgres"
	policiesvc "github.com/Exonical/custos/internal/policies/service"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
	tenantsvc "github.com/Exonical/custos/internal/tenants/service"
	"github.com/Exonical/custos/internal/users"
	userpg "github.com/Exonical/custos/internal/users/postgres"
)

func rawCall(t *testing.T, base, method, path string, hdr map[string]string, body []byte) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func untar(t *testing.T, gz []byte) map[string]string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

// TestAPINodeHooks covers the node-config admin API, warnings, bundle
// rendering against live bindings, node tokens and the node pull route.
func TestAPINodeHooks(t *testing.T) {
	dsn := dbtest.URL(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	idp := authntest.New(t)
	grantPlatformRole(t, dsn, idp.Issuer, "admin-sub", "platform-admin")

	verifier, err := authn.NewOIDCVerifier(context.Background(), config.OIDC{
		Issuer: idp.Issuer, Audiences: []string{"custos"},
		AllowedAlgorithms: []string{"RS256", "ES256"}, Discovery: true,
		JWKSCacheTTL: time.Hour, JWKSRefreshMinInterval: 30 * time.Second,
		ClockSkew: time.Minute, AcceptedTokenTypes: []string{"at+jwt", "JWT", ""},
		MaxTokenLifetime: 24 * time.Hour,
		Claims:           config.OIDCClaims{Subject: "sub", Email: "email", Name: "name", Groups: "groups"},
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
	projectSvc := projectsvc.NewService(projectRepo, projectRepo, projectRepo,
		trepo, clusterRepo, authz.RBAC{}, rec)
	policySvc := policiesvc.NewService(policypg.New(pool), authz.RBAC{}, rec)
	clusterSvc := clustersvc.New(clustersvc.Deps{
		Repository: clusterRepo, Tenants: trepo, Authorizer: authz.RBAC{}, Recorder: rec,
	})
	nodeSvc := nodehookssvc.New(nodehookssvc.Deps{
		Repo: nodehookspg.New(pool), Clusters: clusterRepo, Tenants: trepo,
		Bindings: projectRepo, AZ: authz.RBAC{}, Audit: rec,
	})

	mux := http.NewServeMux()
	api.Mount(mux, api.Deps{
		Health: health.NewRegistry(), ReadyBudget: time.Second,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier: verifier, Provisioner: users.NewService(urepo, rec, users.WithGroupsClaim("groups")),
		Audit: rec, Tenants: tenantsvc.NewService(trepo, trepo, trepo, urepo, authz.RBAC{}, rec),
		TenantRepo: trepo, Clusters: clusterSvc, Projects: projectSvc,
		ProjectRepo: projectRepo, ProjectMembers: projectRepo, Policies: policySvc,
		NodeHooks: nodeSvc, NodeLimiter: httpx.NewPrincipalRateLimiter(600, 120, 0),
		AZ: authz.RBAC{},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	token := func(sub string) string {
		c := idp.Claims()
		c["sub"] = sub
		return idp.Token(t, c)
	}
	adminTok := token("admin-sub")
	call := func(tok, method, path string, body any) (int, map[string]any) {
		return apiCall(t, srv.URL, tok, method, path, body, "")
	}
	ctx := context.Background()

	mkTenant := func(slug string) uuid.UUID {
		if code, resp := call(adminTok, "POST", "/api/v1/tenants",
			map[string]any{"slug": slug, "name": slug}); code != 201 {
			t.Fatalf("tenant %s: %d %v", slug, code, resp)
		}
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT id FROM tenants WHERE slug=$1`, slug).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tenantA, tenantB := mkTenant("nh-a"), mkTenant("nh-b")
	_, adminMe := call(adminTok, "GET", "/api/v1/me", nil)
	for _, slug := range []string{"nh-a", "nh-b"} {
		if code, resp := call(adminTok, "POST", "/api/v1/tenants/"+slug+"/members",
			map[string]any{"user_id": adminMe["user_id"], "roles": []string{"tenant-admin"}}); code != 201 {
			t.Fatalf("admin member %s: %d %v", slug, code, resp)
		}
	}

	cid := uuid.Must(uuid.NewV7())
	if err := clusterRepo.Create(ctx, clusters.Cluster{
		ID: cid, Name: "nhc1", DisplayName: "nhc1",
		BaseURL: "https://203.0.113.30/", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "tok"},
		Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	setVersion := func(v string) {
		if _, err := clusterRepo.RecordSyncResult(ctx, cid, clusters.SyncResult{OK: true, At: time.Now(),
			Capabilities: &slurm.Capabilities{SlurmVersion: v, APIVersion: "v0.0.45"}}); err != nil {
			t.Fatal(err)
		}
	}
	setVersion("26.05.4")
	for _, id := range []uuid.UUID{tenantA, tenantB} {
		if err := clusterRepo.UpsertAssignment(ctx, tenants.PlatformScope(), clusters.Assignment{
			ClusterID: cid, TenantID: id, Source: clusters.SourceManual}); err != nil {
			t.Fatal(err)
		}
	}
	for slug, acct := range map[string]string{"nh-a": "acct-a", "nh-b": "acct-b"} {
		if code, resp := call(adminTok, "POST", "/api/v1/tenants/"+slug+"/projects",
			map[string]any{"slug": "proj-1", "name": "proj-1"}); code != 201 {
			t.Fatalf("project: %d %v", code, resp)
		}
		if code, resp := call(adminTok, "POST", "/api/v1/tenants/"+slug+"/projects/proj-1/cluster-bindings",
			map[string]any{"cluster_id": cid.String(), "slurm_account": acct}); code != 201 {
			t.Fatalf("binding: %d %v", code, resp)
		}
	}

	nc := "/api/v1/clusters/nhc1/node-config"

	// Non-admin tenant member is denied.
	userTok := token("user-r")
	_, me := call(userTok, "GET", "/api/v1/me", nil)
	if code, resp := call(adminTok, "POST", "/api/v1/tenants/nh-a/members",
		map[string]any{"user_id": me["user_id"], "roles": []string{"tenant-admin"}}); code != 201 {
		t.Fatalf("member: %d %v", code, resp)
	}
	if code, _ := call(userTok, "GET", nc, nil); code != 403 {
		t.Fatalf("tenant-admin node-config GET: %d, want 403", code)
	}
	if code, _ := call(userTok, "PUT", nc, map[string]any{"config": map[string]any{}, "version": 0}); code != 403 {
		t.Fatalf("tenant-admin node-config PUT: %d, want 403", code)
	}

	// Defaults before configuration.
	code, got := call(adminTok, "GET", nc, nil)
	if code != 200 || got["revision"].(float64) != 0 || got["version"].(float64) != 0 {
		t.Fatalf("default config: %d %v", code, got)
	}

	cfg := func(extra func(map[string]any)) map[string]any {
		c := map[string]any{
			"isolation_mode": "namespace", "mount_timeout_seconds": 30,
			"shared_mounts": []any{map[string]any{"name": "apps", "fstype": "nfs4",
				"source": "nfs1.example.org:/hpc/apps", "target": "/apps",
				"options": []string{"ro", "nfsvers=4.2"}}},
			"tenant_mounts": []any{
				map[string]any{"tenant": tenantA.String(), "name": "data", "fstype": "nfs4",
					"source": "nfs2.example.org:/a/data", "target": "/mnt/data"},
				map[string]any{"tenant": tenantB.String(), "name": "data", "fstype": "nfs4",
					"source": "nfs2.example.org:/b/data", "target": "/mnt/data"},
			},
			"hooks": []any{map[string]any{"name": "metrics", "phase": "prolog", "order": 0,
				"script": "#!/bin/bash\necho SECRET-SCRIPT-BODY\n"}},
		}
		if extra != nil {
			extra(c)
		}
		return c
	}

	// Invalid configs.
	code, resp := call(adminTok, "PUT", nc, map[string]any{"version": 0, "config": cfg(func(c map[string]any) {
		c["shared_mounts"].([]any)[0].(map[string]any)["target"] = "/etc/x"
	})})
	if code != 422 || resp["error"].(map[string]any)["code"] != "NODE_MOUNT_INVALID" {
		t.Fatalf("reserved target: %d %v", code, resp)
	}
	code, resp = call(adminTok, "PUT", nc, map[string]any{"version": 0, "config": cfg(func(c map[string]any) {
		c["hooks"].([]any)[0].(map[string]any)["script"] = "echo no shebang\n"
	})})
	if code != 422 || resp["error"].(map[string]any)["code"] != "NODE_HOOK_INVALID" {
		t.Fatalf("hook shebang: %d %v", code, resp)
	}
	code, resp = call(adminTok, "PUT", nc, map[string]any{"version": 0, "config": cfg(func(c map[string]any) {
		c["tenant_mounts"].([]any)[0].(map[string]any)["tenant"] = uuid.NewString()
	})})
	if code != 422 || resp["error"].(map[string]any)["code"] != "NODE_MOUNT_INVALID" {
		t.Fatalf("unknown tenant: %d %v", code, resp)
	}
	code, _ = call(adminTok, "PUT", nc, map[string]any{"version": 0, "config": cfg(nil), "extra": 1})
	if code != 400 {
		t.Fatalf("unknown field: %d", code)
	}

	// Valid create, optimistic concurrency, revision bump.
	code, got = call(adminTok, "PUT", nc, map[string]any{"version": 0, "config": cfg(nil)})
	if code != 200 || got["revision"].(float64) != 1 || got["version"].(float64) != 1 {
		t.Fatalf("create config: %d %v", code, got)
	}
	if warns := got["warnings"].([]any); len(warns) != 1 ||
		warns[0].(map[string]any)["code"] != "SHARED_SERVICE_USER" {
		t.Fatalf("warnings = %v (want SHARED_SERVICE_USER only)", got["warnings"])
	}
	if code, resp = call(adminTok, "PUT", nc, map[string]any{"version": 0, "config": cfg(nil)}); code != 409 ||
		resp["error"].(map[string]any)["code"] != "VERSION_CONFLICT" {
		t.Fatalf("stale create: %d %v", code, resp)
	}
	code, got = call(adminTok, "PUT", nc, map[string]any{"version": 1, "config": cfg(func(c map[string]any) {
		c["isolation_mode"] = "tenant_exclusive"
	})})
	if code != 200 || got["revision"].(float64) != 2 || got["version"].(float64) != 2 {
		t.Fatalf("update config: %d %v", code, got)
	}
	if code, _ = call(adminTok, "PUT", nc, map[string]any{"version": 1, "config": cfg(nil)}); code != 409 {
		t.Fatalf("stale update: %d", code)
	}

	// Namespace mode warns on old/unknown Slurm.
	code, got = call(adminTok, "PUT", nc, map[string]any{"version": 2, "config": cfg(nil)})
	if code != 200 {
		t.Fatalf("back to namespace: %d %v", code, got)
	}
	setVersion("24.11.2")
	_, got = call(adminTok, "GET", nc, nil)
	found := false
	for _, w := range got["warnings"].([]any) {
		if w.(map[string]any)["code"] == "NAMESPACE_REQUIRES_SLURM_25_11" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing namespace warning: %v", got["warnings"])
	}
	setVersion("26.05.4")

	// Audit never carries script bodies.
	var details string
	if err := pool.QueryRow(ctx, `SELECT string_agg(details::text, ' ') FROM audit_events
		WHERE action='cluster.node_config.updated'`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if details == "" || strings.Contains(details, "SECRET-SCRIPT-BODY") {
		t.Fatalf("audit details missing or leaking script: %q", details)
	}

	// Bundle download reflects bindings.
	adminHdr := map[string]string{"Authorization": "Bearer " + adminTok}
	st, hdr, body := rawCall(t, srv.URL, "GET", nc+"/bundle", adminHdr, nil)
	if st != 200 || hdr.Get("Content-Type") != "application/gzip" ||
		hdr.Get("Content-Disposition") != `attachment; filename="custos-node-nhc1-r3.tar.gz"` {
		t.Fatalf("bundle: %d %v", st, hdr)
	}
	files := untar(t, body)
	tsv := files["etc/custos/node/mounts.tsv"]
	for _, want := range []string{"meta\trevision\t3\n", "meta\tmode\tnamespace\n",
		"account\tacct-a\tnh-a\tdata\tnfs4\tnfs2.example.org:/a/data\t/mnt/data\tnodev,nosuid,rw\n",
		"account\tacct-b\tnh-b\tdata\tnfs4\tnfs2.example.org:/b/data\t/mnt/data\tnodev,nosuid,rw\n"} {
		if !strings.Contains(tsv, want) {
			t.Fatalf("mounts.tsv lacks %q:\n%s", want, tsv)
		}
	}
	if _, ok := files["etc/custos/node/prolog.d/800-metrics"]; !ok {
		t.Fatalf("hook missing from bundle: %v", files)
	}
	if st, _, _ := rawCall(t, srv.URL, "GET", nc+"/bundle", map[string]string{"Authorization": "Bearer " + userTok}, nil); st != 403 {
		t.Fatalf("bundle as tenant-admin: %d", st)
	}

	// Node tokens and the pull route.
	tk := "/api/v1/clusters/nhc1/node-tokens"
	if code, _ = call(adminTok, "POST", tk, map[string]any{"name": "Bad Name"}); code != 422 {
		t.Fatalf("bad token name: %d", code)
	}
	code, created := call(adminTok, "POST", tk, map[string]any{"name": "rack1"})
	secret, _ := created["token"].(string)
	if code != 201 || !strings.HasPrefix(secret, "cnt_") || len(secret) < 40 {
		t.Fatalf("create token: %d %v", code, created)
	}
	code, list := call(adminTok, "GET", tk, nil)
	items := list["items"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["token"] != nil {
		t.Fatalf("list tokens: %d %v", code, list)
	}
	tokenID := items[0].(map[string]any)["id"].(string)
	if strings.Contains(string(mustJSON(list)), secret) {
		t.Fatal("token secret leaked in list")
	}

	pull := func(auth, node, inm string) (int, http.Header, []byte) {
		h := map[string]string{"X-Custos-Node": node}
		if auth != "" {
			h["Authorization"] = auth
		}
		if inm != "" {
			h["If-None-Match"] = inm
		}
		return rawCall(t, srv.URL, "GET", "/api/v1/node/bundle", h, nil)
	}
	for name, a := range map[string]string{"missing": "", "garbage": "Bearer nope", "oidc-ish": "Bearer " + adminTok, "wrong scheme": "Basic " + secret} {
		if st, _, _ := pull(a, "c1", ""); st != 401 {
			t.Fatalf("pull with %s credentials: %d", name, st)
		}
	}
	if st, _, _ := pull("Bearer "+secret, "bad node!", ""); st != 400 {
		t.Fatalf("bad node name: %d", st)
	}
	st, hdr, body = pull("Bearer "+secret, "c1", "")
	etag := strings.Trim(hdr.Get("ETag"), `"`)
	if st != 200 || len(etag) != 64 || hdr.Get("X-Custos-Revision") != "3" ||
		nodehooksSHA(body) != etag {
		t.Fatalf("pull: %d %v", st, hdr)
	}
	if st, hdr, _ = pull("Bearer "+secret, "c1", `"`+etag+`"`); st != 304 || hdr.Get("ETag") == "" {
		t.Fatalf("conditional pull: %d", st)
	}
	if st, _, _ = pull("Bearer "+secret, "c1", `"`+strings.Repeat("0", 64)+`"`); st != 200 {
		t.Fatalf("stale etag pull: %d", st)
	}
	// A node pulling the old revision shows up stale after a config change.
	if st, _, _ := pull("Bearer "+secret, "c2", ""); st != 200 {
		t.Fatalf("second node: %d", st)
	}
	code, _ = call(adminTok, "PUT", nc, map[string]any{"version": 3, "config": cfg(func(c map[string]any) {
		c["mount_timeout_seconds"] = 60
	})})
	if code != 200 {
		t.Fatalf("update after pulls: %d", code)
	}
	code, status := call(adminTok, "GET", "/api/v1/clusters/nhc1/node-status", nil)
	rows := status["items"].([]any)
	if code != 200 || len(rows) != 2 {
		t.Fatalf("node status: %d %v", code, status)
	}
	for _, r := range rows {
		m := r.(map[string]any)
		if m["revision"].(float64) != 3 || m["stale"] != true || len(m["bundle_sha256"].(string)) != 64 {
			t.Fatalf("node status row = %v", m)
		}
	}
	if status["current_revision"].(float64) != 4 || len(status["current_bundle_sha256"].(string)) != 64 {
		t.Fatalf("current bundle fields: %v", status)
	}
	if rows[0].(map[string]any)["bundle_sha256"] != etag {
		t.Fatalf("status bundle_sha256 = %v, want first pulled etag %s", rows[0], etag)
	}
	// Re-pulling brings both nodes current; a new binding then makes them
	// stale without any revision bump.
	for _, n := range []string{"c1", "c2"} {
		if st, _, _ := pull("Bearer "+secret, n, ""); st != 200 {
			t.Fatalf("repull %s: %d", n, st)
		}
	}
	_, status = call(adminTok, "GET", "/api/v1/clusters/nhc1/node-status", nil)
	for _, r := range status["items"].([]any) {
		if r.(map[string]any)["stale"] != false {
			t.Fatalf("node should be current after pull: %v", r)
		}
	}
	if code, resp := call(adminTok, "POST", "/api/v1/tenants/nh-a/projects",
		map[string]any{"slug": "proj-2", "name": "proj-2"}); code != 201 {
		t.Fatalf("project p2: %d %v", code, resp)
	}
	if code, resp := call(adminTok, "POST", "/api/v1/tenants/nh-a/projects/proj-2/cluster-bindings",
		map[string]any{"cluster_id": cid.String(), "slurm_account": "acct-a-new"}); code != 201 {
		t.Fatalf("binding p2: %d %v", code, resp)
	}
	_, status = call(adminTok, "GET", "/api/v1/clusters/nhc1/node-status", nil)
	if status["current_revision"].(float64) != 4 {
		t.Fatalf("revision must not change on binding change: %v", status)
	}
	for _, r := range status["items"].([]any) {
		if r.(map[string]any)["stale"] != true {
			t.Fatalf("node should be stale after new binding: %v", r)
		}
	}
	var used *time.Time
	if err := pool.QueryRow(ctx, `SELECT last_used_at FROM cluster_node_tokens WHERE id=$1`, tokenID).Scan(&used); err != nil || used == nil {
		t.Fatalf("last_used_at not set: %v %v", used, err)
	}

	// Revocation.
	if code, _ = call(adminTok, "DELETE", tk+"/"+tokenID, nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if st, _, _ := pull("Bearer "+secret, "c1", ""); st != 401 {
		t.Fatalf("revoked token pull: %d", st)
	}
	if code, _ = call(adminTok, "DELETE", tk+"/"+uuid.NewString(), nil); code != 404 {
		t.Fatalf("revoke unknown: %d", code)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func nodehooksSHA(b []byte) string { return nodehooks.BundleSHA256(b) }
