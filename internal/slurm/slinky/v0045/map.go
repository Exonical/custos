package v0045

import (
	"slices"

	api "github.com/SlinkyProject/slurm-client/api/v0045"

	"github.com/Exonical/custos/internal/slurm"
)

// Response body aliases (JSON200/JSONDefault share the shape).
type (
	pingBody      = api.V0045OpenapiPingArrayResp
	partitionResp = api.V0045OpenapiPartitionResp
	nodesResp     = api.V0045OpenapiNodesResp
	resResp       = api.V0045OpenapiReservationResp
)

// Empty param structs for the read calls.
var (
	partitionParams api.SlurmV0045GetPartitionsParams
	nodeParams      api.SlurmV0045GetNodesParams
	resParams       api.SlurmV0045GetReservationsParams
)

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func mapPartition(p api.V0045PartitionInfo) slurm.Partition {
	out := slurm.Partition{
		Name:      strV(p.Name),
		IsDefault: false,
	}
	if p.Cpus != nil {
		out.TotalCPUs = i32(p.Cpus.Total)
	}
	if p.Nodes != nil {
		out.Nodes = i32(p.Nodes.Total)
	}
	if p.Minimums != nil {
		out.MinNodes = i32(p.Minimums.Nodes)
	}
	if p.Maximums != nil {
		out.MaxNodes = u32v(p.Maximums.Nodes)
		out.MaxTime = durVal(p.Maximums.Time)
	}
	if p.Defaults != nil {
		out.DefaultTime = durVal(p.Defaults.Time)
	}
	if p.Accounts != nil {
		out.AllowedAccounts = csvSplit(p.Accounts.Allowed)
		out.DeniedAccounts = csvSplit(p.Accounts.Deny)
	}
	if p.Qos != nil {
		out.AllowedQoS = csvSplit(p.Qos.Allowed)
		out.DefaultQoS = strV(p.Qos.Assigned)
	}
	if p.Partition != nil && p.Partition.State != nil {
		states := *p.Partition.State
		if len(states) > 0 {
			out.State = string(states[0])
		}
	}
	if p.Flags != nil {
		for _, f := range *p.Flags {
			if f == api.DEFAULT {
				out.IsDefault = true
			}
		}
	}
	return out
}

func mapNode(n api.V0045Node) slurm.Node {
	out := slurm.Node{
		Name:           strV(n.Name),
		CPUs:           i32(n.Cpus),
		AllocCPUs:      i32(n.AllocCpus),
		MemoryMiB:      i64(n.RealMemory),
		AllocMemoryMiB: i64(n.AllocMemory),
		GRES:           strV(n.Gres),
		GRESUsed:       strV(n.GresUsed),
		Partitions:     csv(n.Partitions),
		Features:       csv(n.Features),
		Reason:         strV(n.Reason),
	}
	if n.State != nil {
		for _, s := range *n.State {
			out.State = append(out.State, string(s))
		}
	}
	return out
}

func mapReservation(r api.V0045ReservationInfo) slurm.Reservation {
	out := slurm.Reservation{
		Name:      strV(r.Name),
		StartTime: tsVal(r.StartTime),
		EndTime:   tsVal(r.EndTime),
		Nodes:     i32(r.NodeCount),
		Users:     csvSplit(r.Users),
		Accounts:  csvSplit(r.Accounts),
	}
	if r.Flags != nil {
		for _, f := range *r.Flags {
			out.Flags = append(out.Flags, string(f))
		}
	}
	return out
}
