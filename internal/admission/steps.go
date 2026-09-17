package admission

import (
	"fmt"
	"slices"
	"time"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

func deny(code, field, msg string) *Denial {
	return &Denial{Code: code, Field: field, Message: msg,
		Severity: validation.SeverityPolicy}
}

// ResourcePolicy bounds what a principal may request. Zero numeric
// limits mean unlimited; nil lists mean any.
type ResourcePolicy struct {
	MaxNodes            int                   `json:"max_nodes,omitempty"`
	MaxTasks            int                   `json:"max_tasks,omitempty"`
	MaxCPUsPerTask      int                   `json:"max_cpus_per_task,omitempty"`
	MaxMemoryPerNodeMiB int64                 `json:"max_memory_per_node_mib,omitempty"`
	MaxGPUsPerJob       int                   `json:"max_gpus_per_job,omitempty"`
	MaxWalltime         workflowspec.Duration `json:"max_walltime,omitempty"`
	AllowedPartitions   []string              `json:"allowed_partitions,omitempty"`
	AllowedQoS          []string              `json:"allowed_qos,omitempty"`
	AllowedGPUTypes     []string              `json:"allowed_gpu_types,omitempty"`
	AllowExclusive      bool                  `json:"allow_exclusive,omitempty"`
	MaxArraySize        int                   `json:"max_array_size,omitempty"`
}

// CheckResourcePolicy enforces numeric limits and allow-lists.
func CheckResourcePolicy(req workflowspec.Resources, pol ResourcePolicy) *Denial {
	bump := func(limit int, got int, field, what string) *Denial {
		if limit > 0 && got > limit {
			return deny("RESOURCE_LIMIT", field,
				fmt.Sprintf("%s %d exceeds policy limit %d", what, got, limit))
		}
		return nil
	}
	if d := bump(pol.MaxNodes, req.Nodes, "nodes", "nodes"); d != nil {
		return d
	}
	if d := bump(pol.MaxTasks, req.Tasks, "tasks", "tasks"); d != nil {
		return d
	}
	if d := bump(pol.MaxCPUsPerTask, req.CPUsPerTask, "cpus_per_task", "cpus_per_task"); d != nil {
		return d
	}
	if pol.MaxMemoryPerNodeMiB > 0 && req.MemoryPerNodeMiB > pol.MaxMemoryPerNodeMiB {
		return deny("RESOURCE_LIMIT", "memory_per_node_mib", "memory exceeds policy limit")
	}
	if req.GPU != nil {
		if d := bump(pol.MaxGPUsPerJob, req.GPU.Count, "gpu", "gpus"); d != nil {
			return d
		}
		if len(pol.AllowedGPUTypes) > 0 && req.GPU.Type != "" &&
			!slices.Contains(pol.AllowedGPUTypes, req.GPU.Type) {
			return deny("GPU_TYPE_DENIED", "gpu",
				"gpu type "+req.GPU.Type+" is not allowed")
		}
	}
	if pol.MaxWalltime > 0 && time.Duration(req.Walltime) > time.Duration(pol.MaxWalltime) {
		return deny("RESOURCE_LIMIT", "walltime", "walltime exceeds policy limit")
	}
	if req.Exclusive && !pol.AllowExclusive {
		return deny("EXCLUSIVE_DENIED", "exclusive", "exclusive allocation is not allowed")
	}
	if req.Array != nil && pol.MaxArraySize > 0 &&
		req.Array.End-req.Array.Start+1 > pol.MaxArraySize {
		return deny("RESOURCE_LIMIT", "array", "array size exceeds policy limit")
	}
	return nil
}

// Binding is the project↔cluster entitlement.
type Binding struct {
	Account           string
	DefaultPartition  string
	AllowedPartitions []string
	AllowedQoS        []string
	DefaultQoS        string
}

// CheckEntitlement resolves account/partition/qos against the binding.
func CheckEntitlement(b Binding, partition, qos string) (account, part, q string, d *Denial) {
	account = b.Account
	part = partition
	if part == "" {
		part = b.DefaultPartition
	}
	if len(b.AllowedPartitions) > 0 && part != "" &&
		!slices.Contains(b.AllowedPartitions, part) {
		return "", "", "", deny("PARTITION_DENIED", "partition",
			"partition "+part+" is not in the project binding")
	}
	q = qos
	if q == "" {
		q = b.DefaultQoS
	}
	if len(b.AllowedQoS) > 0 && q != "" && !slices.Contains(b.AllowedQoS, q) {
		return "", "", "", deny("QOS_DENIED", "qos",
			"qos "+q+" is not in the project binding")
	}
	return account, part, q, nil
}

// CheckAdmission verifies the request fits the cluster's reported
// capabilities (partition exists, GRES type exists, within partition
// limits).
func CheckAdmission(req workflowspec.Resources, partition string,
	caps validation.ClusterSnapshot) *Denial {
	if len(caps.Partitions) > 0 && partition != "" &&
		!slices.Contains(caps.Partitions, partition) {
		return deny("NO_PARTITION", "partition",
			"partition "+partition+" does not exist on the cluster")
	}
	// GRESTypes entries carry the gres name ("gpu:h100"); the request
	// holds just the type ("h100").
	if req.GPU != nil && req.GPU.Type != "" && len(caps.GRESTypes) > 0 &&
		!slices.Contains(caps.GRESTypes, req.GPU.Type) &&
		!slices.Contains(caps.GRESTypes, "gpu:"+req.GPU.Type) {
		return deny("NO_GRES", "gpu",
			"gpu type "+req.GPU.Type+" is not available on the cluster")
	}
	if partition != "" && caps.MaxWalltime != nil {
		if maxW, ok := caps.MaxWalltime[partition]; ok && maxW > 0 &&
			time.Duration(req.Walltime) > maxW {
			return deny("WALLTIME_EXCEEDS_PARTITION", "walltime",
				"walltime exceeds the partition maximum")
		}
	}
	return nil
}

// BuildInput carries everything Build needs (authn/authz already done
// by the caller; allocation is a no-op hook for now).
type BuildInput struct {
	Spec     ExecutionSpec // identity, placement, payload refs prefilled
	Request  workflowspec.Resources
	Policy   ResourcePolicy
	Binding  Binding
	Cluster  validation.ClusterSnapshot
	Allocate func() *Denial // nil → allow
}

// Build chains the pure admission steps in the documented order and
// freezes the spec on success.
func Build(in BuildInput) (ExecutionSpec, *Denial) {
	if d := CheckResourcePolicy(in.Request, in.Policy); d != nil {
		return ExecutionSpec{}, d
	}
	account, partition, qos, d := CheckEntitlement(in.Binding,
		in.Spec.Partition, in.Spec.QoS)
	if d != nil {
		return ExecutionSpec{}, d
	}
	if in.Allocate != nil {
		if d := in.Allocate(); d != nil {
			return ExecutionSpec{}, d
		}
	}
	if d := CheckAdmission(in.Request, partition, in.Cluster); d != nil {
		return ExecutionSpec{}, d
	}
	spec := in.Spec
	spec.SchemaVersion = SchemaVersion
	spec.Account = account
	spec.Partition = partition
	spec.QoS = qos
	spec.Resources = ResolvedResources{
		Nodes:            in.Request.Nodes,
		Tasks:            in.Request.Tasks,
		TasksPerNode:     in.Request.TasksPerNode,
		CPUsPerTask:      in.Request.CPUsPerTask,
		MemoryPerNodeMiB: in.Request.MemoryPerNodeMiB,
		MemoryPerCPUMiB:  in.Request.MemoryPerCPUMiB,
		WalltimeSeconds:  int64(time.Duration(in.Request.Walltime) / time.Second),
		Licenses:         in.Request.Licenses,
		Constraints:      in.Request.Constraints,
		Exclusive:        in.Request.Exclusive,
		Array:            in.Request.Array,
	}
	if in.Request.GPU != nil {
		spec.Resources.GPUType = in.Request.GPU.Type
		spec.Resources.GPUCount = in.Request.GPU.Count
	}
	spec.AdmittedBy = "admission/v1"
	if err := spec.Freeze(); err != nil {
		return ExecutionSpec{}, &Denial{Code: "INTERNAL", Message: err.Error(),
			Severity: validation.SeverityError}
	}
	return spec, nil
}
