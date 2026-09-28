package policysync

import (
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/slurm"
)

// OpKind names one slurmdbd policy mutation.
type OpKind string

// The Op constants identify mutations in deterministic reconcile order.
const (
	OpCreateAccount     OpKind = "create_account"
	OpUpsertAssociation OpKind = "upsert_association"
	OpDeleteAssociation OpKind = "delete_association"
	OpDeleteAccount     OpKind = "delete_account"
)

// ObjectState contains the before or desired account/association payload.
type ObjectState struct {
	Account     *slurm.Account     `json:"account,omitempty"`
	Association *slurm.Association `json:"association,omitempty"`
}

// Op is one deterministic reconcile operation and its audit context.
type Op struct {
	Kind      OpKind       `json:"kind"`
	Key       string       `json:"key"`
	Before    *ObjectState `json:"before,omitempty"`
	After     *ObjectState `json:"after,omitempty"`
	BindingID *uuid.UUID   `json:"binding_id,omitempty"`
}

// DesiredAccount is an account required by one binding.
type DesiredAccount struct {
	Value     slurm.Account
	BindingID uuid.UUID
}

// DesiredAssociation is one user or account association from a binding.
type DesiredAssociation struct {
	Value     slurm.Association
	BindingID uuid.UUID
}

// DesiredState contains the complete binding-owned Slurm policy.
type DesiredState struct {
	Accounts              []DesiredAccount
	Associations          []DesiredAssociation
	ActiveBindings        map[uuid.UUID]bool
	ServiceUsersByAccount map[string]string
	BindingByAccount      map[string]uuid.UUID
	SkippedAssociations   map[string]bool
	Conflicts             map[string][]uuid.UUID
}

// ManagedObject identifies a Slurm object created by Custos.
type ManagedObject struct {
	Kind      string
	Key       string
	BindingID *uuid.UUID
}

// ObservedState is the relevant account/association state read from slurmdbd.
type ObservedState struct {
	Accounts     []slurm.Account
	Associations []slurm.Association
	Managed      []ManagedObject
}

// AssociationKey returns the stable managed-object key for one association.
func AssociationKey(a slurm.Association) string {
	return strings.Join([]string{a.Account, a.Cluster, a.User, a.Partition}, "|")
}

// Plan computes an exact, deterministic reconcile from desired and observed state.
func Plan(desired DesiredState, observed ObservedState) []Op {
	accountByName := make(map[string]slurm.Account, len(observed.Accounts))
	for _, account := range observed.Accounts {
		accountByName[account.Name] = account
	}
	assocByKey := make(map[string]slurm.Association, len(observed.Associations))
	for _, assoc := range observed.Associations {
		assocByKey[AssociationKey(assoc)] = assoc
	}
	ownedAssociations := map[string]bool{}
	for _, managed := range observed.Managed {
		if managed.Kind == "association" {
			ownedAssociations[managed.Key] = true
		}
	}
	conflicted := func(account string) bool { return len(desired.Conflicts[account]) > 1 }
	var creates, upserts, deletes, deleteAccounts []Op
	wantedAccounts := make(map[string]bool, len(desired.Accounts))
	for _, target := range desired.Accounts {
		name := target.Value.Name
		wantedAccounts[name] = true
		if conflicted(name) {
			continue
		}
		if _, exists := accountByName[name]; !exists {
			a := target.Value
			b := target.BindingID
			creates = append(creates, Op{Kind: OpCreateAccount, Key: name, After: &ObjectState{Account: &a}, BindingID: &b})
		}
	}
	wantedAssociations := make(map[string]DesiredAssociation, len(desired.Associations))
	for _, target := range desired.Associations {
		assoc := target.Value
		if conflicted(assoc.Account) {
			continue
		}
		key := AssociationKey(assoc)
		wantedAssociations[key] = target
		before, exists := assocByKey[key]
		metadataOwned := !exists || ownedAssociations[key]
		if exists && assoc.User == "" {
			assoc.QoS = append([]string(nil), before.QoS...)
			assoc.DefaultQoS = before.DefaultQoS
		}
		if exists && !metadataOwned {
			assoc.Comment = before.Comment
			assoc.ParentAccount = before.ParentAccount
		}
		if exists && associationEqual(before, assoc) {
			continue
		}
		var beforeObj *ObjectState
		if exists {
			prior := before
			beforeObj = &ObjectState{Association: &prior}
		}
		after := assoc
		bindingID := target.BindingID
		upserts = append(upserts, Op{Kind: OpUpsertAssociation, Key: key, Before: beforeObj, After: &ObjectState{Association: &after}, BindingID: &bindingID})
	}
	deleteKeys := map[string]bool{}
	for _, observedAssoc := range observed.Associations {
		if observedAssoc.IsDefault {
			continue
		}
		serviceUser, managedAccount := desired.ServiceUsersByAccount[observedAssoc.Account]
		if managedAccount && serviceUser != "" && !conflicted(observedAssoc.Account) && observedAssoc.User == serviceUser {
			key := AssociationKey(observedAssoc)
			if _, wanted := wantedAssociations[key]; !wanted && !desired.SkippedAssociations[key] {
				var bindingID *uuid.UUID
				if id, ok := desired.BindingByAccount[observedAssoc.Account]; ok {
					v := id
					bindingID = &v
				}
				addDeleteAssociation(&deletes, deleteKeys, observedAssoc, bindingID)
			}
		}
	}
	for _, managed := range observed.Managed {
		if managed.Kind != "association" || managed.BindingID != nil && desired.ActiveBindings[*managed.BindingID] {
			continue
		}
		if _, wanted := wantedAssociations[managed.Key]; wanted {
			continue
		}
		assoc, exists := assocByKey[managed.Key]
		if exists && assoc.IsDefault {
			continue
		}
		if exists && !conflicted(assoc.Account) {
			addDeleteAssociation(&deletes, deleteKeys, assoc, managed.BindingID)
		}
	}
	pendingDelete := map[string]bool{}
	for _, op := range deletes {
		pendingDelete[op.Key] = true
	}
	for _, managed := range observed.Managed {
		if managed.Kind != "account" || managed.BindingID != nil && desired.ActiveBindings[*managed.BindingID] || wantedAccounts[managed.Key] || conflicted(managed.Key) {
			continue
		}
		account, exists := accountByName[managed.Key]
		if !exists {
			continue
		}
		hasAssociations := false
		for _, assoc := range observed.Associations {
			if assoc.Account == managed.Key && !pendingDelete[AssociationKey(assoc)] {
				hasAssociations = true
				break
			}
		}
		if !hasAssociations {
			prior := account
			b := managed.BindingID
			deleteAccounts = append(deleteAccounts, Op{Kind: OpDeleteAccount, Key: managed.Key, Before: &ObjectState{Account: &prior}, BindingID: b})
		}
	}
	sort.Slice(creates, func(i, j int) bool { return creates[i].Key < creates[j].Key })
	sort.Slice(upserts, func(i, j int) bool {
		userI := upserts[i].After.Association.User
		userJ := upserts[j].After.Association.User
		if (userI == "") != (userJ == "") {
			return userI == ""
		}
		return upserts[i].Key < upserts[j].Key
	})
	sort.Slice(deletes, func(i, j int) bool { return deletes[i].Key < deletes[j].Key })
	sort.Slice(deleteAccounts, func(i, j int) bool { return deleteAccounts[i].Key < deleteAccounts[j].Key })
	ops := make([]Op, 0, len(creates)+len(upserts)+len(deletes)+len(deleteAccounts))
	ops = append(ops, creates...)
	ops = append(ops, upserts...)
	ops = append(ops, deletes...)
	ops = append(ops, deleteAccounts...)
	return ops
}

// DefaultAssociationDrift reports service-user default associations that cannot be deleted safely.
func DefaultAssociationDrift(desired DesiredState, observed ObservedState) map[uuid.UUID][]projects.DriftItem {
	out := map[uuid.UUID][]projects.DriftItem{}
	wanted := make(map[string]bool, len(desired.Associations))
	for _, target := range desired.Associations {
		wanted[AssociationKey(target.Value)] = true
	}
	for _, assoc := range observed.Associations {
		serviceUser, ok := desired.ServiceUsersByAccount[assoc.Account]
		if !ok || !assoc.IsDefault || assoc.User != serviceUser || wanted[AssociationKey(assoc)] {
			continue
		}
		if len(desired.Conflicts[assoc.Account]) > 1 {
			continue
		}
		id, ok := desired.BindingByAccount[assoc.Account]
		if !ok {
			continue
		}
		out[id] = appendDrift(out[id], projects.DriftItem{Code: "DEFAULT_ASSOCIATION_RETAINED", Detail: "Slurm's default service-user association grants all partitions and cannot be deleted"})
	}
	return out
}

func addDeleteAssociation(ops *[]Op, seen map[string]bool, assoc slurm.Association, bindingID *uuid.UUID) {
	key := AssociationKey(assoc)
	if seen[key] {
		return
	}
	seen[key] = true
	prior := assoc
	*ops = append(*ops, Op{Kind: OpDeleteAssociation, Key: key, Before: &ObjectState{Association: &prior}, BindingID: bindingID})
}

func associationEqual(a, b slurm.Association) bool {
	parentEqual := b.User != "" || a.ParentAccount == b.ParentAccount || a.ParentAccount == "" && b.ParentAccount == "root"
	return stringSetEqual(a.QoS, b.QoS) && a.DefaultQoS == b.DefaultQoS && a.Comment == b.Comment && parentEqual && limitsEqual(a.GrpTRESMins, b.GrpTRESMins)
}
func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
func limitsEqual(observed, desired map[string]int64) bool {
	for tres, want := range desired {
		got, ok := observed[tres]
		if want < 0 {
			if ok && got >= 0 {
				return false
			}
			continue
		}
		if !ok || got != want {
			return false
		}
	}
	return true
}

func boundedOps(ops []Op, limit int) []Op {
	if limit < 0 {
		limit = 0
	}
	if len(ops) > limit {
		return ops[:limit]
	}
	return ops
}

// DriftForOps maps a dry-run plan into binding-scoped drift findings.
func DriftForOps(ops []Op) map[uuid.UUID][]projects.DriftItem {
	out := map[uuid.UUID][]projects.DriftItem{}
	add := func(id uuid.UUID, code, detail string) {
		out[id] = appendDrift(out[id], projects.DriftItem{Code: code, Detail: detail})
	}
	for _, op := range ops {
		if op.BindingID == nil {
			continue
		}
		id := *op.BindingID
		switch op.Kind {
		case OpCreateAccount:
			add(id, "ACCOUNT_MISSING", "account is missing and would be created")
		case OpUpsertAssociation:
			if op.Before == nil {
				add(id, "ASSOCIATION_MISSING", "required association is missing")
				continue
			}
			if op.Before.Association == nil || op.After == nil || op.After.Association == nil {
				continue
			}
			before, after := op.Before.Association, op.After.Association
			known := false
			if !stringSetEqual(before.QoS, after.QoS) {
				add(id, "ASSOCIATION_QOS_MISMATCH", "association QoS differs from the binding")
				known = true
			}
			if before.DefaultQoS != after.DefaultQoS {
				add(id, "DEFAULT_QOS_MISMATCH", "default QoS differs from the binding")
				known = true
			}
			if !limitsEqual(before.GrpTRESMins, after.GrpTRESMins) {
				add(id, "LIMIT_MISMATCH", "GrpTRESMins differs from hard allocations")
				known = true
			}
			if before.Comment != after.Comment || before.User == "" && before.ParentAccount != after.ParentAccount {
				if !known {
					add(id, "ASSOCIATION_MISSING", "association metadata differs from desired state")
				}
			}
		case OpDeleteAssociation:
			add(id, "ASSOCIATION_EXTRA", "service-user association is outside desired state")
		}
	}
	return out
}
