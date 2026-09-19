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
	"github.com/Exonical/custos/internal/executions/engine"
	execpg "github.com/Exonical/custos/internal/executions/postgres"
	execsvc "github.com/Exonical/custos/internal/executions/service"
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
	wfpg "github.com/Exonical/custos/internal/workflows/postgres"
	wfsvc "github.com/Exonical/custos/internal/workflows/service"
)

// TestAPIExecutions exercises the M5-C surface end to end: publish →
// execute with Idempotency-Key → the work-item pump drives the engine
// (advance → admit → submit → reconcile → advance) to SUCCEEDED, plus
// idempotency replay/409, admission denial and the task endpoints.
func TestAPIExecutions(t *testing.T) {
	dsn := dbtest.URL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	idp := authntest.New(t)
	grantPlatformRole(t, dsn, idp.Issuer, "admin-sub", "platform-admin")

	verifier, err := authn.NewOIDCVerifier(ctx, config.OIDC{
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
	scripts := scriptpg.New(pool)
	pipe := pipeline.New([]validation.ScriptValidator{
		shsyntax.Validator{}, sbatchscan.Validator{},
		envcheck.Validator{}})
	vpolSvc := vpolicy.NewService(vpolicy.Deps{
		Store: vpolicypg.NewPolicyStore(pool), Clusters: clusterRepo,
		AZ: authz.RBAC{}, Audit: rec})
	vstore := vpolicypg.NewValidationStore(pool)
	execRepo := execpg.New(pool)
	wfRepo := wfpg.New(pool)
	jobRepo := jobpg.New(pool)
	wfSvc := wfsvc.New(wfsvc.Deps{
		Repo: wfRepo, Projects: projectSvc,
		Policies: policySvc, Clusters: clusterRepo,
		Pipeline: pipe, VPolicy: vpolSvc, Validations: vstore,
		Scripts: scripts, AZ: authz.RBAC{}, Audit: rec,
	})
	execSvc := execsvc.New(execsvc.Deps{
		Repo: execRepo, Workflows: wfRepo, Validations: vstore,
		AZ: authz.RBAC{}, Audit: rec,
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
		Policies:   policySvc,
		Scripts:    scripts,
		Pipeline:   pipe,
		VPolicy:    vpolSvc,
		VStore:     vstore,
		Workflows:  wfSvc,
		Executions: execSvc,
		AZ:         authz.RBAC{},
		Clusters: clustersvc.New(clustersvc.Deps{
			Repository: clusterRepo, Tenants: trepo,
			Authorizer: authz.RBAC{}, Recorder: rec}),
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	token := func(sub string) string {
		c := idp.Claims()
		c["sub"] = sub
		return idp.Token(t, c)
	}
	call := func(tok, method, path string, body any,
		idemKey string) (int, map[string]any) {
		var rdr io.Reader
		if body != nil {
			switch b := body.(type) {
			case string:
				rdr = strings.NewReader(b)
			default:
				j, _ := json.Marshal(body)
				rdr = bytes.NewReader(j)
			}
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
			ct := "application/json"
			if _, ok := body.(string); ok {
				ct = "application/yaml"
			}
			req.Header.Set("Content-Type", ct)
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
		map[string]any{"slug": "e-tenant", "name": "E"}, ""); code != 201 {
		t.Fatalf("tenant: %d %v", code, resp)
	}
	var tid uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM tenants WHERE slug='e-tenant'`).Scan(&tid); err != nil {
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
		ur: {"workflow-author"}, ua: {"tenant-admin"},
	} {
		if code, resp := call(adminTok, "POST",
			"/api/v1/tenants/e-tenant/members",
			map[string]any{"user_id": uid, "roles": roles},
			""); code != 201 {
			t.Fatalf("member %s: %d %v", uid, code, resp)
		}
	}

	// Cluster + assignment + capabilities + project binding.
	cid := uuid.Must(uuid.NewV7())
	if err := clusterRepo.Create(ctx, clusters.Cluster{
		ID: cid, Name: "ec1", DisplayName: "ec1",
		BaseURL: "https://203.0.113.31/", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "tok"},
		Visibility: clusters.VisibilityAssigned,
		State:      clusters.StateActive, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := clusterRepo.UpsertAssignment(ctx,
		tenants.PlatformScope(), clusters.Assignment{
			ClusterID: cid, TenantID: tid, Source: clusters.SourceManual,
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := clusterRepo.RecordSyncResult(ctx, cid,
		clusters.SyncResult{OK: true, At: time.Now(),
			Capabilities: &slurm.Capabilities{
				APIVersion: "v0.0.45",
				Partitions: []slurm.Partition{{Name: "gpu"}},
			}}); err != nil {
		t.Fatal(err)
	}
	if code, resp := call(adminTok, "POST",
		"/api/v1/tenants/e-tenant/projects",
		map[string]any{"slug": "eproj", "name": "EP"}, ""); code != 201 {
		t.Fatalf("project: %d %v", code, resp)
	}
	var pid uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM projects WHERE slug='eproj'`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if code, resp := call(adminTok, "POST",
		"/api/v1/tenants/e-tenant/projects/eproj/cluster-bindings",
		map[string]any{"cluster_id": cid.String(), "slurm_account": "acct-e",
			"allowed_partitions": []string{"gpu"},
			"default_partition":  "gpu"}, ""); code != 201 {
		t.Fatalf("binding: %d %v", code, resp)
	}
	if code, resp := call(adminTok, "POST",
		"/api/v1/tenants/e-tenant/projects/eproj/members",
		map[string]any{"user_id": ur, "roles": []string{"project-member"}},
		""); code != 201 {
		t.Fatalf("pmember: %d %v", code, resp)
	}

	// Workflow with a two-task command DAG; publish.
	specYAML := func(partition string) string {
		return "apiVersion: custos.io/v1alpha1\nkind: Workflow\n" +
			"metadata: {name: runner}\nspec:\n" +
			"  placement: {cluster: ec1}\n" +
			"  tasks:\n" +
			"    - name: a\n      type: batch\n" +
			"      resources: {cpu: 1, memory: 512Mi, walltime: 10m}\n" +
			"      partition: " + partition + "\n" +
			"      command: [\"/bin/true\"]\n" +
			"    - name: b\n      type: batch\n" +
			"      dependsOn: [a]\n" +
			"      resources: {cpu: 1, memory: 512Mi, walltime: 10m}\n" +
			"      partition: " + partition + "\n" +
			"      command: [\"/bin/true\"]\n"
	}
	mkPublished := func(partition string) string {
		code, wresp := call(tokR, "POST",
			"/api/v1/tenants/e-tenant/workflows",
			map[string]any{"project": pid.String(),
				"name": "w-" + partition}, "")
		if code != 201 {
			t.Fatalf("workflow: %d %v", code, wresp)
		}
		wid, _ := wresp["id"].(string)
		code, vresp := call(tokR, "POST",
			"/api/v1/tenants/e-tenant/workflows/"+wid+"/versions",
			specYAML(partition), "")
		if code != 201 {
			t.Fatalf("version: %d %v", code, vresp)
		}
		vid, _ := vresp["id"].(string)
		code, pub := call(tokR, "POST",
			"/api/v1/tenants/e-tenant/workflows/"+wid+
				"/versions/"+vid+"/publish", nil, "")
		if code != 200 {
			t.Fatalf("publish: %d %v", code, pub)
		}
		return wid
	}
	wid := mkPublished("gpu")

	// --- idempotency semantics -----------------------------------------
	exBase := "/api/v1/tenants/e-tenant/workflow-executions"
	if code, resp := call(tokR, "POST", exBase,
		map[string]any{"workflow": wid}, ""); code != 400 {
		t.Fatalf("missing idem key: %d %v", code, resp)
	}
	body := map[string]any{"workflow": wid}
	code, er := call(tokR, "POST", exBase, body, "exec-1")
	if code != 201 && code != 202 {
		t.Fatalf("execute: %d %v", code, er)
	}
	execID, _ := er["id"].(string)
	if execID == "" {
		t.Fatalf("no execution id: %v", er)
	}
	code, replay := call(tokR, "POST", exBase, body, "exec-1")
	if code != 201 && code != 202 {
		t.Fatalf("replay: %d %v", code, replay)
	}
	if replay["id"] != execID {
		t.Fatalf("replay returned %v, want %s", replay["id"], execID)
	}
	if code, resp := call(tokR, "POST", exBase,
		map[string]any{"workflow": wid,
			"parameters": map[string]any{}}, "exec-1"); code != 409 {
		t.Fatalf("reused key different body: %d %v", code, resp)
	}

	// --- worker pump: run pending work items through the real handlers.
	fc := fake.New()
	fc.SetPartitions([]slurm.Partition{{Name: "gpu"}})
	execDeps := engine.Deps{
		Execs: execRepo, Workflows: wfRepo, Policies: policySvc,
		VPolicy: vpolSvc, Clusters: clusterRepo, Projects: projectSvc,
		Pipeline: pipe, Validations: vstore, Scripts: scripts,
		Jobs: jobRepo, Audit: rec,
	}
	wdeps := jobsworker.Deps{
		Jobs: jobRepo, Scripts: scripts, Clusters: clusterRepo,
		Factory: fakeFactory{fc}, Exec: pool, Audit: rec,
	}
	handlers := map[string]workqueue.Handler{
		engine.KindAdvance:       engine.Advance(execDeps),
		engine.KindAdmit:         engine.Admit(execDeps),
		jobssvc.KindSubmit:       jobsworker.Submit(wdeps),
		jobsworker.KindReconcile: jobsworker.Reconcile(wdeps),
		jobssvc.KindCancel:       jobsworker.Cancel(wdeps),
	}
	pump := func(limit int) {
		for i := 0; i < limit; i++ {
			var itemID uuid.UUID
			var kind string
			var payload []byte
			// run_at is ignored: the real worker would wait out
			// backoff windows, the pump drains them immediately.
			err := pool.QueryRow(ctx, `
				SELECT id, kind, payload FROM work_items
				WHERE state='pending'
				ORDER BY created_at LIMIT 1`).
				Scan(&itemID, &kind, &payload)
			if err != nil {
				return // no pending work
			}
			h, ok := handlers[kind]
			if !ok {
				t.Fatalf("no handler for %s", kind)
			}
			// Before a reconcile runs, mark every submitted fake job
			// COMPLETED so the mirror can settle the chain.
			if kind == jobsworker.KindReconcile {
				fj, err := fc.ListJobs(ctx, slurm.JobFilter{})
				if err != nil {
					t.Fatal(err)
				}
				for _, j := range fj {
					fc.Advance(j.ID.ID, slurm.JobCompleted)
				}
			}
			if err := h(ctx, workqueue.Item{
				Kind: kind, Payload: json.RawMessage(payload),
			}); err != nil {
				t.Fatalf("%s handler: %v", kind, err)
			}
			if _, err := pool.Exec(ctx, `
				UPDATE work_items SET state='done', finished_at=now()
				WHERE id=$1`, itemID); err != nil {
				t.Fatal(err)
			}
		}
	}

	pump(50)
	code, gr := call(tokR, "GET", exBase+"/"+execID, nil, "")
	if code != 200 || gr["state"] != "SUCCEEDED" {
		code, tl := call(tokR, "GET", exBase+"/"+execID+"/tasks", nil, "")
		t.Fatalf("execution: %d %v tasks=%v", code, gr, tl)
	}
	if len(fc.Submissions()) != 2 {
		t.Fatalf("%d submissions, want 2", len(fc.Submissions()))
	}

	// Task list + execution-spec endpoint.
	code, tl := call(tokR, "GET", exBase+"/"+execID+"/tasks", nil, "")
	if code != 200 {
		t.Fatalf("tasks: %d %v", code, tl)
	}
	items, _ := tl["tasks"].([]any)
	if len(items) != 2 {
		t.Fatalf("%d task rows, want 2: %v", len(items), tl)
	}
	taskID, _ := items[0].(map[string]any)["id"].(string)
	code, es := call(tokR, "GET",
		exBase+"/"+execID+"/tasks/"+taskID+"/execution-spec", nil, "")
	if code != 200 {
		t.Fatalf("execution-spec: %d %v", code, es)
	}
	if _, ok := es["argv"]; !ok {
		t.Fatalf("no argv in execution spec: %v", es)
	}

	// --- admission denial: policy tightened after materialize ----------
	// The execution is created and materialized under the permissive
	// policy (one pump step runs execution.advance → QUEUED with
	// task.admit pending). Tightening the tenant policy afterwards means
	// admission must deny at admit time (task 10m > policy 60s), leaving
	// the task FAILED/ADMISSION_DENIED with no scheduler submissions.
	code, er = call(tokR, "POST", exBase,
		map[string]any{"workflow": wid}, "exec-deny")
	if code != 201 && code != 202 {
		t.Fatalf("execute deny: %d %v", code, er)
	}
	denyID, _ := er["id"].(string)
	pump(1) // execution.advance → tasks materialized
	if code, tp := call(adminTok, "PUT",
		"/api/v1/tenants/e-tenant/policies/resource",
		map[string]any{"policy": map[string]any{
			"max_walltime": 60}}, ""); code != 200 {
		t.Fatalf("tighten policy: %d %v", code, tp)
	}
	nSub := len(fc.Submissions())
	pump(50)
	code, gr = call(tokR, "GET", exBase+"/"+denyID, nil, "")
	if code != 200 || gr["state"] != "FAILED" {
		t.Fatalf("denied execution: %d %v", code, gr)
	}
	if len(fc.Submissions()) != nSub {
		t.Fatal("denied admission produced submissions")
	}
	code, tl = call(tokR, "GET", exBase+"/"+denyID+"/tasks", nil, "")
	if code != 200 {
		t.Fatalf("denied tasks: %d %v", code, tl)
	}
	items, _ = tl["tasks"].([]any)
	var sawDeny bool
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["stateReason"] == "ADMISSION_DENIED" {
			sawDeny = true
		}
	}
	if !sawDeny {
		t.Fatalf("no ADMISSION_DENIED task: %v", tl)
	}
}
