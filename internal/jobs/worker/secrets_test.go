package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/jobs"
	"github.com/Exonical/custos/internal/secretrefs"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/submission"
	"github.com/Exonical/custos/internal/workflowspec"
)

type fakeDelivery struct {
	values  []secrets.Value
	modes   []string
	wrapped bool
}

func TestImagePullValuesOnlyEnterSubmitEnvironment(t *testing.T) {
	spec := admission.ExecutionSpec{
		ID: uuid.New(), TenantID: uuid.New(), TaskName: "pull",
		Launch: workflowspec.LaunchSbatch,
		Container: &admission.ContainerSpec{
			Runtime: "apptainer", Image: "docker://registry.example.com/team/app:1.2",
			Binary: "apptainer", PullSecret: true, PullUsernameSecret: true,
		},
		Argv: []admission.ArgvElement{{Literal: "./run"}},
		Environment: admission.EnvSet{SecretRefs: []admission.SecretEnvRef{
			{Name: admission.ImagePullUsernameEnvName, ReferenceID: uuid.New(), Mode: "image_pull", Handle: "pull-user"},
			{Name: admission.ImagePullPasswordEnvName, ReferenceID: uuid.New(), Mode: "image_pull", Handle: "pull-password"},
		}},
	}
	wrapper, err := submission.Wrapper(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	sub := submission.JobSubmission(spec, wrapper)
	if sub.Environment[admission.ImagePullUsernameEnvName] != "[SECRET]" ||
		sub.Environment[admission.ImagePullPasswordEnvName] != "[SECRET]" {
		t.Fatalf("submit preview did not use markers: %#v", sub.Environment)
	}
	delivery := &fakeDelivery{}
	job := jobs.Job{
		ID: uuid.New(), TenantID: spec.TenantID,
		ExecutionSpec: spec,
	}
	values, err := deliverSecrets(context.Background(), Deps{Secrets: delivery}, job, &sub)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wrapper, "very-secret") {
		t.Fatalf("secret value reached generated wrapper:\n%s", wrapper)
	}
	if sub.Environment[admission.ImagePullUsernameEnvName] != "very-secret" ||
		sub.Environment[admission.ImagePullPasswordEnvName] != "very-secret" {
		t.Fatalf("pull values were not resolved for scheduler submission: %#v", sub.Environment)
	}
	for i := range values {
		values[i].Wipe()
	}
}

func (f *fakeDelivery) Deliver(_ context.Context, req secretrefs.DeliveryRequest) (secretrefs.DeliveredSecret, error) {
	v := secrets.NewValue([]byte("very-secret"))
	f.values = append(f.values, v)
	f.modes = append(f.modes, req.Mode)
	out := secretrefs.DeliveredSecret{Value: v, ConnectorKind: "platform-openbao"}
	if req.Mode == "wrapped_token" {
		f.wrapped = true
		out.Address = "https://bao"
		out.Namespace = "custos/tenants/t"
		out.Path = "kv/data/x"
		out.Key = "value"
	}
	return out, nil
}

func TestDeliverSecretsToSchedulerEnvironmentAndWipe(t *testing.T) {
	d := &fakeDelivery{}
	job := jobs.Job{ID: uuid.New(), TenantID: uuid.New(), ExecutionSpec: admission.ExecutionSpec{Resources: admission.ResolvedResources{WalltimeSeconds: 60}, Environment: admission.EnvSet{SecretRefs: []admission.SecretEnvRef{
		{Name: "HF_TOKEN", ReferenceID: uuid.New(), Mode: "env", Handle: "hf"},
		{Name: "CUSTOS_SECRET_WRAP_WRAP_TOKEN", ReferenceID: uuid.New(), Mode: "wrapped_token", Handle: "wrap"},
		{Name: admission.ImagePullUsernameEnvName, ReferenceID: uuid.New(), Mode: "image_pull", Handle: "pull-user"},
		{Name: admission.ImagePullPasswordEnvName, ReferenceID: uuid.New(), Mode: "image_pull", Handle: "pull-password"},
	}}}}
	sub := slurm.JobSubmission{Environment: map[string]string{}}
	values, err := deliverSecrets(context.Background(), Deps{Secrets: d}, job, &sub)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Environment["HF_TOKEN"] != "very-secret" ||
		sub.Environment["CUSTOS_SECRET_WRAP_WRAP_TOKEN"] != "very-secret" ||
		sub.Environment["CUSTOS_SECRET_WRAP_PATH"] != "kv/data/x" ||
		sub.Environment["CUSTOS_BAO_ADDR"] != "https://bao" ||
		sub.Environment[admission.ImagePullUsernameEnvName] != "very-secret" ||
		sub.Environment[admission.ImagePullPasswordEnvName] != "very-secret" {
		t.Fatalf("environment: %#v", sub.Environment)
	}
	if len(d.modes) != 4 || d.modes[2] != "image_pull" || d.modes[3] != "image_pull" {
		t.Fatalf("delivery modes = %v", d.modes)
	}
	for i := range values {
		values[i].Wipe()
	}
	for _, value := range d.values {
		for _, b := range value.Reveal() {
			if b != 0 {
				t.Fatal("delivered value was not wiped")
			}
		}
	}
}
