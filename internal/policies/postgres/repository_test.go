package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/policies"
	policypg "github.com/Exonical/custos/internal/policies/postgres"
	"github.com/Exonical/custos/internal/projects"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/workflowspec"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func TestPolicyUpsertGet(t *testing.T) {
	pool := dbtest.Pool(t)
	repo := policypg.New(pool)
	ctx := context.Background()

	var tid uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tenants (id, slug, name, state, settings, version)
		VALUES ($1,'pol-t','T','active','{}',1) RETURNING id`,
		uuid.Must(uuid.NewV7())).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	ps := tenants.PlatformScope()

	if _, err := repo.GetTenant(ctx, ps, tid); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	pol := admission.ResourcePolicy{MaxGPUsPerJob: 8,
		AllowedPartitions: []string{"gpu"},
		MaxWalltime:       workflowspec.Duration(4 * time.Hour)}
	if err := repo.Upsert(ctx, ps, policies.Policy{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid,
		Scope: policies.ScopeTenant, Policy: pol,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetTenant(ctx, ps, tid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy.MaxGPUsPerJob != 8 || got.Version != 1 ||
		got.Policy.AllowedPartitions[0] != "gpu" {
		t.Fatalf("policy: %+v", got)
	}

	// Replace bumps version.
	if err := repo.Upsert(ctx, ps, policies.Policy{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid,
		Scope: policies.ScopeTenant, Policy: admission.ResourcePolicy{MaxNodes: 4},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetTenant(ctx, ps, tid)
	if got.Version != 2 || got.Policy.MaxNodes != 4 || got.Policy.MaxGPUsPerJob != 0 {
		t.Fatalf("replace: %+v", got)
	}

	// Project scope.
	pid := uuid.Must(uuid.NewV7())
	pj := projectpg.New(pool)
	if err := pj.Create(ctx, ps, projects.Project{
		ID: pid, TenantID: tid, Slug: "pp", Name: "pp",
		State: projects.StateActive, Settings: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Upsert(ctx, ps, policies.Policy{
		ID: uuid.Must(uuid.NewV7()), TenantID: tid,
		Scope: policies.ScopeProject, ProjectID: &pid,
		Policy: admission.ResourcePolicy{MaxGPUsPerJob: 4},
	}); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetProject(ctx, ps, pid)
	if err != nil || got.Policy.MaxGPUsPerJob != 4 || *got.ProjectID != pid {
		t.Fatalf("project policy: %v %+v", err, got)
	}
}
