package validate_test

import (
	"testing"

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
		{"env collision", func(w *workflowspec.Workflow) { w.Spec.Tasks[0].Env["HF_TOKEN"] = "literal" }, "SECRET_ENV_COLLISION"},
		{"controlled env", func(w *workflowspec.Workflow) {
			u := w.Spec.Secrets["hf-token"]
			u.EnvName = "CUSTOS_TASK"
			w.Spec.Secrets["hf-token"] = u
		}, "SECRET_ENV_CONTROLLED"},
		{"non-whole reference", func(w *workflowspec.Workflow) { w.Spec.Tasks[0].Env["TOKEN"] = "prefix-{{ secrets.hf-token }}" }, "REF_SECRET_WHOLE"},
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
