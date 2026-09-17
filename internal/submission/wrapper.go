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

// wrapperTemplate is the documented v1 wrapper, verbatim.
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
{{ end }}{{ range .Env }}export {{ .Name }}={{ q .Value }}
{{ end }}
# --- payload (base64, verified) ---
base64 -d > "$CUSTOS_JOB_DIR/payload" <<'CUSTOS_PAYLOAD_{{ .Nonce }}'
{{ .PayloadBase64 }}
CUSTOS_PAYLOAD_{{ .Nonce }}
echo {{ q .PayloadDigest }}"  $CUSTOS_JOB_DIR/payload" | sha256sum -c --quiet
chmod 0500 "$CUSTOS_JOB_DIR/payload"
cd {{ q .WorkingDir }}
{{ if .MPI }}exec srun --ntasks={{ .Tasks }} {{ q .Interpreter }} "$CUSTOS_JOB_DIR/payload" {{ range .Args }}{{ q . }} {{ end }}
{{ else }}exec {{ q .Interpreter }} "$CUSTOS_JOB_DIR/payload" {{ range .Args }}{{ q . }} {{ end }}{{ end }}
`

type envKV struct {
	Name  SafeToken
	Value string
}

type wrapperData struct {
	ExecutionID   SafeToken
	SpecDigest    SafeToken
	TaskName      string
	Modules       []string
	Env           []envKV
	Nonce         SafeToken
	PayloadBase64 string
	PayloadDigest SafeToken
	WorkingDir    string
	MPI           bool
	Tasks         SafeToken
	Interpreter   string
	Args          []string
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

// Wrapper renders the wrapper script for spec embedding payload
// (base64). Verifies sha256(payload) == spec.Payload.Digest first — the
// admission↔bytes integrity leg.
func Wrapper(spec admission.ExecutionSpec, payload []byte) (string, error) {
	if validation.DigestOf(payload) != spec.Payload.Digest {
		return "", apperr.New(apperr.Internal, "INTERNAL",
			"submission.digest_mismatch: payload does not match spec digest")
	}
	body := base64.StdEncoding.EncodeToString(payload)
	nonce, err := newNonce(body)
	if err != nil {
		return "", err
	}
	env := make([]envKV, 0, len(spec.Environment.Controlled)+len(spec.Environment.User))
	for n, v := range spec.Environment.Controlled {
		env = append(env, envKV{safeToken(n), v})
	}
	for n, v := range spec.Environment.User {
		env = append(env, envKV{safeToken(n), v})
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
		PayloadBase64: body,
		PayloadDigest: safeToken(hex.EncodeToString(spec.Payload.Digest[:])),
		WorkingDir:    spec.WorkingDir,
		MPI:           spec.Resources.Tasks > 1,
		Tasks:         safeToken(fmt.Sprint(spec.Resources.Tasks)),
		Interpreter:   string(spec.Payload.Interpreter),
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
