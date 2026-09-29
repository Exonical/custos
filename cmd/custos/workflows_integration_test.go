package main

import (
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
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/health"
	policypg "github.com/Exonical/custos/internal/policies/postgres"
	policiesvc "github.com/Exonical/custos/internal/policies/service"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
	scriptpg "github.com/Exonical/custos/internal/scripts/postgres"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
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
	"github.com/Exonical/custos/internal/workflowspec"
	wfvalidate "github.com/Exonical/custos/internal/workflowspec/validate"
)

// TestAPIWorkflows exercises the M5-B surface end to end: version
// authoring, the publish gate (steps 1-8 + per-task
// ScriptValidation), immutability, shell gating, preview and the
// secrets fail-closed rule.
func TestAPIWorkflows(t *testing.T) {
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
	scripts := scriptpg.New(pool)
	pipe := pipeline.New([]validation.ScriptValidator{
		shsyntax.Validator{}, sbatchscan.Validator{},
		envcheck.Validator{}})
	vpolSvc := vpolicy.NewService(vpolicy.Deps{
		Store: vpolicypg.NewPolicyStore(pool), Clusters: clusterRepo,
		AZ: authz.RBAC{}, Audit: rec})
	vstore := vpolicypg.NewValidationStore(pool)
	wfSvc := wfsvc.New(wfsvc.Deps{
		Repo: wfpg.New(pool), Projects: projectSvc,
		Policies: policySvc, Clusters: clusterRepo,
		Pipeline: pipe, VPolicy: vpolSvc, Validations: vstore,
		Scripts: scripts, AZ: authz.RBAC{}, Audit: rec,
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
		Policies:  policySvc,
		Scripts:   scripts,
		Pipeline:  pipe,
		VPolicy:   vpolSvc,
		VStore:    vstore,
		Workflows: wfSvc,
		AZ:        authz.RBAC{},
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
	call := func(tok, method, path string, body any) (int, map[string]any) {
		return apiCall(t, srv.URL, tok, method, path, body, "")
	}

	adminTok := token("admin-sub")
	if code, resp := call(adminTok, "POST", "/api/v1/tenants",
		map[string]any{"slug": "w-tenant", "name": "w"}); code != 201 {
		t.Fatalf("tenant: %d %v", code, resp)
	}
	var tid uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM tenants WHERE slug='w-tenant'`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	tokR := token("user-r")
	code, me := call(tokR, "GET", "/api/v1/me", nil)
	if code != 200 {
		t.Fatalf("me: %d", code)
	}
	ur, _ := me["user_id"].(string)
	code, meA := call(adminTok, "GET", "/api/v1/me", nil)
	if code != 200 {
		t.Fatalf("me admin: %d", code)
	}
	ua, _ := meA["user_id"].(string)
	for uid, roles := range map[string][]string{
		ur: {"workflow-author"}, ua: {"tenant-admin"},
	} {
		if code, resp := call(adminTok, "POST",
			"/api/v1/tenants/w-tenant/members",
			map[string]any{"user_id": uid, "roles": roles}); code != 201 {
			t.Fatalf("member %s: %d %v", uid, code, resp)
		}
	}

	// Cluster with capabilities + assignment + project binding.
	cid := uuid.Must(uuid.NewV7())
	if err := clusterRepo.Create(context.Background(), clusters.Cluster{
		ID: cid, Name: "wc1", DisplayName: "wc1",
		BaseURL: "https://203.0.113.30/", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "tok"},
		Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := clusterRepo.UpsertAssignment(context.Background(),
		tenants.PlatformScope(), clusters.Assignment{
			ClusterID: cid, TenantID: tid, Source: clusters.SourceManual,
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := clusterRepo.RecordSyncResult(context.Background(), cid,
		clusters.SyncResult{OK: true, At: time.Now(),
			Capabilities: &slurm.Capabilities{
				APIVersion: "v0.0.45",
				Partitions: []slurm.Partition{{Name: "gpu"}},
			}}); err != nil {
		t.Fatal(err)
	}
	if code, resp := call(adminTok, "POST",
		"/api/v1/tenants/w-tenant/projects",
		map[string]any{"slug": "wproj", "name": "WP"}); code != 201 {
		t.Fatalf("project: %d %v", code, resp)
	}
	var pid uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM projects WHERE slug='wproj'`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if code, resp := call(adminTok, "POST",
		"/api/v1/tenants/w-tenant/projects/wproj/cluster-bindings",
		map[string]any{"cluster_id": cid.String(), "slurm_account": "acct-w",
			"allowed_partitions": []string{"gpu"},
			"default_partition":  "gpu"}); code != 201 {
		t.Fatalf("binding: %d %v", code, resp)
	}
	if code, resp := call(adminTok, "POST",
		"/api/v1/tenants/w-tenant/projects/wproj/members",
		map[string]any{"user_id": ur, "roles": []string{"project-member"}}); code != 201 {
		t.Fatalf("pmember: %d %v", code, resp)
	}

	// Script upload -> digest.
	code, up := call(tokR, "POST", "/api/v1/tenants/w-tenant/scripts",
		map[string]any{"language": "bash",
			"script": "#!/bin/bash\n#SBATCH --qos=admin\necho hi\n"})
	if code != 200 {
		t.Fatalf("upload: %d %v", code, up)
	}
	badDigest, _ := up["digest"].(string)
	code, up = call(tokR, "POST", "/api/v1/tenants/w-tenant/scripts",
		map[string]any{"language": "bash", "script": "#!/bin/bash\necho hi\n"})
	if code != 200 {
		t.Fatalf("upload2: %d %v", code, up)
	}
	goodDigest, _ := up["digest"].(string)

	// Workflow + draft version with the directive-bearing script.
	code, wresp := call(tokR, "POST", "/api/v1/tenants/w-tenant/workflows",
		map[string]any{"project": pid.String(), "name": "pipe",
			"description": "d"})
	if code != 201 {
		t.Fatalf("workflow: %d %v", code, wresp)
	}
	wid, _ := wresp["id"].(string)
	wbase := "/api/v1/tenants/w-tenant/workflows/" + wid

	specYAML := func(digest string) string {
		return "apiVersion: custos.io/v1alpha1\nkind: Workflow\n" +
			"metadata: {name: pipe}\nspec:\n  placement: {cluster: wc1}\n" +
			"  tasks:\n    - name: run\n      type: batch\n" +
			"      resources: {cpu: 2, memory: 1Gi, walltime: 30m}\n" +
			"      script: {ref: \"" + digest + "\", language: bash}\n"
	}
	code, vresp := call(tokR, "POST", wbase+"/versions", specYAML(badDigest))
	if code != 201 {
		t.Fatalf("version: %d %v", code, vresp)
	}
	vid, _ := vresp["id"].(string)
	vbase := wbase + "/versions/" + vid

	// Publish blocked: the script carries a raw directive (CUSTOS102).
	code, pub := call(tokR, "POST", vbase+"/publish", nil)
	if code != 422 {
		t.Fatalf("publish should fail: %d %v", code, pub)
	}
	errBody, _ := json.Marshal(pub)
	if !strings.Contains(string(errBody), "CUSTOS102") {
		t.Fatalf("want CUSTOS102 diagnostic: %s", errBody)
	}

	// Fix the draft (PUT new spec with the clean script) -> publish ok.
	code, vresp = call(tokR, "PUT", vbase, specYAML(goodDigest))
	if code != 200 {
		t.Fatalf("update draft: %d %v", code, vresp)
	}
	code, pub = call(tokR, "POST", vbase+"/publish", nil)
	if code != 200 {
		t.Fatalf("publish: %d %v", code, pub)
	}
	if pub["state"] != "published" {
		t.Fatalf("state: %v", pub)
	}
	code, wgot := call(tokR, "GET", wbase, nil)
	if code != 200 || wgot["latestPublishedVersionId"] != vid {
		t.Fatalf("latest pointer: %d %v", code, wgot)
	}
	// Immutable now.
	if code, resp := call(tokR, "PUT", vbase,
		specYAML(goodDigest)); code != 409 {
		t.Fatalf("published PUT: %d %v", code, resp)
	}
	// Validations list has the persisted per-task rows.
	code, vals := call(tokR, "GET", vbase+"/validations", nil)
	if code != 200 {
		t.Fatalf("validations: %d %v", code, vals)
	}
	if vs, _ := vals["validations"].([]any); len(vs) == 0 {
		t.Fatalf("no validations persisted: %v", vals)
	}

	// Stale-context gate: a draft edit that changes the task env with an
	// unchanged script digest must re-run validation at publish — the
	// previously persisted task validation must not satisfy the gate.
	code, wresp = call(tokR, "POST", "/api/v1/tenants/w-tenant/workflows",
		map[string]any{"project": pid.String(), "name": "stale",
			"description": "d"})
	if code != 201 {
		t.Fatalf("workflow2: %d %v", code, wresp)
	}
	wid2, _ := wresp["id"].(string)
	wbase2 := "/api/v1/tenants/w-tenant/workflows/" + wid2
	specEnv := func(env string) string {
		return "apiVersion: custos.io/v1alpha1\nkind: Workflow\n" +
			"metadata: {name: stale}\nspec:\n  placement: {cluster: wc1}\n" +
			"  tasks:\n    - name: run\n      type: batch\n" +
			"      resources: {cpu: 2, memory: 1Gi, walltime: 30m}\n" +
			env +
			"      script: {ref: \"" + goodDigest + "\", language: bash}\n"
	}
	code, vresp = call(tokR, "POST", wbase2+"/versions",
		specEnv("      env: {FOO: bar}\n"))
	if code != 201 {
		t.Fatalf("version2: %d %v", code, vresp)
	}
	vid2, _ := vresp["id"].(string)
	vbase2 := wbase2 + "/versions/" + vid2
	code, tv := call(tokR, "POST", vbase2+"/tasks/run/validate", nil)
	if code != 200 {
		t.Fatalf("task validate: %d %v", code, tv)
	}
	// Swap in a Custos/Slurm-controlled env var; the script digest is
	// unchanged, so only the input hash catches this.
	code, vresp = call(tokR, "PUT", vbase2,
		specEnv("      env: {SLURM_JOB_ID: \"x\"}\n"))
	if code != 200 {
		t.Fatalf("update draft env: %d %v", code, vresp)
	}
	code, pub = call(tokR, "POST", vbase2+"/publish", nil)
	if code != 422 {
		t.Fatalf("publish with stale env should fail: %d %v", code, pub)
	}
	errBody, _ = json.Marshal(pub)
	if !strings.Contains(string(errBody), "CUSTOS301") {
		t.Fatalf("want envcheck CUSTOS301 diagnostic: %s", errBody)
	}

	// Preview: wrapper has no #SBATCH and the job name is Custos-minted.
	code, prev := call(tokR, "POST",
		vbase+"/tasks/run/preview-submission", nil)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, prev)
	}
	previewSpec, _ := prev["executionSpec"].(map[string]any)
	if previewSpec["launch"] != "sbatch" {
		t.Fatalf("preview launch = %v, want sbatch", previewSpec["launch"])
	}
	wrapper, _ := prev["wrapper"].(string)
	if strings.Contains(wrapper, "#SBATCH") {
		t.Fatalf("wrapper leaked directives: %s", wrapper)
	}
	js, _ := prev["jobSubmission"].(map[string]any)
	name, _ := js["Name"].(string)
	if !strings.HasPrefix(name, "custos-") {
		t.Fatalf("job name not custos-controlled: %q %v", name, js)
	}

	// GetVersion negotiates a whole-workflow YAML representation.
	yamlReq, _ := http.NewRequest(http.MethodGet, srv.URL+vbase, nil)
	yamlReq.Header.Set("Authorization", "Bearer "+tokR)
	yamlReq.Header.Set("Accept", "application/yaml")
	yamlResp, err := http.DefaultClient.Do(yamlReq)
	if err != nil {
		t.Fatal(err)
	}
	yamlBody, _ := io.ReadAll(yamlResp.Body)
	_ = yamlResp.Body.Close()
	if yamlResp.StatusCode != http.StatusOK || yamlResp.Header.Get("Content-Type") != "application/yaml" || !strings.Contains(string(yamlBody), "apiVersion: custos.io/v1alpha1") {
		t.Fatalf("whole workflow YAML: status=%d type=%q body=%s", yamlResp.StatusCode, yamlResp.Header.Get("Content-Type"), yamlBody)
	}

	// Multi-script import returns a complete, statically valid proposal;
	// it stores rewritten scripts but does not create a workflow record.
	code, beforeImport := call(tokR, "GET", "/api/v1/tenants/w-tenant/workflows", nil)
	if code != http.StatusOK {
		t.Fatalf("list before import: %d %v", code, beforeImport)
	}
	workflowsBefore, _ := beforeImport["workflows"].([]any)
	importBody := map[string]any{"name": "OpenMP Imported", "scripts": []any{
		map[string]any{"filename": "worker.sbatch", "content": "#!/bin/bash\n#SBATCH --job-name=worker\n#SBATCH --nodes=1\n#SBATCH --ntasks=1\n#SBATCH --cpus-per-task=4\n#SBATCH --mem=4096M\n#SBATCH --time=01:30:00\n#SBATCH --partition=gpu\n#SBATCH --qos=normal\n#SBATCH --output=out-%j.log\n#SBATCH --error=err-%j.log\n#SBATCH --chdir=/scratch/openmp\nexport OMP_NUM_THREADS=4\n./a.out\n"},
		map[string]any{"filename": "worker-copy.sbatch", "content": "#!/bin/sh\n#SBATCH --job-name=worker\n#SBATCH --mem-per-cpu=1024M\n#SBATCH --time=00:30:00\n#SBATCH --account=legacy\necho second\n"},
	}}
	code, imported := call(tokR, "POST", "/api/v1/tenants/w-tenant/workflow-imports/sbatch", importBody)
	if code != 200 {
		t.Fatalf("workflow sbatch import: %d %v", code, imported)
	}
	importSpec, ok := imported["spec"].(map[string]any)
	if !ok {
		t.Fatalf("workflow import omitted spec: %v", imported)
	}
	importMetadata, _ := importSpec["metadata"].(map[string]any)
	if importMetadata["name"] != "openmp-imported" {
		t.Fatalf("workflow name not sanitized: %v", importMetadata)
	}
	importDocumentSpec, _ := importSpec["spec"].(map[string]any)
	importDocumentTasks, _ := importDocumentSpec["tasks"].([]any)
	if len(importDocumentTasks) != 2 {
		t.Fatalf("imported spec task count: %v", importDocumentSpec["tasks"])
	}
	secondScript, _ := importDocumentTasks[1].(map[string]any)["script"].(map[string]any)
	if secondScript["language"] != "sh" {
		t.Fatalf("/bin/sh script language = %v", secondScript["language"])
	}
	importYAML, _ := imported["yaml"].(string)
	if !strings.Contains(importYAML, "launch: sbatch") || !strings.Contains(importYAML, "worker-2") {
		t.Fatalf("workflow import YAML/names: %s", importYAML)
	}
	importTasks, _ := imported["tasks"].([]any)
	if len(importTasks) != 2 {
		t.Fatalf("import task count: %v", imported["tasks"])
	}
	diagnosticCodes := map[string]bool{}
	memPerCPUFieldScoped := false
	for _, rawTask := range importTasks {
		taskResult, _ := rawTask.(map[string]any)
		for _, rawDiagnostic := range taskResult["diagnostics"].([]any) {
			diagnostic, _ := rawDiagnostic.(map[string]any)
			code := diagnostic["code"].(string)
			diagnosticCodes[code] = true
			if code == "CUSTOS202" && strings.Contains(diagnostic["field"].(string), "resources.memoryPerCpu") {
				memPerCPUFieldScoped = true
			}
		}
	}
	if !diagnosticCodes["CUSTOS201"] || !diagnosticCodes["CUSTOS202"] || !memPerCPUFieldScoped {
		t.Fatalf("unmappable directive/memory warnings missing or unscoped: codes=%v scoped=%v", diagnosticCodes, memPerCPUFieldScoped)
	}
	importSpecJSON, err := json.Marshal(importSpec)
	if err != nil {
		t.Fatal(err)
	}
	importedDocument, err := workflowspec.Decode(importSpecJSON, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if errs := wfvalidate.Static(importedDocument); len(errs) != 0 {
		t.Fatalf("imported spec fails static validation: %+v", errs)
	}
	code, afterImport := call(tokR, "GET", "/api/v1/tenants/w-tenant/workflows", nil)
	if code != http.StatusOK {
		t.Fatalf("list after import: %d %v", code, afterImport)
	}
	workflowsAfter, _ := afterImport["workflows"].([]any)
	if len(workflowsAfter) != len(workflowsBefore) {
		t.Fatalf("import created workflow records: before=%d after=%d", len(workflowsBefore), len(workflowsAfter))
	}

	importedMetadata, _ := importSpec["metadata"].(map[string]any)
	code, importedWorkflow := call(tokR, "POST", "/api/v1/tenants/w-tenant/workflows",
		map[string]any{"project": pid.String(), "name": importedMetadata["name"]})
	workflowImportID, _ := importedWorkflow["id"].(string)
	if code != http.StatusCreated || workflowImportID == "" {
		t.Fatalf("create imported workflow: %d %v", code, importedWorkflow)
	}
	workflowImportBase := "/api/v1/tenants/w-tenant/workflows/" + workflowImportID
	code, importedVersion := call(tokR, "POST", workflowImportBase+"/versions", importSpec)
	if code != http.StatusCreated {
		t.Fatalf("create imported workflow version: %d %v", code, importedVersion)
	}
	importedVersionID, _ := importedVersion["id"].(string)
	exportPath := workflowImportBase + "/versions/" + importedVersionID + "/tasks/worker/sbatch"
	exportReq, _ := http.NewRequest(http.MethodGet, srv.URL+exportPath, nil)
	exportReq.Header.Set("Authorization", "Bearer "+tokR)
	exportResp, err := http.DefaultClient.Do(exportReq)
	if err != nil {
		t.Fatal(err)
	}
	exportBody, _ := io.ReadAll(exportResp.Body)
	_ = exportResp.Body.Close()
	if exportResp.StatusCode != http.StatusOK || exportResp.Header.Get("Content-Type") != "text/x-shellscript; charset=utf-8" || exportResp.Header.Get("Content-Disposition") != `attachment; filename="worker.sbatch"` {
		t.Fatalf("task sbatch export: status=%d headers=%v body=%s", exportResp.StatusCode, exportResp.Header, exportBody)
	}
	if !strings.Contains(string(exportBody), "#SBATCH --cpus-per-task=4") || !strings.Contains(string(exportBody), "./a.out") {
		t.Fatalf("task sbatch export body: %s", exportBody)
	}
	if code, resp := call(tokR, "GET", workflowImportBase+"/versions/"+importedVersionID+"/tasks/missing/sbatch", nil); code != http.StatusNotFound {
		t.Fatalf("unknown task export: %d %v", code, resp)
	}

	// Secret declarations are accepted in drafts; publish/contextual
	// validation resolves the named SecretReference.
	code, secretDraft := call(tokR, "POST", wbase+"/versions",
		"apiVersion: custos.io/v1alpha1\nkind: Workflow\n"+
			"metadata: {name: pipe}\nspec:\n"+
			"  secrets: {tok: {ref: x, use: env, envName: T}}\n"+
			"  tasks: [{name: a, command: [\"true\"]}]\n")
	if code != 201 {
		t.Fatalf("secret draft: %d %v", code, secretDraft)
	}

	// Shell task: publish denied until cluster + tenant allow it.
	shellSpec := "apiVersion: custos.io/v1alpha1\nkind: Workflow\n" +
		"metadata: {name: pipe}\nspec:\n  placement: {cluster: wc1}\n" +
		"  tasks:\n    - name: sh\n      type: shell\n" +
		"      script: {ref: \"" + goodDigest + "\", language: bash}\n"
	code, vresp = call(tokR, "POST", wbase+"/versions", shellSpec)
	if code != 201 {
		t.Fatalf("shell version: %d %v", code, vresp)
	}
	svid, _ := vresp["id"].(string)
	if code, resp := call(tokR, "POST",
		wbase+"/versions/"+svid+"/publish", nil); code != 422 {
		t.Fatalf("shell publish should be denied: %d %v", code, resp)
	}
	// Cluster policy allows shell; tenant narrows to ERROR blockAt and
	// also allows shell (must be at least as strict as cluster).
	for _, pol := range []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/clusters/" + cid.String() + "/policies/validation",
			map[string]any{"version": 0, "policy": map[string]any{
				"blockAt": "WARNING", "allowShellTasks": true}}},
		{"/api/v1/tenants/w-tenant/policies/validation",
			map[string]any{"version": 0, "policy": map[string]any{
				"blockAt": "WARNING", "allowShellTasks": true}}},
	} {
		if code, resp := call(adminTok, "PUT", pol.path, pol.body); code != 200 {
			t.Fatalf("policy %s: %d %v", pol.path, code, resp)
		}
	}
	if code, resp := call(tokR, "POST",
		wbase+"/versions/"+svid+"/publish", nil); code != 200 {
		t.Fatalf("shell publish after allow: %d %v", code, resp)
	}

	// A stored policy can still turn the default-on workflow import off.
	if code, disabled := call(tokR, "POST", "/api/v1/tenants/w-tenant/workflow-imports/sbatch",
		map[string]any{"scripts": []any{map[string]any{"filename": "one.sbatch", "content": "#!/bin/bash\necho one\n"}}}); code != http.StatusForbidden {
		t.Fatalf("workflow import should honor disabled policy: %d %v", code, disabled)
	}

	// Schema endpoint is unauthenticated and cacheable.
	req, _ := http.NewRequest("GET",
		srv.URL+"/api/v1/schemas/workflow/v1alpha1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("schema: %v %d", err, resp.StatusCode)
	}
	_ = resp.Body.Close()
}
