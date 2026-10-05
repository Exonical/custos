package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/executions"
	execsvc "github.com/Exonical/custos/internal/executions/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/workflows"
)

type testExecutionRepository struct {
	executions.Repository
	created *executions.Execution
}

func (r *testExecutionRepository) CreateWithIdempotency(_ context.Context,
	_ tenants.Scope, execution executions.Execution, _ executions.IdemRecord,
	_ executions.EnqueueFunc,
) (executions.CreateResult, error) {
	r.created = &execution
	return executions.CreateResult{Execution: execution}, nil
}

type testWorkflowRepository struct {
	workflows.Repository
	workflow workflows.Workflow
	version  workflows.Version
}

func (r *testWorkflowRepository) Get(context.Context, tenants.Scope, uuid.UUID, uuid.UUID) (workflows.Workflow, error) {
	return r.workflow, nil
}

func (r *testWorkflowRepository) GetVersion(context.Context, tenants.Scope,
	uuid.UUID, uuid.UUID, uuid.UUID,
) (workflows.Version, error) {
	return r.version, nil
}

type testAuthorizer struct {
	actions []authz.Action
	denied  authz.Action
}

func (a *testAuthorizer) Check(_ context.Context, _ authn.Principal,
	action authz.Action, _ authz.Resource,
) (authz.Decision, error) {
	a.actions = append(a.actions, action)
	return authz.Decision{Allow: action != a.denied, Reason: "test"}, nil
}

type testAudit struct {
	events []audit.Event
}

func (a *testAudit) Record(_ context.Context, event audit.Event) error {
	a.events = append(a.events, event)
	return nil
}

func testExecutionService(state workflows.VersionState, authorizer *testAuthorizer, recorder *testAudit) (*execsvc.Service, *testExecutionRepository, authn.Principal, tenants.TenantContext, uuid.UUID, uuid.UUID) {
	tenantID, projectID, workflowID, versionID, userID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	workflowRepo := &testWorkflowRepository{
		workflow: workflows.Workflow{
			ID: workflowID, TenantID: tenantID, ProjectID: projectID,
			Name: "test", CreatedBy: userID,
		},
		version: workflows.Version{
			ID: versionID, WorkflowID: workflowID, TenantID: tenantID,
			Number: 1, State: state,
			Spec:     []byte(`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow","metadata":{"name":"test"},"spec":{"tasks":[]}}`),
			SpecHash: [32]byte{1}, Version: 1, CreatedAt: now,
		},
	}
	execRepo := &testExecutionRepository{}
	deps := execsvc.Deps{Repo: execRepo, Workflows: workflowRepo, AZ: authorizer}
	if recorder != nil {
		deps.Audit = recorder
	}
	service := execsvc.New(deps)
	return service, execRepo,
		authn.Principal{UserID: userID, Kind: authn.KindUser},
		tenants.TenantContext{Tenant: tenants.Tenant{ID: tenantID}},
		workflowID, versionID
}

func TestTestRunRequiresBothWorkflowPermissionsAndAuditsFlag(t *testing.T) {
	authorizer := &testAuthorizer{}
	recorder := &testAudit{}
	service, repo, principal, tenant, workflowID, versionID :=
		testExecutionService(workflows.VersionDraft, authorizer, recorder)
	result, err := service.Execute(context.Background(), principal, tenant,
		execsvc.ExecuteInput{
			WorkflowID: workflowID, VersionID: &versionID,
			Parameters: json.RawMessage(`{}`), Test: true,
		}, "test-run", [32]byte{1})
	if err != nil {
		t.Fatalf("test execution rejected: %v", err)
	}
	if repo.created == nil || !repo.created.IsTest || !result.Execution.IsTest {
		t.Fatalf("test flag was not persisted: created=%+v result=%+v", repo.created, result.Execution)
	}
	if len(authorizer.actions) != 2 ||
		authorizer.actions[0] != authz.WorkflowCreate ||
		authorizer.actions[1] != authz.WorkflowExecute {
		t.Fatalf("test run permissions = %v", authorizer.actions)
	}
	if len(recorder.events) != 1 || recorder.events[0].Details["test"] != true {
		t.Fatalf("test run audit details = %+v", recorder.events)
	}
}

func TestTestRunVersionRequirements(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state workflows.VersionState
		test  bool
		want  string
	}{
		{"draft without test", workflows.VersionDraft, false, "DRAFT_REQUIRES_TEST_RUN"},
		{"published with test", workflows.VersionPublished, true, "TEST_RUN_REQUIRES_DRAFT"},
		{"deprecated with test", workflows.VersionDeprecated, true, "TEST_RUN_REQUIRES_DRAFT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authorizer := &testAuthorizer{}
			service, _, principal, tenant, workflowID, versionID :=
				testExecutionService(tc.state, authorizer, nil)
			_, err := service.Execute(context.Background(), principal, tenant,
				execsvc.ExecuteInput{
					WorkflowID: workflowID, VersionID: &versionID, Test: tc.test,
				}, "test-run", [32]byte{1})
			var domainErr *apperr.Error
			if !errors.As(err, &domainErr) || domainErr.Code != tc.want {
				t.Fatalf("Execute error = %v, want %s", err, tc.want)
			}
		})
	}
	authorizer := &testAuthorizer{}
	service, _, principal, tenant, workflowID, _ :=
		testExecutionService(workflows.VersionDraft, authorizer, nil)
	_, err := service.Execute(context.Background(), principal, tenant,
		execsvc.ExecuteInput{WorkflowID: workflowID, Test: true},
		"test-run-no-version", [32]byte{1})
	var domainErr *apperr.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "TEST_RUN_REQUIRES_DRAFT" {
		t.Fatalf("test run without version = %v, want TEST_RUN_REQUIRES_DRAFT", err)
	}
}

func TestNormalExecutionNeedsNoCreatePermission(t *testing.T) {
	authorizer := &testAuthorizer{denied: authz.WorkflowCreate}
	service, repo, principal, tenant, workflowID, versionID :=
		testExecutionService(workflows.VersionPublished, authorizer, nil)
	result, err := service.Execute(context.Background(), principal, tenant,
		execsvc.ExecuteInput{WorkflowID: workflowID, VersionID: &versionID},
		"normal-run", [32]byte{1})
	if err != nil {
		t.Fatalf("normal execution unexpectedly required workflow.create: %v", err)
	}
	if repo.created == nil || repo.created.IsTest || result.Execution.IsTest {
		t.Fatalf("normal execution test flag = created:%+v result:%+v", repo.created, result.Execution)
	}
	if len(authorizer.actions) != 1 || authorizer.actions[0] != authz.WorkflowExecute {
		t.Fatalf("normal execution permissions = %v", authorizer.actions)
	}
}

// failingAudit fails every Record call.
type failingAudit struct{ calls int }

func (a *failingAudit) Record(context.Context, audit.Event) error {
	a.calls++
	return errors.New("audit sink down")
}

func TestAuditWriteFailureLogsWarnAndContinues(t *testing.T) {
	tenantID, projectID, workflowID, versionID, userID :=
		uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	wfRepo := &testWorkflowRepository{
		workflow: workflows.Workflow{
			ID: workflowID, TenantID: tenantID, ProjectID: projectID,
			Name: "test", CreatedBy: userID,
		},
		version: workflows.Version{
			ID: versionID, WorkflowID: workflowID, TenantID: tenantID,
			Number: 1, State: workflows.VersionPublished,
			Spec:     []byte(`{"apiVersion":"custos.io/v1alpha1","kind":"Workflow","metadata":{"name":"test"},"spec":{"tasks":[]}}`),
			SpecHash: [32]byte{1}, Version: 1, CreatedAt: now,
		},
	}
	repo := &testExecutionRepository{}
	rec := &failingAudit{}
	var logs bytes.Buffer
	service := execsvc.New(execsvc.Deps{
		Repo: repo, Workflows: wfRepo, AZ: &testAuthorizer{},
		Audit:  rec,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	principal := authn.Principal{UserID: userID, Kind: authn.KindUser}
	tenant := tenants.TenantContext{Tenant: tenants.Tenant{ID: tenantID}}
	if _, err := service.Execute(context.Background(), principal, tenant,
		execsvc.ExecuteInput{WorkflowID: workflowID, VersionID: &versionID},
		"audit-fail", [32]byte{1}); err != nil {
		t.Fatalf("Execute must not fail on audit write errors: %v", err)
	}
	if repo.created == nil || rec.calls != 1 {
		t.Fatalf("execution created=%v audit calls=%d", repo.created != nil, rec.calls)
	}
	if !strings.Contains(logs.String(), "audit record failed") ||
		!strings.Contains(logs.String(), "workflow.execution.submitted") {
		t.Fatalf("missing audit failure warning:\n%s", logs.String())
	}
}

func TestTestRunIsDeniedWithoutEitherPermission(t *testing.T) {
	for _, denied := range []authz.Action{authz.WorkflowCreate, authz.WorkflowExecute} {
		t.Run(string(denied), func(t *testing.T) {
			authorizer := &testAuthorizer{denied: denied}
			service, repo, principal, tenant, workflowID, versionID :=
				testExecutionService(workflows.VersionDraft, authorizer, nil)
			_, err := service.Execute(context.Background(), principal, tenant,
				execsvc.ExecuteInput{
					WorkflowID: workflowID, VersionID: &versionID, Test: true,
				}, "denied-test-run", [32]byte{1})
			if err == nil || repo.created != nil {
				t.Fatalf("test run with denied %s = err:%v execution:%+v", denied, err, repo.created)
			}
			if len(authorizer.actions) == 0 || authorizer.actions[0] != authz.WorkflowCreate {
				t.Fatalf("test run permission checks = %v", authorizer.actions)
			}
			if denied == authz.WorkflowExecute &&
				(len(authorizer.actions) != 2 || authorizer.actions[1] != authz.WorkflowExecute) {
				t.Fatalf("workflow.execute not checked: %v", authorizer.actions)
			}
		})
	}
}
