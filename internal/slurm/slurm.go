// Package slurm defines the Custos-owned Slurm port: neutral types and
// small interfaces. Nothing outside internal/slurm/slinky imports Slinky
// or generated types (docs/slurm.md).
package slurm

import (
	"context"
	"crypto/tls"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/secrets"
)

// Cluster is the slurmctld-facing port: read operations plus job
// submission/cancellation (M4).
type Cluster interface {
	Ping(ctx context.Context) (PingResult, error)
	Capabilities(ctx context.Context) (Capabilities, error)

	SubmitJob(ctx context.Context, req JobSubmission) (JobRef, error)
	GetJob(ctx context.Context, id JobID) (Job, error)
	ListJobs(ctx context.Context, f JobFilter) ([]Job, error)
	CancelJob(ctx context.Context, id JobID, opts CancelOptions) error

	GetNodes(ctx context.Context) ([]Node, error)
	GetPartitions(ctx context.Context) ([]Partition, error)
	GetReservations(ctx context.Context) ([]Reservation, error)
}

// Accounting is the slurmdbd-facing port. It is separate because it talks
// to a different daemon, fails independently, and may be disabled.
type Accounting interface {
	GetAssociations(ctx context.Context, f AssociationFilter) ([]Association, error)
	GetAccounts(ctx context.Context) ([]Account, error)
	GetQoS(ctx context.Context) ([]QoS, error)
	GetJobRecords(ctx context.Context, f JobRecordFilter) ([]JobRecord, error)
}

// Factory builds clients for a registered cluster. Credentials are
// resolved through secrets.Resolver at call time, never stored.
type Factory interface {
	Open(ctx context.Context, c ClusterConfig) (Cluster, Accounting, error)
}

// ClusterConfig is the adapter-facing view of a cluster registry row.
// (M3-B maps clusters.Cluster onto this.)
type ClusterConfig struct {
	ID            uuid.UUID
	Name          string
	BaseURL       string
	APIVersion    string // e.g. "v0.0.45"
	CABundle      []byte
	IdentityMode  string // "service" | "impersonate"
	ServiceUser   string
	TokenRef      secrets.Reference
	ClientCertRef *secrets.Reference
}

// Credential is the slurmrestd auth material for one cluster.
type Credential struct {
	UserName string
	Token    secrets.Value
}

// Endpoint is the resolved transport target.
type Endpoint struct {
	BaseURL     string
	CABundlePEM []byte
	ClientCert  *tls.Certificate
}

// --- Neutral types -----------------------------------------------------

// JobSubmission is everything needed to submit one job (docs/slurm.md).
type JobSubmission struct {
	Name        string
	Account     string
	Partition   string
	QoS         string
	Reservation string
	Script      string
	Argv        []string
	WorkingDir  string
	Environment map[string]string
	Stdout      string
	Stderr      string

	Nodes            int
	Tasks            int
	TasksPerNode     int
	CPUsPerTask      int
	MemoryPerNodeMiB int64
	MemoryPerCPUMiB  int64

	GRES         []GRESRequest
	Constraints  string
	Licenses     []string
	Walltime     time.Duration
	Array        *ArraySpec
	Dependencies []Dependency
	Nice         *int
	Comment      string
	UserName     string
}

// GRESRequest is a generic-resource request ({Name:"gpu",Type:"h100"}).
type GRESRequest struct {
	Name  string
	Type  string
	Count int64
}

// ArraySpec describes a job array.
type ArraySpec struct {
	Start, End, Step, MaxConcurrent int
}

// DependencyKind enumerates Slurm dependency types.
type DependencyKind string

// Dependency kinds accepted by Slurm.
const (
	DepAfterOK    DependencyKind = "afterok"
	DepAfterNotOK DependencyKind = "afternotok"
	DepAfterAny   DependencyKind = "afterany"
	DepSingleton  DependencyKind = "singleton"
)

// Dependency is a job dependency.
type Dependency struct {
	Kind   DependencyKind
	JobIDs []JobID
}

// JobID identifies a Slurm job (optionally array task / het component).
type JobID struct {
	ID           uint32
	ArrayTaskID  *uint32
	HetComponent *uint32
}

// JobRef is the result of a successful submission.
type JobRef struct {
	ID    JobID
	State JobState
}

// JobFilter scopes ListJobs.
type JobFilter struct {
	Names  []string
	Users  []string
	States []JobState
	Since  *time.Time
}

// CancelOptions tunes CancelJob.
type CancelOptions struct {
	Signal string
}

// JobState is the neutral job-state enum.
type JobState string

// Job states as reported by Slurm.
const (
	JobPending     JobState = "PENDING"
	JobRunning     JobState = "RUNNING"
	JobSuspended   JobState = "SUSPENDED"
	JobCompleting  JobState = "COMPLETING"
	JobCompleted   JobState = "COMPLETED"
	JobFailed      JobState = "FAILED"
	JobCancelled   JobState = "CANCELLED"
	JobTimeout     JobState = "TIMEOUT"
	JobNodeFail    JobState = "NODE_FAIL"
	JobPreempted   JobState = "PREEMPTED"
	JobBootFail    JobState = "BOOT_FAIL"
	JobDeadline    JobState = "DEADLINE"
	JobOutOfMemory JobState = "OUT_OF_MEMORY"
	JobUnknown     JobState = "UNKNOWN"
)

// ExitCode is a Slurm exit code (numeric plus signal when present).
type ExitCode struct {
	Code   int
	Signal int // 0 = none
}

// Job is the neutral job record.
type Job struct {
	ID           JobID
	Name         string
	Account      string
	Partition    string
	QoS          string
	UserName     string
	State        JobState
	StateReason  string
	ExitCode     *ExitCode
	SubmitTime   time.Time
	EligibleTime time.Time
	StartTime    time.Time
	EndTime      time.Time
	Nodes        int
	CPUs         int
	TRESAlloc    map[string]int64
	NodeList     string
	Comment      string
}

// Node is the neutral node record.
type Node struct {
	Name           string
	State          []string
	CPUs           int
	AllocCPUs      int
	MemoryMiB      int64
	AllocMemoryMiB int64
	GRES           string
	GRESUsed       string
	Partitions     []string
	Features       []string
	Reason         string
}

// Partition is the neutral partition record.
type Partition struct {
	Name            string         `json:"name"`
	State           string         `json:"state"`
	Nodes           int            `json:"nodes"`
	TotalCPUs       int            `json:"total_cpus"`
	MaxTime         *time.Duration `json:"max_time,omitempty"`
	DefaultTime     *time.Duration `json:"default_time,omitempty"`
	MaxNodes        int            `json:"max_nodes"`
	MinNodes        int            `json:"min_nodes"`
	AllowedAccounts []string       `json:"allowed_accounts"`
	DeniedAccounts  []string       `json:"denied_accounts"`
	AllowedQoS      []string       `json:"allowed_qos"`
	DefaultQoS      string         `json:"default_qos"`
	IsDefault       bool           `json:"is_default"`
}

// Reservation is the neutral reservation record.
type Reservation struct {
	Name      string
	StartTime time.Time
	EndTime   time.Time
	Nodes     int
	Users     []string
	Accounts  []string
	Flags     []string
}

// PingResult is the neutral ping record.
type PingResult struct {
	Hostname   string
	Mode       string // "primary" | "backup" | ""
	Responding bool
	Latency    time.Duration
}

// Capabilities is the cluster-sync snapshot persisted on clusters.
type Capabilities struct {
	SlurmVersion string
	APIVersion   string
	Partitions   []Partition
	QoSNames     []string
	GRESTypes    []string // e.g. "gpu:h100"
	Features     []string
	NodeSummary  map[string]int // state -> count
	CollectedAt  time.Time
}

// --- Accounting types --------------------------------------------------

// AssociationFilter scopes GetAssociations.
type AssociationFilter struct {
	Users      []string
	Accounts   []string
	Clusters   []string
	Partitions []string
}

// Association is a user-account(-partition) association.
type Association struct {
	ID         int32
	User       string
	Account    string
	Cluster    string
	Partition  string
	IsDefault  bool
	DefaultQoS string
}

// Account is a Slurm account.
type Account struct {
	Name         string
	Description  string
	Organization string
}

// QoS is a quality-of-service entry.
type QoS struct {
	ID          int32
	Name        string
	Description string
	Priority    int32
}

// JobRecordFilter scopes GetJobRecords (sacct-like, windowed).
type JobRecordFilter struct {
	Names    []string
	Users    []string
	Accounts []string
	States   []JobState
	Since    *time.Time
	Until    *time.Time
}

// JobRecord is a historical accounting record.
type JobRecord struct {
	ID        JobID
	Name      string
	User      string
	Account   string
	Partition string
	State     JobState
	ExitCode  *ExitCode
	StartTime time.Time
	EndTime   time.Time
	Elapsed   time.Duration
	TRESUsage map[string]int64
}

// --- Errors ------------------------------------------------------------

var (
	// ErrNotFound marks a job/resource that does not exist.
	ErrNotFound = errors.New("slurm: not found")
	// ErrUnavailable marks transient failures (daemon down, timeout).
	ErrUnavailable = errors.New("slurm: unavailable")
	// ErrUnauthorized marks auth failures at slurmrestd.
	ErrUnauthorized = errors.New("slurm: unauthorized")
)

// Classify maps a port error onto an apperr kind.
func Classify(err error) apperr.Kind {
	switch {
	case errors.Is(err, ErrNotFound):
		return apperr.NotFound
	case errors.Is(err, ErrUnauthorized):
		return apperr.Unauthenticated
	case errors.Is(err, ErrUnavailable):
		return apperr.Unavailable
	default:
		return apperr.Internal
	}
}
