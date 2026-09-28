package policysync

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/projects"
	projectpg "github.com/Exonical/custos/internal/projects/postgres"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/fake"
	"github.com/Exonical/custos/internal/tenants"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type policyTestFactory struct{ c *fake.Cluster }
type policyAudit struct{ events []audit.Event }

func (a *policyAudit) Record(_ context.Context, event audit.Event) error {
	a.events = append(a.events, event)
	return nil
}

func (f policyTestFactory) Open(context.Context, slurm.ClusterConfig) (slurm.Cluster, slurm.Accounting, error) {
	return f.c, f.c, nil
}

type policyFixture struct {
	pool                 *pgxpool.Pool
	deps                 Deps
	fake                 *fake.Cluster
	clusterID, bindingID uuid.UUID
}

func newPolicyFixture(t *testing.T, mode string) policyFixture {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Pool(t)
	tid, pid, cid, bid := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	slug := "ps-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name,state) VALUES($1,$2,$3,'active')`, tid, slug, "Policy test"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO projects(id,tenant_id,slug,name,state) VALUES($1,$2,'proj','Policy project','active')`, pid, tid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	crepo := clusterpg.New(pool)
	c := clusters.Cluster{ID: cid, Name: "c-" + slug, DisplayName: "Policy cluster", BaseURL: "https://slurm.example", APIVersion: "v0.0.45", IdentityMode: clusters.IdentityService, ServiceUser: "custos", PolicyManagement: mode, PolicyParentAccount: "root", TokenRef: secrets.Reference{Provider: "file", Path: "/unused"}, Visibility: clusters.VisibilityAssigned, State: clusters.StateActive, Version: 1}
	if err := crepo.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	caps := &slurm.Capabilities{Partitions: []slurm.Partition{{Name: "gpu"}}, GRESTypes: []string{"gpu:h100"}}
	if _, err := crepo.RecordSyncResult(ctx, cid, clusters.SyncResult{OK: true, Capabilities: caps, Partitions: caps.Partitions, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := crepo.UpsertAssignment(ctx, tenants.PlatformScope(), clusters.Assignment{ClusterID: cid, TenantID: tid, Source: clusters.SourceManual}); err != nil {
		t.Fatal(err)
	}
	prepo := projectpg.New(pool)
	binding := projects.ClusterBinding{ID: bid, TenantID: tid, ProjectID: pid, ClusterID: cid, SlurmAccount: "acct-" + slug, DefaultPartition: "gpu", AllowedPartitions: []string{"gpu"}, DefaultQoS: "normal", AllowedQoS: []string{"normal"}, Enabled: true, Version: 1}
	if err := prepo.CreateBinding(ctx, tenants.TenantScope(tid), binding); err != nil {
		t.Fatal(err)
	}
	fc := fake.New()
	fc.SetQoS([]slurm.QoS{{Name: "normal"}})
	deps := Deps{Clusters: crepo, Bindings: prepo, Factory: policyTestFactory{fc}, Pool: pool, Metrics: NewMetrics(nil), ConfigMode: "enforce"}
	return policyFixture{pool: pool, deps: deps, fake: fc, clusterID: cid, bindingID: bid}
}

func bindingForTest(t *testing.T, f policyFixture) projects.ClusterBinding {
	t.Helper()
	b, err := f.deps.Bindings.BindingByID(context.Background(), f.bindingID)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPolicyReportModeDoesNotWrite(t *testing.T) {
	f := newPolicyFixture(t, "report")
	if err := f.deps.Reconcile(context.Background(), f.clusterID); err != nil {
		t.Fatal(err)
	}
	if calls := f.fake.AdminCalls(); len(calls) != 0 {
		t.Fatalf("report mode wrote to Slurm: %+v", calls)
	}
	b := bindingForTest(t, f)
	if b.DriftState != "drift" || !hasDrift(b.Drift, "ACCOUNT_MISSING") {
		t.Fatalf("binding drift=%+v", b)
	}
	summary, err := NewSummaryService(f.deps).Summary(context.Background(), f.clusterID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Mode != "report" || summary.OpsApplied != 0 {
		t.Fatalf("report summary=%+v", summary)
	}
}

func TestPolicyEnforceConverges(t *testing.T) {
	f := newPolicyFixture(t, "enforce")
	recorder := &policyAudit{}
	f.deps.Audit = recorder
	if err := f.deps.Reconcile(context.Background(), f.clusterID); err != nil {
		t.Fatal(err)
	}
	if calls := f.fake.AdminCalls(); len(calls) != 3 {
		t.Fatalf("calls=%+v", calls)
	}
	if len(recorder.events) != 3 {
		t.Fatalf("policy operation audits=%+v", recorder.events)
	}
	for _, event := range recorder.events {
		if event.Action != "slurm.policy_applied" || event.TenantID == nil || event.Details["after"] == nil {
			t.Fatalf("operation audit=%+v", event)
		}
	}
	b := bindingForTest(t, f)
	if b.DriftState != "ok" || len(b.Drift) != 0 {
		t.Fatalf("binding did not converge: %+v", b)
	}
	summary, err := NewSummaryService(f.deps).Summary(context.Background(), f.clusterID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Mode != "enforce" || summary.OpsApplied != 3 || summary.LastAppliedAt == nil {
		t.Fatalf("policy summary=%+v", summary)
	}
	if err := f.deps.Reconcile(context.Background(), f.clusterID); err != nil {
		t.Fatal(err)
	}
	if calls := f.fake.AdminCalls(); len(calls) != 3 {
		t.Fatalf("converged policy re-wrote Slurm: %+v", calls)
	}
}

func TestApplyPreExistingAssociationOmitsSiteMetadata(t *testing.T) {
	before := slurm.Association{Account: "acct", Cluster: "c1", User: "custos", Partition: "debug", QoS: []string{"high"}, ParentAccount: "physics", Comment: "site-owned"}
	after := before
	after.QoS = []string{"normal"}
	f := fake.New()
	op := Op{Kind: OpUpsertAssociation, Key: AssociationKey(after), Before: &ObjectState{Association: &before}, After: &ObjectState{Association: &after}}
	if err := (Deps{}).applyOp(context.Background(), f, uuid.New(), nil, op); err != nil {
		t.Fatal(err)
	}
	calls := f.AdminCalls()
	if len(calls) != 1 || calls[0].Association == nil {
		t.Fatalf("admin calls=%+v", calls)
	}
	if calls[0].Association.ParentAccount != "" || calls[0].Association.Comment != "" || calls[0].Association.QoS[0] != "normal" {
		t.Fatalf("site metadata passed to slurmdbd: %+v", calls[0].Association)
	}
}

func TestPolicyForbiddenStopsWritesAndSetsError(t *testing.T) {
	f := newPolicyFixture(t, "enforce")
	f.fake.FailNextAdmin(1, slurm.ErrForbidden)
	err := Handler(f.deps)(context.Background(), workqueue.Item{Kind: Kind, Key: "cluster:" + f.clusterID.String()})
	var reschedule workqueue.Reschedule
	if !errors.As(err, &reschedule) {
		t.Fatalf("forbidden run result=%v, want reschedule", err)
	}
	calls := f.fake.AdminCalls()
	if len(calls) != 1 {
		t.Fatalf("writes continued after forbidden result: %+v", calls)
	}
	b := bindingForTest(t, f)
	if b.DriftState != "error" || !hasDrift(b.Drift, "PERMISSION_DENIED") {
		t.Fatalf("binding=%+v", b)
	}
	summary, err := NewSummaryService(f.deps).Summary(context.Background(), f.clusterID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Errors != 1 || summary.OpsFailed != 1 {
		t.Fatalf("forbidden summary=%+v", summary)
	}
}

func TestPolicyUnavailablePreservesExistingDrift(t *testing.T) {
	f := newPolicyFixture(t, "enforce")
	old := []projects.DriftItem{{Code: "ASSOCIATION_EXTRA", Detail: "keep"}}
	if _, err := f.deps.Bindings.SetBindingDrift(context.Background(), f.bindingID, "drift", old, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.fake.FailNext(1, slurm.ErrUnavailable)
	if err := f.deps.Reconcile(context.Background(), f.clusterID); err != nil {
		t.Fatal(err)
	}
	b := bindingForTest(t, f)
	if b.DriftState != "unknown" || len(b.Drift) != 1 || b.Drift[0] != old[0] {
		t.Fatalf("unavailable erased drift: %+v", b)
	}
	if len(f.fake.AdminCalls()) != 0 {
		t.Fatalf("unavailable accounting wrote policy: %+v", f.fake.AdminCalls())
	}
}

func TestPolicyRejectedOperationContinuesAndRecordsDrift(t *testing.T) {
	f := newPolicyFixture(t, "enforce")
	f.fake.FailNextAdmin(1, slurm.ErrRejected)
	if err := f.deps.Reconcile(context.Background(), f.clusterID); err != nil {
		t.Fatal(err)
	}
	calls := f.fake.AdminCalls()
	if len(calls) != 3 {
		t.Fatalf("rejected operation stopped later writes: %+v", calls)
	}
	b := bindingForTest(t, f)
	if b.DriftState != "drift" || !hasDrift(b.Drift, "SLURM_REJECTED") {
		t.Fatalf("rejected operation did not leave a drift finding: %+v", b)
	}
	summary, err := NewSummaryService(f.deps).Summary(context.Background(), f.clusterID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OpsApplied != 2 || summary.OpsFailed != 1 || summary.LastError == "" {
		t.Fatalf("rejected-operation summary=%+v", summary)
	}
}

func hasDrift(items []projects.DriftItem, code string) bool {
	for _, item := range items {
		if item.Code == code {
			return true
		}
	}
	return false
}
