package v0045

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	api "github.com/SlinkyProject/slurm-client/api/v0045"
)

func TestAccountingAdminRequestBodies(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/associations/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"associations":[{"id":42,"account":"custos-e2e-managed","cluster":"e2e","user":"custos","partition":"gpu","is_default":false}]}`))
			return
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/accounts_association/"):
			var got struct {
				Cond struct {
					Accounts    []string `json:"accounts"`
					Clusters    []string `json:"clusters"`
					Association struct {
						Parent string `json:"parent"`
					} `json:"association"`
				} `json:"association_condition"`
				Account struct {
					Description  string `json:"description"`
					Organization string `json:"organization"`
				} `json:"account"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Error(err)
				return
			}
			if len(got.Cond.Accounts) != 1 || got.Cond.Accounts[0] != "custos-e2e-managed" || len(got.Cond.Clusters) != 1 || got.Cond.Clusters[0] != "e2e" || got.Cond.Association.Parent != "root" || got.Account.Description != "custos:acme/p1" || got.Account.Organization != "custos" {
				t.Errorf("accounts_association body=%s", body)
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/associations/"):
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Error(err)
				return
			}
			items := got["associations"].([]any)
			if len(items) != 2 {
				t.Fatalf("association body=%s", body)
			}
			accountAssoc := items[0].(map[string]any)
			if accountAssoc["account"] != "custos-e2e-managed" || accountAssoc["user"] != "" || accountAssoc["comment"] != "custos:acme/p1" {
				t.Errorf("account association=%v", accountAssoc)
			}
			if _, ok := accountAssoc["parent_account"]; ok {
				t.Errorf("parent is set only at account creation, never on association writes: %v", accountAssoc)
			}
			if _, ok := accountAssoc["qos"]; ok {
				t.Errorf("account-level QoS is not binding-derived: %v", accountAssoc)
			}
			if _, ok := accountAssoc["default"]; ok {
				t.Errorf("account-level default QoS is not binding-derived: %v", accountAssoc)
			}
			maxRecord := accountAssoc["max"].(map[string]any)
			tres := maxRecord["tres"].(map[string]any)
			group := tres["group"].(map[string]any)
			minutes := group["minutes"].([]any)
			if len(minutes) != 2 {
				t.Fatalf("GrpTRESMins=%v", minutes)
			}
			if minutes[0].(map[string]any)["type"] != "cpu" || minutes[0].(map[string]any)["count"] != float64(60) {
				t.Errorf("set limit=%v", minutes[0])
			}
			if minutes[1].(map[string]any)["type"] != "gres" || minutes[1].(map[string]any)["name"] != "gpu" || minutes[1].(map[string]any)["count"] != float64(-1) {
				t.Errorf("clear limit=%v", minutes[1])
			}
			userAssoc := items[1].(map[string]any)
			if userAssoc["account"] != "custos-e2e-managed" || userAssoc["user"] != "custos" || userAssoc["partition"] != "gpu" || userAssoc["comment"] != "custos:acme/p1" || userAssoc["default"].(map[string]any)["qos"] != "normal" || userAssoc["qos"].([]any)[0] != "normal" {
				t.Errorf("service-user association=%v", userAssoc)
			}
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/association/"):
			q, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil {
				t.Error(err)
			}
			if q.Get("id") != "42" || q.Get("account") != "custos-e2e-managed" || q.Get("user") != "custos" || q.Get("cluster") != "e2e" || q.Get("partition") != "gpu" {
				t.Errorf("delete association query=%s", r.URL.RawQuery)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/account/custos-e2e-managed"):
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := New(slurm.Endpoint{BaseURL: server.URL}, slurm.Credential{UserName: "custos", Token: secrets.NewValue([]byte("test-token"))}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err = client.UpsertAccounts(t.Context(), []slurm.Account{{Name: "custos-e2e-managed", Description: "custos:acme/p1", Organization: "custos", ParentAccount: "root", Cluster: "e2e"}}); err != nil {
		t.Fatal(err)
	}
	if err = client.UpsertAssociations(t.Context(), []slurm.Association{{Account: "custos-e2e-managed", User: "custos", Cluster: "e2e", Partition: "gpu", QoS: []string{"normal"}, DefaultQoS: "normal", Comment: "custos:acme/p1"}, {Account: "custos-e2e-managed", Cluster: "e2e", ParentAccount: "root", Comment: "custos:acme/p1", GrpTRESMins: map[string]int64{"cpu": 60, "gres/gpu": -1}}}); err != nil {
		t.Fatal(err)
	}
	if err = client.DeleteAssociation(t.Context(), slurm.AssociationKey{Account: "custos-e2e-managed", User: "custos", Cluster: "e2e", Partition: "gpu"}); err != nil {
		t.Fatal(err)
	}
	if err = client.DeleteAccount(t.Context(), "custos-e2e-managed"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 5 {
		t.Fatalf("requests=%v", paths)
	}
}

func TestSlurmdbdForbiddenMapsToErrForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := New(slurm.Endpoint{BaseURL: server.URL}, slurm.Credential{UserName: "custos", Token: secrets.NewValue([]byte("test-token"))}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.DeleteAccount(t.Context(), "denied")
	if !errors.Is(err, slurm.ErrForbidden) {
		t.Fatalf("error=%v, want ErrForbidden", err)
	}
}

func TestSlurmdbdRejectedMapsToValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"error":"INVALID_ASSOCIATION","error_number":2008,"description":"default association cannot be deleted","source":"sacctmgr"}]}`))
	}))
	defer server.Close()
	client, err := New(slurm.Endpoint{BaseURL: server.URL}, slurm.Credential{UserName: "custos", Token: secrets.NewValue([]byte("test-token"))}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.DeleteAccount(t.Context(), "rejected")
	if !errors.Is(err, slurm.ErrRejected) || slurm.Classify(err) != apperr.Validation {
		t.Fatalf("error=%v classification=%v, want ErrRejected/validation", err, slurm.Classify(err))
	}
}

func TestAssociationWriteOmitsUnownedMetadata(t *testing.T) {
	assoc, err := associationForWrite(slurm.Association{Account: "acct", Cluster: "c1", User: "custos", QoS: []string{"normal"}, DefaultQoS: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(assoc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"comment"`) || strings.Contains(string(body), `"parent_account"`) {
		t.Fatalf("unexpected metadata in pre-existing association payload: %s", body)
	}
}

func TestGrpTRESMinsUnsetFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/grptresmins_unset.json")
	if err != nil {
		t.Fatal(err)
	}
	var response api.V0045OpenapiAssocsResp
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Associations) != 1 {
		t.Fatalf("fixture associations=%d", len(response.Associations))
	}
	assoc := associationRecord(response.Associations[0])
	if assoc.Account != "custos-e2e-admin-probe" || assoc.User != "" || assoc.Cluster != "e2e" {
		t.Fatalf("fixture association=%+v", assoc)
	}
	if _, exists := assoc.GrpTRESMins["cpu"]; exists {
		t.Fatalf("cleared cpu TRES remained set: %+v", assoc.GrpTRESMins)
	}
}
