package policysync

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/allocations"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/slurm"
)

func TestPlanMissingAccountAndCorrectsExistingAssociation(t *testing.T) {
	bid := uuid.New()
	want := slurm.Association{Account: "acct", Cluster: "c1", User: "custos", Partition: "gpu", QoS: []string{"normal"}, DefaultQoS: "normal", Comment: "custos:t/p"}
	d := DesiredState{Accounts: []DesiredAccount{{Value: slurm.Account{Name: "acct"}, BindingID: bid}}, Associations: []DesiredAssociation{{Value: want, BindingID: bid}}, ActiveBindings: map[uuid.UUID]bool{bid: true}, ServiceUsersByAccount: map[string]string{"acct": "custos"}, BindingByAccount: map[string]uuid.UUID{"acct": bid}}
	o := ObservedState{Accounts: []slurm.Account{{Name: "acct"}}, Associations: []slurm.Association{{Account: "acct", Cluster: "c1", User: "custos", Partition: "gpu", QoS: []string{"high"}, DefaultQoS: "high"}}}
	ops := Plan(d, o)
	if len(ops) != 1 || ops[0].Kind != OpUpsertAssociation || ops[0].Before == nil {
		t.Fatalf("ops=%+v", ops)
	}
	if got := DriftForOps(ops)[bid]; !hasDrift(got, "ASSOCIATION_QOS_MISMATCH") || !hasDrift(got, "DEFAULT_QOS_MISMATCH") {
		t.Fatalf("drift=%+v", got)
	}
	ops = Plan(d, ObservedState{Associations: o.Associations})
	if len(ops) < 1 || ops[0].Kind != OpCreateAccount {
		t.Fatalf("missing account op order: %+v", ops)
	}
}

func TestPlanPreservesPreExistingAccountMetadata(t *testing.T) {
	id := uuid.New()
	desired := DesiredState{
		Accounts: []DesiredAccount{{BindingID: id, Value: slurm.Account{Name: "acct", Description: "custos:tenant/project", Organization: "custos", ParentAccount: "root"}}},
		Associations: []DesiredAssociation{
			{BindingID: id, Value: slurm.Association{Account: "acct", Cluster: "c1", User: "custos", Partition: "debug", QoS: []string{"normal"}, DefaultQoS: "normal", Comment: "custos:tenant/project"}},
			{BindingID: id, Value: slurm.Association{Account: "acct", Cluster: "c1", ParentAccount: "root", Comment: "custos:tenant/project", GrpTRESMins: map[string]int64{"cpu": -1, "node": -1}}},
		},
		ActiveBindings:        map[uuid.UUID]bool{id: true},
		ServiceUsersByAccount: map[string]string{"acct": "custos"},
		BindingByAccount:      map[string]uuid.UUID{"acct": id},
	}
	observed := ObservedState{
		Accounts: []slurm.Account{{Name: "acct", Description: "site description", Organization: "physics"}},
		Associations: []slurm.Association{
			{Account: "acct", Cluster: "c1", User: "custos", Partition: "debug", QoS: []string{"normal"}, DefaultQoS: "normal", ParentAccount: "physics", Comment: "site service comment"},
			{Account: "acct", Cluster: "c1", ParentAccount: "physics", Comment: "site account comment", QoS: []string{"normal"}, DefaultQoS: "site-default", GrpTRESMins: map[string]int64{"cpu": -1, "node": -1}},
		},
	}
	if ops := Plan(desired, observed); len(ops) != 0 {
		t.Fatalf("site hierarchy/comments changed despite matching binding policy: %+v", ops)
	}

	wrongQoS := ObservedState{Accounts: observed.Accounts, Associations: append([]slurm.Association(nil), observed.Associations...)}
	wrongQoS.Associations[0].QoS = []string{"high"}
	ops := Plan(desired, wrongQoS)
	if len(ops) != 1 || ops[0].Kind != OpUpsertAssociation || ops[0].After.Association.QoS[0] != "normal" {
		t.Fatalf("wrong QoS plan=%+v", ops)
	}
	after := ops[0].After.Association
	if after.ParentAccount != "physics" || after.Comment != "site service comment" {
		t.Fatalf("pre-existing service association metadata was not preserved: op=%+v", ops[0])
	}

	wrongLimit := ObservedState{Accounts: observed.Accounts, Associations: append([]slurm.Association(nil), observed.Associations...)}
	wrongLimit.Associations[1].GrpTRESMins["cpu"] = 120
	ops = Plan(desired, wrongLimit)
	if len(ops) != 1 || ops[0].Kind != OpUpsertAssociation || ops[0].After.Association.GrpTRESMins["cpu"] != -1 {
		t.Fatalf("site GrpTRESMins clearing plan=%+v", ops)
	}
	if ops[0].After.Association.ParentAccount != "physics" || ops[0].After.Association.Comment != "site account comment" {
		t.Fatalf("pre-existing account association metadata was not preserved: op=%+v", ops[0])
	}
	if !reflect.DeepEqual(ops[0].After.Association.QoS, []string{"normal"}) || ops[0].After.Association.DefaultQoS != "site-default" {
		t.Fatalf("account-level site QoS was not preserved: op=%+v", ops[0])
	}
	postApply := ObservedState{Accounts: observed.Accounts, Associations: append([]slurm.Association(nil), observed.Associations...)}
	postApply.Associations[1].GrpTRESMins = map[string]int64{"cpu": -1, "node": -1}
	if ops := Plan(desired, postApply); len(ops) != 0 {
		t.Fatalf("account association emitted repeated QoS/limit operations: %+v", ops)
	}
}

func TestPlanRetainsDefaultServiceAssociationAndReportsDrift(t *testing.T) {
	id := uuid.New()
	desired := DesiredState{
		Associations: []DesiredAssociation{
			{BindingID: id, Value: slurm.Association{Account: "acct", Cluster: "c1", User: "custos", Partition: "debug", QoS: []string{"normal"}, DefaultQoS: "normal", Comment: "custos:t/p"}},
			{BindingID: id, Value: slurm.Association{Account: "acct", Cluster: "c1", ParentAccount: "root", Comment: "custos:t/p", GrpTRESMins: map[string]int64{"cpu": -1, "node": -1}}},
		},
		ActiveBindings:        map[uuid.UUID]bool{id: true},
		ServiceUsersByAccount: map[string]string{"acct": "custos"},
		BindingByAccount:      map[string]uuid.UUID{"acct": id},
	}
	observed := ObservedState{Associations: []slurm.Association{
		{Account: "acct", Cluster: "c1", User: "custos", Partition: "debug", QoS: []string{"normal"}, DefaultQoS: "normal", Comment: "site service comment"},
		{Account: "acct", Cluster: "c1", User: "custos", QoS: []string{"normal"}, DefaultQoS: "normal", IsDefault: true},
		{Account: "acct", Cluster: "c1", ParentAccount: "physics", Comment: "site account comment", QoS: []string{"normal"}, DefaultQoS: "site-default", GrpTRESMins: map[string]int64{"cpu": -1, "node": -1}},
	}}
	if ops := Plan(desired, observed); len(ops) != 0 {
		t.Fatalf("default association was planned for deletion or account QoS changed: %+v", ops)
	}
	drift := DefaultAssociationDrift(desired, observed)[id]
	if !hasDrift(drift, "DEFAULT_ASSOCIATION_RETAINED") {
		t.Fatalf("default association retention drift missing: %+v", drift)
	}
}

func TestPlanCreatedAccountGetsCustosMetadata(t *testing.T) {
	id := uuid.New()
	parent, comment := "physics", "custos:tenant/project"
	desired := DesiredState{
		Accounts: []DesiredAccount{{BindingID: id, Value: slurm.Account{Name: "acct", Description: comment, Organization: "custos", ParentAccount: parent}}},
		Associations: []DesiredAssociation{
			{BindingID: id, Value: slurm.Association{Account: "acct", Cluster: "c1", User: "custos", Partition: "debug", QoS: []string{"normal"}, DefaultQoS: "normal", Comment: comment}},
			{BindingID: id, Value: slurm.Association{Account: "acct", Cluster: "c1", ParentAccount: parent, Comment: comment, GrpTRESMins: map[string]int64{"cpu": -1, "node": -1}}},
		},
	}
	ops := Plan(desired, ObservedState{})
	if len(ops) != 3 || ops[0].Kind != OpCreateAccount || ops[0].After.Account.Description != comment || ops[0].After.Account.Organization != "custos" || ops[0].After.Account.ParentAccount != parent {
		t.Fatalf("managed account creation plan=%+v", ops)
	}
	for _, op := range ops[1:] {
		if op.Kind != OpUpsertAssociation || op.Before != nil || op.After.Association.Comment != comment {
			t.Fatalf("new association lost Custos metadata: %+v", op)
		}
		if op.After.Association.User == "" && op.After.Association.ParentAccount != parent {
			t.Fatalf("account association parent=%q want %q", op.After.Association.ParentAccount, parent)
		}
	}
}

func TestPlanDeletesExtraServicePartitionButKeepsOtherUsers(t *testing.T) {
	bid := uuid.New()
	desired := DesiredState{Accounts: []DesiredAccount{{Value: slurm.Account{Name: "acct"}, BindingID: bid}}, Associations: []DesiredAssociation{{Value: slurm.Association{Account: "acct", Cluster: "c1", User: "custos", Partition: "gpu", QoS: []string{"normal"}}, BindingID: bid}}, ActiveBindings: map[uuid.UUID]bool{bid: true}, ServiceUsersByAccount: map[string]string{"acct": "custos"}, BindingByAccount: map[string]uuid.UUID{"acct": bid}}
	observed := ObservedState{Accounts: []slurm.Account{{Name: "acct"}}, Associations: []slurm.Association{{Account: "acct", Cluster: "c1", User: "custos", Partition: "gpu", QoS: []string{"normal"}}, {Account: "acct", Cluster: "c1", User: "custos", Partition: "debug"}, {Account: "acct", Cluster: "c1", User: "alice", Partition: "debug"}}}
	ops := Plan(desired, observed)
	if len(ops) != 1 || ops[0].Kind != OpDeleteAssociation {
		t.Fatalf("ops=%+v", ops)
	}
	if ops[0].Before.Association.User != "custos" || ops[0].Before.Association.Partition != "debug" {
		t.Fatalf("deleted wrong association: %+v", ops[0])
	}
}

func TestPlanConflictWritesNothing(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	d := DesiredState{Accounts: []DesiredAccount{{Value: slurm.Account{Name: "shared"}, BindingID: a}}, Associations: []DesiredAssociation{{Value: slurm.Association{Account: "shared", Cluster: "c1", User: "custos"}, BindingID: a}}, ActiveBindings: map[uuid.UUID]bool{a: true, b: true}, ServiceUsersByAccount: map[string]string{"shared": "custos"}, Conflicts: map[string][]uuid.UUID{"shared": {a, b}}}
	o := ObservedState{Accounts: nil, Associations: []slurm.Association{{Account: "shared", Cluster: "c1", User: "custos", Partition: "old"}}}
	if ops := Plan(d, o); len(ops) != 0 {
		t.Fatalf("conflicted account ops=%+v", ops)
	}
}

func TestBuildDesiredSkipsUnknownQoSAndPartition(t *testing.T) {
	bid := uuid.New()
	binding := projects.ClusterBinding{ID: bid, TenantID: uuid.New(), ProjectID: uuid.New(), SlurmAccount: "acct", Enabled: true, AllowedPartitions: []string{"gpu", "missing"}, AllowedQoS: []string{"normal", "missing"}, DefaultQoS: "normal"}
	cluster := clusters.Cluster{Name: "c1", ServiceUser: "custos", PolicyParentAccount: "root", Capabilities: &slurm.Capabilities{Partitions: []slurm.Partition{{Name: "gpu"}}}}
	d, drift := BuildDesired(cluster, []BindingInput{{Binding: binding, TenantSlug: "tenant", ProjectSlug: "proj"}}, []slurm.QoS{{Name: "normal"}}, map[string]bool{"gpu": true})
	if len(d.Associations) != 1 {
		t.Fatalf("expected account-level association only, got %+v", d.Associations)
	}
	if d.Associations[0].Value.Comment != "" {
		t.Fatalf("slurmdbd drops account-association comments; desiring one never converges: %+v", d.Associations[0].Value)
	}
	codes := map[string]bool{}
	for _, item := range drift[bid] {
		codes[item.Code] = true
	}
	for _, code := range []string{"QOS_UNKNOWN", "PARTITION_UNKNOWN"} {
		if !codes[code] {
			t.Fatalf("missing %s drift: %+v", code, drift[bid])
		}
	}
	if !d.SkippedAssociations[AssociationKey(slurm.Association{Account: "acct", Cluster: "c1", User: "custos", Partition: "gpu"})] || !d.SkippedAssociations[AssociationKey(slurm.Association{Account: "acct", Cluster: "c1", User: "custos", Partition: "missing"})] {
		t.Fatalf("unknown-resource associations not protected: %+v", d.SkippedAssociations)
	}
}

func TestGrpTRESMinsSetMinimumAndClear(t *testing.T) {
	cluster := clusters.Cluster{Capabilities: &slurm.Capabilities{GRESTypes: []string{"gpu:h100"}}}
	items := []allocations.Allocation{{Unit: "cpu_hours", LimitAmount: 2.1, Enforcement: "hard"}, {Unit: "cpu_hours", LimitAmount: 1.001, Enforcement: "hard"}, {Unit: "gpu_hours", LimitAmount: 1.2, Enforcement: "hard"}, {Unit: "node_hours", LimitAmount: 100, Enforcement: "soft"}}
	got := accountGrpTRESMins(items, cluster)
	want := map[string]int64{"cpu": 61, "gres/gpu": 72, "node": -1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("limits=%v want %v", got, want)
	}
	withoutGPU := accountGrpTRESMins(nil, clusters.Cluster{})
	if _, ok := withoutGPU["gres/gpu"]; ok {
		t.Fatalf("unsupported GPU TRES included: %v", withoutGPU)
	}
}

func TestPlanUnboundCleanupDeletesOnlyManagedObjects(t *testing.T) {
	key := AssociationKey(slurm.Association{Account: "owned", Cluster: "c1", User: "custos", Partition: "gpu"})
	managed := []ManagedObject{{Kind: "account", Key: "owned"}, {Kind: "association", Key: key}}
	observed := ObservedState{Accounts: []slurm.Account{{Name: "owned"}}, Associations: []slurm.Association{{Account: "owned", Cluster: "c1", User: "custos", Partition: "gpu"}, {Account: "owned", Cluster: "c1", User: "alice", Partition: "gpu"}}, Managed: managed}
	ops := Plan(DesiredState{ActiveBindings: map[uuid.UUID]bool{}}, observed)
	if len(ops) != 1 || ops[0].Kind != OpDeleteAssociation {
		t.Fatalf("site association must retain account: %+v", ops)
	}
	observed.Associations = observed.Associations[:1]
	ops = Plan(DesiredState{ActiveBindings: map[uuid.UUID]bool{}}, observed)
	if len(ops) != 2 || ops[0].Kind != OpDeleteAssociation || ops[1].Kind != OpDeleteAccount {
		t.Fatalf("unbound cleanup order=%+v", ops)
	}
}

func TestPlanOperationOrderIsDeterministicAndCapped(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	makeDesired := func(reverse bool) DesiredState {
		accounts := []DesiredAccount{{Value: slurm.Account{Name: "b"}, BindingID: b}, {Value: slurm.Account{Name: "a"}, BindingID: a}}
		assocs := []DesiredAssociation{{Value: slurm.Association{Account: "b", Cluster: "c1", User: "custos", Partition: "gpu"}, BindingID: b}, {Value: slurm.Association{Account: "a", Cluster: "c1", User: "custos", Partition: "gpu"}, BindingID: a}}
		if reverse {
			accounts[0], accounts[1] = accounts[1], accounts[0]
			assocs[0], assocs[1] = assocs[1], assocs[0]
		}
		return DesiredState{Accounts: accounts, Associations: assocs, ActiveBindings: map[uuid.UUID]bool{a: true, b: true}}
	}
	one := Plan(makeDesired(false), ObservedState{})
	two := Plan(makeDesired(true), ObservedState{})
	if !reflect.DeepEqual(one, two) {
		t.Fatalf("non-deterministic plans: %v / %v", one, two)
	}
	if len(one) < 4 || one[0].Kind != OpCreateAccount || one[1].Kind != OpCreateAccount || one[2].Kind != OpUpsertAssociation {
		t.Fatalf("operation ordering=%+v", one)
	}
	many := make([]Op, 205)
	for i := range many {
		many[i].Key = strconv.Itoa(i)
	}
	if len(boundedOps(many, maxOpsPerRun)) != 200 {
		t.Fatal("policy run operation cap exceeded")
	}
}
