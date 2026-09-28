package policysync

import (
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/allocations"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/slurm"
)

// BindingInput contains project identity and active hard budgets for one binding.
type BindingInput struct {
	Binding         projects.ClusterBinding
	TenantSlug      string
	ProjectSlug     string
	HardAllocations []allocations.Allocation
}

// BuildDesired translates enabled bindings and hard budgets into Slurm objects.
func BuildDesired(cluster clusters.Cluster, bindings []BindingInput, qos []slurm.QoS, partitions map[string]bool) (DesiredState, map[uuid.UUID][]projects.DriftItem) {
	desired := DesiredState{ActiveBindings: map[uuid.UUID]bool{}, ServiceUsersByAccount: map[string]string{}, BindingByAccount: map[string]uuid.UUID{}, SkippedAssociations: map[string]bool{}, Conflicts: map[string][]uuid.UUID{}}
	baseDrift := map[uuid.UUID][]projects.DriftItem{}
	counts := map[string][]BindingInput{}
	for _, entry := range bindings {
		b := entry.Binding
		if !b.Enabled {
			continue
		}
		desired.ActiveBindings[b.ID] = true
		counts[b.SlurmAccount] = append(counts[b.SlurmAccount], entry)
	}
	for account, entries := range counts {
		if len(entries) > 1 {
			ids := make([]uuid.UUID, 0, len(entries))
			for _, entry := range entries {
				ids = append(ids, entry.Binding.ID)
				baseDrift[entry.Binding.ID] = appendDrift(baseDrift[entry.Binding.ID], projects.DriftItem{Code: "POLICY_CONFLICT", Detail: "multiple enabled bindings use the same Slurm account"})
			}
			desired.Conflicts[account] = ids
			continue
		}
		entry := entries[0]
		b := entry.Binding
		desired.ServiceUsersByAccount[account] = cluster.ServiceUser
		desired.BindingByAccount[account] = b.ID
		desired.Accounts = append(desired.Accounts, DesiredAccount{BindingID: b.ID, Value: slurm.Account{Name: account, Description: "custos:" + entry.TenantSlug + "/" + entry.ProjectSlug, Organization: "custos", ParentAccount: cluster.PolicyParentAccount, Cluster: cluster.Name}})
	}
	qosSet := map[string]bool{}
	for _, q := range qos {
		qosSet[q.Name] = true
	}
	for account, entries := range counts {
		if len(entries) != 1 {
			continue
		}
		entry := entries[0]
		b := entry.Binding
		allowedQoS := append([]string(nil), b.AllowedQoS...)
		sort.Strings(allowedQoS)
		blockedQoS := false
		for _, name := range allowedQoS {
			if !qosSet[name] {
				baseDrift[b.ID] = appendDrift(baseDrift[b.ID], projects.DriftItem{Code: "QOS_UNKNOWN", Detail: "allowed QoS is absent from slurmdbd: " + name})
				blockedQoS = true
			}
		}
		if b.DefaultQoS != "" && !qosSet[b.DefaultQoS] {
			baseDrift[b.ID] = appendDrift(baseDrift[b.ID], projects.DriftItem{Code: "DEFAULT_QOS_UNKNOWN", Detail: "default QoS is absent from slurmdbd: " + b.DefaultQoS})
			blockedQoS = true
		}
		if b.DefaultPartition != "" && !partitions[b.DefaultPartition] {
			baseDrift[b.ID] = appendDrift(baseDrift[b.ID], projects.DriftItem{Code: "DEFAULT_PARTITION_UNKNOWN", Detail: "default partition is absent from cluster: " + b.DefaultPartition})
		}
		parts := append([]string(nil), b.AllowedPartitions...)
		sort.Strings(parts)
		if len(parts) == 0 {
			parts = []string{""}
		}
		for _, part := range parts {
			if part != "" && !partitions[part] {
				baseDrift[b.ID] = appendDrift(baseDrift[b.ID], projects.DriftItem{Code: "PARTITION_UNKNOWN", Detail: "allowed partition is absent from cluster: " + part})
				desired.SkippedAssociations[AssociationKey(slurm.Association{Account: account, Cluster: cluster.Name, User: cluster.ServiceUser, Partition: part})] = true
				continue
			}
			if blockedQoS {
				desired.SkippedAssociations[AssociationKey(slurm.Association{Account: account, Cluster: cluster.Name, User: cluster.ServiceUser, Partition: part})] = true
				continue
			}
			desired.Associations = append(desired.Associations, DesiredAssociation{BindingID: b.ID, Value: slurm.Association{Account: account, Cluster: cluster.Name, User: cluster.ServiceUser, Partition: part, QoS: allowedQoS, DefaultQoS: b.DefaultQoS, Comment: "custos:" + entry.TenantSlug + "/" + entry.ProjectSlug}})
		}
		limits := accountGrpTRESMins(entry.HardAllocations, cluster)
		// No Comment: slurmdbd does not persist comments on account-level
		// associations (26.05.4), so desiring one never converges. The
		// account description carries the custos:<tenant>/<project> tag.
		desired.Associations = append(desired.Associations, DesiredAssociation{BindingID: b.ID, Value: slurm.Association{Account: account, Cluster: cluster.Name, ParentAccount: cluster.PolicyParentAccount, GrpTRESMins: limits}})
	}
	return desired, baseDrift
}

func accountGrpTRESMins(items []allocations.Allocation, cluster clusters.Cluster) map[string]int64 {
	out := map[string]int64{"cpu": -1, "node": -1}
	hasGPU := false
	if cluster.Capabilities != nil {
		for _, gres := range cluster.Capabilities.GRESTypes {
			g := strings.ToLower(gres)
			if strings.HasPrefix(g, "gpu") || strings.Contains(g, "/gpu") {
				hasGPU = true
				break
			}
		}
	}
	if hasGPU {
		out["gres/gpu"] = -1
	}
	for _, item := range items {
		if item.Enforcement != "hard" {
			continue
		}
		key := ""
		switch item.Unit {
		case "cpu_hours":
			key = "cpu"
		case "node_hours":
			key = "node"
		case "gpu_hours":
			if hasGPU {
				key = "gres/gpu"
			}
		}
		if key == "" {
			continue
		}
		minutes := int64(math.Ceil(item.LimitAmount * 60))
		if out[key] < 0 || minutes < out[key] {
			out[key] = minutes
		}
	}
	return out
}

func appendDrift(items []projects.DriftItem, item projects.DriftItem) []projects.DriftItem {
	for _, existing := range items {
		if existing.Code == item.Code && existing.Detail == item.Detail {
			return items
		}
	}
	return append(items, item)
}
