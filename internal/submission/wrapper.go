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
	"strings"
	"text/template"
	"time"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/validation"
)

// SafeToken is a value proven safe for unquoted shell interpolation by
// regex (env names, digests, nonces, interpreter paths).
type SafeToken string

var safeTokenRe = regexp.MustCompile(`^[A-Za-z0-9_.:@/-]+$`)

func safeToken(s string) SafeToken {
	if !safeTokenRe.MatchString(s) {
		panic("submission: unsafe token " + s) // admission-validated; panic = defect
	}
	return SafeToken(s)
}

// q single-quotes a value for shell ('a'\”b' style).
func q(v any) string {
	return "'" + strings.ReplaceAll(fmt.Sprint(v), "'", `'\''`) + "'"
}

// wrapperTemplate is the documented v1 wrapper, verbatim. Argv
// elements and runtime env values arrive pre-quoted ('literal' or
// "$VAR") — the template never interpolates raw user text.
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
{{ end }}{{ range .Env }}{{ if .Runtime }}export {{ .Name }}={{ .Quoted }}
{{ else }}export {{ .Name }}={{ q .Value }}
{{ end }}{{ end }}{{ if .HasPayload }}
# --- payload (base64, verified) ---
base64 -d > "$CUSTOS_JOB_DIR/payload" <<'CUSTOS_PAYLOAD_{{ .Nonce }}'
{{ .PayloadBase64 }}
CUSTOS_PAYLOAD_{{ .Nonce }}
echo {{ q .PayloadDigest }}"  $CUSTOS_JOB_DIR/payload" | sha256sum -c --quiet
chmod 0500 "$CUSTOS_JOB_DIR/payload"{{ end }}
cd {{ q .WorkingDir }}
{{ if .MPI }}exec srun --ntasks={{ .Tasks }} {{ end }}{{ if .HasPayload }}{{ q .Interpreter }} "$CUSTOS_JOB_DIR/payload"{{ end }}{{ range .Argv }} {{ . }}{{ end }}
`

type envKV struct {
	Name    SafeToken
	Value   string
	Quoted  SafeToken // Runtime only: pre-quoted "$VAR", emit without q
	Runtime bool
}

type wrapperData struct {
	ExecutionID   SafeToken
	SpecDigest    SafeToken
	TaskName      string
	Modules       []string
	Env           []envKV
	Nonce         SafeToken
	HasPayload    bool
	PayloadBase64 string
	PayloadDigest SafeToken
	WorkingDir    string
	MPI           bool
	Tasks         SafeToken
	Interpreter   string
	Argv          []string // pre-quoted: 'literal' or "$VAR"
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
		if !strings.Contains(body, "CUSTOS_PAYLOAD_"+n) {
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
			if !admission.ValidRuntime(e.Runtime) {
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
	env := make([]envKV, 0, len(spec.Environment.Controlled)+
		len(spec.Environment.User)+len(spec.Environment.Runtime))
	for n, v := range spec.Environment.Controlled {
		env = append(env, envKV{Name: safeToken(n), Value: v})
	}
	for n, v := range spec.Environment.User {
		env = append(env, envKV{Name: safeToken(n), Value: v})
	}
	for n, rt := range spec.Environment.Runtime {
		if !admission.ValidRuntime(rt) {
			return "", apperr.New(apperr.Internal, "INTERNAL",
				"submission: unlisted runtime env "+n)
		}
		env = append(env, envKV{Name: safeToken(n),
			Quoted: SafeToken(`"$` + rt + `"`), Runtime: true})
	}
	var modules []string
	for _, s := range spec.Software {
		modules = append(modules, s.ModuleSpec...)
	}
	d := wrapperData{
		ExecutionID:   safeToken(spec.ID.String()),
		SpecDigest:    safeToken(hex.EncodeToString(spec.Digest[:])),
		TaskName:      spec.TaskName,
		Modules:       modules,
		Env:           env,
		Nonce:         SafeToken(nonce),
		HasPayload:    hasPayload,
		PayloadBase64: body,
		PayloadDigest: safeToken(hex.EncodeToString(spec.Payload.Digest[:])),
		WorkingDir:    spec.WorkingDir,
		MPI:           spec.Resources.Tasks > 1,
		Tasks:         safeToken(fmt.Sprint(spec.Resources.Tasks)),
		Interpreter:   string(spec.Payload.Interpreter),
		Argv:          argv,
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, d); err != nil {
		return "", fmt.Errorf("submission: template: %w", err)
	}
	return b.String(), nil
}

// JobSubmission maps an ExecutionSpec + wrapper to the neutral Slurm
// submission. Pure: every field comes from the spec, never payload text.
// SecretRefs are resolved later (M6) and are not part of Environment.
func JobSubmission(spec admission.ExecutionSpec, wrapper string) slurm.JobSubmission {
	env := make(map[string]string,
		len(spec.Environment.Controlled)+len(spec.Environment.User))
	for n, v := range spec.Environment.Controlled {
		env[n] = v
	}
	for n, v := range spec.Environment.User {
		env[n] = v
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
