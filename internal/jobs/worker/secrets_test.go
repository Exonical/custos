package worker

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/jobs"
	"github.com/Exonical/custos/internal/secretrefs"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
)

type fakeDelivery struct {
	value   secrets.Value
	wrapped bool
}

func (f *fakeDelivery) Deliver(_ context.Context, req secretrefs.DeliveryRequest) (secretrefs.DeliveredSecret, error) {
	v := secrets.NewValue([]byte("very-secret"))
	f.value = v
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
	job := jobs.Job{ID: uuid.New(), TenantID: uuid.New(), ExecutionSpec: admission.ExecutionSpec{Resources: admission.ResolvedResources{WalltimeSeconds: 60}, Environment: admission.EnvSet{SecretRefs: []admission.SecretEnvRef{{Name: "HF_TOKEN", ReferenceID: uuid.New(), Mode: "env", Handle: "hf"}, {Name: "CUSTOS_SECRET_WRAP_WRAP_TOKEN", ReferenceID: uuid.New(), Mode: "wrapped_token", Handle: "wrap"}}}}}
	sub := slurm.JobSubmission{Environment: map[string]string{}}
	values, err := deliverSecrets(context.Background(), Deps{Secrets: d}, job, &sub)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Environment["HF_TOKEN"] != "very-secret" || sub.Environment["CUSTOS_SECRET_WRAP_WRAP_TOKEN"] != "very-secret" || sub.Environment["CUSTOS_SECRET_WRAP_PATH"] != "kv/data/x" || sub.Environment["CUSTOS_BAO_ADDR"] != "https://bao" {
		t.Fatalf("environment: %#v", sub.Environment)
	}
	for i := range values {
		values[i].Wipe()
	}
	for _, b := range d.value.Reveal() {
		if b != 0 {
			t.Fatal("delivered value was not wiped")
		}
	}
}
