// Package submission generates the trusted wrapper script and maps an
// ExecutionSpec to slurm.JobSubmission. The wrapper template is fixed;
// `q` (single-quote escaping) is the only interpolation function and
// the payload is never interpolated as text.
package submission

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// SafeToken is a value proven safe for unquoted shell interpolation by
// regex (env names, digests, nonces, interpreter paths).
type SafeToken string

var safeTokenRe = regexp.MustCompile(`^[A-Za-z0-9_.:@/=-]+$`)

func safeToken(s string) (SafeToken, error) {
	if !safeTokenRe.MatchString(s) {
		return "", apperr.New(apperr.Internal, "INTERNAL",
			"submission: unsafe token "+strconv.Quote(s))
	}
	return SafeToken(s), nil
}

// q single-quotes a value for shell ('a'\”b' style).
func q(v any) string {
	return "'" + strings.ReplaceAll(fmt.Sprint(v), "'", `'\''`) + "'"
}

// wrapperTemplate is the generated v1 wrapper. User values are either
// single-quoted with q or carried as allow-listed runtime references;
// ContainerWords are pre-quoted strings built by Custos.
const wrapperTemplate = `#!/bin/bash
# custos wrapper v1 — generated; do not edit
# execution: {{ .ExecutionID }} digest: {{ .SpecDigest }}
set -euo pipefail
umask 077
export CUSTOS_EXECUTION_ID={{ q .ExecutionID }}
export CUSTOS_TASK={{ q .TaskName }}
export CUSTOS_JOB_DIR="${SLURM_TMPDIR:-${TMPDIR:-/tmp}}/custos-${SLURM_JOB_ID}"
mkdir -p "$CUSTOS_JOB_DIR"
trap 'rm -rf "$CUSTOS_JOB_DIR"' EXIT
{{ range .Modules }}module load {{ q . }}
{{ end }}{{ if .GenericMultinode }}
CUSTOS_HOSTS=$(scontrol show hostnames "$SLURM_JOB_NODELIST")
export MULTINODE_HOSTLIST_NOSLOTS=$(printf '%s\n' $CUSTOS_HOSTS | paste -sd, -)
export MULTINODE_HOSTLIST=$(printf '%s:{{ .SlotsPerNode }}\n' $CUSTOS_HOSTS | paste -sd, -)
export MULTINODE_TOTAL_SLOTS={{ .TotalSlots }}
export MULTINODE_NODE_IP=$( { getent hosts "$(hostname)" || true; } | awk '{print $1; exit}')
cat > "$CUSTOS_JOB_DIR/rsh" <<'CUSTOS_RSH_{{ .Nonce }}'
#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -*) shift ;; *) break ;; esac; done
host=$1; shift
exec srun --overlap --nodes=1 --ntasks=1 --nodelist="$host" {{ range .RshContainerWords }}{{ . }} {{ end }}{{ range .RshEnvPrefix }}{{ . }} {{ end }}/bin/sh -c "$*"
CUSTOS_RSH_{{ .Nonce }}
chmod 0500 "$CUSTOS_JOB_DIR/rsh"
export MULTINODE_SSH_WRAPPER="$CUSTOS_JOB_DIR/rsh" MULTINODE_RSH_WRAPPER="$CUSTOS_JOB_DIR/rsh"{{ end }}
{{ range .Env }}{{ if .Runtime }}export {{ .Name }}={{ .Quoted }}
{{ else }}export {{ .Name }}={{ q .Value }}
{{ end }}{{ end }}{{ if .SlurmCPUBind }}export SLURM_CPU_BIND={{ q .SlurmCPUBind }}
{{ end }}{{ if .HasPayload }}
# --- payload (base64, verified) ---
base64 -d > "$CUSTOS_JOB_DIR/payload.in" <<'CUSTOS_PAYLOAD_{{ .Nonce }}'
{{ .PayloadBase64 }}
CUSTOS_PAYLOAD_{{ .Nonce }}
echo {{ q .PayloadDigest }}"  $CUSTOS_JOB_DIR/payload.in" | sha256sum -c --quiet
{{ if .SrunLaunch }}chmod 0500 "$CUSTOS_JOB_DIR/payload.in"
srun --ntasks="$SLURM_JOB_NUM_NODES" --ntasks-per-node=1 mkdir -p -m 0700 "$CUSTOS_JOB_DIR"
sbcast --force --preserve "$CUSTOS_JOB_DIR/payload.in" "$CUSTOS_JOB_DIR/payload"{{ else }}mv "$CUSTOS_JOB_DIR/payload.in" "$CUSTOS_JOB_DIR/payload"
chmod 0500 "$CUSTOS_JOB_DIR/payload"{{ end }}{{ end }}
cd {{ q .WorkingDir }}
{{ range .LaunchWords }}{{ . }} {{ end }}{{ range .ContainerWords }}{{ . }} {{ end }}{{ range .EnvPrefix }}{{ . }} {{ end }}{{ if .HasPayload }}{{ q .Interpreter }} "$CUSTOS_JOB_DIR/payload"{{ end }}{{ range $i, $a := .Argv }}{{ if or $i $.HasPayload }} {{ end }}{{ $a }}{{ end }}
`

type envKV struct {
	Name    SafeToken
	Value   string
	Quoted  SafeToken // Runtime only: pre-quoted "$VAR", emit without q
	Runtime bool
}

type wrapperData struct {
	ExecutionID       SafeToken
	SpecDigest        SafeToken
	TaskName          string
	Modules           []string
	Env               []envKV
	Nonce             SafeToken
	HasPayload        bool
	PayloadBase64     string
	PayloadDigest     SafeToken
	WorkingDir        string
	LaunchWords       []SafeToken
	ContainerWords    []string // pre-quoted shell words built by Custos
	EnvPrefix         []string
	RshContainerWords []string
	RshEnvPrefix      []string
	SlotsPerNode      SafeToken
	TotalSlots        SafeToken
	SrunLaunch        bool
	GenericMultinode  bool
	SlurmCPUBind      string
	Interpreter       string
	Argv              []string // pre-quoted: 'literal' or "$VAR"
}

var tmpl = template.Must(template.New("wrapper").
	Funcs(template.FuncMap{"q": q}).Parse(wrapperTemplate))

// newNonce returns a hex nonce not occurring in body.
func newNonce(body string) (string, error) {
	for range 8 {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		n := hex.EncodeToString(b)
		if !strings.Contains(body, "CUSTOS_PAYLOAD_"+n) &&
			!strings.Contains(body, "CUSTOS_RSH_"+n) {
			return n, nil
		}
	}
	return "", apperr.New(apperr.Internal, "INTERNAL",
		"submission: cannot mint a collision-free nonce")
}

// renderArgv renders each classified element: literals single-quoted,
// runtime vars as "$VAR" — both Custos-authored shell, never raw text.
func renderArgv(spec admission.ExecutionSpec) ([]string, error) {
	out := make([]string, 0, len(spec.Argv))
	for _, e := range spec.Argv {
		switch {
		case e.Runtime != "" && e.Literal == "":
			if !admission.ValidRuntimeFor(e.Runtime, spec.Multinode != nil && spec.Multinode.Implementation == "generic") {
				return nil, apperr.New(apperr.Internal, "INTERNAL",
					"submission: unlisted runtime "+e.Runtime)
			}
			out = append(out, `"$`+e.Runtime+`"`)
		case e.Literal != "" && e.Runtime == "":
			out = append(out, q(e.Literal))
		default:
			return nil, apperr.New(apperr.Internal, "INTERNAL",
				"submission: argv element sets both or neither of literal/runtime")
		}
	}
	return out, nil
}

// launchWords composes the trusted srun prefix. Empty Launch is reserved
// for older frozen specs; pyxis requires an srun even for sbatch payloads.
func launchWords(spec admission.ExecutionSpec) ([]SafeToken, bool, error) {
	useSrun := false
	switch spec.Launch {
	case workflowspec.LaunchSbatch:
	case workflowspec.LaunchSrun:
		useSrun = true
	case "":
		useSrun = spec.Resources.Tasks > 1
	default:
		return nil, false, apperr.New(apperr.Internal, "INTERNAL", "submission: invalid frozen launch mode")
	}
	pyxisSingle := spec.Container != nil && spec.Container.Runtime == "pyxis" && !useSrun
	if pyxisSingle {
		useSrun = true
	}
	if !useSrun {
		return nil, false, nil
	}
	words := []string{"srun"}
	if pyxisSingle {
		words = append(words, "--nodes=1", "--ntasks=1")
	}
	if spec.Multinode != nil &&
		(spec.Multinode.Implementation == "openmpi" || spec.Multinode.Implementation == "mpich") &&
		spec.Multinode.MPIPlugin != "" {
		words = append(words, "--mpi="+spec.Multinode.MPIPlugin)
	}
	if spec.CPUBind != "" && spec.CPUBind != "none" {
		words = append(words, "--cpu-bind="+spec.CPUBind)
	}
	if spec.Multinode == nil && !pyxisSingle && spec.Resources.Tasks > 0 {
		words = append(words, fmt.Sprintf("--ntasks=%d", spec.Resources.Tasks))
	}
	out := make([]SafeToken, 0, len(words))
	for _, word := range words {
		safe, err := safeToken(word)
		if err != nil {
			return nil, false, err
		}
		out = append(out, safe)
	}
	return out, useSrun, nil
}

func containerEnvValues(spec admission.ExecutionSpec) map[string]string {
	if spec.ContainerEnv != nil {
		return spec.ContainerEnv
	}
	return spec.Environment.User
}

func containerEnvNames(spec admission.ExecutionSpec) ([]string, error) {
	set := map[string]bool{}
	for name := range spec.Environment.Runtime {
		if _, err := safeToken(name); err != nil {
			return nil, err
		}
		set[name] = true
	}
	for _, ref := range spec.Environment.SecretRefs {
		if _, err := safeToken(ref.Name); err != nil {
			return nil, err
		}
		set[ref.Name] = true
	}
	if spec.Multinode != nil && spec.Multinode.Implementation == "generic" {
		for _, name := range []string{
			admission.RuntimeMultinodeHostlist,
			admission.RuntimeMultinodeHostlistNoSlots,
			admission.RuntimeMultinodeTotalSlots,
			admission.RuntimeMultinodeNodeIP,
			admission.RuntimeMultinodeSSHWrapper,
			admission.RuntimeMultinodeRSHWrapper,
		} {
			set[name] = true
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func apptainerWords(container *admission.ContainerSpec) []string {
	binary := container.Binary
	if binary == "" {
		binary = "apptainer"
	}
	return []string{q(binary), q("exec"), q("--no-eval"), q("--bind"), `"$CUSTOS_JOB_DIR"`, q(container.Image)}
}

func pyxisWords(spec admission.ExecutionSpec) ([]string, error) {
	words := []string{"--container-image=" + q(spec.Container.Image),
		`--container-mounts="$CUSTOS_JOB_DIR:$CUSTOS_JOB_DIR"`}
	names, err := containerEnvNames(spec)
	if err != nil {
		return nil, err
	}
	if len(names) > 0 {
		words = append(words, "--container-env="+q(strings.Join(names, ",")))
	}
	return words, nil
}

func containerEnvPrefix(spec admission.ExecutionSpec) ([]string, error) {
	values := containerEnvValues(spec)
	if len(values) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []string{q("/usr/bin/env")}
	for _, name := range names {
		if _, err := safeToken(name); err != nil {
			return nil, err
		}
		out = append(out, q(name+"="+values[name]))
	}
	return out, nil
}

func containerWords(spec admission.ExecutionSpec) (words, envPrefix, rshWords, rshEnvPrefix []string, err error) {
	if spec.Container == nil {
		return nil, nil, nil, nil, nil
	}
	switch spec.Container.Runtime {
	case "apptainer":
		words = apptainerWords(spec.Container)
		rshWords = append([]string(nil), words...)
		envPrefix, err = containerEnvPrefix(spec)
		if err == nil {
			rshEnvPrefix = append([]string(nil), envPrefix...)
		}
	case "pyxis":
		words, err = pyxisWords(spec)
		if err == nil {
			rshWords = append([]string(nil), words...)
			envPrefix, err = containerEnvPrefix(spec)
			if err == nil {
				rshEnvPrefix = append([]string(nil), envPrefix...)
			}
		}
	default:
		err = apperr.New(apperr.Internal, "INTERNAL", "submission: unsupported container runtime")
	}
	return words, envPrefix, rshWords, rshEnvPrefix, err
}

// Wrapper renders the wrapper script for spec. Script tasks embed the
// payload (base64) after verifying sha256(payload) ==
// spec.Payload.Digest — the admission↔bytes integrity leg. Command
// tasks (zero payload digest) take nil payload and exec Argv directly.
func Wrapper(spec admission.ExecutionSpec, payload []byte) (string, error) {
	hasPayload := spec.Payload.Digest != validation.Digest{}
	if hasPayload && validation.DigestOf(payload) != spec.Payload.Digest {
		return "", apperr.New(apperr.Internal, "INTERNAL",
			"submission.digest_mismatch: payload does not match spec digest")
	}
	if !hasPayload && len(payload) != 0 {
		return "", apperr.New(apperr.Internal, "INTERNAL",
			"submission: payload bytes for a payload-less spec")
	}
	body := base64.StdEncoding.EncodeToString(payload)
	nonce, err := newNonce(body)
	if err != nil {
		return "", err
	}
	argv, err := renderArgv(spec)
	if err != nil {
		return "", err
	}
	launcherWords, useSrun, err := launchWords(spec)
	if err != nil {
		return "", err
	}
	containerPrefix, envPrefix, rshPrefix, rshEnvPrefix, err := containerWords(spec)
	if err != nil {
		return "", err
	}
	varsToExport := []map[string]string{spec.Environment.Controlled}
	if spec.Container == nil {
		varsToExport = append(varsToExport, spec.Environment.User)
	}
	env := make([]envKV, 0, len(spec.Environment.Controlled)+
		len(spec.Environment.User)+len(spec.Environment.Runtime))
	for _, vars := range varsToExport {
		for n, v := range vars {
			name, err := safeToken(n)
			if err != nil {
				return "", err
			}
			env = append(env, envKV{Name: name, Value: v})
		}
	}
	for n, rt := range spec.Environment.Runtime {
		if !admission.ValidRuntimeFor(rt, spec.Multinode != nil && spec.Multinode.Implementation == "generic") {
			return "", apperr.New(apperr.Internal, "INTERNAL",
				"submission: unlisted runtime env "+n)
		}
		name, err := safeToken(n)
		if err != nil {
			return "", err
		}
		env = append(env, envKV{Name: name,
			Quoted: SafeToken(`"$` + rt + `"`), Runtime: true})
	}
	var modules []string
	for _, s := range spec.Software {
		modules = append(modules, s.ModuleSpec...)
	}
	genericMultinode := spec.Multinode != nil && spec.Multinode.Implementation == "generic"
	var slotsPerNode, totalSlots SafeToken
	if genericMultinode {
		if spec.Multinode.Nodes < 1 || spec.Multinode.SlotsPerNode < 1 {
			return "", apperr.New(apperr.Internal, "INTERNAL",
				"submission: invalid frozen multinode dimensions")
		}
		maxInt := int(^uint(0) >> 1)
		if spec.Multinode.Nodes > maxInt/spec.Multinode.SlotsPerNode {
			return "", apperr.New(apperr.Internal, "INTERNAL",
				"submission: frozen multinode slot count overflows")
		}
		slotsPerNode, err = safeToken(strconv.Itoa(spec.Multinode.SlotsPerNode))
		if err != nil {
			return "", err
		}
		totalSlots, err = safeToken(strconv.Itoa(spec.Multinode.Nodes * spec.Multinode.SlotsPerNode))
		if err != nil {
			return "", err
		}
	}
	cpuBind := ""
	if !useSrun && spec.CPUBind != "" && spec.CPUBind != "none" {
		if spec.CPUBind != "cores" && spec.CPUBind != "sockets" && spec.CPUBind != "ldoms" {
			return "", apperr.New(apperr.Internal, "INTERNAL", "submission: invalid frozen cpu bind")
		}
		cpuBind = spec.CPUBind
	}
	d := wrapperData{
		TaskName:          spec.TaskName,
		Modules:           modules,
		Env:               env,
		Nonce:             SafeToken(nonce),
		HasPayload:        hasPayload,
		PayloadBase64:     body,
		WorkingDir:        spec.WorkingDir,
		LaunchWords:       launcherWords,
		ContainerWords:    containerPrefix,
		EnvPrefix:         envPrefix,
		RshContainerWords: rshPrefix,
		RshEnvPrefix:      rshEnvPrefix,
		SlotsPerNode:      slotsPerNode,
		TotalSlots:        totalSlots,
		SrunLaunch:        useSrun,
		GenericMultinode:  genericMultinode,
		SlurmCPUBind:      cpuBind,
		Interpreter:       string(spec.Payload.Interpreter),
		Argv:              argv,
	}
	for _, t := range []struct {
		dst *SafeToken
		src string
	}{
		{&d.ExecutionID, spec.ID.String()},
		{&d.SpecDigest, hex.EncodeToString(spec.Digest[:])},
		{&d.PayloadDigest, hex.EncodeToString(spec.Payload.Digest[:])},
	} {
		if *t.dst, err = safeToken(t.src); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, d); err != nil {
		return "", fmt.Errorf("submission: template: %w", err)
	}
	return b.String(), nil
}

// JobSubmission maps an ExecutionSpec + wrapper to the neutral Slurm
// submission. Pure: every field comes from the spec, never payload text.
// SecretRefs appear only as [SECRET] preview markers; the worker replaces them
// immediately before SubmitJob.
func JobSubmission(spec admission.ExecutionSpec, wrapper string) slurm.JobSubmission {
	env := make(map[string]string,
		len(spec.Environment.Controlled)+len(spec.Environment.User))
	for n, v := range spec.Environment.Controlled {
		env[n] = v
	}
	if spec.Container == nil {
		for n, v := range spec.Environment.User {
			env[n] = v
		}
	}
	for _, ref := range spec.Environment.SecretRefs {
		env[ref.Name] = "[SECRET]"
	}
	sub := slurm.JobSubmission{
		Name:        "custos-" + spec.ID.String(),
		Comment:     "custos:" + spec.ID.String() + "/" + spec.TaskName,
		Account:     spec.Account,
		Partition:   spec.Partition,
		QoS:         spec.QoS,
		Reservation: spec.Reservation,
		Script:      wrapper,
		WorkingDir:  spec.WorkingDir,
		Environment: env,
		Stdout:      spec.Stdout,
		Stderr:      spec.Stderr,
		UserName:    spec.Security.SlurmUser,

		Nodes:            spec.Resources.Nodes,
		Tasks:            spec.Resources.Tasks,
		TasksPerNode:     spec.Resources.TasksPerNode,
		CPUsPerTask:      spec.Resources.CPUsPerTask,
		MemoryPerNodeMiB: spec.Resources.MemoryPerNodeMiB,
		MemoryPerCPUMiB:  spec.Resources.MemoryPerCPUMiB,
		Constraints:      spec.Resources.Constraints,
		Licenses:         spec.Resources.Licenses,
		Walltime:         time.Duration(spec.Resources.WalltimeSeconds) * time.Second,
	}
	if spec.Resources.GPUCount > 0 {
		sub.GRES = []slurm.GRESRequest{{
			Name:  "gpu",
			Type:  spec.Resources.GPUType,
			Count: int64(spec.Resources.GPUCount),
		}}
	}
	if a := spec.Resources.Array; a != nil {
		sub.Array = &slurm.ArraySpec{
			Start: a.Start, End: a.End, Step: a.Step,
			MaxConcurrent: a.MaxConcurrent,
		}
	}
	return sub
}
