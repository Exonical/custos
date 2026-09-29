package admission

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
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

// ResolveSoftware maps each requirement onto the cluster's module
// catalog, preferring an exact version entry over a version-less one.
// Unresolvable requirements fail closed: running without the requested
// environment produces failures far from their cause.
func ResolveSoftware(reqs []workflowspec.SoftwareRequirement,
	catalog []validation.SoftwareModule) ([]ResolvedSoftware, *Denial) {
	if len(reqs) == 0 {
		return nil, nil
	}
	out := make([]ResolvedSoftware, 0, len(reqs))
	for _, req := range reqs {
		var match *validation.SoftwareModule
		for i := range catalog {
			entry := &catalog[i]
			if entry.Name != req.Name {
				continue
			}
			if entry.Version == req.Version {
				match = entry
				break
			}
			if entry.Version == "" && match == nil {
				match = entry
			}
		}
		if match == nil {
			return nil, deny("SOFTWARE_UNAVAILABLE", "software",
				fmt.Sprintf("software %s@%s has no module mapping on the cluster",
					req.Name, req.Version))
		}
		out = append(out, ResolvedSoftware{Name: req.Name,
			Version: req.Version, ModuleSpec: slices.Clone(match.Modules)})
	}
	return out, nil
}

// BuildInput carries everything Build needs (authn/authz already done
// by the caller).
type BuildInput struct {
	Spec         ExecutionSpec // identity, placement, payload refs prefilled
	Software     []workflowspec.SoftwareRequirement
	Request      workflowspec.Resources
	Image        *workflowspec.Image
	Multinode    *workflowspec.Multinode
	CPUAffinity  string
	ContainerEnv map[string]string
	Policy       ResourcePolicy
	Binding      Binding
	Cluster      validation.ClusterSnapshot
	Allocate     func(ResolvedResources) (*Denial, []Warning) // nil → no budgets
}

// CheckArgv enforces the argv/payload invariant: a spec is either a
// script task (Payload.Digest set; Argv = extra arguments) or a
// command task (no payload; Argv non-empty with Argv[0] a literal
// program). Every element is exactly one of literal/runtime and
// runtime is allow-listed.
func CheckArgv(spec ExecutionSpec) *Denial {
	hasPayload := spec.Payload.Digest != validation.Digest{}
	if !hasPayload && len(spec.Argv) == 0 {
		return deny("ARGV_REQUIRED", "argv",
			"spec has neither a payload nor argv")
	}
	if !hasPayload && spec.Argv[0].Literal == "" {
		return deny("ARGV_PROGRAM", "argv",
			"argv[0] must be a literal program name")
	}
	genericMultinode := spec.Multinode != nil && spec.Multinode.Implementation == "generic"
	for i, e := range spec.Argv {
		switch {
		case e.Literal != "" && e.Runtime != "":
			return deny("ARGV_ELEMENT", "argv",
				fmt.Sprintf("argv[%d] sets both literal and runtime", i))
		case e.Literal == "" && e.Runtime == "":
			return deny("ARGV_ELEMENT", "argv",
				fmt.Sprintf("argv[%d] sets neither literal nor runtime", i))
		case e.Runtime != "" && !ValidRuntimeFor(e.Runtime, genericMultinode):
			return deny("ARGV_RUNTIME", "argv",
				fmt.Sprintf("argv[%d] runtime %q is not allow-listed", i, e.Runtime))
		}
	}
	for name, rt := range spec.Environment.Runtime {
		if !ValidRuntimeFor(rt, genericMultinode) {
			return deny("ARGV_RUNTIME", "environment",
				"env "+name+" runtime "+rt+" is not allow-listed")
		}
	}
	return nil
}

var containerDigestRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

func pyxisImageURI(uri string) string {
	rest := strings.TrimPrefix(uri, "docker://")
	registry, imagePath, hasPath := strings.Cut(rest, "/")
	if !hasPath {
		return rest
	}
	if registry == "localhost" || strings.Contains(registry, ".") || strings.Contains(registry, ":") {
		return registry + "#" + imagePath
	}
	return rest
}

func resolveContainer(image *workflowspec.Image, runtime *validation.ContainerRuntime,
	multinode *workflowspec.Multinode) (*ContainerSpec, *Denial) {
	if image == nil {
		return nil, nil
	}
	if runtime == nil {
		return nil, deny("CONTAINER_RUNTIME_UNAVAILABLE", "image",
			"the target cluster has no container runtime configured")
	}
	uri := image.URI
	if runtime.RequireDigest && !containerDigestRe.MatchString(uri) {
		return nil, deny("IMAGE_DIGEST_REQUIRED", "image.uri",
			"the cluster requires images pinned by sha256 digest")
	}
	if len(runtime.AllowedImagePrefixes) > 0 {
		allowed := false
		for _, prefix := range runtime.AllowedImagePrefixes {
			if strings.HasPrefix(uri, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, deny("IMAGE_NOT_ALLOWED", "image.uri",
				"image URI does not match a cluster allow-list prefix")
		}
	}
	container := &ContainerSpec{Runtime: runtime.Type, Image: uri}
	switch runtime.Type {
	case "apptainer":
		container.Binary = runtime.Binary
		if container.Binary == "" {
			container.Binary = "apptainer"
		}
	case "pyxis":
		if strings.HasPrefix(uri, "oras://") {
			return nil, deny("IMAGE_RUNTIME_UNSUPPORTED", "image.uri",
				"pyxis accepts docker image references or absolute paths")
		}
		if !strings.HasPrefix(uri, "/") {
			if !strings.HasPrefix(uri, "docker://") {
				return nil, deny("IMAGE_RUNTIME_UNSUPPORTED", "image.uri",
					"pyxis accepts docker image references or absolute paths")
			}
			container.Image = pyxisImageURI(uri)
		}
	default:
		return nil, deny("CONTAINER_RUNTIME_UNAVAILABLE", "image",
			"the target cluster has an unsupported container runtime")
	}
	if multinode != nil && multinode.EffectiveImplementation() == "generic" &&
		!runtime.SlurmInContainer {
		return nil, deny("MULTINODE_CONTAINER_UNSUPPORTED", "image",
			"generic multinode container execution requires slurm_in_container")
	}
	return container, nil
}

func resolvedMultinode(m *workflowspec.Multinode,
	runtime *validation.ContainerRuntime, request workflowspec.Resources) *MultinodeSpec {
	if m == nil {
		return nil
	}
	implementation := m.EffectiveImplementation()
	out := &MultinodeSpec{Implementation: implementation, Nodes: m.Nodes,
		SlotsPerNode: m.ProcsPerNode}
	if out.SlotsPerNode < 1 {
		if implementation == "generic" {
			out.SlotsPerNode = request.CPUsPerTask
		} else {
			out.SlotsPerNode = request.TasksPerNode
		}
	}
	if out.SlotsPerNode < 1 {
		out.SlotsPerNode = 1
	}
	if implementation == "openmpi" || implementation == "mpich" {
		out.MPIPlugin = "pmix"
		if runtime != nil && runtime.MPIPlugin != "" {
			out.MPIPlugin = runtime.MPIPlugin
		}
	}
	return out
}

func cpuBind(affinity string) (string, *Denial) {
	switch affinity {
	case "", "none":
		return "", nil
	case "core":
		return "cores", nil
	case "socket":
		return "sockets", nil
	case "numa":
		return "ldoms", nil
	default:
		return "", deny("CPU_AFFINITY_INVALID", "resources.cpuAffinity",
			"cpu affinity must be none, core, socket, or numa")
	}
}

// Build chains the pure admission steps in the documented order and
// freezes the spec on success.
func Build(in BuildInput) (ExecutionSpec, *Denial) {
	spec := in.Spec
	spec.Multinode = resolvedMultinode(in.Multinode, in.Cluster.ContainerRuntime, in.Request)
	container, denial := resolveContainer(in.Image, in.Cluster.ContainerRuntime, in.Multinode)
	if denial != nil {
		return ExecutionSpec{}, denial
	}
	spec.Container = container
	spec.ContainerEnv = nil
	if container != nil && len(in.ContainerEnv) > 0 {
		spec.ContainerEnv = make(map[string]string, len(in.ContainerEnv))
		for name, value := range in.ContainerEnv {
			spec.ContainerEnv[name] = value
		}
	}
	var err *Denial
	spec.CPUBind, err = cpuBind(in.CPUAffinity)
	if err != nil {
		return ExecutionSpec{}, err
	}
	if d := CheckArgv(spec); d != nil {
		return ExecutionSpec{}, d
	}
	if d := CheckResourcePolicy(in.Request, in.Policy); d != nil {
		return ExecutionSpec{}, d
	}
	account, partition, qos, d := CheckEntitlement(in.Binding,
		in.Spec.Partition, in.Spec.QoS)
	if d != nil {
		return ExecutionSpec{}, d
	}
	if d := CheckAdmission(in.Request, partition, in.Cluster); d != nil {
		return ExecutionSpec{}, d
	}
	software, d := ResolveSoftware(in.Software, in.Cluster.Software)
	if d != nil {
		return ExecutionSpec{}, d
	}
	spec.Software = software
	spec.SchemaVersion = SchemaVersion
	// Slurm requires a current working directory and the wrapper cd's to
	// it — default to /tmp when the request leaves it unset.
	if spec.WorkingDir == "" {
		spec.WorkingDir = "/tmp"
	}
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
	spec.Admission.EstimatedCost = EstimateCost(spec.Resources)
	if in.Allocate != nil {
		denial, warnings := in.Allocate(spec.Resources)
		if denial != nil {
			return ExecutionSpec{}, denial
		}
		spec.Admission.Warnings = warnings
	}
	spec.AdmittedBy = "admission/v1"
	if err := spec.Freeze(); err != nil {
		return ExecutionSpec{}, &Denial{Code: "INTERNAL", Message: err.Error(),
			Severity: validation.SeverityError}
	}
	return spec, nil
}
