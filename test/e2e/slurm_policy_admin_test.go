package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	v0045 "github.com/Exonical/custos/internal/slurm/slinky/v0045"
)

func TestLiveSlurmPolicyAdminOperator(t *testing.T) {
	rawToken, err := readRepoFile("deploy/e2e/.secrets/slurm/token")
	if err != nil {
		t.Fatal(err)
	}
	token := secrets.NewValue([]byte(strings.TrimSpace(string(rawToken))))
	defer token.Wipe()
	client, err := v0045.New(slurm.Endpoint{BaseURL: "https://slurmrestd.e2e:6820"}, slurm.Credential{UserName: "custos", Token: token}, e.hc)
	if err != nil {
		t.Fatal(err)
	}
	account := "custos-e2e-admin-probe-" + uuid.NewString()[:8]
	if err := client.UpsertAccounts(t.Context(), []slurm.Account{{Name: account, Description: "custos:e2e/admin-probe", Organization: "custos", ParentAccount: "root", Cluster: "e2e"}}); err != nil {
		t.Fatalf("AdminLevel=Operator cannot create an account on Slurm 26.05.4: %v", err)
	}
	if created := slurmAssociation(t, account, "", ""); created == nil || created["parent_account"] != "root" || created["lineage"] != "/"+account+"/" {
		t.Fatalf("account association is not attached under root: %v", created)
	}
	defer func() {
		if err := client.DeleteAssociation(t.Context(), slurm.AssociationKey{Account: account, Cluster: "e2e", User: "custos", Partition: "debug"}); err != nil {
			t.Errorf("delete probe service association: %v", err)
		}
		if err := client.DeleteAccount(t.Context(), account); err != nil {
			t.Errorf("remove live policy privilege probe account: %v", err)
		}
		if leftover := slurmAssociation(t, account, "", ""); leftover != nil {
			t.Errorf("account deletion left an association behind: %v", leftover)
		}
	}()
	if err := client.UpsertAssociations(t.Context(), []slurm.Association{
		{Account: account, Cluster: "e2e", User: "custos", Partition: "debug", QoS: []string{"normal"}, DefaultQoS: "normal", Comment: "custos:e2e/admin-probe"},
		{Account: account, Cluster: "e2e", ParentAccount: "root", GrpTRESMins: map[string]int64{"cpu": 60, "node": -1}},
	}); err != nil {
		t.Fatalf("AdminLevel=Operator cannot write associations on Slurm 26.05.4: %v", err)
	}
	if !slurmAssociationExists(t, account, "custos", "debug", "normal") {
		t.Fatalf("AdminLevel=Operator write did not create the service-user association: %v", slurmAssociation(t, account, "custos", "debug"))
	}
	set := slurmAssociation(t, account, "", "")
	if value, ok := tresMinuteValue(set, "cpu"); !ok || value != 60 {
		t.Fatalf("GrpTRESMins set response=%v", set)
	}
	if err := client.UpsertAssociations(t.Context(), []slurm.Association{{Account: account, Cluster: "e2e", ParentAccount: "root", GrpTRESMins: map[string]int64{"cpu": -1, "node": -1}}}); err != nil {
		t.Fatalf("AdminLevel=Operator cannot clear GrpTRESMins on Slurm 26.05.4: %v", err)
	}
	cleared := slurmAssociation(t, account, "", "")
	if value, ok := tresMinuteValue(cleared, "cpu"); ok && value >= 0 {
		t.Fatalf("clear count was not treated as unset: %v", cleared)
	}
	fixture, err := json.Marshal(cleared)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live Slurm 26.05.4 unset GrpTRESMins fixture: %s", fixture)
}
