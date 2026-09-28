package postgres_test

import (
	"testing"

	"github.com/Exonical/custos/internal/clusters"
	projectsvc "github.com/Exonical/custos/internal/projects/service"
)

func TestBindingMutationsEnqueuePolicySync(t *testing.T) {
	f := newSvcFixture(t)
	tn := f.tenant(t, "policy-enqueue")
	uid := f.mkUser2(t, "policy-enqueue-admin")
	f.member(t, tn, uid, "tenant-admin")
	p := f.project(t, tn, "policy-enqueue-project")
	clusterID := f.assignedCluster(t, tn, "policy-enqueue-cluster", clusters.AssignmentDefaults{})
	f.svc.SetEnqueuer(f.pool)
	ctx, principal, tc, pc := f.req(t, tn, uid, &p)
	binding, err := f.svc.CreateBinding(ctx, principal, tc, pc, projectsvc.UpsertBinding{ClusterID: clusterID, SlurmAccount: "policy-acct"})
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		var count int
		if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM work_items WHERE kind='policy.sync' AND key=$1 AND state IN ('pending','leased')`, "cluster:"+clusterID.String()).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("active policy.sync items=%d", count)
		}
	}
	check()
	qos := []string{"normal"}
	updated, err := f.svc.UpdateBinding(ctx, principal, tc, pc, binding.ID, projectsvc.UpsertBinding{AllowedQoS: qos, Version: binding.Version})
	if err != nil {
		t.Fatal(err)
	}
	check()
	disabled := false
	updated, err = f.svc.UpdateBinding(ctx, principal, tc, pc, binding.ID, projectsvc.UpsertBinding{Enabled: &disabled, Version: updated.Version})
	if err != nil {
		t.Fatal(err)
	}
	check()
	enabled := true
	_, err = f.svc.UpdateBinding(ctx, principal, tc, pc, binding.ID, projectsvc.UpsertBinding{Enabled: &enabled, Version: updated.Version})
	if err != nil {
		t.Fatal(err)
	}
	check()
	if err := f.svc.DeleteBinding(ctx, principal, tc, pc, binding.ID); err != nil {
		t.Fatal(err)
	}
	check()
}
