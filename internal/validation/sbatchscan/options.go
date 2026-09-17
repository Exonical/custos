// Package sbatchscan scans payloads for #SBATCH directives, canonicalizes
// them against the sbatch 26.05 option table and judges them under
// policy (reject mode in M4; import mode in M5).
package sbatchscan

// Field is the canonical scheduler-field family a directive option
// belongs to; one CUSTOS1xx code per family.
type Field string

// Canonical field families.
const (
	FieldAccount       Field = "account"
	FieldPartition     Field = "partition"
	FieldQoS           Field = "qos"
	FieldReservation   Field = "reservation"
	FieldNodes         Field = "nodes"
	FieldTasks         Field = "tasks"
	FieldTasksPerNode  Field = "tasks_per_node"
	FieldCPUsPerTask   Field = "cpus_per_task"
	FieldMemory        Field = "memory"
	FieldGRES          Field = "gres"
	FieldConstraints   Field = "constraints"
	FieldLicenses      Field = "licenses"
	FieldWalltime      Field = "walltime"
	FieldArray         Field = "array"
	FieldDependencies  Field = "dependencies"
	FieldPriority      Field = "priority"
	FieldExclusive     Field = "exclusive"
	FieldNodeSelection Field = "node_selection"
	FieldTopology      Field = "topology"
	FieldCluster       Field = "cluster"
	FieldIdentityEnv   Field = "identity_env"
	FieldNaming        Field = "naming"
	FieldIOPaths       Field = "io_paths"
	FieldMail          Field = "mail"
	FieldBehavior      Field = "behavior"
	FieldUnknown       Field = "unknown"
	FieldOther         Field = "other"
)

// FieldCode maps each field family to its CUSTOS1xx diagnostic code.
func FieldCode(f Field) string {
	switch f {
	case FieldGRES:
		return "CUSTOS101"
	case FieldQoS:
		return "CUSTOS102"
	case FieldAccount:
		return "CUSTOS103"
	case FieldPartition:
		return "CUSTOS104"
	case FieldReservation:
		return "CUSTOS105"
	case FieldNodes, FieldTasks, FieldTasksPerNode, FieldCPUsPerTask, FieldExclusive:
		return "CUSTOS106"
	case FieldMemory:
		return "CUSTOS107"
	case FieldWalltime:
		return "CUSTOS108"
	case FieldArray:
		return "CUSTOS109"
	case FieldDependencies:
		return "CUSTOS110"
	case FieldPriority:
		return "CUSTOS111"
	case FieldNodeSelection, FieldTopology, FieldConstraints, FieldLicenses:
		return "CUSTOS112"
	case FieldIdentityEnv:
		return "CUSTOS113"
	case FieldNaming:
		return "CUSTOS114"
	case FieldIOPaths:
		return "CUSTOS115"
	case FieldCluster:
		return "CUSTOS116"
	case FieldMail:
		return "CUSTOS117"
	case FieldBehavior, FieldOther:
		return "CUSTOS118"
	}
	return "CUSTOS199"
}

// ValueKind describes whether an option takes a value.
type ValueKind int

// Value kinds.
const (
	ValueNone     ValueKind = iota // flag only
	ValueRequired                  // must carry a value
	ValueOptional                  // may carry =value, never consumes next word
)

// Class is the policy class; in v1 every option is Controlled.
type Class string

// ClassControlled marks options only the Custos envelope may set.
const ClassControlled Class = "controlled"

// Option is one row of the canonical option table.
type Option struct {
	Long  string
	Short string // single letter, "" if none
	Field Field
	Value ValueKind
	Class Class
}

// Options is the sbatch 26.05 option table; derived from
// testdata/sbatch-26.05-options.txt (SchedMD sbatch man page, Slurm
// 26.05). A test asserts the two stay in sync.
var Options = []Option{
	{Long: "account", Short: "A", Field: FieldAccount, Value: ValueRequired, Class: ClassControlled},
	{Long: "acctg-freq", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "array", Short: "a", Field: FieldArray, Value: ValueRequired, Class: ClassControlled},
	{Long: "batch", Field: FieldNodeSelection, Value: ValueRequired, Class: ClassControlled},
	{Long: "bb", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "bbf", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "begin", Short: "b", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "chdir", Short: "D", Field: FieldIOPaths, Value: ValueRequired, Class: ClassControlled},
	{Long: "cluster-constraint", Field: FieldCluster, Value: ValueRequired, Class: ClassControlled},
	{Long: "clusters", Short: "M", Field: FieldCluster, Value: ValueRequired, Class: ClassControlled},
	{Long: "comment", Field: FieldNaming, Value: ValueRequired, Class: ClassControlled},
	{Long: "consolidate-segments", Field: FieldTopology, Value: ValueNone, Class: ClassControlled},
	{Long: "constraint", Short: "C", Field: FieldConstraints, Value: ValueRequired, Class: ClassControlled},
	{Long: "container", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "container-id", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "container-type", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "contiguous", Field: FieldNodeSelection, Value: ValueNone, Class: ClassControlled},
	{Long: "core-spec", Short: "S", Field: FieldNodes, Value: ValueRequired, Class: ClassControlled},
	{Long: "cores-per-socket", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "cpu-freq", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "cpus-per-gpu", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "cpus-per-task", Short: "c", Field: FieldCPUsPerTask, Value: ValueRequired, Class: ClassControlled},
	{Long: "deadline", Field: FieldWalltime, Value: ValueRequired, Class: ClassControlled},
	{Long: "delay-boot", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "dependency", Short: "d", Field: FieldDependencies, Value: ValueRequired, Class: ClassControlled},
	{Long: "distribution", Short: "m", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "error", Short: "e", Field: FieldIOPaths, Value: ValueRequired, Class: ClassControlled},
	{Long: "exclude", Short: "x", Field: FieldNodeSelection, Value: ValueRequired, Class: ClassControlled},
	{Long: "exclusive", Field: FieldExclusive, Value: ValueOptional, Class: ClassControlled},
	{Long: "export", Field: FieldIdentityEnv, Value: ValueRequired, Class: ClassControlled},
	{Long: "export-file", Field: FieldIdentityEnv, Value: ValueRequired, Class: ClassControlled},
	{Long: "extra", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "extra-node-info", Short: "B", Field: FieldNodes, Value: ValueRequired, Class: ClassControlled},
	{Long: "get-user-env", Field: FieldIdentityEnv, Value: ValueNone, Class: ClassControlled},
	{Long: "gid", Field: FieldIdentityEnv, Value: ValueRequired, Class: ClassControlled},
	{Long: "gpu-bind", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "gpu-freq", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "gpus", Short: "G", Field: FieldGRES, Value: ValueRequired, Class: ClassControlled},
	{Long: "gpus-per-node", Field: FieldGRES, Value: ValueRequired, Class: ClassControlled},
	{Long: "gpus-per-socket", Field: FieldGRES, Value: ValueRequired, Class: ClassControlled},
	{Long: "gpus-per-task", Field: FieldGRES, Value: ValueRequired, Class: ClassControlled},
	{Long: "gres", Field: FieldGRES, Value: ValueRequired, Class: ClassControlled},
	{Long: "gres-flags", Field: FieldGRES, Value: ValueRequired, Class: ClassControlled},
	{Long: "help", Short: "h", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "hint", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "hold", Short: "H", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "ignore-pbs", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "input", Short: "i", Field: FieldIOPaths, Value: ValueRequired, Class: ClassControlled},
	{Long: "job-name", Short: "J", Field: FieldNaming, Value: ValueRequired, Class: ClassControlled},
	{Long: "kill-on-invalid-dep", Field: FieldDependencies, Value: ValueRequired, Class: ClassControlled},
	{Long: "licenses", Short: "L", Field: FieldLicenses, Value: ValueRequired, Class: ClassControlled},
	{Long: "mail-type", Field: FieldMail, Value: ValueRequired, Class: ClassControlled},
	{Long: "mail-user", Field: FieldMail, Value: ValueRequired, Class: ClassControlled},
	{Long: "mcs-label", Field: FieldIdentityEnv, Value: ValueRequired, Class: ClassControlled},
	{Long: "mem", Field: FieldMemory, Value: ValueRequired, Class: ClassControlled},
	{Long: "mem-bind", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "mem-per-cpu", Field: FieldMemory, Value: ValueRequired, Class: ClassControlled},
	{Long: "mem-per-gpu", Field: FieldMemory, Value: ValueRequired, Class: ClassControlled},
	{Long: "mem-update", Field: FieldMemory, Value: ValueRequired, Class: ClassControlled},
	{Long: "mincpus", Field: FieldCPUsPerTask, Value: ValueRequired, Class: ClassControlled},
	{Long: "network", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "nice", Field: FieldPriority, Value: ValueRequired, Class: ClassControlled},
	{Long: "no-kill", Short: "k", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "no-requeue", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "nodefile", Short: "F", Field: FieldNodeSelection, Value: ValueRequired, Class: ClassControlled},
	{Long: "nodelist", Short: "w", Field: FieldNodeSelection, Value: ValueRequired, Class: ClassControlled},
	{Long: "nodes", Short: "N", Field: FieldNodes, Value: ValueRequired, Class: ClassControlled},
	{Long: "ntasks", Short: "n", Field: FieldTasks, Value: ValueRequired, Class: ClassControlled},
	{Long: "ntasks-per-core", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "ntasks-per-gpu", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "ntasks-per-node", Field: FieldTasksPerNode, Value: ValueRequired, Class: ClassControlled},
	{Long: "ntasks-per-socket", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "oom-kill-step", Field: FieldBehavior, Value: ValueOptional, Class: ClassControlled},
	{Long: "open-mode", Field: FieldIOPaths, Value: ValueRequired, Class: ClassControlled},
	{Long: "output", Short: "o", Field: FieldIOPaths, Value: ValueRequired, Class: ClassControlled},
	{Long: "overcommit", Short: "O", Field: FieldExclusive, Value: ValueNone, Class: ClassControlled},
	{Long: "oversubscribe", Short: "s", Field: FieldExclusive, Value: ValueNone, Class: ClassControlled},
	{Long: "parsable", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "partition", Short: "p", Field: FieldPartition, Value: ValueRequired, Class: ClassControlled},
	{Long: "power", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "prefer", Field: FieldConstraints, Value: ValueRequired, Class: ClassControlled},
	{Long: "priority", Field: FieldPriority, Value: ValueRequired, Class: ClassControlled},
	{Long: "profile", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "propagate", Field: FieldIdentityEnv, Value: ValueOptional, Class: ClassControlled},
	{Long: "qos", Short: "q", Field: FieldQoS, Value: ValueRequired, Class: ClassControlled},
	{Long: "quiet", Short: "Q", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "reboot", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "requeue", Field: FieldBehavior, Value: ValueOptional, Class: ClassControlled},
	{Long: "reservation", Field: FieldReservation, Value: ValueRequired, Class: ClassControlled},
	{Long: "resources", Field: FieldNodes, Value: ValueRequired, Class: ClassControlled},
	{Long: "resv-ports", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "segment", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "signal", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "sockets-per-node", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "spread-job", Field: FieldTopology, Value: ValueNone, Class: ClassControlled},
	{Long: "spread-segments", Field: FieldTopology, Value: ValueNone, Class: ClassControlled},
	{Long: "stepmgr", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "switches", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "test-only", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "thread-spec", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "threads-per-core", Field: FieldTopology, Value: ValueRequired, Class: ClassControlled},
	{Long: "time", Short: "t", Field: FieldWalltime, Value: ValueRequired, Class: ClassControlled},
	{Long: "time-min", Field: FieldWalltime, Value: ValueRequired, Class: ClassControlled},
	{Long: "tmp", Field: FieldMemory, Value: ValueRequired, Class: ClassControlled},
	{Long: "tres-bind", Field: FieldGRES, Value: ValueRequired, Class: ClassControlled},
	{Long: "tres-per-task", Field: FieldGRES, Value: ValueRequired, Class: ClassControlled},
	{Long: "uid", Field: FieldIdentityEnv, Value: ValueRequired, Class: ClassControlled},
	{Long: "usage", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "use-min-nodes", Field: FieldNodes, Value: ValueNone, Class: ClassControlled},
	{Long: "verbose", Short: "v", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "version", Short: "V", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "wait", Short: "W", Field: FieldBehavior, Value: ValueNone, Class: ClassControlled},
	{Long: "wait-all-nodes", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
	{Long: "wckey", Field: FieldIdentityEnv, Value: ValueRequired, Class: ClassControlled},
	{Long: "wrap", Field: FieldBehavior, Value: ValueRequired, Class: ClassControlled},
}

// byLong indexes long option names.
var byLong = func() map[string]*Option {
	m := make(map[string]*Option, len(Options))
	for i := range Options {
		m[Options[i].Long] = &Options[i]
	}
	return m
}()

// byShort indexes short option letters.
var byShort = func() map[byte]*Option {
	m := make(map[byte]*Option, len(Options))
	for i := range Options {
		if Options[i].Short != "" {
			m[Options[i].Short[0]] = &Options[i]
		}
	}
	return m
}()

// LookupLong resolves a long option name, including unambiguous
// getopt_long-style prefix abbreviations ("--parti" → "partition").
// The second return value reports whether an abbreviation matched.
func LookupLong(name string) (*Option, bool) {
	if o, ok := byLong[name]; ok {
		return o, false
	}
	var match *Option
	count := 0
	for i := range Options {
		if len(Options[i].Long) > len(name) && Options[i].Long[:len(name)] == name {
			match = &Options[i]
			count++
		}
	}
	if count == 1 {
		return match, true
	}
	return nil, false
}

// LookupShort resolves a short option letter.
func LookupShort(c byte) *Option { return byShort[c] }
