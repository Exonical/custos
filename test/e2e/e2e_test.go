package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func assertConnectorCredentialIndicator(t *testing.T, connector map[string]any, want bool) {
	t.Helper()
	if got, ok := connector["has_credential"].(bool); !ok || got != want {
		t.Fatalf("has_credential = %#v, want %t", connector["has_credential"], want)
	}
	for _, field := range []string{"credential_ref", "credential"} {
		if _, found := connector[field]; found {
			t.Fatalf("connector response exposed %s", field)
		}
	}
	if config, ok := connector["config"].(map[string]any); ok {
		for _, field := range []string{"credential_ref", "credential", "secret_id", "token", "jwt"} {
			if _, found := config[field]; found {
				t.Fatalf("connector config exposed %s", field)
			}
		}
		if auth, ok := config["auth"].(map[string]any); ok {
			for _, field := range []string{"secret_id", "token", "jwt", "jwt_audience"} {
				if _, found := auth[field]; found {
					t.Fatalf("connector auth config exposed %s", field)
				}
			}
		}
	}
}

func assertConnectorConfig(t *testing.T, connector map[string]any) {
	t.Helper()
	config, ok := connector["config"].(map[string]any)
	if !ok {
		t.Fatalf("connector config = %#v, want object", connector["config"])
	}
	if config["address"] != "https://openbao-byo.e2e:8250" || config["namespace"] != "customer" || config["mount"] != "kv" {
		t.Fatalf("connector config round-trip = %#v", config)
	}
	auth, ok := config["auth"].(map[string]any)
	if !ok || auth["method"] != "token" {
		t.Fatalf("connector auth config round-trip = %#v", config["auth"])
	}
}

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
		secretRefName              string
		e2eJobID                   string
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
		if status == http.StatusOK {
			_, bobMe := api(t, http.MethodGet, "/me", nil, tokBob, nil)
			bobID, _ := bobMe["user_id"].(string)
			status, body = api(t, http.MethodDelete,
				"/tenants/acme/members/"+bobID, nil, tokAdmin, nil)
			if status != http.StatusNoContent {
				t.Fatalf("remove bob from prior run: HTTP %d: %v", status, body)
			}
			status, body = api(t, http.MethodGet, "/tenants/acme", nil, tokBob, nil)
		}
		if status != http.StatusNotFound {
			t.Fatalf("bob /tenants/acme: HTTP %d (want 404): %v", status, body)
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
			"token_ref": map[string]any{"provider": "openbao", "namespace": "custos",
				"mount": "kv", "path": "clusters/e2e", "key": "token"},
			"visibility": "assigned",
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

		// Keep one live assertion for the file provider, then restore the
		// OpenBao reference used by the rest of the scenario.
		status, cl = api(t, http.MethodPatch, "/clusters/e2e", map[string]any{
			"token_ref": map[string]any{"provider": "file", "path": "/etc/custos/secrets/slurm/token"},
			"version":   cl["version"],
		}, tokAdmin, nil)
		want(t, status, cl, http.StatusOK, "file-provider cluster credential")
		status, tc = api(t, http.MethodPost, "/clusters/e2e/test-connection", nil, tokAdmin, nil)
		want(t, status, tc, http.StatusOK, "file-provider test-connection")
		status, cl = api(t, http.MethodGet, "/clusters/e2e", nil, tokAdmin, nil)
		want(t, status, cl, http.StatusOK, "refresh cluster version")
		status, cl = api(t, http.MethodPatch, "/clusters/e2e", map[string]any{
			"token_ref": map[string]any{"provider": "openbao", "namespace": "custos",
				"mount": "kv", "path": "clusters/e2e", "key": "token"},
			"version": cl["version"],
		}, tokAdmin, nil)
		want(t, status, cl, http.StatusOK, "restore OpenBao cluster credential")

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

	t.Run("03c_secret_connectors_and_references", func(t *testing.T) {
		secretRefName = "hf-token" + runSfx
		status, refs := api(t, http.MethodPost, "/tenants/acme/secret-references",
			map[string]any{"name": secretRefName, "path": "users/" + aliceID + "/hf-token",
				"key": "value", "kind": "generic",
				"allowed_uses": []string{"workflow_env", "wrapped_token"}},
			tokAlice, nil)
		want(t, status, refs, http.StatusCreated, "create default secret reference")
		refID, _ := refs["id"].(string)
		status, tested := api(t, http.MethodPost,
			"/tenants/acme/secret-references/"+refID+"/test", nil, tokAlice, nil)
		want(t, status, tested, http.StatusOK, "test default secret reference")
		if tested["ok"] != true {
			t.Fatalf("secret test = %v", tested)
		}
		status, _ = api(t, http.MethodGet,
			"/tenants/acme/secret-references/"+refID, nil, tokBob, nil)
		if status != http.StatusNotFound {
			t.Fatalf("bob secret reference: HTTP %d, want 404", status)
		}
		for _, bad := range []map[string]any{
			{"name": "bad-path" + runSfx, "path": "users/../other", "key": "value", "kind": "generic"},
			{"name": "bad-ns" + runSfx, "namespace": "foreign", "path": "x", "key": "value", "kind": "generic"},
		} {
			status, body := api(t, http.MethodPost, "/tenants/acme/secret-references", bad, tokAlice, nil)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("invalid reference: HTTP %d want 422: %v", status, body)
			}
		}

		caPEM, err := readFile("deploy/e2e/.secrets/e2e-ca.crt")
		if err != nil {
			t.Fatal(err)
		}
		status, conn := api(t, http.MethodPost, "/tenants/acme/secret-connectors", map[string]any{
			"name": "byo" + runSfx, "kind": "openbao",
			"config": map[string]any{"address": "https://openbao-byo.e2e:8250", "ca_pem": caPEM,
				"namespace": "customer", "mount": "kv", "auth": map[string]any{"method": "token"}},
			"credential": map[string]any{"token": "e2e-byo-token"},
		}, tokAdmin, nil)
		want(t, status, conn, http.StatusCreated, "create BYO connector")
		connID, _ := conn["id"].(string)
		assertConnectorCredentialIndicator(t, conn, true)
		assertConnectorConfig(t, conn)
		status, fetchedConnector := api(t, http.MethodGet, "/tenants/acme/secret-connectors/"+connID, nil, tokAdmin, nil)
		want(t, status, fetchedConnector, http.StatusOK, "get BYO connector")
		assertConnectorCredentialIndicator(t, fetchedConnector, true)
		assertConnectorConfig(t, fetchedConnector)
		status, connectorList := api(t, http.MethodGet, "/tenants/acme/secret-connectors", nil, tokAdmin, nil)
		want(t, status, connectorList, http.StatusOK, "list connectors")
		var sawBYO, sawDefault bool
		for _, item := range connectorList["items"].([]any) {
			listed := item.(map[string]any)
			switch listed["name"] {
			case "byo" + runSfx:
				assertConnectorCredentialIndicator(t, listed, true)
				assertConnectorConfig(t, listed)
				sawBYO = true
			case "default":
				assertConnectorCredentialIndicator(t, listed, false)
				sawDefault = true
			}
		}
		if !sawBYO || !sawDefault {
			t.Fatalf("connector list missing BYO/default entries: %+v", connectorList["items"])
		}
		status, tested = api(t, http.MethodPost,
			"/tenants/acme/secret-connectors/"+connID+"/test", nil, tokAdmin, nil)
		want(t, status, tested, http.StatusOK, "test BYO connector")
		status, refs = api(t, http.MethodPost, "/tenants/acme/secret-references",
			map[string]any{"name": "byo-ref" + runSfx, "connector": connID,
				"path": "e2e", "key": "value", "kind": "generic"}, tokAlice, nil)
		want(t, status, refs, http.StatusCreated, "create BYO secret reference")
		status, tested = api(t, http.MethodPost,
			"/tenants/acme/secret-references/"+refs["id"].(string)+"/test", nil, tokAlice, nil)
		want(t, status, tested, http.StatusOK, "resolve BYO secret reference")

		for i, address := range []string{"http://openbao-byo:8200", "https://127.0.0.1:8200", "https://169.254.169.254"} {
			status, body := api(t, http.MethodPost, "/tenants/acme/secret-connectors", map[string]any{
				"name": fmt.Sprintf("bad-connector-%d%s", i, runSfx), "kind": "openbao",
				"config": map[string]any{"address": address, "ca_pem": caPEM,
					"namespace": "customer", "mount": "kv", "auth": map[string]any{"method": "token"}},
				"credential": map[string]any{"token": "e2e-byo-token"},
			}, tokAdmin, nil)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("unsafe connector %s: HTTP %d want 422: %v", address, status, body)
			}
		}
		status, _ = api(t, http.MethodGet, "/tenants/acme/secret-connectors", nil, tokBob, nil)
		if status != http.StatusNotFound {
			t.Fatalf("bob connectors: HTTP %d, want 404", status)
		}
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
		e2eJobID = jobID
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

	t.Run("05b_accounting_collection", func(t *testing.T) {
		status, body := api(t, http.MethodPost,
			"/clusters/e2e/accounting/collect", nil, tokAdmin, nil)
		if status != http.StatusAccepted {
			t.Fatalf("collect accounting: HTTP %d: %v", status, body)
		}
		poll(t, "job resource_usage", 90*time.Second, func() bool {
			_, job := api(t, http.MethodGet,
				"/tenants/acme/projects/p1/jobs/"+e2eJobID, nil, tokAlice, nil)
			return job["resource_usage"] != nil
		})
		status, body = api(t, http.MethodPost,
			"/clusters/e2e/accounting/aggregate", nil, tokAdmin, nil)
		if status != http.StatusAccepted {
			t.Fatalf("aggregate accounting: HTTP %d: %v", status, body)
		}
		from := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
		to := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
		path := "/tenants/acme/accounting/usage?group_by=user&from=" + from + "&to=" + to
		tooOld := time.Now().Add(-401 * 24 * time.Hour).UTC().Format(time.RFC3339)
		status, body = api(t, http.MethodGet,
			"/tenants/acme/accounting/usage?group_by=user&from="+tooOld+"&to="+to,
			nil, tokAlice, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("accounting range: HTTP %d: %v", status, body)
		}
		poll(t, "alice usage aggregate", 90*time.Second, func() bool {
			status, usage := api(t, http.MethodGet, path, nil, tokAlice, nil)
			if status != http.StatusOK {
				return false
			}
			items, _ := usage["items"].([]any)
			for _, item := range items {
				row := item.(map[string]any)
				if row["key"] == aliceID && row["jobs"].(float64) >= 1 {
					return true
				}
			}
			return false
		})
	})

	t.Run("05c_allocations", func(t *testing.T) {
		status, bindList := api(t, http.MethodGet, "/tenants/acme/projects/p1/cluster-bindings", nil, tokAdmin, nil)
		want(t, status, bindList, http.StatusOK, "list allocation binding")
		bindings, _ := bindList["items"].([]any)
		if len(bindings) == 0 {
			t.Fatal("missing project binding")
		}
		binding := bindings[0].(map[string]any)
		bindingID := binding["id"].(string)
		now := time.Now().UTC()
		start := now.Add(-time.Hour).Format(time.RFC3339)
		end := now.Add(time.Hour).Format(time.RFC3339)
		status, allocation := api(t, http.MethodPost, "/tenants/acme/projects/p1/allocations", map[string]any{"binding_id": bindingID, "name": "e2e-budget" + runSfx, "unit": "cpu_hours", "limit_amount": 0.001, "period_start": start, "period_end": end, "enforcement": "hard"}, tokAdmin, nil)
		want(t, status, allocation, http.StatusCreated, "create hard allocation")
		aid := allocation["id"].(string)
		status, body := api(t, http.MethodGet, "/tenants/acme/projects/p1/allocations/"+aid, nil, tokAlice, nil)
		want(t, status, body, http.StatusOK, "researcher reads allocation")
		status, body = api(t, http.MethodPost, "/tenants/acme/projects/p1/allocations", map[string]any{"binding_id": bindingID, "name": "forbidden" + runSfx, "unit": "cpu_hours", "limit_amount": 1, "period_start": start, "period_end": end, "enforcement": "hard"}, tokAlice, nil)
		if status != http.StatusForbidden {
			t.Fatalf("researcher allocation create: HTTP %d: %v", status, body)
		}
		jobRequest := map[string]any{"cluster": "e2e", "partition": "debug", "resources": map[string]any{"tasks": 1, "walltime": "2m"}, "script": map[string]any{"language": "bash", "body": "echo allocation"}}
		status, denied := api(t, http.MethodPost, "/tenants/acme/projects/p1/jobs", jobRequest, tokAlice, map[string]string{"Idempotency-Key": "e2e-allocation-denied-" + runSfx})
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("hard allocation: HTTP %d: %v", status, denied)
		}
		if e, ok := denied["error"].(map[string]any); !ok || e["code"] != "ALLOCATION_EXHAUSTED" {
			t.Fatalf("allocation denial code: %v", denied)
		}
		status, updated := api(t, http.MethodPatch, "/tenants/acme/projects/p1/allocations/"+aid, map[string]any{"limit_amount": 1.0, "version": allocation["version"]}, tokAdmin, nil)
		want(t, status, updated, http.StatusOK, "raise allocation limit")
		status, job := api(t, http.MethodPost, "/tenants/acme/projects/p1/jobs", jobRequest, tokAlice, map[string]string{"Idempotency-Key": "e2e-allocation-allowed-" + runSfx})
		want(t, status, job, http.StatusAccepted, "submit under allocation")
		jobID := job["id"].(string)
		poll(t, "allocation job completed", 90*time.Second, func() bool {
			_, current := api(t, http.MethodGet, "/tenants/acme/projects/p1/jobs/"+jobID, nil, tokAlice, nil)
			return current["state"] == "COMPLETED"
		})
		_, allocList := api(t, http.MethodGet, "/tenants/acme/accounting/allocations", nil, tokAlice, nil)
		items, ok := allocList["items"].([]any)
		if !ok || len(items) == 0 {
			t.Fatalf("accounting allocations response: %v", allocList)
		}
		view := items[0].(map[string]any)
		for _, key := range []string{"consumed", "remaining", "percent_used", "as_of"} {
			if _, ok := view[key]; !ok {
				t.Fatalf("allocation summary missing %s: %v", key, view)
			}
		}
		status, body = api(t, http.MethodDelete, "/tenants/acme/projects/p1/allocations/"+aid, nil, tokAdmin, nil)
		if status != http.StatusNoContent {
			t.Fatalf("delete allocation: HTTP %d: %v", status, body)
		}
		poll(t, "base account GrpTRESMins cleared after allocation deletion", 30*time.Second, func() bool {
			assoc := slurmAssociation(t, "e2e-acct", "", "")
			if assoc == nil {
				return false
			}
			value, ok := tresMinuteValue(assoc, "cpu")
			return !ok || value < 0
		})
	})

	t.Run("05d_policy_sync", func(t *testing.T) {
		setClusterPolicyMode(t, "enforce", tokAdmin)
		status, p2 := api(t, http.MethodPost, "/tenants/acme/projects", map[string]any{"slug": "p2", "name": "Drift Project"}, tokAdmin, nil)
		if status == http.StatusConflict {
			_, list := api(t, http.MethodGet, "/tenants/acme/projects", nil, tokAdmin, nil)
			for _, raw := range list["items"].([]any) {
				p := raw.(map[string]any)
				if p["slug"] == "p2" {
					p2 = p
				}
			}
		} else {
			want(t, status, p2, http.StatusCreated, "create drift project")
		}
		status, stale := api(t, http.MethodGet, "/tenants/acme/projects/p2/cluster-bindings", nil, tokAdmin, nil)
		want(t, status, stale, http.StatusOK, "list stale policy bindings")
		for _, raw := range stale["items"].([]any) {
			staleBinding := raw.(map[string]any)
			id := staleBinding["id"].(string)
			status, body := api(t, http.MethodDelete, "/tenants/acme/projects/p2/cluster-bindings/"+id, nil, tokAdmin, nil)
			if status != http.StatusNoContent {
				t.Fatalf("clean stale policy binding %s: HTTP %d %v", id, status, body)
			}
		}
		triggerPolicySync(t, tokAdmin)
		poll(t, "stale managed policy accounts cleaned", 30*time.Second, func() bool { return !slurmAccountExists(t, "custos-e2e-managed") })
		reportAccount := "nope-" + runSfx
		setClusterPolicyMode(t, "report", tokAdmin)
		status, b := api(t, http.MethodPost, "/tenants/acme/projects/p2/cluster-bindings", map[string]any{"cluster_id": clusterID, "slurm_account": reportAccount, "default_partition": "debug", "allowed_partitions": []string{"debug"}, "default_qos": "normal", "allowed_qos": []string{"normal"}}, tokAdmin, nil)
		if status != http.StatusCreated && status != http.StatusConflict {
			t.Fatalf("create drift binding: HTTP %d: %v", status, b)
		}
		status, body := api(t, http.MethodPost, "/clusters/e2e/policy-sync", nil, tokAdmin, nil)
		want(t, status, body, http.StatusAccepted, "trigger policy sync")
		var driftBindingID string
		poll(t, "policy drift recorded", 30*time.Second, func() bool {
			_, goodList := api(t, http.MethodGet, "/tenants/acme/projects/p1/cluster-bindings", nil, tokAdmin, nil)
			goodItems, _ := goodList["items"].([]any)
			if len(goodItems) == 0 {
				return false
			}
			goodBinding := goodItems[0].(map[string]any)
			if goodBinding["drift_state"] != "drift" {
				return false
			}
			goodDrift, _ := goodBinding["drift"].([]any)
			retainedDefault := false
			for _, raw := range goodDrift {
				if raw.(map[string]any)["code"] == "DEFAULT_ASSOCIATION_RETAINED" {
					retainedDefault = true
				}
			}
			if !retainedDefault {
				return false
			}
			_, list := api(t, http.MethodGet, "/tenants/acme/projects/p2/cluster-bindings", nil, tokAdmin, nil)
			items, _ := list["items"].([]any)
			if len(items) == 0 {
				return false
			}
			b := items[0].(map[string]any)
			driftBindingID, _ = b["id"].(string)
			state, _ := b["drift_state"].(string)
			drift, _ := b["drift"].([]any)
			found := false
			for _, raw := range drift {
				if raw.(map[string]any)["code"] == "ACCOUNT_MISSING" {
					found = true
				}
			}
			return state == "drift" && found
		})
		_, summary := api(t, http.MethodGet, "/clusters/e2e/policy-sync", nil, tokAdmin, nil)
		if summary["drifted"].(float64) < 1 || summary["mode"] != "report" {
			t.Fatalf("policy summary: %v", summary)
		}
		_, plan := api(t, http.MethodGet, "/clusters/e2e/policy-sync/plan", nil, tokAdmin, nil)
		ops, _ := plan["ops"].([]any)
		if len(ops) == 0 {
			t.Fatalf("expected dry-run create plan: %v", plan)
		}
		accounts := slurmDBGET(t, "accounts/", url.Values{})["accounts"].([]any)
		for _, raw := range accounts {
			if raw.(map[string]any)["name"] == reportAccount {
				t.Fatalf("report mode created the %s account", reportAccount)
			}
		}
		status, body = api(t, http.MethodDelete, "/tenants/acme/projects/p2/cluster-bindings/"+driftBindingID, nil, tokAdmin, nil)
		if status != http.StatusNoContent {
			t.Fatalf("delete drift binding: HTTP %d: %v", status, body)
		}
		setClusterPolicyMode(t, "inherit", tokAdmin)
	})

	t.Run("05e_slurmdb_policy_administration", func(t *testing.T) {
		setClusterPolicyMode(t, "enforce", tokAdmin)
		const account = "custos-e2e-managed"
		status, bindings := api(t, http.MethodGet, "/tenants/acme/projects/p2/cluster-bindings", nil, tokAdmin, nil)
		want(t, status, bindings, http.StatusOK, "list managed-policy bindings")
		items, _ := bindings["items"].([]any)
		var bindingID string
		for _, raw := range items {
			b := raw.(map[string]any)
			if b["slurm_account"] == account {
				bindingID = b["id"].(string)
				break
			}
		}
		if bindingID == "" {
			status, b := api(t, http.MethodPost, "/tenants/acme/projects/p2/cluster-bindings", map[string]any{"cluster_id": clusterID, "slurm_account": account, "default_partition": "debug", "allowed_partitions": []string{"debug"}, "default_qos": "normal", "allowed_qos": []string{"normal"}}, tokAdmin, nil)
			want(t, status, b, http.StatusCreated, "create managed binding")
			bindingID = b["id"].(string)
		}
		poll(t, "managed account and service association created", 30*time.Second, func() bool {
			info := slurmAccountInfo(t, account)
			return info != nil && info["description"] == "custos:acme/p2" && info["organization"] == "custos" && slurmAssociationExists(t, account, "custos", "debug", "normal")
		})
		accountAssoc := slurmAssociation(t, account, "", "")
		if accountAssoc == nil || (stringOrEmpty(accountAssoc["parent_account"]) != "" && accountAssoc["parent_account"] != "root") {
			t.Fatalf("managed account parent association=%v", accountAssoc)
		}
		_, policySummary := api(t, http.MethodGet, "/clusters/e2e/policy-sync", nil, tokAdmin, nil)
		if policySummary["mode"] != "enforce" {
			t.Fatalf("enforce summary=%v", policySummary)
		}
		if _, ok := policySummary["ops_applied"].(float64); !ok {
			t.Fatalf("summary missing ops counters: %v", policySummary)
		}
		jobReq := map[string]any{"cluster": "e2e", "partition": "debug", "resources": map[string]any{"tasks": 1, "walltime": "2m"}, "script": map[string]any{"language": "bash", "body": "echo managed-policy"}}
		status, job := api(t, http.MethodPost, "/tenants/acme/projects/p2/jobs", jobReq, tokAdmin, map[string]string{"Idempotency-Key": "e2e-managed-policy-" + runSfx})
		want(t, status, job, http.StatusAccepted, "submit through managed account")
		jobID := job["id"].(string)
		poll(t, "managed-account job completed", 90*time.Second, func() bool {
			_, current := api(t, http.MethodGet, "/tenants/acme/projects/p2/jobs/"+jobID, nil, tokAdmin, nil)
			return current["state"] == "COMPLETED"
		})
		now := time.Now().UTC()
		start := now.Add(-time.Hour).Format(time.RFC3339)
		end := now.Add(time.Hour).Format(time.RFC3339)
		status, allocation := api(t, http.MethodPost, "/tenants/acme/projects/p2/allocations", map[string]any{"binding_id": bindingID, "name": "policy-limit" + runSfx, "unit": "cpu_hours", "limit_amount": 1.25, "period_start": start, "period_end": end, "enforcement": "hard"}, tokAdmin, nil)
		want(t, status, allocation, http.StatusCreated, "create pushed hard allocation")
		poll(t, "cpu GrpTRESMins set", 30*time.Second, func() bool {
			assoc := slurmAssociation(t, account, "", "")
			value, ok := tresMinuteValue(assoc, "cpu")
			return ok && value == 75
		})
		status, body := api(t, http.MethodDelete, "/tenants/acme/projects/p2/allocations/"+allocation["id"].(string), nil, tokAdmin, nil)
		if status != http.StatusNoContent {
			t.Fatalf("delete pushed allocation: HTTP %d %v", status, body)
		}
		poll(t, "cpu GrpTRESMins cleared", 30*time.Second, func() bool {
			assoc := slurmAssociation(t, account, "", "")
			if assoc == nil {
				return false
			}
			value, ok := tresMinuteValue(assoc, "cpu")
			return !ok || value < 0
		})
		cleared := slurmAssociation(t, account, "", "")
		fixture, _ := json.Marshal(cleared)
		t.Logf("live Slurm 26.05.4 unset GrpTRESMins fixture: %s", fixture)
		_ = cexec(t, "slurmctld", "sacctmgr", "-i", "modify", "user", "where", "names=custos", "accounts="+account, "clusters=e2e", "partitions=debug", "set", "qoslevel=normal,high")
		if !slurmAssociationHasQoS(slurmAssociation(t, account, "custos", "debug"), "high") {
			t.Fatal("test setup failed to add extra QoS")
		}
		triggerPolicySync(t, tokAdmin)
		poll(t, "extra QoS removed", 30*time.Second, func() bool { return qosExactly(slurmAssociation(t, account, "custos", "debug"), "normal") })
		setClusterPolicyMode(t, "report", tokAdmin)
		_, bindingList := api(t, http.MethodGet, "/tenants/acme/projects/p2/cluster-bindings", nil, tokAdmin, nil)
		binding := findBinding(t, bindingList, bindingID)
		status, updated := api(t, http.MethodPatch, "/tenants/acme/projects/p2/cluster-bindings/"+bindingID, map[string]any{"allowed_qos": []string{"high"}, "default_qos": "high", "version": binding["version"]}, tokAdmin, nil)
		want(t, status, updated, http.StatusOK, "change binding QoS in report mode")
		poll(t, "report-mode QoS drift", 30*time.Second, func() bool {
			_, list := api(t, http.MethodGet, "/tenants/acme/projects/p2/cluster-bindings", nil, tokAdmin, nil)
			b := findBinding(t, list, bindingID)
			return b["drift_state"] == "drift" && bindingHasDrift(b, "ASSOCIATION_QOS_MISMATCH")
		})
		if !qosExactly(slurmAssociation(t, account, "custos", "debug"), "normal") {
			t.Fatal("report mode changed the Slurm association")
		}
		_, plan := api(t, http.MethodGet, "/clusters/e2e/policy-sync/plan", nil, tokAdmin, nil)
		if plan["mode"] != "report" || len(plan["ops"].([]any)) == 0 {
			t.Fatalf("report plan=%v", plan)
		}
		setClusterPolicyMode(t, "enforce", tokAdmin)
		status, updated = api(t, http.MethodPatch, "/tenants/acme/projects/p2/cluster-bindings/"+bindingID, map[string]any{"allowed_qos": []string{"normal"}, "default_qos": "normal", "version": updated["version"]}, tokAdmin, nil)
		want(t, status, updated, http.StatusOK, "restore binding QoS")
		poll(t, "enforce mode converged", 30*time.Second, func() bool {
			_, list := api(t, http.MethodGet, "/tenants/acme/projects/p2/cluster-bindings", nil, tokAdmin, nil)
			return findBinding(t, list, bindingID)["drift_state"] == "ok"
		})
		status, body = api(t, http.MethodDelete, "/tenants/acme/projects/p2/cluster-bindings/"+bindingID, nil, tokAdmin, nil)
		if status != http.StatusNoContent {
			t.Fatalf("delete managed binding: HTTP %d %v", status, body)
		}
		poll(t, "managed Slurm objects removed", 30*time.Second, func() bool {
			return !slurmAccountExists(t, account) && !slurmAssociationExists(t, account, "custos", "debug", "")
		})
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

	t.Run("08b_workflow_secret_delivery", func(t *testing.T) {
		upload := func(script string) string {
			s, body := api(t, http.MethodPost, "/tenants/acme/scripts",
				map[string]any{"language": "bash", "script": script}, tokAlice, nil)
			want(t, s, body, http.StatusOK, "upload secret workflow script")
			return body["digest"].(string)
		}
		create := func(name, mode, script string) (string, string) {
			status, wf := api(t, http.MethodPost, "/tenants/acme/workflows",
				map[string]any{"project": projectID, "name": name}, tokAlice, nil)
			want(t, status, wf, http.StatusCreated, "create secret workflow")
			wid := wf["id"].(string)
			use := map[string]any{"ref": secretRefName, "use": mode}
			task := map[string]any{"name": "run", "type": "batch",
				"resources": map[string]any{"tasks": 1, "walltime": "2m"},
				"script":    map[string]any{"ref": upload(script), "language": "bash"}}
			if mode == "env" {
				use["envName"] = "HF_TOKEN"
				task["env"] = map[string]any{"HF_TOKEN": "{{ secrets.hf }}"}
			}
			doc := map[string]any{"apiVersion": "custos.io/v1alpha1", "kind": "Workflow",
				"metadata": map[string]any{"name": name}, "spec": map[string]any{
					"placement": map[string]any{"cluster": "e2e"},
					"defaults":  map[string]any{"partition": "debug", "qos": "normal"},
					"secrets":   map[string]any{"hf": use}, "tasks": []any{task}}}
			status, ver := api(t, http.MethodPost,
				"/tenants/acme/workflows/"+wid+"/versions", doc, tokAlice, nil)
			want(t, status, ver, http.StatusCreated, "create secret workflow version")
			vid := ver["id"].(string)
			status, body := api(t, http.MethodPost,
				fmt.Sprintf("/tenants/acme/workflows/%s/versions/%s/publish", wid, vid),
				nil, tokAlice, nil)
			want(t, status, body, http.StatusOK, "publish secret workflow")
			return wid, vid
		}
		execute := func(tok, wid, vid, suffix string) string {
			status, ex := api(t, http.MethodPost, "/tenants/acme/workflow-executions",
				map[string]any{"workflow": wid, "version": vid}, tok,
				map[string]string{"Idempotency-Key": "secret-" + suffix + runSfx})
			want(t, status, ex, http.StatusAccepted, "execute secret workflow")
			return ex["id"].(string)
		}

		envID, envVer := create("secret-env"+runSfx, "env",
			`test -n "$HF_TOKEN" && echo ok`)
		envExec := execute(tokAlice, envID, envVer, "env")
		poll(t, "env-secret execution SUCCEEDED", 180*time.Second, func() bool {
			_, body := api(t, http.MethodGet,
				"/tenants/acme/workflow-executions/"+envExec, nil, tokAlice, nil)
			return body["state"] == "SUCCEEDED"
		})

		wrapID, wrapVer := create("secret-wrap"+runSfx, "wrapped_token", "sleep 2")
		wrapExec := execute(tokAlice, wrapID, wrapVer, "wrap")
		poll(t, "wrapped-secret execution SUCCEEDED", 180*time.Second, func() bool {
			_, body := api(t, http.MethodGet,
				"/tenants/acme/workflow-executions/"+wrapExec, nil, tokAlice, nil)
			return body["state"] == "SUCCEEDED"
		})
		_, tasks := api(t, http.MethodGet,
			"/tenants/acme/workflow-executions/"+wrapExec+"/tasks", nil, tokAlice, nil)
		items := tasks["tasks"].([]any)
		taskID := items[0].(map[string]any)["id"].(string)
		status, frozen := api(t, http.MethodGet,
			fmt.Sprintf("/tenants/acme/workflow-executions/%s/tasks/%s/execution-spec", wrapExec, taskID),
			nil, tokAlice, nil)
		want(t, status, frozen, http.StatusOK, "wrapped execution spec")
		security := frozen["security"].(map[string]any)
		if refs, _ := security["wrapped_token_refs"].([]any); len(refs) != 1 || refs[0] != "hf" {
			t.Fatalf("wrapped_token_refs: %v", security)
		}
		auditOut := cexec(t, "postgres", "sh", "-c",
			`PGPASSWORD=$(cat /run/secrets/postgres-superuser-password) psql -U postgres -d custos -tAc "SELECT count(*) FROM audit_events WHERE action='secret.accessed' AND details->>'purpose'='wrapped_token'"`)
		if strings.TrimSpace(auditOut) == "0" {
			t.Fatal("wrapped_token secret.accessed audit event missing")
		}
		leaks := cexec(t, "postgres", "sh", "-c",
			`PGPASSWORD=$(cat /run/secrets/postgres-superuser-password) psql -U postgres -d custos -tAc "SELECT (SELECT count(*) FROM audit_events WHERE details::text LIKE '%e2e-hf-token%') + (SELECT count(*) FROM jobs j WHERE row_to_json(j)::text LIKE '%e2e-hf-token%')"`)
		if strings.TrimSpace(leaks) != "0" {
			t.Fatalf("secret value persisted in jobs/audit: %s", leaks)
		}

		_, bobMe := api(t, http.MethodGet, "/me", nil, tokBob, nil)
		bobID := bobMe["user_id"].(string)
		status, body := api(t, http.MethodPost, "/tenants/acme/members",
			map[string]any{"user_id": bobID, "roles": []string{"researcher"}}, tokAdmin, nil)
		if status != http.StatusCreated && status != http.StatusConflict {
			t.Fatalf("add bob: HTTP %d: %v", status, body)
		}
		bobExec := execute(tokBob, envID, envVer, "bob")
		poll(t, "bob secret execution forbidden", 60*time.Second, func() bool {
			_, body := api(t, http.MethodGet,
				"/tenants/acme/workflow-executions/"+bobExec, nil, tokBob, nil)
			return body["state"] == "FAILED" && body["stateReason"] == "SECRET_FORBIDDEN"
		})
		status, body = api(t, http.MethodDelete,
			"/tenants/acme/members/"+bobID, nil, tokAdmin, nil)
		if status != http.StatusNoContent {
			t.Fatalf("remove bob: HTTP %d: %v", status, body)
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

	t.Run("10_node_isolation_submission", func(t *testing.T) {
		// Prolog/Epilog cannot run here (slurmd is unprivileged); this
		// covers the submission side of ADR-032: the node-sharing mode
		// and MCS label Custos sends to real Slurm, plus the bundle,
		// token and pull endpoints.
		nodeConfig := func(mode, mechanism string) map[string]any {
			return map[string]any{
				"isolation_mode": mode, "tenant_exclusive_mechanism": mechanism,
				"mount_timeout_seconds": 30,
				"shared_mounts":         []any{}, "tenant_mounts": []any{}, "hooks": []any{},
			}
		}
		status, cur := api(t, http.MethodGet, "/clusters/e2e/node-config", nil, tokAdmin, nil)
		want(t, status, cur, http.StatusOK, "get node config")
		version := int(cur["version"].(float64))
		putMode := func(mode, mechanism string) map[string]any {
			t.Helper()
			status, view := api(t, http.MethodPut, "/clusters/e2e/node-config",
				map[string]any{"config": nodeConfig(mode, mechanism), "version": version},
				tokAdmin, nil)
			want(t, status, view, http.StatusOK, "put node config "+mode)
			version = int(view["version"].(float64))
			return view
		}
		t.Cleanup(func() {
			status, view := api(t, http.MethodPut, "/clusters/e2e/node-config",
				map[string]any{"config": nodeConfig("namespace", "mcs_label"), "version": version},
				tokAdmin, nil)
			if status != http.StatusOK {
				t.Errorf("restore default node config: HTTP %d: %v", status, view)
			}
		})
		submit := func(name string, resources map[string]any) (int, map[string]any) {
			return api(t, http.MethodPost, "/tenants/acme/projects/p1/jobs",
				map[string]any{
					"cluster": "e2e", "partition": "debug", "resources": resources,
					"script": map[string]any{"language": "bash", "body": "echo " + name + "; sleep 1"},
				}, tokAlice, map[string]string{
					"Idempotency-Key": "e2e-" + name + "-" + fmt.Sprint(time.Now().UnixNano()),
				})
		}
		// runJob submits, waits for COMPLETED and returns `scontrol show
		// job` captured while the record is still in memory (MinJobAge).
		runJob := func(name string, resources map[string]any) string {
			t.Helper()
			status, job := submit(name, resources)
			want(t, status, job, http.StatusAccepted, "submit "+name)
			jobID, _ := job["id"].(string)
			var final map[string]any
			poll(t, name+" COMPLETED", 90*time.Second, func() bool {
				_, j := api(t, http.MethodGet, "/tenants/acme/projects/p1/jobs/"+jobID,
					nil, tokAlice, nil)
				final = j
				return j["state"] == "COMPLETED"
			})
			sid := strings.TrimSuffix(fmt.Sprint(final["slurm_job_id"]), ".0")
			out := cexec(t, "slurmctld", "scontrol", "show", "job", sid)
			t.Logf("%s: slurm job %s: %s", name, sid, scontrolFields(out, "JobState", "OverSubscribe", "Exclusive", "MCS_label", "Account", "Partition"))
			return out
		}
		oneCPU := func() map[string]any { return map[string]any{"tasks": 1, "walltime": "2m"} }

		// (a) tenant_exclusive + mcs_label: shared=mcs, label = tenant slug.
		putMode("tenant_exclusive", "mcs_label")
		out := runJob("node-mcs", oneCPU())
		if f := scontrolField(out, "Exclusive"); f != "MCS" {
			t.Fatalf("tenant_exclusive/mcs_label: Exclusive=%q, want MCS\n%s", f, out)
		}
		if f := scontrolField(out, "MCS_label"); f != "acme" {
			t.Fatalf("tenant_exclusive/mcs_label: MCS_label=%q, want acme\n%s", f, out)
		}

		// Bundle download, token, pull (200 then 304) and node status.
		hdr := map[string]string{"Authorization": "Bearer " + tokAdmin}
		resp := rawGet(t, "/api/v1/clusters/e2e/node-config/bundle", hdr)
		if resp.status != http.StatusOK || !strings.Contains(resp.header.Get("Content-Disposition"), "custos-node-e2e-r") {
			t.Fatalf("bundle: HTTP %d %v", resp.status, resp.header)
		}
		files := untarGz(t, resp.body)
		tsv := string(files["etc/custos/node/mounts.tsv"])
		if !strings.Contains(tsv, "meta\tmode\ttenant_exclusive") || !strings.Contains(tsv, "meta\tmechanism\tmcs_label") {
			t.Fatalf("mounts.tsv lacks the configured mode:\n%s", tsv)
		}
		for _, name := range []string{"etc/custos/node/prolog.d/900-custos-mounts",
			"etc/custos/node/epilog.d/100-custos-unmount", "etc/custos/node/custos-node-sync", "README.md"} {
			if len(files[name]) == 0 {
				t.Fatalf("bundle lacks %s (have %d files)", name, len(files))
			}
		}

		status, tok := api(t, http.MethodPost, "/clusters/e2e/node-tokens",
			map[string]any{"name": "e2e-node" + runSfx}, tokAdmin, nil)
		want(t, status, tok, http.StatusCreated, "create node token")
		nodeTok, _ := tok["token"].(string)
		tokenID, _ := tok["id"].(string)
		if !strings.HasPrefix(nodeTok, "cnt_") || tokenID == "" {
			t.Fatalf("node token response: %v", tok)
		}
		revoked := false
		t.Cleanup(func() {
			if !revoked {
				api(t, http.MethodDelete, "/clusters/e2e/node-tokens/"+tokenID, nil, tokAdmin, nil)
			}
		})
		pullHdr := map[string]string{"Authorization": "Bearer " + nodeTok, "X-Custos-Node": "c1"}
		first := rawGet(t, "/api/v1/node/bundle", pullHdr)
		etag := first.header.Get("ETag")
		if first.status != http.StatusOK || etag == "" || first.header.Get("X-Custos-Revision") == "" {
			t.Fatalf("node pull: HTTP %d %v", first.status, first.header)
		}
		if len(untarGz(t, first.body)["etc/custos/node/mounts.tsv"]) == 0 {
			t.Fatal("node pull archive lacks mounts.tsv")
		}
		pullHdr["If-None-Match"] = etag
		if again := rawGet(t, "/api/v1/node/bundle", pullHdr); again.status != http.StatusNotModified {
			t.Fatalf("conditional pull: HTTP %d, want 304", again.status)
		}
		status, ns := api(t, http.MethodGet, "/clusters/e2e/node-status", nil, tokAdmin, nil)
		want(t, status, ns, http.StatusOK, "node status")
		var seen bool
		for _, it := range ns["items"].([]any) {
			row := it.(map[string]any)
			if row["node_name"] == "c1" {
				seen = true
				if row["bundle_sha256"] != strings.Trim(etag, `"`) || row["stale"] != false {
					t.Fatalf("node status row = %v (etag %s)", row, etag)
				}
			}
		}
		if !seen || ns["current_bundle_sha256"] != strings.Trim(etag, `"`) {
			t.Fatalf("node c1 missing from node-status: %v", ns)
		}
		status, rv := api(t, http.MethodDelete, "/clusters/e2e/node-tokens/"+tokenID, nil, tokAdmin, nil)
		if status != http.StatusNoContent {
			t.Fatalf("revoke node token: HTTP %d: %v", status, rv)
		}
		revoked = true
		delete(pullHdr, "If-None-Match")
		if after := rawGet(t, "/api/v1/node/bundle", pullHdr); after.status != http.StatusUnauthorized {
			t.Fatalf("pull with revoked token: HTTP %d, want 401", after.status)
		}

		// (b) node_exclusive is exclusive even though the project policy
		// does not allow users to request exclusive.
		putMode("node_exclusive", "mcs_label")
		out = runJob("node-exclusive", oneCPU())
		if f := scontrolField(out, "Exclusive"); f != "NODE" {
			t.Fatalf("node_exclusive: Exclusive=%q, want NODE\n%s", f, out)
		}

		// (c) namespace mode: a user exclusive request is gated by the
		// policy and, once allowed, reaches Slurm (the old defect dropped it).
		putMode("namespace", "mcs_label")
		out = runJob("node-namespace", oneCPU())
		if f := scontrolField(out, "Exclusive"); f != "NO" || scontrolField(out, "MCS_label") != "N/A" {
			t.Fatalf("namespace job must carry no sharing option: Exclusive=%q\n%s", f, out)
		}
		exclusive := oneCPU()
		exclusive["exclusive"] = true
		status, denied := submit("denied-exclusive", exclusive)
		if status == http.StatusAccepted {
			t.Fatalf("exclusive accepted without allow_exclusive: %v", denied)
		}
		t.Logf("exclusive without policy: HTTP %d %v", status, errCode(denied))
		policyURL := "/tenants/acme/projects/p1/policies/resource"
		setPolicy := func(p map[string]any) {
			t.Helper()
			status, body := api(t, http.MethodPut, policyURL, map[string]any{"policy": p}, tokAdmin, nil)
			if status != http.StatusOK && status != http.StatusCreated {
				t.Fatalf("project resource policy: HTTP %d: %v", status, body)
			}
		}
		t.Cleanup(func() { setPolicy(map[string]any{"max_walltime": "10m"}) })
		setPolicy(map[string]any{"max_walltime": "10m", "allow_exclusive": true})
		out = runJob("user-exclusive", exclusive)
		if f := scontrolField(out, "Exclusive"); f != "NODE" {
			t.Fatalf("namespace + resources.exclusive: Exclusive=%q, want NODE\n%s", f, out)
		}
	})
}

func triggerPolicySync(t *testing.T, token string) {
	t.Helper()
	status, body := api(t, http.MethodPost, "/clusters/e2e/policy-sync", nil, token, nil)
	want(t, status, body, http.StatusAccepted, "trigger policy sync")
}

func slurmAccountInfo(t *testing.T, name string) map[string]any {
	t.Helper()
	items, _ := slurmDBGET(t, "accounts/", url.Values{})["accounts"].([]any)
	for _, raw := range items {
		account, ok := raw.(map[string]any)
		if ok && account["name"] == name {
			return account
		}
	}
	return nil
}
func slurmAccountExists(t *testing.T, name string) bool { return slurmAccountInfo(t, name) != nil }

func slurmAssociation(t *testing.T, account, user, partition string) map[string]any {
	t.Helper()
	query := url.Values{"account": {account}, "cluster": {"e2e"}}
	if user != "" {
		query.Set("user", user)
	}
	if partition != "" {
		query.Set("partition", partition)
	}
	items, _ := slurmDBGET(t, "associations/", query)["associations"].([]any)
	for _, raw := range items {
		assoc := raw.(map[string]any)
		if assoc["account"] == account && stringOrEmpty(assoc["user"]) == user && stringOrEmpty(assoc["partition"]) == partition {
			return assoc
		}
	}
	return nil
}
func slurmAssociationExists(t *testing.T, account, user, partition, qos string) bool {
	assoc := slurmAssociation(t, account, user, partition)
	if assoc == nil {
		return false
	}
	return qos == "" || slurmAssociationHasQoS(assoc, qos)
}
func slurmAssociationHasQoS(assoc map[string]any, name string) bool {
	if assoc == nil {
		return false
	}
	items, _ := assoc["qos"].([]any)
	for _, q := range items {
		if q == name {
			return true
		}
	}
	return false
}
func qosExactly(assoc map[string]any, names ...string) bool {
	if assoc == nil {
		return false
	}
	got, _ := assoc["qos"].([]any)
	if len(got) != len(names) {
		return false
	}
	for _, name := range names {
		if !slurmAssociationHasQoS(assoc, name) {
			return false
		}
	}
	return true
}
func tresMinuteValue(assoc map[string]any, key string) (int64, bool) {
	if assoc == nil {
		return 0, false
	}
	maxRecord, _ := assoc["max"].(map[string]any)
	tres, _ := maxRecord["tres"].(map[string]any)
	group, _ := tres["group"].(map[string]any)
	items, _ := group["minutes"].([]any)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.ToLower(stringOrEmpty(item["type"]))
		if gres := stringOrEmpty(item["name"]); gres != "" {
			name += "/" + strings.ToLower(gres)
		}
		if name == key {
			if n, ok := item["count"].(float64); ok {
				return int64(n), true
			}
		}
	}
	return 0, false
}
func stringOrEmpty(v any) string { s, _ := v.(string); return s }
func findBinding(t *testing.T, response map[string]any, id string) map[string]any {
	t.Helper()
	items, _ := response["items"].([]any)
	for _, raw := range items {
		b := raw.(map[string]any)
		if b["id"] == id {
			return b
		}
	}
	t.Fatalf("binding %s missing from %v", id, response)
	return nil
}
func bindingHasDrift(binding map[string]any, code string) bool {
	items, _ := binding["drift"].([]any)
	for _, raw := range items {
		if raw.(map[string]any)["code"] == code {
			return true
		}
	}
	return false
}

func setClusterPolicyMode(t *testing.T, mode, token string) {
	t.Helper()
	status, cluster := api(t, http.MethodGet, "/clusters/e2e", nil, token, nil)
	want(t, status, cluster, http.StatusOK, "get cluster policy mode")
	version := int(cluster["version"].(float64))
	status, updated := api(t, http.MethodPatch, "/clusters/e2e", map[string]any{"policy_management": mode, "version": version}, token, nil)
	want(t, status, updated, http.StatusOK, "set cluster policy mode")
	if updated["policy_management"] != mode {
		t.Fatalf("policy mode=%v want %s", updated["policy_management"], mode)
	}
}

// readFile reads a repo-relative file from the test's working dir.
func readFile(rel string) (string, error) {
	b, err := readRepoFile(rel)
	return string(b), err
}
