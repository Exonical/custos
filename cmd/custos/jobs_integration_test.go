package main

import (
	"bytes"
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
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	jobssvc "github.com/Exonical/custos/internal/jobs/service"
	jobsworker "github.com/Exonical/custos/internal/jobs/worker"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/health"
	"github.com/Exonical/custos/internal/platform/workqueue"
	policypg "github.com/Exonical/custos/internal/policies/postgres"
	policiesvc "github.com/Exonical/custos/internal/policies/service"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	scriptpg "github.com/Exonical/custos/internal/scripts/postgres"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/fake"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
	tenantsvc "github.com/Exonical/custos/internal/tenants/service"
	"github.com/Exonical/custos/internal/users"
	userpg "github.com/Exonical/custos/internal/users/postgres"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/envcheck"
	"github.com/Exonical/custos/internal/validation/pipeline"
	vpolicy "github.com/Exonical/custos/internal/validation/policy"
	vpolicypg "github.com/Exonical/custos/internal/validation/postgres"
	"github.com/Exonical/custos/internal/validation/sbatchscan"
	"github.com/Exonical/custos/internal/validation/shsyntax"
)

type fakeFactory struct{ c *fake.Cluster }

func (f fakeFactory) Open(context.Context, slurm.ClusterConfig) (slurm.Cluster, slurm.Accounting, error) {
	return f.c, f.c, nil
}

// TestAPIJobs exercises the full HTTP job path: POST → 202 → submit
// worker against fake Slurm → GET shows QUEUED → cancel → 202.
func TestAPIJobs(t *testing.T) {
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
	projectSvc := projectsvc.NewService(projectRepo, projectRepo, projectRepo,
		trepo, clusterRepo, authz.RBAC{}, rec)
	policySvc := policiesvc.NewService(policypg.New(pool), authz.RBAC{}, rec)
	jobRepo := jobpg.New(pool)
	pipe := pipeline.New([]validation.ScriptValidator{
		shsyntax.Validator{}, sbatchscan.Validator{},
		envcheck.Validator{}})
	vpolSvc := vpolicy.NewService(vpolicy.Deps{
		Store: vpolicypg.NewPolicyStore(pool), Clusters: clusterRepo,
		AZ: authz.RBAC{}, Audit: rec})
	jobSvc := jobssvc.New(jobssvc.Deps{
		Jobs: jobRepo, Scripts: scriptpg.New(pool),
		Projects: projectSvc, Policies: policySvc, Clusters: clusterRepo,
		Pipeline:    pipe,
		VPolicy:     vpolSvc,
		Validations: vpolicypg.NewValidationStore(pool),
		AZ:          authz.RBAC{}, Audit: rec,
	})
	clusterSvc := clustersvc.New(clustersvc.Deps{
		Repository: clusterRepo, Tenants: trepo,
		Authorizer: authz.RBAC{}, Recorder: rec,
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
		Projects:    projectSvc,
		ProjectRepo: projectRepo, ProjectMembers: projectRepo,
		Policies: policySvc,
		Jobs:     jobSvc,
		JobExec:  pool,
		Scripts:  scriptpg.New(pool),
		Pipeline: pipe,
		VPolicy:  vpolSvc,
		VStore:   vpolicypg.NewValidationStore(pool),
		AZ:       authz.RBAC{},
		Clusters: clusterSvc,
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	token := func(sub string) string {
		c := idp.Claims()
		c["sub"] = sub
		return idp.Token(t, c)
	}
	// call sends body as given; idemKey is set explicitly by callers.
	call := func(tok, method, path string, body any, idemKey string) (int, map[string]any) {
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
		if idemKey != "" {
			req.Header.Set("Idempotency-Key", idemKey)
		}
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
	if code, resp := call(adminTok, "POST", "/api/v1/tenants",
		map[string]any{"slug": "j-tenant", "name": "j"}, ""); code != 201 {
		t.Fatalf("tenant: %d %v", code, resp)
	}
	var tid uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM tenants WHERE slug='j-tenant'`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	tokR := token("user-r")
	code, me := call(tokR, "GET", "/api/v1/me", nil, "")
	if code != 200 {
		t.Fatalf("me: %d", code)
	}
	ur, _ := me["user_id"].(string)
	code, meA := call(adminTok, "GET", "/api/v1/me", nil, "")
	if code != 200 {
		t.Fatalf("me admin: %d", code)
	}
	ua, _ := meA["user_id"].(string)
	for uid, roles := range map[string][]string{
		ur: {"researcher"}, ua: {"tenant-admin"},
	} {
		if code, resp := call(adminTok, "POST", "/api/v1/tenants/j-tenant/members",
			map[string]any{"user_id": uid, "roles": roles}, ""); code != 201 {
			t.Fatalf("member %s: %d %v", uid, code, resp)
		}
	}

	// Cluster with capabilities + assignment + binding.
	fc := fake.New()
	cid := uuid.Must(uuid.NewV7())
	if err := clusterRepo.Create(context.Background(), clusters.Cluster{
		ID: cid, Name: "jc1", DisplayName: "jc1",
		BaseURL: "https://203.0.113.20/", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "tok"},
		Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := clusterRepo.UpsertAssignment(context.Background(),
		tenants.PlatformScope(), clusters.Assignment{
			ClusterID: cid, TenantID: tid, Source: clusters.SourceManual,
			Defaults: clusters.AssignmentDefaults{
				AllowedPartitions: []string{"gpu"}},
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := clusterRepo.RecordSyncResult(context.Background(), cid,
		clusters.SyncResult{OK: true, At: time.Now(),
			Capabilities: &slurm.Capabilities{
				APIVersion: "v0.0.45",
				Partitions: []slurm.Partition{{Name: "gpu"}},
				GRESTypes:  []string{"gpu:h100"},
			}}); err != nil {
		t.Fatal(err)
	}
	if code, resp := call(adminTok, "POST",
		"/api/v1/tenants/j-tenant/projects",
		map[string]any{"slug": "jproj", "name": "JP"}, ""); code != 201 {
		t.Fatalf("project: %d %v", code, resp)
	}
	if code, resp := call(adminTok, "POST",
		"/api/v1/tenants/j-tenant/projects/jproj/cluster-bindings",
		map[string]any{"cluster_id": cid.String(), "slurm_account": "acct-j",
			"allowed_partitions": []string{"gpu"},
			"default_partition":  "gpu"}, ""); code != 201 {
		t.Fatalf("binding: %d %v", code, resp)
	}
	if code, resp := call(adminTok, "POST",
		"/api/v1/tenants/j-tenant/projects/jproj/members",
		map[string]any{"user_id": ur, "roles": []string{"project-member"}},
		""); code != 201 {
		t.Fatalf("pmember: %d %v", code, resp)
	}

	base := "/api/v1/tenants/j-tenant/projects/jproj/jobs"
	jobBody := map[string]any{
		"name": "j1", "cluster": cid.String(),
		"resources": map[string]any{"nodes": 1, "walltime": "1h"},
		"script": map[string]any{"language": "bash",
			"body": "#!/bin/bash\necho hi\n"},
	}

	// Missing Idempotency-Key -> 400.
	if code, er := call(tokR, "POST", base, jobBody, ""); code != 400 {
		t.Fatalf("missing key: %d %v", code, er)
	}
	// Submit -> 202 SUBMITTING.
	code, jr := call(tokR, "POST", base, jobBody, "idem-1")
	if code != 202 || jr["state"] != "SUBMITTING" {
		t.Fatalf("submit: %d %v", code, jr)
	}
	jid, _ := jr["id"].(string)
	// Replay same key + body -> same job.
	code, jr2 := call(tokR, "POST", base, jobBody, "idem-1")
	if code != 200 && code != 202 {
		t.Fatalf("replay: %d %v", code, jr2)
	}
	if jr2["id"] != jid {
		t.Fatalf("replay id: %v vs %v", jr2["id"], jid)
	}
	// Same key, different body -> 409.
	other := map[string]any{
		"cluster":   cid.String(),
		"resources": map[string]any{"nodes": 2, "walltime": "2h"},
		"script":    map[string]any{"language": "bash", "body": "echo x"},
	}
	if code, _ := call(tokR, "POST", base, other, "idem-1"); code != 409 {
		t.Fatalf("want 409, got %d", code)
	}
	// #SBATCH in payload -> 422 with diagnostics.
	bad := map[string]any{
		"cluster":   cid.String(),
		"resources": map[string]any{"nodes": 1, "walltime": "1h"},
		"script": map[string]any{"language": "bash",
			"body": "#!/bin/bash\n#SBATCH --gres=gpu:8\necho hi\n"},
	}
	code, ve := call(tokR, "POST", base, bad, "idem-bad")
	if code != 422 {
		t.Fatalf("directive body: %d %v", code, ve)
	}
	diags, ok := ve["diagnostics"].([]any)
	if !ok || len(diags) == 0 {
		t.Fatalf("no diagnostics: %v", ve)
	}

	// Run job.submit against the fake; GET shows QUEUED + slurm_job_id.
	wdeps := jobsworker.Deps{
		Jobs: jobRepo, Scripts: scriptpg.New(pool),
		Clusters: clusterRepo, Factory: fakeFactory{fc},
		Exec: pool, Audit: rec,
	}
	if err := jobsworker.Submit(wdeps)(context.Background(), workqueue.Item{
		Kind: jobssvc.KindSubmit, Key: "job:" + jid,
		Payload: json.RawMessage(`{"job_id":"` + jid + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	code, gr := call(tokR, "GET", base+"/"+jid, nil, "")
	if code != 200 || gr["state"] != "QUEUED" {
		t.Fatalf("get: %d %v", code, gr)
	}
	if _, ok := gr["slurm_job_id"]; !ok {
		t.Fatalf("no slurm_job_id: %v", gr)
	}
	if _, ok := gr["validation_id"]; !ok {
		t.Fatalf("no validation_id: %v", gr)
	}
	// Exactly one submission reached the fake.
	if n := len(fc.Submissions()); n != 1 {
		t.Fatalf("fake submissions: %d", n)
	}

	// Tenant-wide list requires job.read.tenant (researcher lacks it).
	if code, _ := call(tokR, "GET", "/api/v1/tenants/j-tenant/jobs",
		nil, ""); code != 403 {
		t.Fatalf("tenant jobs: %d", code)
	}
	// Execution spec is retrievable.
	code, er := call(tokR, "GET", base+"/"+jid+"/execution-spec", nil, "")
	if code != 200 || er["payload"] == nil {
		t.Fatalf("execution-spec: %d %v", code, er)
	}

	// Cancel via HTTP -> 202; cancel worker + reconcile -> CANCELED.
	if code, cr := call(tokR, "POST", base+"/"+jid+"/cancel",
		nil, ""); code != 202 {
		t.Fatalf("cancel: %d %v", code, cr)
	}
	if err := jobsworker.Cancel(wdeps)(context.Background(), workqueue.Item{
		Kind: jobssvc.KindCancel, Key: "jobcancel:" + jid,
		Payload: json.RawMessage(`{"job_id":"` + jid + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := jobsworker.Reconcile(wdeps)(context.Background(), workqueue.Item{
		Kind: jobsworker.KindReconcile, Key: "job:" + jid,
		Payload: json.RawMessage(`{"job_id":"` + jid + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	code, gr = call(tokR, "GET", base+"/"+jid, nil, "")
	if code != 200 || gr["state"] != "CANCELED" {
		t.Fatalf("after cancel: %d %v", code, gr)
	}

	// --- M5-A: ad-hoc validation / import / validation-policy routes ---
	// Ad-hoc validate: directive-bearing script -> 200, valid=false.
	code, vr := call(adminTok, "POST", "/api/v1/tenants/j-tenant/scripts/validate",
		map[string]any{"language": "bash",
			"script": "#!/bin/bash\n#SBATCH --qos=premium\necho hi\n"}, "")
	if code != 200 || vr["valid"] != false {
		t.Fatalf("validate: %d %v", code, vr)
	}
	vdiags, _ := vr["diagnostics"].([]any)
	if len(vdiags) == 0 || vr["digest"] == nil {
		t.Fatalf("validate response: %v", vr)
	}
	// Legacy import disabled by default -> 403 IMPORT_DISABLED.
	if code, ir := call(adminTok, "POST",
		"/api/v1/tenants/j-tenant/scripts/import-sbatch",
		map[string]any{"language": "bash",
			"script": "#SBATCH --nodes=2\nsrun x\n"}, ""); code != 403 {
		t.Fatalf("import disabled: %d %v", code, ir)
	}
	// Cluster policy enables legacy import (platform-admin route);
	// without it the tenant PUT below would be rejected as looser.
	if code, cp := call(adminTok, "PUT",
		"/api/v1/clusters/jc1/policies/validation",
		map[string]any{"policy": map[string]any{
			"blockAt":                 "WARNING",
			"allowLegacySbatchImport": true}, "version": 0}, ""); code != 200 {
		t.Fatalf("cluster policy: %d %v", code, cp)
	}
	// Tenant policy write, version 0 create; then import works.
	code, pr := call(adminTok, "PUT",
		"/api/v1/tenants/j-tenant/policies/validation",
		map[string]any{"policy": map[string]any{
			"blockAt":                   "WARNING",
			"allowLegacySbatchImport":   true,
			"shellcheckShell":           "bash",
			"forbiddenCommands":         []string{"sbatch", "salloc"},
			"forbiddenCommandsSeverity": "WARNING"}, "version": 0}, "")
	if code != 200 {
		t.Fatalf("policy put: %d %v", code, pr)
	}
	pv := int64(pr["version"].(float64))
	code, ir := call(adminTok, "POST",
		"/api/v1/tenants/j-tenant/scripts/import-sbatch",
		map[string]any{"language": "bash",
			"script": "#SBATCH --nodes=2\nsrun x\n"}, "")
	if code != 200 || ir["rewritten"] == nil {
		t.Fatalf("import: %d %v", code, ir)
	}
	if s, _ := ir["rewritten"].(string); !strings.Contains(s, "# [custos] imported:") {
		t.Fatalf("rewritten: %v", ir["rewritten"])
	}
	// Looser-than-cluster rule: enabling shell tasks conflicts with the
	// (default, unset) cluster policy where it is off.
	if code, lr := call(adminTok, "PUT",
		"/api/v1/tenants/j-tenant/policies/validation",
		map[string]any{"policy": map[string]any{
			"blockAt":         "WARNING",
			"allowShellTasks": true,
		}, "version": pv}, ""); code != 422 {
		t.Fatalf("not stricter: %d %v", code, lr)
	}
	// Stale version -> conflict.
	if code, sv := call(adminTok, "PUT",
		"/api/v1/tenants/j-tenant/policies/validation",
		map[string]any{"policy": map[string]any{
			"blockAt": "WARNING"}, "version": 0}, ""); code != 409 {
		t.Fatalf("stale version: %d %v", code, sv)
	}
}
