package submission_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/submission"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/shsyntax"
	"github.com/Exonical/custos/internal/workflowspec"
)

func TestImagePullSecretWrapperSetup(t *testing.T) {
	validateWrapper := func(t *testing.T, wrapper string) {
		t.Helper()
		result, err := shsyntax.Validator{}.Validate(t.Context(), validation.Input{
			Language: workflowspec.LanguageBash,
			Script:   []byte(wrapper),
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, diagnostic := range result.Diagnostics {
			if diagnostic.Severity.AtLeast(validation.SeverityError) {
				t.Fatalf("wrapper has %s: %s\n%s", diagnostic.Code, diagnostic.Message, wrapper)
			}
		}
	}
	pullRefs := []admission.SecretEnvRef{
		{Name: admission.ImagePullUsernameEnvName, ReferenceID: uuid.New(), Mode: "image_pull", Handle: "registry-user"},
		{Name: admission.ImagePullPasswordEnvName, ReferenceID: uuid.New(), Mode: "image_pull", Handle: "registry-token"},
	}
	secretValues := []string{"username-secret-value", "password-secret-value"}

	for _, runtime := range []string{"apptainer", "pyxis"} {
		t.Run(runtime, func(t *testing.T) {
			spec := mkSpec(nil)
			spec.Payload = admission.PayloadRef{}
			spec.Argv = []admission.ArgvElement{{Literal: "./run"}}
			spec.Launch = workflowspec.LaunchSbatch
			spec.Environment.Controlled = nil
			spec.Environment.SecretRefs = append([]admission.SecretEnvRef(nil), pullRefs...)
			spec.ContainerEnv = map[string]string{"PATH": "/usr/bin"}
			spec.Container = &admission.ContainerSpec{
				Runtime: runtime, PullSecret: true, PullUsernameSecret: true,
				RegistryHost: "docker.io",
			}
			if runtime == "apptainer" {
				spec.Container.Image = "oras://docker.io/team/app:1.2"
				spec.Container.Binary = "apptainer"
			} else {
				spec.Container.Image = "docker.io#team/app:1.2"
				spec.Container.EnrootImage = "docker://docker.io#team/app:1.2"
			}

			wrapper, err := submission.Wrapper(spec, nil)
			if err != nil {
				t.Fatal(err)
			}
			validateWrapper(t, wrapper)
			for _, value := range secretValues {
				if strings.Contains(wrapper, value) {
					t.Fatalf("secret value %q leaked into generated script:\n%s", value, wrapper)
				}
			}
			for _, name := range []string{
				admission.ImagePullUsernameEnvName,
				admission.ImagePullPasswordEnvName,
			} {
				if !strings.Contains(wrapper, name) {
					t.Errorf("wrapper omitted the runtime reference to %s", name)
				}
			}
			if runtime == "apptainer" &&
				!strings.Contains(wrapper, `APPTAINER_DOCKER_USERNAME="$CUSTOS_IMAGE_PULL_USERNAME" APPTAINER_DOCKER_PASSWORD="$CUSTOS_IMAGE_PULL_PASSWORD"`) {
				t.Fatalf("Apptainer pull credentials are not command-scoped variable references:\n%s", wrapper)
			}
			unset := strings.Index(wrapper, "unset CUSTOS_IMAGE_PULL_USERNAME CUSTOS_IMAGE_PULL_PASSWORD")
			launchNeedle := `'apptainer' 'exec'`
			if runtime == "pyxis" {
				launchNeedle = `--container-image="$CUSTOS_JOB_DIR/image.sqsh"`
			}
			launch := strings.LastIndex(wrapper, launchNeedle)
			if unset < 0 || launch < 0 || unset > launch {
				t.Fatalf("pull variables are not unset before the local image launch:\n%s", wrapper)
			}
			for _, line := range strings.Split(wrapper, "\n") {
				if (strings.Contains(line, "--container-env=") || strings.Contains(line, "'/usr/bin/env'")) &&
					strings.Contains(line, "CUSTOS_IMAGE_PULL_") {
					t.Fatalf("pull variables were forwarded into the container environment: %s", line)
				}
			}
			submissionEnv := submission.JobSubmission(spec, wrapper).Environment
			if len(submissionEnv) != 2 ||
				submissionEnv[admission.ImagePullUsernameEnvName] != "[SECRET]" ||
				submissionEnv[admission.ImagePullPasswordEnvName] != "[SECRET]" {
				t.Fatalf("JobSubmission environment did not contain only secret markers: %#v", submissionEnv)
			}
		})
	}
}

func TestPyxisPullUsesHostOnlyCredentialsUntilImportCompletes(t *testing.T) {
	spec := mkSpec(nil)
	spec.Payload = admission.PayloadRef{}
	spec.Argv = []admission.ArgvElement{{Literal: "./run"}}
	spec.Launch = workflowspec.LaunchSbatch
	spec.Environment.Controlled = nil
	spec.Environment.SecretRefs = []admission.SecretEnvRef{
		{Name: admission.ImagePullUsernameEnvName, ReferenceID: uuid.New(), Mode: "image_pull", Handle: "registry-user"},
		{Name: admission.ImagePullPasswordEnvName, ReferenceID: uuid.New(), Mode: "image_pull", Handle: "registry-token"},
	}
	spec.Container = &admission.ContainerSpec{
		Runtime: "pyxis", Image: "docker.io#team/app:1.2", PullSecret: true,
		PullUsernameSecret: true, RegistryHost: "docker.io",
		EnrootImage: "docker://docker.io#team/app:1.2",
	}
	wrapper, err := submission.Wrapper(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	mktemp := strings.Index(wrapper, `creds_dir=$(mktemp -d "${TMPDIR:-/tmp}/custos-enroot.XXXXXX")`)
	if mktemp < 0 {
		t.Fatalf("host-only credentials directory was not created:\n%s", wrapper)
	}
	mktempEnd := strings.Index(wrapper[mktemp:], "\n")
	if mktempEnd < 0 || strings.Contains(wrapper[mktemp:mktemp+mktempEnd], "CUSTOS_JOB_DIR") {
		t.Fatalf("credentials directory is inside the mounted job directory:\n%s", wrapper)
	}
	if !strings.Contains(wrapper, `trap 'if [ -n "${creds_dir:-}" ]; then rm -rf "$creds_dir" || true; fi; rm -rf "$CUSTOS_JOB_DIR"' EXIT`) {
		t.Fatalf("EXIT trap does not clean credentials on an import failure:\n%s", wrapper)
	}
	if strings.Count(wrapper, "ENROOT_CONFIG_PATH=") != 1 ||
		!strings.Contains(wrapper, `if ! ENROOT_CONFIG_PATH="$creds_dir" 'enroot' 'import'`) ||
		strings.Contains(wrapper, "export ENROOT_CONFIG_PATH") {
		t.Fatalf("ENROOT_CONFIG_PATH is not scoped to enroot import:\n%s", wrapper)
	}
	if !strings.Contains(wrapper, `chmod 0700 "$creds_dir"`) ||
		!strings.Contains(wrapper, `chmod 0600 "$creds_dir/.credentials"`) ||
		!strings.Contains(wrapper, "umask 077") {
		t.Fatalf("credentials directory/file permissions are not restricted:\n%s", wrapper)
	}
	if !strings.Contains(wrapper, `printf '%s' "$CUSTOS_IMAGE_PULL_USERNAME" >> "$creds_dir/.credentials"`) ||
		!strings.Contains(wrapper, `printf '%s' "$CUSTOS_IMAGE_PULL_PASSWORD" >> "$creds_dir/.credentials"`) {
		t.Fatalf("Enroot credentials are not written from pull environment references:\n%s", wrapper)
	}
	removed := strings.Index(wrapper, `rm -rf "$creds_dir"`)
	unset := strings.Index(wrapper, "unset CUSTOS_IMAGE_PULL_USERNAME CUSTOS_IMAGE_PULL_PASSWORD")
	launch := strings.LastIndex(wrapper, `--container-image="$CUSTOS_JOB_DIR/image.sqsh"`)
	if removed < 0 || unset < removed || launch < unset {
		t.Fatalf("credentials or pull variables remain before the local image launch:\n%s", wrapper)
	}
	if !strings.Contains(wrapper, `--container-image="$CUSTOS_JOB_DIR/image.sqsh"`) {
		t.Fatalf("Pyxis launch did not use the local squashfs image:\n%s", wrapper)
	}
	for _, line := range strings.Split(wrapper, "\n") {
		if strings.Contains(line, "--container-env=") && strings.Contains(line, "CUSTOS_IMAGE_PULL_") {
			t.Fatalf("pull variables were included in --container-env: %s", line)
		}
	}
}

func TestImagePullSecretJobSubmissionContainsMarkersOnly(t *testing.T) {
	spec := mkSpec(nil)
	spec.Payload = admission.PayloadRef{}
	spec.Argv = []admission.ArgvElement{{Literal: "./run"}}
	spec.Environment.Controlled = nil
	spec.Environment.User = nil
	spec.Environment.SecretRefs = []admission.SecretEnvRef{
		{Name: admission.ImagePullUsernameEnvName, ReferenceID: uuid.New(), Mode: "image_pull", Handle: "registry-user"},
		{Name: admission.ImagePullPasswordEnvName, ReferenceID: uuid.New(), Mode: "image_pull", Handle: "registry-token"},
	}
	sub := submission.JobSubmission(spec, "#!/bin/bash\n")
	if len(sub.Environment) != 2 ||
		sub.Environment[admission.ImagePullUsernameEnvName] != "[SECRET]" ||
		sub.Environment[admission.ImagePullPasswordEnvName] != "[SECRET]" {
		t.Fatalf("JobSubmission environment = %#v", sub.Environment)
	}
}
