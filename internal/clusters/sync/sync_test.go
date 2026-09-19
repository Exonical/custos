package sync_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	clustersync "github.com/Exonical/custos/internal/clusters/sync"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/fake"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type fakeFactory struct{ cluster slurm.Cluster }

func (f *fakeFactory) Open(_ context.Context, _ slurm.ClusterConfig) (slurm.Cluster, slurm.Accounting, error) {
	return f.cluster, nil, nil
}

func mkCluster(name string) clusters.Cluster {
	return clusters.Cluster{
		ID: uuid.Must(uuid.NewV7()), Name: name, DisplayName: name,
		BaseURL: "https://slurm.example:6820", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "slurm/token"},
		Visibility: clusters.VisibilityAssigned, State: clusters.StateUnreachable,
		Version: 1,
	}
}

// A periodic handler must signal its next run via RescheduleAt: the
// queue moves the leased row back to pending itself. (Enqueue with the
// same kind+key while the item is leased is deduped and kills the
// chain — the regression this guards.)
func TestHandlerReturnsReschedule(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := clusterpg.New(pool)
	ctx := context.Background()

	c := mkCluster("self")
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}

	interval := 10 * time.Second
	h := clustersync.Handler(repo, &fakeFactory{cluster: fake.New()},
		interval, nil)

	before := time.Now()
	err := h(ctx, workqueue.Item{
		Kind: clustersync.Kind, Key: "cluster:" + c.ID.String()})
	var rs workqueue.Reschedule
	if !errors.As(err, &rs) {
		t.Fatalf("handler error = %v, want workqueue.Reschedule", err)
	}
	if lo, hi := before.Add(8*time.Second), time.Now().Add(12*time.Second); rs.At.Before(lo) || rs.At.After(hi) {
		t.Fatalf("reschedule at %v, want ~now+%v", rs.At, interval)
	}
}

// A disabled cluster stops the chain: the handler returns nil and does
// not reschedule.
func TestHandlerDisabledStopsChain(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := clusterpg.New(pool)
	ctx := context.Background()

	c := mkCluster("off")
	c.State = clusters.StateDisabled
	if err := repo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	h := clustersync.Handler(repo, &fakeFactory{cluster: fake.New()},
		time.Second, nil)
	if err := h(ctx, workqueue.Item{
		Kind: clustersync.Kind, Key: "cluster:" + c.ID.String()}); err != nil {
		t.Fatalf("disabled cluster: %v", err)
	}
}
