// Package admission holds ExecutionSpec — the immutable canonical record
// of what Custos intends to run — and the pure builder pipeline.
//
// Invariant (docs/script-validation.md): this package never sees script
// bodies, only PayloadRef{ScriptID, Digest, Language}; depguard forbids
// importing internal/scripts.
package admission

import (
	"encoding/json"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// SchemaVersion is the ExecutionSpec schema version.
const SchemaVersion = 1

// Interpreter is an enum; never user-specified.
type Interpreter string

// Supported interpreters.
const (
	InterpreterBash   Interpreter = "/bin/bash"
	InterpreterSh     Interpreter = "/bin/sh"
	InterpreterPython Interpreter = "python3"
)

// ClusterRef identifies the target cluster.
type ClusterRef struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	APIVersion string    `json:"api_version"`
}

// ResolvedResources is the normalized resource request (MiB, seconds).
type ResolvedResources struct {
	Nodes            int                     `json:"nodes,omitempty"`
	Tasks            int                     `json:"tasks,omitempty"`
	TasksPerNode     int                     `json:"tasks_per_node,omitempty"`
	CPUsPerTask      int                     `json:"cpus_per_task,omitempty"`
	MemoryPerNodeMiB int64                   `json:"memory_per_node_mib,omitempty"`
	MemoryPerCPUMiB  int64                   `json:"memory_per_cpu_mib,omitempty"`
	GPUType          string                  `json:"gpu_type,omitempty"`
	GPUCount         int                     `json:"gpu_count,omitempty"`
	WalltimeSeconds  int64                   `json:"walltime_seconds"`
	Licenses         []string                `json:"licenses,omitempty"`
	Constraints      string                  `json:"constraints,omitempty"`
	Exclusive        bool                    `json:"exclusive,omitempty"`
	Array            *workflowspec.ArraySpec `json:"array,omitempty"`
}

// PlacementDecision records where the job lands and why.
type PlacementDecision struct {
	Cluster string `json:"cluster"`
	Reason  string `json:"reason"` // "explicit" | engine name
}

// ResolvedSoftware is a software-catalog resolution result.
type ResolvedSoftware struct {
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	ModuleSpec []string `json:"module_spec,omitempty"`
}

// SecretEnvRef names a secret-injected variable; the value never
// appears in the spec.
type SecretEnvRef struct {
	Name        string    `json:"name"`
	ReferenceID uuid.UUID `json:"reference_id"`
	Mode        string    `json:"mode"` // env | wrapped_token | image_pull
	Handle      string    `json:"handle"`
}

const (
	// ImagePullUsernameEnvName is the internal username slot for a secret pull.
	ImagePullUsernameEnvName = "CUSTOS_IMAGE_PULL_USERNAME"
	// ImagePullPasswordEnvName is the internal password slot for a secret pull.
	ImagePullPasswordEnvName = "CUSTOS_IMAGE_PULL_PASSWORD" // #nosec G101 -- environment-variable name, not a credential
)

// EnvSet is the classified environment.
type EnvSet struct {
	Controlled map[string]string `json:"controlled,omitempty"`
	User       map[string]string `json:"user,omitempty"`
	// Runtime maps an env name to an allow-listed Slurm runtime variable.
	Runtime    map[string]string `json:"runtime,omitempty"`
	SecretRefs []SecretEnvRef    `json:"secret_refs,omitempty"`
}

// ServiceDependency freezes one service task required by this job.
type ServiceDependency struct {
	TaskExecutionID uuid.UUID `json:"task_execution_id"`
	ClusterID       uuid.UUID `json:"cluster_id"`
	TaskName        string    `json:"task_name"`
	EnvName         string    `json:"env_name"`
	SlurmJobID      uint32    `json:"slurm_job_id,omitempty"`
	After           bool      `json:"after,omitempty"`
	OnFailureRun    bool      `json:"on_failure_run,omitempty"`
}

var serviceSlurmJobIDRE = regexp.MustCompile(`^[0-9]+(_[0-9]+)?$`)

// ValidServiceSlurmJobID validates an ID before placing it in a frozen task environment.
func ValidServiceSlurmJobID(value string) bool {
	return serviceSlurmJobIDRE.MatchString(value)
}

const (
	// RuntimeSlurmArrayTaskID references the current Slurm array index.
	RuntimeSlurmArrayTaskID = "SLURM_ARRAY_TASK_ID"
	// RuntimeMultinodeHostlist references the slot-qualified host list.
	RuntimeMultinodeHostlist = "MULTINODE_HOSTLIST"
	// RuntimeMultinodeHostlistNoSlots references the host list without slots.
	RuntimeMultinodeHostlistNoSlots = "MULTINODE_HOSTLIST_NOSLOTS"
	// RuntimeMultinodeTotalSlots references the total generic multinode slots.
	RuntimeMultinodeTotalSlots = "MULTINODE_TOTAL_SLOTS"
	// RuntimeMultinodeNodeIP references the current node's first IP address.
	RuntimeMultinodeNodeIP = "MULTINODE_NODE_IP"
	// RuntimeMultinodeSSHWrapper references the generated remote launcher.
	RuntimeMultinodeSSHWrapper = "MULTINODE_SSH_WRAPPER"
	// RuntimeMultinodeRSHWrapper aliases the generated remote launcher.
	RuntimeMultinodeRSHWrapper = "MULTINODE_RSH_WRAPPER"
)

var multinodeRuntimeNames = map[string]bool{
	RuntimeMultinodeHostlist: true, RuntimeMultinodeHostlistNoSlots: true,
	RuntimeMultinodeTotalSlots: true, RuntimeMultinodeNodeIP: true,
	RuntimeMultinodeSSHWrapper: true, RuntimeMultinodeRSHWrapper: true,
}

// ValidRuntime reports whether s is an allow-listed runtime variable.
func ValidRuntime(s string) bool {
	return s == RuntimeSlurmArrayTaskID || multinodeRuntimeNames[s]
}

// ValidRuntimeFor rejects multinode references outside generic tasks.
func ValidRuntimeFor(s string, genericMultinode bool) bool {
	if multinodeRuntimeNames[s] {
		return genericMultinode
	}
	return s == RuntimeSlurmArrayTaskID
}

// ArgvElement is one rendered argv word: either a literal (emitted
// single-quoted) or a runtime variable reference (emitted as "$VAR",
// Custos-authored). Exactly one field is set.
type ArgvElement struct {
	Literal string `json:"literal,omitempty"`
	Runtime string `json:"runtime,omitempty"`
}

// PayloadRef points at the stored script; admission never holds bytes.
type PayloadRef struct {
	ScriptID    uuid.UUID             `json:"script_id"`
	Digest      validation.Digest     `json:"digest"`
	Language    workflowspec.Language `json:"language"`
	Interpreter Interpreter           `json:"interpreter"`
}

// ArtifactRef points at an input/output artifact.
type ArtifactRef struct {
	Path string `json:"path"`
	Ref  string `json:"ref"`
}

// SecurityContext is the execution security posture.
type SecurityContext struct {
	SlurmUser         string   `json:"slurm_user"`
	ImpersonationMode string   `json:"impersonation_mode"` // "service" | "impersonate"
	ShellTask         bool     `json:"shell_task"`
	WrappedTokenRefs  []string `json:"wrapped_token_refs,omitempty"`
}

// Warning records a non-blocking admission policy outcome.
type Warning struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Allocation string `json:"allocation,omitempty"`
	Unit       string `json:"unit,omitempty"`
	Message    string `json:"message"`
}

// Result records value-free cost estimates and warnings in the execution digest.
type Result struct {
	EstimatedCost map[string]float64 `json:"estimated_cost,omitempty"`
	Warnings      []Warning          `json:"warnings,omitempty"`
}

// ContainerSpec freezes the cluster-selected image runtime.
type ContainerSpec struct {
	Runtime            string `json:"runtime"`
	Image              string `json:"image"`
	Binary             string `json:"binary,omitempty"`
	PullSecret         bool   `json:"pull_secret,omitempty"`
	PullUsername       string `json:"pull_username,omitempty"`
	PullUsernameSecret bool   `json:"pull_username_secret,omitempty"`
	RegistryHost       string `json:"registry_host,omitempty"`
	EnrootImage        string `json:"enroot_image,omitempty"`
}

// MultinodeSpec freezes the distributed launcher shape.
type MultinodeSpec struct {
	Implementation string `json:"implementation"`
	Nodes          int    `json:"nodes"`
	SlotsPerNode   int    `json:"slots_per_node"`
	MPIPlugin      string `json:"mpi_plugin,omitempty"`
}

// ExecutionSpec is the immutable canonical record persisted on
// task_executions.execution_spec.
type ExecutionSpec struct {
	SchemaVersion     int            `json:"schema_version"`
	ID                uuid.UUID      `json:"id"`
	TenantID          uuid.UUID      `json:"tenant_id"`
	ProjectID         uuid.UUID      `json:"project_id"`
	PrincipalID       uuid.UUID      `json:"principal_id"`
	WorkflowVersionID uuid.UUID      `json:"workflow_version_id"`
	TaskName          string         `json:"task_name"`
	Launch            string         `json:"launch,omitempty"`
	Container         *ContainerSpec `json:"container,omitempty"`
	Multinode         *MultinodeSpec `json:"multinode,omitempty"`
	Isolation         *IsolationSpec `json:"isolation,omitempty"`
	CPUBind           string         `json:"cpu_bind,omitempty"`
	Attempt           int            `json:"attempt"`

	Cluster     ClusterRef `json:"cluster"`
	Account     string     `json:"account,omitempty"`
	Partition   string     `json:"partition,omitempty"`
	QoS         string     `json:"qos,omitempty"`
	Reservation string     `json:"reservation,omitempty"`

	Resources           ResolvedResources   `json:"resources"`
	Placement           PlacementDecision   `json:"placement"`
	Software            []ResolvedSoftware  `json:"software,omitempty"`
	Environment         EnvSet              `json:"environment"`
	ContainerEnv        map[string]string   `json:"container_env,omitempty"`
	ServiceDependencies []ServiceDependency `json:"service_dependencies,omitempty"`

	// Payload is the script payload; zero Digest means a command task —
	// Argv then carries the program with Argv[0] a literal. Script tasks
	// set Payload and may add extra arguments in Argv.
	Payload    PayloadRef    `json:"payload"`
	Argv       []ArgvElement `json:"argv,omitempty"`
	Inputs     []ArtifactRef `json:"inputs,omitempty"`
	Outputs    []ArtifactRef `json:"outputs,omitempty"`
	WorkingDir string        `json:"working_dir,omitempty"`
	Stdout     string        `json:"stdout,omitempty"`
	Stderr     string        `json:"stderr,omitempty"`

	Security  SecurityContext `json:"security"`
	Admission Result          `json:"admission"`

	Digest     validation.Digest `json:"digest"`
	AdmittedAt time.Time         `json:"admitted_at"`
	AdmittedBy string            `json:"admitted_by"`
}

// canonicalView mirrors ExecutionSpec minus Digest/AdmittedAt for the
// digest computation.
type canonicalView ExecutionSpec

// Canonical marshals the spec deterministically (struct field order is
// fixed; encoding/json sorts map keys), excluding Digest and
// AdmittedAt.
func (s ExecutionSpec) Canonical() ([]byte, error) {
	v := canonicalView(s)
	v.Digest = validation.Digest{}
	v.AdmittedAt = time.Time{}
	return json.Marshal(v)
}

// Freeze computes Digest over the canonical form.
func (s *ExecutionSpec) Freeze() error {
	b, err := s.Canonical()
	if err != nil {
		return err
	}
	s.Digest = validation.DigestOf(b)
	return nil
}

// Denial is a typed admission rejection.
type Denial struct {
	Code     string
	Field    string
	Message  string
	Severity validation.Severity
}

func (d *Denial) Error() string { return d.Code + ": " + d.Message }
