package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/admission"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/jobs"
	jobpg "github.com/Exonical/custos/internal/jobs/postgres"
	jobsworker "github.com/Exonical/custos/internal/jobs/worker"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/secretrefs"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm/fake"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
)

type deliveryFake struct {
	err  error
	held secrets.Value
}

func (f *deliveryFake) Deliver(context.Context, secretrefs.DeliveryRequest) (secretrefs.DeliveredSecret, error) {
	if f.err != nil {
		return secretrefs.DeliveredSecret{}, f.err
	}
	v := secrets.NewValue([]byte("database-must-not-contain-this"))
	f.held = v
	return secretrefs.DeliveredSecret{Value: v, ConnectorKind: "platform-openbao"}, nil
}

func secretJob(t *testing.T, pool *pgxpool.Pool) (jobs.Job, *fake.Cluster, jobsworker.Deps) {
	t.Helper()
	ctx := context.Background()
	tid, uid := mkTenant(t, pool, "sd-a"), mkUser(t, pool, "sd-u")
	pid, cid := mkProject(t, pool, tid), mkCluster(t, pool, "sd-c")
	j := mkJob(t, tid, pid, cid, uid)
	j.ScriptDigest = validation.Digest{}
	j.ScriptLanguage = ""
	j.ExecutionSpec.Payload = admission.PayloadRef{}
	j.ExecutionSpec.Argv = []admission.ArgvElement{{Literal: "true"}}
	j.ExecutionSpec.Environment.SecretRefs = []admission.SecretEnvRef{{Name: "TOKEN", ReferenceID: uuid.New(), Mode: "env", Handle: "token"}}
	if err := j.ExecutionSpec.Freeze(); err != nil {
		t.Fatal(err)
	}
	j.ExecutionSpecDigest = j.ExecutionSpec.Digest
	repo := jobpg.New(pool)
	if err := repo.Create(ctx, tenants.TenantScope(tid), j, nil); err != nil {
		t.Fatal(err)
	}
	fc := fake.New()
	return j, fc, jobsworker.Deps{Jobs: repo, Clusters: clusterpg.New(pool), Factory: fakeFactory{fc}, Exec: pool}
}

func TestSubmitSecretDeliveryAndErrors(t *testing.T) {
	t.Run("delivers and wipes", func(t *testing.T) {
		pool := dbtest.Pool(t)
		j, fc, d := secretJob(t, pool)
		fd := &deliveryFake{}
		d.Secrets = fd
		err := jobsworker.Submit(d)(context.Background(), workqueue.Item{Kind: "job.submit", Key: "job:" + j.ID.String()})
		if err != nil {
			t.Fatal(err)
		}
		subs := fc.Submissions()
		if len(subs) != 1 || subs[0].Environment["TOKEN"] != "database-must-not-contain-this" {
			t.Fatalf("submissions: %#v", subs)
		}
		var raw string
		if err := pool.QueryRow(context.Background(), `SELECT row_to_json(j)::text FROM jobs j WHERE id=$1`, j.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "database-must-not-contain-this") {
			t.Fatal("secret persisted in jobs row")
		}
		for _, b := range fd.held.Reveal() {
			if b != 0 {
				t.Fatal("secret value not wiped")
			}
		}
	})
	t.Run("unavailable retries", func(t *testing.T) {
		pool := dbtest.Pool(t)
		j, _, d := secretJob(t, pool)
		d.Secrets = &deliveryFake{err: apperr.New(apperr.Unavailable, "SECRETS_UNAVAILABLE", "down")}
		err := jobsworker.Submit(d)(context.Background(), workqueue.Item{Kind: "job.submit", Key: "job:" + j.ID.String()})
		if !apperr.Is(err, apperr.Unavailable) {
			t.Fatalf("error=%v", err)
		}
		got, _ := d.Jobs.Get(context.Background(), tenants.PlatformScope(), j.ID)
		if got.State != jobs.StateSubmitting {
			t.Fatalf("state=%s", got.State)
		}
	})
	t.Run("forbidden fails", func(t *testing.T) {
		pool := dbtest.Pool(t)
		j, _, d := secretJob(t, pool)
		d.Secrets = &deliveryFake{err: apperr.New(apperr.Forbidden, "secrets.forbidden", "denied")}
		err := jobsworker.Submit(d)(context.Background(), workqueue.Item{Kind: "job.submit", Key: "job:" + j.ID.String()})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := d.Jobs.Get(context.Background(), tenants.PlatformScope(), j.ID)
		if got.State != jobs.StateFailed || got.StateReason != "SECRET_UNAVAILABLE" {
			t.Fatalf("job=%+v", got)
		}
	})
}
