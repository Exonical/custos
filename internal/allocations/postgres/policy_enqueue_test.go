package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/allocations"
	allocationpg "github.com/Exonical/custos/internal/allocations/postgres"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/projects"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/tenants"
)

type allowAllocationManagement struct{}

func (allowAllocationManagement) Check(context.Context, authn.Principal, authz.Action, authz.Resource) (authz.Decision, error) {
	return authz.Decision{Allow: true}, nil
}

func TestAllocationMutationsEnqueuePolicySync(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Pool(t)
	tenantID, userID, projectID, bindingID, clusterID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,issuer,subject,kind) VALUES($1,'alloc-enqueue',$2,'user')`, userID, userID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,slug,name,state,settings) VALUES($1,$2,'tenant','active','{}')`, tenantID, "alloc-enqueue-"+userID.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO projects(id,tenant_id,slug,name,state,settings) VALUES($1,$2,$3,'project','active','{}')`, projectID, tenantID, "alloc-enqueue-"+userID.String()[:8]); err != nil {
		t.Fatal(err)
	}
	cluster := clusters.Cluster{ID: clusterID, Name: "alloc-enqueue-" + userID.String()[:8], DisplayName: "allocation enqueue", BaseURL: "https://slurm.example", APIVersion: "v0.0.45", IdentityMode: clusters.IdentityService, ServiceUser: "custos", TokenRef: secrets.Reference{Provider: "file", Path: "slurm/token"}, Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1}
	if err := clusterpg.New(pool).Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO cluster_tenant_assignments(cluster_id,tenant_id,source,defaults) VALUES($1,$2,'manual','{}')`, clusterID, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_cluster_bindings(id,tenant_id,project_id,cluster_id,slurm_account,enabled) VALUES($1,$2,$3,$4,'alloc-enqueue-acct',true)`, bindingID, tenantID, projectID, clusterID); err != nil {
		t.Fatal(err)
	}

	projectRepo := projectpg.New(pool)
	service := allocations.NewService(allocationpg.New(pool), projectRepo, allowAllocationManagement{}, nil, projectRepo)
	service.SetEnqueuer(pool)
	principal := authn.Principal{UserID: userID}
	tenantContext := tenants.TenantContext{Tenant: tenants.Tenant{ID: tenantID, Slug: "alloc-enqueue", State: tenants.StateActive}}
	projectContext := projects.ProjectContext{Project: projects.Project{ID: projectID, TenantID: tenantID, Slug: "alloc-enqueue", Name: "project", State: projects.StateActive}}
	check := func() {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE kind='policy.sync' AND key=$1 AND state IN ('pending','leased')`, "cluster:"+clusterID.String()).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("active policy.sync items=%d", count)
		}
	}

	now := time.Now().UTC()
	created, err := service.Create(ctx, principal, tenantContext, projectContext, allocations.CreateInput{BindingID: bindingID, Name: "hard-budget", Unit: "cpu_hours", LimitAmount: 4, PeriodStart: now.Add(-time.Hour), PeriodEnd: now.Add(time.Hour), Enforcement: "hard"})
	if err != nil {
		t.Fatal(err)
	}
	check()
	newLimit := 5.0
	updated, err := service.Update(ctx, principal, tenantContext, projectContext, created.ID, allocations.UpdateInput{LimitAmount: &newLimit, Version: created.Version})
	if err != nil {
		t.Fatal(err)
	}
	check()
	if err := service.Delete(ctx, principal, tenantContext, projectContext, updated.ID); err != nil {
		t.Fatal(err)
	}
	check()
}
