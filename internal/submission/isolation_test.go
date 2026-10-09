package submission_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/submission"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/shsyntax"
	"github.com/Exonical/custos/internal/workflowspec"
)

var testMounts = []admission.NodeMount{
	{Target: "/apps", ReadOnly: true, Shared: true},
	{Target: "/mnt/data"},
}

func mustParse(t *testing.T, wrapper string) {
	t.Helper()
	res, err := shsyntax.Validator{}.Validate(context.Background(), validation.Input{
		Language: workflowspec.LanguageBash, Script: []byte(wrapper)})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diagnostics {
		if d.Severity.AtLeast(validation.SeverityError) {
			t.Fatalf("wrapper has %s: %s\n%s", d.Code, d.Message, wrapper)
		}
	}
}

func TestWrapperMountChecksPrecedePayload(t *testing.T) {
	spec := mkSpec(payload)
	spec.Isolation = &admission.IsolationSpec{Mode: "namespace", Mounts: testMounts}
	w, err := submission.Wrapper(spec, payload)
	if err != nil {
		t.Fatal(err)
	}
	mustParse(t, w)
	for _, want := range []string{
		`mountpoint -q -- '/apps' || { printf '%s\n' 'custos: required node mount /apps is missing (node bundle out of date?)' >&2; exit 97; }`,
		`mountpoint -q -- '/mnt/data' || { printf '%s\n' 'custos: required node mount /mnt/data is missing (node bundle out of date?)' >&2; exit 97; }`,
	} {
		if !strings.Contains(w, want) {
			t.Errorf("wrapper lacks %s\n%s", want, w)
		}
	}
	if strings.Index(w, "mountpoint -q") > strings.Index(w, "base64 -d") ||
		strings.Index(w, "mountpoint -q") < strings.Index(w, "trap ") {
		t.Fatal("mount checks must follow the cleanup trap and precede the payload")
	}
}

func TestWrapperWithoutIsolationHasNoMountChecks(t *testing.T) {
	w, err := submission.Wrapper(mkSpec(payload), payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w, "mountpoint") {
		t.Fatal("legacy spec must not render mount checks")
	}
	spec := mkSpec(payload)
	spec.Isolation = &admission.IsolationSpec{Mode: "tenant_exclusive", Shared: "mcs", MCSLabel: "t"}
	w, err = submission.Wrapper(spec, payload)
	if err != nil || strings.Contains(w, "mountpoint") {
		t.Fatalf("isolation without mounts rendered checks: %v", err)
	}
}

func TestContainerBindsFromNodeMounts(t *testing.T) {
	t.Run("apptainer", func(t *testing.T) {
		spec := mkSpec(payload)
		spec.Container = &admission.ContainerSpec{Runtime: "apptainer", Image: "oras://docker.io/example/a.sif", Binary: "apptainer"}
		spec.Isolation = &admission.IsolationSpec{Mode: "namespace", Mounts: testMounts}
		w, err := submission.Wrapper(spec, payload)
		if err != nil {
			t.Fatal(err)
		}
		mustParse(t, w)
		want := `'apptainer' 'exec' '--no-eval' '--bind' "$CUSTOS_JOB_DIR" '--bind' '/apps:/apps:ro,/mnt/data:/mnt/data' 'oras://docker.io/example/a.sif'`
		if !strings.Contains(w, want) {
			t.Fatalf("missing bind words:\n%s", w)
		}
	})
	t.Run("pyxis", func(t *testing.T) {
		spec := mkSpec(payload)
		spec.Container = &admission.ContainerSpec{Runtime: "pyxis", Image: "ubuntu:22.04"}
		spec.Isolation = &admission.IsolationSpec{Mode: "namespace", Mounts: testMounts}
		w, err := submission.Wrapper(spec, payload)
		if err != nil {
			t.Fatal(err)
		}
		mustParse(t, w)
		want := `--container-mounts="$CUSTOS_JOB_DIR:$CUSTOS_JOB_DIR"',/apps:/apps:ro,/mnt/data:/mnt/data'`
		if !strings.Contains(w, want) {
			t.Fatalf("missing mount word:\n%s", w)
		}
	})
	t.Run("no isolation keeps legacy words", func(t *testing.T) {
		for _, rt := range []string{"apptainer", "pyxis"} {
			spec := mkSpec(payload)
			spec.Container = &admission.ContainerSpec{Runtime: rt, Image: "x", Binary: "apptainer"}
			w, err := submission.Wrapper(spec, payload)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(w, "'--bind' '/") || strings.Contains(w, `CUSTOS_JOB_DIR"',`) {
				t.Fatalf("%s: unexpected binds:\n%s", rt, w)
			}
		}
	})
}

func TestNodeMountsWithPullSecretAndMultinode(t *testing.T) {
	for _, rt := range []string{"apptainer", "pyxis"} {
		t.Run(rt+" pull secret", func(t *testing.T) {
			spec := mkSpec(nil)
			spec.Payload = admission.PayloadRef{}
			spec.Argv = []admission.ArgvElement{{Literal: "./run"}}
			spec.Launch = workflowspec.LaunchSbatch
			spec.Isolation = &admission.IsolationSpec{Mode: "namespace", Mounts: testMounts}
			spec.Container = &admission.ContainerSpec{Runtime: rt, PullSecret: true,
				PullUsername: "u", RegistryHost: "docker.io"}
			if rt == "apptainer" {
				spec.Container.Image, spec.Container.Binary = "docker://team/app:1", "apptainer"
			} else {
				spec.Container.Image, spec.Container.EnrootImage = "team/app:1", "docker://team/app:1"
			}
			w, err := submission.Wrapper(spec, nil)
			if err != nil {
				t.Fatal(err)
			}
			mustParse(t, w)
			if !strings.Contains(w, "/apps:/apps:ro,/mnt/data:/mnt/data") ||
				!strings.Contains(w, `"$CUSTOS_JOB_DIR/image.s`) {
				t.Fatalf("pull-secret launch lacks binds or local image:\n%s", w)
			}
		})
		t.Run(rt+" generic multinode", func(t *testing.T) {
			spec := mkSpec(payload)
			spec.Launch = workflowspec.LaunchSrun
			spec.Multinode = &admission.MultinodeSpec{Implementation: "generic", Nodes: 2, SlotsPerNode: 4}
			spec.Isolation = &admission.IsolationSpec{Mode: "namespace", Mounts: testMounts}
			spec.Container = &admission.ContainerSpec{Runtime: rt, Image: "oras://docker.io/example/g.sif", Binary: "apptainer"}
			if rt == "pyxis" {
				spec.Container.Image = "ubuntu:22.04"
			}
			w, err := submission.Wrapper(spec, payload)
			if err != nil {
				t.Fatal(err)
			}
			mustParse(t, w)
			// launch line and the rsh helper both carry the binds
			if n := strings.Count(w, "/apps:/apps:ro,/mnt/data:/mnt/data"); n < 2 {
				t.Fatalf("binds appear %d times, want launch + rsh helper:\n%s", n, w)
			}
		})
	}
}

func TestJobSubmissionSharedAndMCS(t *testing.T) {
	cases := []struct {
		name      string
		iso       *admission.IsolationSpec
		exclusive bool
		shared    string
		label     string
	}{
		{"legacy no exclusive", nil, false, "", ""},
		{"legacy exclusive falls back to none", nil, true, "none", ""},
		{"mcs", &admission.IsolationSpec{Mode: "tenant_exclusive", Shared: "mcs", MCSLabel: "tenant-a"}, false, "mcs", "tenant-a"},
		{"user", &admission.IsolationSpec{Mode: "tenant_exclusive", Shared: "user"}, false, "user", ""},
		{"none with label", &admission.IsolationSpec{Mode: "tenant_exclusive", Shared: "none", MCSLabel: "tenant-a"}, true, "none", "tenant-a"},
		{"namespace mounts only", &admission.IsolationSpec{Mode: "namespace", Mounts: testMounts}, false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := mkSpec(payload)
			spec.Isolation = tc.iso
			spec.Resources.Exclusive = tc.exclusive
			sub := submission.JobSubmission(spec, "#!/bin/bash\n")
			if sub.Shared != tc.shared || sub.MCSLabel != tc.label {
				t.Fatalf("Shared=%q MCSLabel=%q, want %q %q", sub.Shared, sub.MCSLabel, tc.shared, tc.label)
			}
		})
	}
}

func TestInvalidFrozenMountTargetIsInternalError(t *testing.T) {
	for _, target := range []string{"relative", "/a b", "/x;rm", "/a/../b", "/it's"} {
		spec := mkSpec(payload)
		spec.Isolation = &admission.IsolationSpec{Mode: "namespace", Mounts: []admission.NodeMount{{Target: target}}}
		if _, err := submission.Wrapper(spec, payload); err == nil {
			t.Fatalf("target %q accepted", target)
		}
	}
}
