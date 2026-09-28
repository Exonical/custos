package policysync

import (
	"testing"

	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/projects"
	"github.com/google/uuid"
)

func TestPreserveDriftAndAuditTransitions(t *testing.T) {
	previous := []projects.DriftItem{{Code: "ACCOUNT_MISSING", Detail: "missing"}}
	preserved := PreserveDrift(previous)
	if len(preserved) != 1 || preserved[0] != previous[0] {
		t.Fatalf("lost prior drift: %+v", preserved)
	}
	if TransitionAudit(projects.ClusterBinding{DriftState: "ok"}, "drift") != "binding.drift_detected" {
		t.Fatal("missing detection audit")
	}
	if TransitionAudit(projects.ClusterBinding{DriftState: "drift"}, "ok") != "binding.drift_cleared" {
		t.Fatal("missing clear audit")
	}
	if TransitionAudit(projects.ClusterBinding{DriftState: "ok"}, "ok") != "" {
		t.Fatal("audit without transition")
	}
}

func TestCompareDriftCodes(t *testing.T) {
	b := projects.ClusterBinding{ID: uuid.New(), SlurmAccount: "missing", DefaultQoS: "qbad", DefaultPartition: "pbad", AllowedQoS: []string{"qbad"}, AllowedPartitions: []string{"pbad"}}
	got := Compare(b, map[string]bool{}, 0, map[string]bool{}, map[string]bool{})
	codes := map[string]bool{}
	for _, d := range got {
		codes[d.Code] = true
	}
	for _, code := range []string{"ACCOUNT_MISSING", "NO_ASSOCIATIONS", "QOS_UNKNOWN", "PARTITION_UNKNOWN", "DEFAULT_QOS_UNKNOWN", "DEFAULT_PARTITION_UNKNOWN"} {
		if !codes[code] {
			t.Fatalf("missing %s: %+v", code, got)
		}
	}
	ok := Compare(projects.ClusterBinding{SlurmAccount: "acct"}, map[string]bool{"acct": true}, 1, map[string]bool{}, map[string]bool{})
	if len(ok) != 0 {
		t.Fatalf("unexpected drift: %+v", ok)
	}
}

func TestEffectiveMode(t *testing.T) {
	cases := []struct{ cluster, config, want string }{{"inherit", "enforce", "enforce"}, {"inherit", "report", "report"}, {"report", "enforce", "report"}, {"enforce", "report", "enforce"}}
	for _, tc := range cases {
		if got := effectiveMode(clusters.Cluster{PolicyManagement: tc.cluster}, tc.config); got != tc.want {
			t.Fatalf("effectiveMode(%s,%s)=%s want %s", tc.cluster, tc.config, got, tc.want)
		}
	}
}
