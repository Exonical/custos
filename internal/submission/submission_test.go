package submission_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/platform/apperr"
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

func TestWrapperLaunchModel(t *testing.T) {
	for _, tt := range []struct {
		name       string
		launch     string
		tasks      int
		wantSrun   bool
		wantNtasks bool
	}{
		{name: "explicit sbatch ignores task count", launch: workflowspec.LaunchSbatch, tasks: 40},
		{name: "explicit srun inherits allocation", launch: workflowspec.LaunchSrun, tasks: 0, wantSrun: true},
		{name: "legacy empty launch retains task-count behavior", tasks: 4, wantSrun: true, wantNtasks: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := mkSpec(nil)
			spec.Payload = admission.PayloadRef{}
			spec.Launch = tt.launch
			spec.Resources.Tasks = tt.tasks
			spec.Argv = []admission.ArgvElement{{Literal: "./program"}}
			wrapper, err := submission.Wrapper(spec, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.HasPrefix(lastLine(wrapper), "srun "); got != tt.wantSrun {
				t.Fatalf("srun present = %v, want %v:\n%s", got, tt.wantSrun, wrapper)
			}
			if got := strings.Contains(wrapper, "--ntasks=4"); got != tt.wantNtasks {
				t.Fatalf("--ntasks=4 present = %v, want %v:\n%s", got, tt.wantNtasks, wrapper)
			}
			if tt.launch == workflowspec.LaunchSrun && tt.tasks == 0 && strings.Contains(wrapper, "--ntasks=") {
				t.Fatalf("inherited srun allocation must not set --ntasks:\n%s", wrapper)
			}
		})
	}
}

func TestDigestMismatch(t *testing.T) {
	if _, err := submission.Wrapper(mkSpec(payload), []byte("tampered")); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestWrapperUnsafeEnvNameIsInternalError(t *testing.T) {
	for _, set := range []func(*admission.EnvSet){
		func(e *admission.EnvSet) { e.Controlled["BAD NAME"] = "1" },
		func(e *admission.EnvSet) { e.User["X;rm -rf /"] = "v" },
		func(e *admission.EnvSet) {
			e.Runtime = map[string]string{"$(id)": admission.RuntimeSlurmArrayTaskID}
		},
	} {
		spec := mkSpec(payload)
		set(&spec.Environment)
		_, err := submission.Wrapper(spec, payload)
		if !apperr.Is(err, apperr.Internal) {
			t.Fatalf("unsafe env name: err = %v, want apperr.Internal", err)
		}
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
		// Quoted is SafeToken: runtime env values arrive pre-quoted
		// ("$VAR") from an allow-listed variable name.
		"Quoted": true,
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
	// Env.Name is interpolated via {{ .Name }} inside the range — once
	// per branch (runtime vs literal).
	if strings.Count(string(src), "{{ .Name }}") != 2 {
		t.Error("Env name interpolation missing/changed")
	}
}

// TestArgvLiteralQuoting: an argv literal with hostile shell text
// appears only single-quoted in the exec line — the wrapper contains
// no unquoted user text.
func TestArgvLiteralQuoting(t *testing.T) {
	spec := mkSpec(nil)
	spec.Payload = admission.PayloadRef{} // command task
	spec.Argv = []admission.ArgvElement{
		{Literal: "/bin/echo"},
		{Literal: "x'; rm -rf / #"},
		{Runtime: admission.RuntimeSlurmArrayTaskID},
	}
	w, err := submission.Wrapper(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The q() escaper emits 'x'\''; rm -rf / #' — the literal survives
	// only inside single quotes.
	if !strings.Contains(w, `'x'\''; rm -rf / #'`) {
		t.Fatalf("hostile literal not single-quoted:\n%s", w)
	}
	if !strings.Contains(w, `"$SLURM_ARRAY_TASK_ID"`) {
		t.Fatal("runtime argv element missing")
	}
	// The whole wrapper must still parse cleanly.
	res, err := shsyntax.Validator{}.Validate(context.Background(),
		validation.Input{Language: workflowspec.LanguageBash,
			Script: []byte(w)})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diagnostics {
		if d.Severity.AtLeast(validation.SeverityError) {
			t.Fatalf("wrapper has %s: %s", d.Code, d.Message)
		}
	}
	launchLine := lastLine(w)
	if launchLine == "" {
		t.Fatal("no launch line in wrapper")
	}
	// Strip the srun prefix, every single-quoted word and the runtime
	// token — nothing but whitespace may remain.
	rest := strings.TrimPrefix(launchLine, "srun --ntasks=4 ")
	rest = regexp.MustCompile(`'[^']*'(\\''[^']*')*`).ReplaceAllString(rest, "")
	rest = strings.ReplaceAll(rest, `"$SLURM_ARRAY_TASK_ID"`, "")
	if strings.TrimSpace(rest) != "" {
		t.Fatalf("unquoted text on exec line: %q", rest)
	}
}

// TestArgvElementShape: ArgvElement is exactly {Literal, Runtime} —
// adding a third channel would bypass the quoting invariant.
func TestArgvElementShape(t *testing.T) {
	typ := reflect.TypeOf(admission.ArgvElement{})
	if typ.NumField() != 2 {
		t.Fatalf("ArgvElement has %d fields, want 2", typ.NumField())
	}
	names := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		names[typ.Field(i).Name] = true
	}
	if !names["Literal"] || !names["Runtime"] {
		t.Fatalf("ArgvElement fields: %v", names)
	}
}

// TestCheckArgvRejects: Build rejects both/neither set and unlisted
// runtime values.
func TestCheckArgvRejects(t *testing.T) {
	base := mkSpec(nil)
	base.Payload = admission.PayloadRef{}
	cases := []struct {
		name string
		argv []admission.ArgvElement
	}{
		{"both", []admission.ArgvElement{{Literal: "x", Runtime: admission.RuntimeSlurmArrayTaskID}}},
		{"neither", []admission.ArgvElement{{Literal: "/bin/echo"}, {}}},
		{"unlisted", []admission.ArgvElement{{Literal: "/bin/echo"}, {Runtime: "SLURM_JOB_ID"}}},
		{"no_argv_no_payload", nil},
	}
	for _, c := range cases {
		spec := base
		spec.Argv = c.argv
		if _, d := admission.Build(admission.BuildInput{Spec: spec}); d == nil {
			t.Errorf("%s: expected denial", c.name)
		}
	}
}

// TestSpecNoSecrets: marshaled spec never contains secret values.
func TestSpecNoSecrets(t *testing.T) {
	spec := mkSpec(payload)
	spec.Environment.SecretRefs = []admission.SecretEnvRef{
		{Name: "TOKEN", ReferenceID: uuid.New(), Mode: "env", Handle: "token"}}
	b, _ := json.Marshal(spec)
	if strings.Contains(string(b), "secret-value") {
		t.Fatal("secret value leaked")
	}
	wrapper, err := submission.Wrapper(spec, payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wrapper, "TOKEN") || strings.Contains(wrapper, "secret-value") {
		t.Fatalf("secret metadata leaked into wrapper: %s", wrapper)
	}
	sub := submission.JobSubmission(spec, wrapper)
	if sub.Environment["TOKEN"] != "[SECRET]" {
		t.Fatalf("preview marker = %q", sub.Environment["TOKEN"])
	}
}

func TestWrapperLoadsResolvedModulesBeforeLaunch(t *testing.T) {
	spec := mkSpec(nil)
	spec.Payload = admission.PayloadRef{}
	spec.Argv = []admission.ArgvElement{{Literal: "./a.out"}}
	spec.Resources.Tasks = 4
	spec.Launch = workflowspec.LaunchSrun
	spec.Software = []admission.ResolvedSoftware{
		{Name: "gcc", Version: "default", ModuleSpec: []string{"gcc"}},
		{Name: "openmpi", Version: "default", ModuleSpec: []string{"openmpi/5.0"}},
	}
	w, err := submission.Wrapper(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	gcc := strings.Index(w, "module load 'gcc'\n")
	mpi := strings.Index(w, "module load 'openmpi/5.0'\n")
	launch := strings.Index(w, "\nsrun --ntasks=4 ")
	if gcc < 0 || mpi < 0 || launch < 0 || gcc > mpi || mpi > launch ||
		!strings.Contains(w[launch:], "'./a.out'") {
		t.Fatalf("modules must load in order before srun:\n%s", w)
	}
}

// lastLine returns the wrapper's final non-empty line (the launch line).
func lastLine(w string) string {
	lines := strings.Split(strings.TrimRight(w, "\n"), "\n")
	return lines[len(lines)-1]
}

// TestWrapperKeepsCleanupTrap: the launch line must not exec, or the EXIT
// trap that removes $CUSTOS_JOB_DIR (and the payload copy) never runs.
func TestWrapperKeepsCleanupTrap(t *testing.T) {
	for _, launch := range []string{workflowspec.LaunchSbatch, workflowspec.LaunchSrun} {
		spec := mkSpec(payload)
		spec.Launch = launch
		w, err := submission.Wrapper(spec, payload)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(w, `trap 'rm -rf "$CUSTOS_JOB_DIR"' EXIT`) {
			t.Fatalf("%s: cleanup trap missing:\n%s", launch, w)
		}
		if line := lastLine(w); strings.HasPrefix(line, "exec") {
			t.Fatalf("%s: launch line execs, skipping the cleanup trap: %q", launch, line)
		}
	}
}
