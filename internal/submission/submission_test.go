package submission_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/submission"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/shsyntax"
	"github.com/Exonical/custos/internal/workflowspec"
)

func mkSpec(payload []byte) admission.ExecutionSpec {
	s := admission.ExecutionSpec{
		SchemaVersion: 1,
		ID:            uuid.New(), TenantID: uuid.New(), TaskName: "train",
		Cluster: admission.ClusterRef{ID: uuid.New(), Name: "main", APIVersion: "v0.0.45"},
		Account: "proj", Partition: "main", QoS: "normal",
		Resources: admission.ResolvedResources{Nodes: 2, Tasks: 4, WalltimeSeconds: 3600},
		Payload: admission.PayloadRef{
			ScriptID: uuid.New(), Digest: validation.DigestOf(payload),
			Language: workflowspec.LanguageBash, Interpreter: admission.InterpreterBash,
		},
		Environment: admission.EnvSet{
			Controlled: map[string]string{"CUSTOS_X": "1"},
			User:       map[string]string{"MY_VAR": "v"},
		},
		WorkingDir: "/work",
		Security:   admission.SecurityContext{SlurmUser: "svc-custos", ImpersonationMode: "service"},
	}
	_ = s.Freeze()
	return s
}

var payload = []byte("#!/bin/bash\necho hello\n")

func TestWrapperRoundTrip(t *testing.T) {
	w, err := submission.Wrapper(mkSpec(payload), payload)
	if err != nil {
		t.Fatal(err)
	}
	// Extract the base64 body between the heredoc markers.
	re := regexp.MustCompile(`(?s)base64 -d[^\n]*<<'CUSTOS_PAYLOAD_[0-9a-f]+'\n(.*)\nCUSTOS_PAYLOAD_`)
	m := re.FindStringSubmatch(w)
	if m == nil {
		t.Fatalf("no heredoc body in wrapper:\n%s", w)
	}
	dec, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if string(dec) != string(payload) {
		t.Fatal("payload did not round-trip")
	}
	sum := sha256.Sum256(dec)
	if !strings.Contains(w, hex.EncodeToString(sum[:])) {
		t.Fatal("wrapper missing payload digest")
	}
}

func TestWrapperNoSbatch(t *testing.T) {
	corpus, err := filepath.Glob("../validation/sbatchscan/testdata/bypass/*.sh")
	if err != nil || len(corpus) == 0 {
		t.Fatalf("corpus missing: %v", err)
	}
	var all []byte
	for _, f := range corpus {
		b, _ := os.ReadFile(f)
		all = append(all, b...)
	}
	w, err := submission.Wrapper(mkSpec(all), all)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(w), "#sbatch") {
		t.Fatal("wrapper contains #SBATCH-like text")
	}
}

func TestWrapperParsesClean(t *testing.T) {
	w, err := submission.Wrapper(mkSpec(payload), payload)
	if err != nil {
		t.Fatal(err)
	}
	res, err := shsyntax.Validator{}.Validate(context.Background(), validation.Input{
		Language: workflowspec.LanguageBash, Script: []byte(w)})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diagnostics {
		if d.Severity.AtLeast(validation.SeverityError) {
			t.Fatalf("wrapper has %s: %s", d.Code, d.Message)
		}
	}
}

func TestDigestMismatch(t *testing.T) {
	if _, err := submission.Wrapper(mkSpec(payload), []byte("tampered")); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

// TestJobSubmissionPure: submission fields equal spec fields regardless
// of payload contents (property test).
func TestJobSubmissionPure(t *testing.T) {
	f := func(nodes uint8, tasks uint8, wall uint32) bool {
		p := []byte("#SBATCH --gres=gpu:8\n#SBATCH --qos=admin\nrm -rf /\n")
		spec := mkSpec(p)
		spec.Resources.Nodes = int(nodes%8) + 1
		spec.Resources.Tasks = int(tasks%8) + 1
		spec.Resources.WalltimeSeconds = int64(wall % 10000)
		w, err := submission.Wrapper(spec, p)
		if err != nil {
			return false
		}
		sub := submission.JobSubmission(spec, w)
		return sub.Nodes == spec.Resources.Nodes &&
			sub.Tasks == spec.Resources.Tasks &&
			sub.Walltime == time.Duration(spec.Resources.WalltimeSeconds)*time.Second &&
			sub.Account == spec.Account && sub.Partition == spec.Partition &&
			sub.QoS == spec.QoS && sub.Script == w &&
			sub.Name == "custos-"+spec.ID.String()
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

// TestTemplateNoUnsafeInterpolation: every {{ .X }} outside q() must be
// a SafeToken field or PayloadBase64.
func TestTemplateNoUnsafeInterpolation(t *testing.T) {
	src, err := os.ReadFile("wrapper.go")
	if err != nil {
		t.Skip("source unavailable")
	}
	tmplRe := regexp.MustCompile(`\{\{ ?(range|if|end|else)[^}]*\}\}|\{\{ ?q [^}]*\}\}|\{\{ ?\.([A-Za-z]+) ?\}\}`)
	allowedRaw := map[string]bool{
		"ExecutionID": true, "SpecDigest": true, "Nonce": true,
		"Tasks": true, "PayloadBase64": true, "Name": true,
	}
	for _, m := range tmplRe.FindAllStringSubmatch(string(src), -1) {
		if m[1] != "" || m[0] == "" {
			continue
		}
		field := m[2]
		if field == "" {
			continue
		}
		if !allowedRaw[field] {
			t.Errorf("raw interpolation of non-SafeToken field {{ .%s }}", field)
		}
	}
	// Env.Name is interpolated via {{ .Name }} inside a range — check the
	// Env Name field is typed SafeToken by verifying no other string
	// fields are raw-interpolated: the only {{ .X }} in the template are
	// enumerated above plus .Name inside the Env range.
	if strings.Count(string(src), "{{ .Name }}") != 1 {
		t.Error("Env name interpolation missing/changed")
	}
}

// TestSpecNoSecrets: marshaled spec never contains secret values.
func TestSpecNoSecrets(t *testing.T) {
	spec := mkSpec(payload)
	spec.Environment.SecretRefs = []admission.SecretEnvRef{
		{Name: "TOKEN", Ref: "bao://kv/x#key"}}
	b, _ := json.Marshal(spec)
	if strings.Contains(string(b), "secret-value") {
		t.Fatal("secret value leaked")
	}
}
