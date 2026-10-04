package validate_test

import (
	"testing"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/validate"
)

func secretWorkflow(use workflowspec.SecretUse) workflowspec.Workflow {
	return workflowspec.Workflow{Spec: workflowspec.Spec{
		Secrets: map[string]workflowspec.SecretUse{"hf-token": use},
		Tasks: []workflowspec.Task{{Name: "run", Type: "batch",
			Env: map[string]string{"TOKEN": "{{ secrets.hf-token }}"}}},
	}}
}

func hasCode(errs []workflowspec.FieldError, code string) bool {
	for _, err := range errs {
		if err.Code == code {
			return true
		}
	}
	return false
}

func TestSecretStaticValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*workflowspec.Workflow)
		code   string
	}{
		{"invalid use", func(w *workflowspec.Workflow) {
			u := w.Spec.Secrets["hf-token"]
			u.Use = "raw"
			w.Spec.Secrets["hf-token"] = u
		}, "SECRET_USE_INVALID"},
		{"invalid env name", func(w *workflowspec.Workflow) {
			u := w.Spec.Secrets["hf-token"]
			u.EnvName = "bad-name"
			w.Spec.Secrets["hf-token"] = u
		}, "SECRET_ENV_NAME_INVALID"},
		{"image pull env name", func(w *workflowspec.Workflow) {
			u := w.Spec.Secrets["hf-token"]
			u.Use = "image_pull"
			u.EnvName = "PULL_TOKEN"
			w.Spec.Secrets["hf-token"] = u
		}, "SECRET_ENV_NAME_UNUSED"},
		{"env collision", func(w *workflowspec.Workflow) { w.Spec.Tasks[0].Env["HF_TOKEN"] = "literal" }, "SECRET_ENV_COLLISION"},
		{"controlled env", func(w *workflowspec.Workflow) {
			u := w.Spec.Secrets["hf-token"]
			u.EnvName = "CUSTOS_TASK"
			w.Spec.Secrets["hf-token"] = u
		}, "SECRET_ENV_CONTROLLED"},
		{"non-whole reference", func(w *workflowspec.Workflow) { w.Spec.Tasks[0].Env["TOKEN"] = "prefix-{{ secrets.hf-token }}" }, "REF_SECRET_WHOLE"},
		{"non-whole defaults reference", func(w *workflowspec.Workflow) {
			w.Spec.Defaults = &workflowspec.Defaults{Env: map[string]string{"DEFAULT_TOKEN": "prefix-{{ secrets.hf-token }}"}}
		}, "REF_SECRET_WHOLE"},
		{"outside env", func(w *workflowspec.Workflow) { w.Spec.Tasks[0].Command = []string{"{{ secrets.hf-token }}"} }, "REF_SECRET_SCOPE"},
		{"wrapped reference", func(w *workflowspec.Workflow) {
			u := w.Spec.Secrets["hf-token"]
			u.Use = "wrapped_token"
			w.Spec.Secrets["hf-token"] = u
		}, "REF_UNKNOWN_SECRET"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := secretWorkflow(workflowspec.SecretUse{Ref: "hf", Use: "env", EnvName: "HF_TOKEN"})
			tc.mutate(&w)
			if errs := validate.Static(w); !hasCode(errs, tc.code) {
				t.Fatalf("want %s: %+v", tc.code, errs)
			}
		})
	}

	w := secretWorkflow(workflowspec.SecretUse{Ref: "hf", Use: "env", EnvName: "TOKEN"})
	w.Spec.Secrets["other"] = workflowspec.SecretUse{Ref: "other", Use: "env", EnvName: "TOKEN"}
	if errs := validate.Static(w); !hasCode(errs, "SECRET_ENV_COLLISION") {
		t.Fatalf("collision: %+v", errs)
	}
}

func TestImagePullSecretContextualValidation(t *testing.T) {
	w := workflowspec.Workflow{Spec: workflowspec.Spec{
		Secrets: map[string]workflowspec.SecretUse{
			"registry-user":  {Ref: "registry-user", Use: "image_pull"},
			"registry-token": {Ref: "registry-token", Use: "image_pull"},
		},
		Tasks: []workflowspec.Task{{
			Name: "run", Command: []string{"true"},
			Image: &workflowspec.Image{
				URI: "docker://registry.example.com/team/app:1.2",
				PullSecret: &workflowspec.ImagePullSecret{
					UsernameSecret: "registry-user", PasswordSecret: "registry-token",
				},
			},
		}},
	}}
	infos := map[string]validate.SecretReferenceInfo{
		"registry-user": {
			ID: "user-id", Kind: "generic", AllowedUses: []string{"image_pull"},
			ConnectorKind: "openbao",
		},
		"registry-token": {
			ID: "token-id", Kind: "generic", AllowedUses: []string{"image_pull"},
			ConnectorKind: "openbao",
		},
	}
	secretContext := validate.Context{SecretReference: func(name string) (validate.SecretReferenceInfo, bool) {
		info, ok := infos[name]
		return info, ok
	}}
	if errs := validate.Contextual(w, secretContext); len(errs) != 0 {
		t.Fatalf("valid image pull references rejected: %+v", errs)
	}
	infos["registry-token"] = validate.SecretReferenceInfo{
		ID: "token-id", Kind: "generic", ConnectorKind: "openbao",
	}
	if errs := validate.Contextual(w, secretContext); !hasCode(errs, "SECRET_USE_NOT_ALLOWED") {
		t.Fatalf("missing image_pull allowed use accepted: %+v", errs)
	}
	infos["registry-token"] = validate.SecretReferenceInfo{
		ID: "token-id", Kind: "api_token", AllowedUses: []string{"image_pull"},
		ConnectorKind: "openbao",
	}
	if errs := validate.Contextual(w, secretContext); !hasCode(errs, "PULL_SECRET_INVALID") {
		t.Fatalf("non-generic image pull reference accepted: %+v", errs)
	}
	w.Spec.Tasks[0].Image.URI = "oras://registry.example.com/team/app:1.2"
	infos["registry-token"] = validate.SecretReferenceInfo{
		ID: "token-id", Kind: "generic", AllowedUses: []string{"image_pull"},
		ConnectorKind: "openbao",
	}
	pyxisContext := secretContext
	pyxisContext.DefaultCluster = "cluster"
	pyxisContext.Cluster = func(string) (admission.Binding, validation.ClusterSnapshot, bool) {
		return admission.Binding{}, validation.ClusterSnapshot{
			ContainerRuntime: &validation.ContainerRuntime{Type: "pyxis"},
		}, true
	}
	if errs := validate.Contextual(w, pyxisContext); !hasCode(errs, "PULL_SECRET_INVALID") {
		t.Fatalf("Pyxis oras pull secret accepted: %+v", errs)
	}
}

func TestImagePullEnvironmentNamesAreReserved(t *testing.T) {
	w := secretWorkflow(workflowspec.SecretUse{Ref: "hf", Use: "env", EnvName: "HF_TOKEN"})
	w.Spec.Tasks[0].Env["CUSTOS_IMAGE_PULL_USERNAME"] = "user-value"
	w.Spec.Defaults = &workflowspec.Defaults{Env: map[string]string{
		"CUSTOS_IMAGE_PULL_PASSWORD": "user-value",
	}}
	errs := validate.Static(w)
	reserved := 0
	for _, err := range errs {
		if err.Code == "SECRET_ENV_CONTROLLED" {
			reserved++
		}
	}
	if reserved != 2 {
		t.Fatalf("pull secret internal names were not reserved: %+v", errs)
	}
}

func TestSecretContextualValidation(t *testing.T) {
	base := secretWorkflow(workflowspec.SecretUse{Ref: "hf", Use: "env", EnvName: "HF_TOKEN"})
	cases := []struct {
		name string
		use  string
		info validate.SecretReferenceInfo
		ok   bool
		code string
	}{
		{"missing", "env", validate.SecretReferenceInfo{}, false, "SECRET_REFERENCE_NOT_FOUND"},
		{"env kind", "env", validate.SecretReferenceInfo{Kind: "api_token", AllowedUses: []string{"workflow_env"}, ConnectorKind: "platform-openbao"}, true, "SECRET_ENV_KIND"},
		{"env allowed use", "env", validate.SecretReferenceInfo{Kind: "generic", ConnectorKind: "platform-openbao"}, true, "SECRET_USE_NOT_ALLOWED"},
		{"wrapped allowed use", "wrapped_token", validate.SecretReferenceInfo{Kind: "generic", ConnectorKind: "platform-openbao"}, true, "SECRET_USE_NOT_ALLOWED"},
		{"wrapped connector", "wrapped_token", validate.SecretReferenceInfo{Kind: "generic", AllowedUses: []string{"wrapped_token"}, ConnectorKind: "openbao"}, true, "WRAPPED_TOKEN_UNSUPPORTED_CONNECTOR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := base
			u := w.Spec.Secrets["hf-token"]
			u.Use = tc.use
			w.Spec.Secrets = map[string]workflowspec.SecretUse{"hf-token": u}
			errs := validate.Contextual(w, validate.Context{SecretReference: func(string) (validate.SecretReferenceInfo, bool) { return tc.info, tc.ok }})
			if !hasCode(errs, tc.code) {
				t.Fatalf("want %s: %+v", tc.code, errs)
			}
		})
	}
}
