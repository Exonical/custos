package e2e

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestE2E drives the ordered scenario from docs/e2e.md against the
// running stack. State is shared between subtests intentionally.
func TestE2E(t *testing.T) {
	var (
		tokAlice, tokAdmin, tokBob string
		aliceID                    string
		clusterID                  string
		projectID                  string
		wfID, wfVerID              string
		execID                     string
		execBody                   map[string]any
	)
	// Per-run suffix so re-runs against the same stack don't collide on
	// unique resource names (workflows).
	runSfx := "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	wfName := "e2e-flow" + runSfx
	slowName := "e2e-slow" + runSfx

	t.Run("01_authentication", func(t *testing.T) {
		tokAlice = token(t, "alice", "")
		status, me := api(t, http.MethodGet, "/me", nil, tokAlice, nil)
		want(t, status, me, http.StatusOK, "alice /me")
		if me["user_id"] == nil || me["user_id"] == "" {
			t.Fatal("alice not provisioned")
		}
		p, _ := me["principal"].(map[string]any)
		if p["email"] != "alice@e2e.test" {
			t.Fatalf("email = %v", p["email"])
		}
		gs, _ := p["groups"].([]any)
		if len(gs) == 0 || gs[0] != "hpc-a" {
			t.Fatalf("groups = %v", p["groups"])
		}
		aliceID, _ = me["user_id"].(string)

		// Wrong audience: the public client has no audience mapper.
		bad := token(t, "alice", "custos-wrong-audience")
		status, body := api(t, http.MethodGet, "/me", nil, bad, nil)
		if status != http.StatusUnauthorized {
			t.Fatalf("wrong-audience /me: HTTP %d (want 401): %v",
				status, body)
		}
	})

	t.Run("02_tenant_claim_reconciliation", func(t *testing.T) {
		tokAdmin = token(t, "platform-admin", "")
		status, tn := api(t, http.MethodPost, "/tenants",
			map[string]any{"slug": "acme", "name": "ACME E2E"},
			tokAdmin, nil)
		if status == http.StatusConflict {
			t.Log("tenant acme already exists (stack re-run)")
		} else {
			want(t, status, tn, http.StatusCreated, "create tenant")
		}

		// IdP reconciliation: groups==hpc-a -> researcher,
		// groups==hpc-admins -> tenant-admin.
		for _, r := range []map[string]any{
			{"claim": "groups", "match_value": "hpc-a",
				"roles": []string{"researcher"}},
			{"claim": "groups", "match_value": "hpc-admins",
				"roles": []string{"tenant-admin"}},
		} {
			status, body := api(t, http.MethodPost,
				"/tenants/acme/claim-rules", r, tokAdmin, nil)
			if status == http.StatusConflict {
				continue
			}
			want(t, status, body, http.StatusCreated, "claim rule")
		}

		// bob (no groups) is not a member -> tenant reads 404.
		tokBob = token(t, "bob", "")
		status, body := api(t, http.MethodGet, "/tenants/acme",
			nil, tokBob, nil)
		if status != http.StatusNotFound {
			t.Fatalf("bob /tenants/acme: HTTP %d (want 404): %v",
				status, body)
		}
	})

	t.Run("03_cluster_registration_sync", func(t *testing.T) {
		caPEM, err := readFile("deploy/e2e/.secrets/e2e-ca.crt")
		if err != nil {
			t.Fatal(err)
		}
		status, cl := api(t, http.MethodPost, "/clusters", map[string]any{
			"name":          "e2e",
			"display_name":  "E2E Slurm 26.05",
			"base_url":      "https://slurmrestd.e2e:6820",
			"api_version":   "v0.0.45",
			"ca_bundle_pem": caPEM,
			"identity_mode": "service",
			"service_user":  "custos",
			"token_ref":     map[string]any{"provider": "file", "path": "/etc/custos/secrets/slurm/token"},
			"visibility":    "assigned",
		}, tokAdmin, nil)
		if status == http.StatusConflict {
			t.Log("cluster e2e already registered")
			status, cl = api(t, http.MethodGet, "/clusters/e2e",
				nil, tokAdmin, nil)
			want(t, status, cl, http.StatusOK, "get cluster")
		} else {
			want(t, status, cl, http.StatusCreated, "register cluster")
		}
		clusterID, _ = cl["id"].(string)
		if clusterID == "" {
			t.Fatalf("cluster id: %v", cl)
		}

		status, tc := api(t, http.MethodPost,
			"/clusters/e2e/test-connection", nil, tokAdmin, nil)
		want(t, status, tc, http.StatusOK, "test-connection")
		if tc["ok"] != true {
			t.Fatalf("test-connection: %v", tc)
		}
		if tc["api_version"] != "v0.0.45" {
			t.Fatalf("api_version = %v", tc["api_version"])
		}

		// cluster.sync drives state to active.
		poll(t, "cluster state active", 90*time.Second, func() bool {
			_, c := api(t, http.MethodGet, "/clusters/e2e", nil, tokAdmin, nil)
			return c["state"] == "active"
		})

		// Assign to tenant, then verify synced partitions.
		status, body := api(t, http.MethodPut,
			"/clusters/e2e/tenants/acme",
			map[string]any{}, tokAdmin, nil)
		if status != http.StatusNoContent {
			t.Fatalf("assign cluster: HTTP %d: %v", status, body)
		}
		poll(t, "partitions synced", 60*time.Second, func() bool {
			s, pl := api(t, http.MethodGet,
				"/tenants/acme/clusters/e2e/partitions", nil, tokAdmin, nil)
			if s != http.StatusOK {
				return false
			}
			items, _ := pl["items"].([]any)
			return len(items) >= 2
		})
		_, pl := api(t, http.MethodGet,
			"/tenants/acme/clusters/e2e/partitions", nil, tokAdmin, nil)
		names := map[string]bool{}
		for _, p := range pl["items"].([]any) {
			names[p.(map[string]any)["name"].(string)] = true
		}
		if !names["debug"] || !names["gpu"] {
			t.Fatalf("partitions = %v", names)
		}

		// QoS + account exist on the real controller.
		out := cexec(t, "slurmctld", "sacctmgr", "-n", "-P",
			"show", "qos", "format=name")
		if !strings.Contains(out, "normal") || !strings.Contains(out, "high") {
			t.Fatalf("qos: %s", out)
		}
		out = cexec(t, "slurmctld", "sacctmgr", "-n", "-P",
			"list", "account", "format=account")
		if !strings.Contains(out, "e2e-acct") {
			t.Fatalf("accounts: %s", out)
		}
	})

	t.Run("03b_idp_reconciliation", func(t *testing.T) {
		// e2e.sh seeds the claim rules before any user token is
		// minted, so alice's first authenticate reconciles the hpc-a ->
		// researcher membership immediately. Poll briefly for safety:
		// reconcile is skipped while the claims-sync freshness window
		// (5min) holds a stale stamp.
		hasRole := func(tok, role string) bool {
			_, me := api(t, http.MethodGet, "/me", nil, tok, nil)
			for _, m := range me["memberships"].([]any) {
				mm := m.(map[string]any)
				if mm["slug"] != "acme" {
					continue
				}
				for _, r := range mm["roles"].([]any) {
					if r == role {
						return true
					}
				}
			}
			return false
		}
		poll(t, "alice reconciled as researcher", 90*time.Second,
			func() bool { return hasRole(tokAlice, "researcher") })
	})

	t.Run("04_project_policy", func(t *testing.T) {
		status, pj := api(t, http.MethodPost,
			"/tenants/acme/projects",
			map[string]any{"slug": "p1", "name": "E2E Project"},
			tokAdmin, nil)
		if status == http.StatusConflict {
			_, list := api(t, http.MethodGet,
				"/tenants/acme/projects", nil, tokAdmin, nil)
			for _, it := range list["items"].([]any) {
				m := it.(map[string]any)
				if m["slug"] == "p1" {
					pj = m
				}
			}
		} else {
			want(t, status, pj, http.StatusCreated, "create project")
		}
		projectID, _ = pj["id"].(string)
		if projectID == "" {
			t.Fatalf("project: %v", pj)
		}

		status, bd := api(t, http.MethodPost,
			"/tenants/acme/projects/p1/cluster-bindings",
			map[string]any{
				"cluster_id":         clusterID,
				"slurm_account":      "e2e-acct",
				"default_partition":  "debug",
				"allowed_partitions": []string{"debug"},
				"default_qos":        "normal",
				"allowed_qos":        []string{"normal"},
			}, tokAdmin, nil)
		if status != http.StatusCreated && status != http.StatusConflict {
			t.Fatalf("binding: HTTP %d: %v", status, bd)
		}

		status, mb := api(t, http.MethodPost,
			"/tenants/acme/projects/p1/members",
			map[string]any{"user_id": aliceID,
				"roles": []string{"project-member"}},
			tokAdmin, nil)
		if status != http.StatusCreated && status != http.StatusConflict {
			t.Fatalf("member add: HTTP %d: %v", status, mb)
		}

		// Alice needs workflow-author (workflow.create/publish) to run
		// the workflow scenario; the claim rule only grants researcher.
		status, um := api(t, http.MethodPatch,
			"/tenants/acme/members/"+aliceID,
			map[string]any{"roles": []string{"researcher", "workflow-author"}},
			tokAdmin, nil)
		want(t, status, um, http.StatusOK, "grant alice workflow-author")

		status, rp := api(t, http.MethodPut,
			"/tenants/acme/projects/p1/policies/resource",
			map[string]any{
				"policy": map[string]any{"max_walltime": "10m"},
			}, tokAdmin, nil)
		if status != http.StatusOK && status != http.StatusCreated {
			t.Fatalf("resource policy: HTTP %d: %v", status, rp)
		}
	})

	t.Run("05_adhoc_job", func(t *testing.T) {
		status, job := api(t, http.MethodPost,
			"/tenants/acme/projects/p1/jobs",
			map[string]any{
				"cluster":   "e2e",
				"partition": "debug",
				"resources": map[string]any{"tasks": 1, "walltime": "2m"},
				"script": map[string]any{
					"language": "bash",
					"body":     "echo hello; hostname; sleep 2",
				},
			}, tokAlice, map[string]string{
				"Idempotency-Key": "e2e-job-1-" + fmt.Sprint(time.Now().Unix()),
			})
		want(t, status, job, http.StatusAccepted, "submit job")
		jobID, _ := job["id"].(string)
		if jobID == "" {
			t.Fatalf("job: %v", job)
		}

		var final map[string]any
		poll(t, "job COMPLETED", 90*time.Second, func() bool {
			_, j := api(t, http.MethodGet,
				"/tenants/acme/projects/p1/jobs/"+jobID,
				nil, tokAlice, nil)
			final = j
			return j["state"] == "COMPLETED"
		})
		if final["slurm_job_id"] == nil {
			t.Fatalf("no slurm_job_id: %v", final)
		}
		if ec, ok := final["exit_code"].(float64); !ok || ec != 0 {
			t.Fatalf("exit_code = %v", final["exit_code"])
		}

		// Accounting: the real Slurm job ran under e2e-acct with a
		// custos-<spec> name.
		sid := fmt.Sprint(final["slurm_job_id"])
		sid = strings.TrimSuffix(sid, ".0")
		out := cexec(t, "slurmctld", "sacct", "-j", sid,
			"-n", "-P", "-o", "JobName,Account,State")
		if !strings.Contains(out, "e2e-acct") ||
			!strings.Contains(out, "custos-") {
			t.Fatalf("sacct -j %s: %s", sid, out)
		}
	})

	t.Run("06_script_directive_rejection", func(t *testing.T) {
		status, body := api(t, http.MethodPost,
			"/tenants/acme/projects/p1/jobs",
			map[string]any{
				"cluster":   "e2e",
				"partition": "debug",
				"resources": map[string]any{"tasks": 1, "walltime": "2m"},
				"script": map[string]any{
					"language": "bash",
					"body":     "#SBATCH --qos=high\necho nope",
				},
			}, tokAlice, map[string]string{
				"Idempotency-Key": "e2e-job-2-" + fmt.Sprint(time.Now().Unix()),
			})
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("directive job: HTTP %d (want 422): %v", status, body)
		}
		if !diagCodes(body)["CUSTOS102"] {
			t.Fatalf("want CUSTOS102 diagnostic: %v", body)
		}

		// No Slurm job was created for this submission.
		out := cexec(t, "slurmctld", "squeue", "-h", "-o", "%j %u")
		if strings.Contains(out, "nope") {
			t.Fatalf("unexpected queued job: %s", out)
		}
	})

	t.Run("07_resource_binding_policy", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body map[string]any
			code string
		}{
			{"walltime", map[string]any{
				"cluster":   "e2e",
				"partition": "debug",
				"resources": map[string]any{"tasks": 1, "walltime": "20m"},
				"script":    map[string]any{"language": "bash", "body": "echo x"},
			}, "RESOURCE_LIMIT"},
			{"qos", map[string]any{
				"cluster":   "e2e",
				"partition": "debug",
				"qos":       "high",
				"resources": map[string]any{"tasks": 1, "walltime": "2m"},
				"script":    map[string]any{"language": "bash", "body": "echo x"},
			}, "QOS_DENIED"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				status, body := api(t, http.MethodPost,
					"/tenants/acme/projects/p1/jobs", tc.body,
					tokAlice, map[string]string{
						"Idempotency-Key": "e2e-pol-" + tc.name +
							"-" + fmt.Sprint(time.Now().Unix()),
					})
				if status != http.StatusUnprocessableEntity {
					t.Fatalf("HTTP %d (want 422): %v", status, body)
				}
				if !diagCodes(body)[tc.code] && errCode(body) != tc.code {
					t.Fatalf("want %s: %v", tc.code, body)
				}
			})
		}
	})

	t.Run("08_workflow", func(t *testing.T) {
		// Upload the two task scripts.
		up := func(script string) string {
			s, r := api(t, http.MethodPost, "/tenants/acme/scripts",
				map[string]any{"language": "bash", "script": script},
				tokAlice, nil)
			want(t, s, r, http.StatusOK, "upload script")
			d, _ := r["digest"].(string)
			if d == "" {
				t.Fatalf("upload: %v", r)
			}
			return d
		}
		prepRef := up("echo prep; sleep 1")
		runRef := up("echo run; sleep 1")

		status, wf := api(t, http.MethodPost, "/tenants/acme/workflows",
			map[string]any{
				"project": projectID,
				"name":    wfName,
			}, tokAlice, nil)
		want(t, status, wf, http.StatusCreated, "create workflow")
		wfID, _ = wf["id"].(string)

		doc := map[string]any{
			"apiVersion": "custos.io/v1alpha1",
			"kind":       "Workflow",
			"metadata":   map[string]any{"name": wfName},
			"spec": map[string]any{
				"placement": map[string]any{"cluster": "e2e"},
				"defaults": map[string]any{
					"partition": "debug",
					"qos":       "normal",
				},
				"tasks": []any{
					map[string]any{
						"name": "prep",
						"type": "batch",
						"resources": map[string]any{
							"tasks": 1, "walltime": "2m",
						},
						"script": map[string]any{
							"ref": prepRef, "language": "bash",
						},
					},
					map[string]any{
						"name":      "run",
						"type":      "batch",
						"dependsOn": []string{"prep"},
						"resources": map[string]any{
							"tasks": 1, "walltime": "2m",
						},
						"script": map[string]any{
							"ref": runRef, "language": "bash",
						},
						"env": map[string]any{
							"PREP_STATE": "{{ tasks.prep.state }}",
						},
					},
				},
			},
		}
		status, ver := api(t, http.MethodPost,
			"/tenants/acme/workflows/"+wfID+"/versions", doc,
			tokAlice, nil)
		want(t, status, ver, http.StatusCreated, "create version")
		wfVerID, _ = ver["id"].(string)
		status, pub := api(t, http.MethodPost,
			fmt.Sprintf("/tenants/acme/workflows/%s/versions/%s/publish",
				wfID, wfVerID), nil, tokAlice, nil)
		if status != http.StatusOK && status != http.StatusNoContent &&
			status != http.StatusAccepted && status != http.StatusCreated {
			t.Fatalf("publish: HTTP %d: %v", status, pub)
		}

		key := "e2e-exec-1-" + fmt.Sprint(time.Now().Unix())
		status, ex := api(t, http.MethodPost,
			"/tenants/acme/workflow-executions",
			map[string]any{"workflow": wfID, "version": wfVerID},
			tokAlice, map[string]string{"Idempotency-Key": key})
		want(t, status, ex, http.StatusAccepted, "execute")
		execID, _ = ex["id"].(string)
		if execID == "" {
			t.Fatalf("execution: %v", ex)
		}
		execBody = ex

		poll(t, "execution SUCCEEDED", 180*time.Second, func() bool {
			_, x := api(t, http.MethodGet,
				"/tenants/acme/workflow-executions/"+execID,
				nil, tokAlice, nil)
			return x["state"] == "SUCCEEDED"
		})

		_, tasks := api(t, http.MethodGet,
			"/tenants/acme/workflow-executions/"+execID+"/tasks",
			nil, tokAlice, nil)
		var completed int
		items, _ := tasks["tasks"].([]any)
		for _, it := range items {
			if it.(map[string]any)["state"] == "COMPLETED" {
				completed++
			}
		}
		if completed != 2 {
			t.Fatalf("task states: %v", tasks)
		}

		// Idempotent replay returns the same execution.
		_, replay := api(t, http.MethodPost,
			"/tenants/acme/workflow-executions",
			map[string]any{"workflow": wfID, "version": wfVerID},
			tokAlice, map[string]string{"Idempotency-Key": key})
		if replay["id"] != execBody["id"] {
			t.Fatalf("replay returned %v, want %v",
				replay["id"], execBody["id"])
		}
	})

	t.Run("09_workflow_cancel", func(t *testing.T) {
		sleepRef := func() string {
			s, r := api(t, http.MethodPost, "/tenants/acme/scripts",
				map[string]any{"language": "bash", "script": "sleep 60"},
				tokAlice, nil)
			want(t, s, r, http.StatusOK, "upload sleep script")
			return r["digest"].(string)
		}()

		status, wf := api(t, http.MethodPost, "/tenants/acme/workflows",
			map[string]any{"project": projectID, "name": slowName},
			tokAlice, nil)
		want(t, status, wf, http.StatusCreated, "create slow workflow")
		slowID := wf["id"].(string)

		doc := map[string]any{
			"apiVersion": "custos.io/v1alpha1",
			"kind":       "Workflow",
			"metadata":   map[string]any{"name": slowName},
			"spec": map[string]any{
				"placement": map[string]any{"cluster": "e2e"},
				"defaults":  map[string]any{"partition": "debug", "qos": "normal"},
				"tasks": []any{
					map[string]any{
						"name": "slow", "type": "batch",
						"resources": map[string]any{
							"tasks": 1, "walltime": "2m"},
						"script": map[string]any{
							"ref": sleepRef, "language": "bash"},
					},
				},
			},
		}
		status, ver := api(t, http.MethodPost,
			"/tenants/acme/workflows/"+slowID+"/versions", doc,
			tokAlice, nil)
		want(t, status, ver, http.StatusCreated, "create slow version")
		slowVerID, _ := ver["id"].(string)
		status, pub := api(t, http.MethodPost,
			fmt.Sprintf("/tenants/acme/workflows/%s/versions/%s/publish",
				slowID, slowVerID), nil, tokAlice, nil)
		if status >= 300 {
			t.Fatalf("publish slow: HTTP %d: %v", status, pub)
		}

		status, ex := api(t, http.MethodPost,
			"/tenants/acme/workflow-executions",
			map[string]any{"workflow": slowID},
			tokAlice, map[string]string{
				"Idempotency-Key": "e2e-exec-2-" + fmt.Sprint(time.Now().Unix()),
			})
		want(t, status, ex, http.StatusAccepted, "execute slow")
		slowExec := ex["id"].(string)

		// Wait until it is actually running (or at least queued with a
		// submitted job), then cancel.
		poll(t, "slow execution has a Slurm job", 120*time.Second,
			func() bool {
				_, tasks := api(t, http.MethodGet,
					"/tenants/acme/workflow-executions/"+slowExec+"/tasks",
					nil, tokAlice, nil)
				items, _ := tasks["tasks"].([]any)
				for _, it := range items {
					if it.(map[string]any)["jobId"] != nil {
						return true
					}
				}
				return false
			})
		status, body := api(t, http.MethodPost,
			"/tenants/acme/workflow-executions/"+slowExec+"/cancel",
			nil, tokAlice, nil)
		if status != http.StatusAccepted && status != http.StatusOK {
			t.Fatalf("cancel: HTTP %d: %v", status, body)
		}
		poll(t, "execution CANCELED", 90*time.Second, func() bool {
			_, x := api(t, http.MethodGet,
				"/tenants/acme/workflow-executions/"+slowExec,
				nil, tokAlice, nil)
			return x["state"] == "CANCELED"
		})
	})
}

// readFile reads a repo-relative file from the test's working dir.
func readFile(rel string) (string, error) {
	b, err := readRepoFile(rel)
	return string(b), err
}
