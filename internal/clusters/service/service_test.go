package service_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	clusterpg "github.com/Exonical/custos/internal/clusters/postgres"
	clustersvc "github.com/Exonical/custos/internal/clusters/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/fake"
	"github.com/Exonical/custos/internal/slurm/httpclient"
	"github.com/Exonical/custos/internal/tenants"
	tenantpg "github.com/Exonical/custos/internal/tenants/postgres"
	userpg "github.com/Exonical/custos/internal/users/postgres"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// --- fakes ------------------------------------------------------------------

type fakeFactory struct {
	cluster   slurm.Cluster
	openErr   error
	openCalls int
}

func (f *fakeFactory) Open(_ context.Context, _ slurm.ClusterConfig) (slurm.Cluster, slurm.Accounting, error) {
	f.openCalls++
	if f.openErr != nil {
		return nil, nil, f.openErr
	}
	return f.cluster, nil, nil
}

type fakeResolver struct{ err error }

func (r fakeResolver) Resolve(_ context.Context, ref secrets.Reference) (secrets.Value, error) {
	if r.err != nil {
		return secrets.Value{}, r.err
	}
	if ref.Provider == "" || ref.Path == "" {
		return secrets.Value{}, apperr.New(apperr.Invalid,
			"secrets.unresolvable", "incomplete reference")
	}
	return secrets.NewValue([]byte("secret")), nil
}

type fakeRec struct{ events []audit.Event }

func (r *fakeRec) Record(_ context.Context, e audit.Event) error {
	r.events = append(r.events, e)
	return nil
}

// --- fixture ----------------------------------------------------------------

type fixture struct {
	svc     *clustersvc.Service
	repo    *clusterpg.Repository
	tenants *tenantpg.Repository
	users   *userpg.Repository
	factory *fakeFactory
	rec     *fakeRec
	ctx     context.Context
}

func newFixture(t *testing.T, factory *fakeFactory) *fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	repo := clusterpg.New(pool)
	trepo := tenantpg.New(pool)
	urepo := userpg.New(pool)
	rec := &fakeRec{}
	if factory == nil {
		factory = &fakeFactory{cluster: fake.New()}
	}
	svc := clustersvc.New(clustersvc.Deps{
		Repository: repo, Tenants: trepo, Factory: factory,
		Authorizer: authz.RBAC{}, Recorder: rec,
		DialPolicy: httpclient.DialPolicy{}, // strict: deny private/loopback/http
		Resolver:   fakeResolver{},
		Enqueuer:   pool,
	})
	return &fixture{svc: svc, repo: repo, tenants: trepo, users: urepo,
		factory: factory, rec: rec, ctx: context.Background()}
}

func (f *fixture) mkTenant(t *testing.T, slug string, state tenants.State) tenants.Tenant {
	t.Helper()
	tn := tenants.Tenant{
		ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: "T",
		State: state, Settings: map[string]any{}, Version: 1,
	}
	if err := f.tenants.Create(f.ctx, tn); err != nil {
		t.Fatal(err)
	}
	return tn
}

func (f *fixture) mkCluster(t *testing.T, name string, vis clusters.Visibility) clusters.Cluster {
	t.Helper()
	c := clusters.Cluster{
		ID: uuid.Must(uuid.NewV7()), Name: name, DisplayName: name,
		BaseURL: "https://203.0.113.10/", APIVersion: "v0.0.45",
		IdentityMode: clusters.IdentityService, ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "slurm/token"},
		Visibility: vis, State: clusters.StateUnreachable, Version: 1,
	}
	if err := f.repo.Create(f.ctx, c); err != nil {
		t.Fatal(err)
	}
	return c
}

func admin() authn.Principal {
	return authn.Principal{UserID: uuid.Must(uuid.NewV7()),
		Kind: authn.KindUser, PlatformRoles: []string{"platform-admin"}}
}

func tcFor(tn tenants.Tenant, roles []string) tenants.TenantContext {
	tc := tenants.TenantContext{Tenant: tn}
	if roles != nil {
		tc.Membership = &tenants.Membership{TenantID: tn.ID,
			UserID: uuid.Nil, Roles: roles}
	}
	return tc
}

// --- validation at create ----------------------------------------------------

func TestCreateValidation(t *testing.T) {
	f := newFixture(t, nil)
	p := admin()
	base := clustersvc.CreateInput{
		Name: "c1", DisplayName: "C1", BaseURL: "https://203.0.113.10/",
		APIVersion: "v0.0.45", IdentityMode: "service", ServiceUser: "custos",
		TokenRef:   secrets.Reference{Provider: "file", Path: "tok"},
		Visibility: "assigned",
	}

	t.Run("ssrf metadata ip denied", func(t *testing.T) {
		in := base
		in.Name, in.BaseURL = "c-ssrf", "https://169.254.169.254/"
		_, err := f.svc.Create(f.ctx, p, in)
		if !apperr.Is(err, apperr.Validation) {
			t.Fatalf("want 422, got %v", err)
		}
		var ae *apperr.Error
		if ok := errorAs(err, &ae); !ok || ae.Code != "slurm.dial_denied" {
			t.Fatalf("want slurm.dial_denied, got %v", err)
		}
	})

	t.Run("http denied outside dev", func(t *testing.T) {
		in := base
		in.Name, in.BaseURL = "c-http", "http://203.0.113.10/"
		_, err := f.svc.Create(f.ctx, p, in)
		if !apperr.Is(err, apperr.Validation) {
			t.Fatalf("want 422, got %v", err)
		}
	})

	t.Run("http denied to public even with http allowed", func(t *testing.T) {
		ff := newFixture(t, nil)
		ff.svc = clustersvc.New(clustersvc.Deps{
			Repository: ff.repo, Tenants: ff.tenants, Factory: ff.factory,
			Authorizer: authz.RBAC{}, Recorder: ff.rec,
			DialPolicy: httpclient.DialPolicy{AllowHTTP: true, AllowLoopback: true},
			Resolver:   fakeResolver{}, Enqueuer: nil,
		})
		in := base
		in.Name, in.BaseURL = "c-http2", "http://203.0.113.10/"
		_, err := ff.svc.Create(ff.ctx, p, in)
		if !apperr.Is(err, apperr.Validation) {
			t.Fatalf("want 422 (http non-loopback), got %v", err)
		}
	})

	t.Run("unresolvable token_ref", func(t *testing.T) {
		ff := newFixture(t, nil)
		ff.svc = clustersvc.New(clustersvc.Deps{
			Repository: ff.repo, Tenants: ff.tenants, Factory: ff.factory,
			Authorizer: authz.RBAC{}, Recorder: ff.rec,
			DialPolicy: httpclient.DialPolicy{},
			Resolver: fakeResolver{err: apperr.New(apperr.NotFound,
				"secrets.unresolvable", "missing")},
			Enqueuer: nil,
		})
		in := base
		in.Name = "c-badref"
		_, err := ff.svc.Create(ff.ctx, p, in)
		if !apperr.Is(err, apperr.Validation) {
			t.Fatalf("want 422, got %v", err)
		}
	})

	t.Run("ok", func(t *testing.T) {
		in := base
		in.Name = "c-ok"
		c, err := f.svc.Create(f.ctx, p, in)
		if err != nil {
			t.Fatal(err)
		}
		if c.State != clusters.StateUnreachable {
			t.Fatalf("initial state: %v", c.State)
		}
	})
}

func errorAs(err error, target any) bool {
	// small errors.As wrapper to keep imports tidy
	if e, ok := err.(*apperr.Error); ok {
		if p, ok := target.(**apperr.Error); ok {
			*p = e
			return true
		}
	}
	return false
}

// --- test connection ----------------------------------------------------------

func TestTestConnection(t *testing.T) {
	f := newFixture(t, nil)
	f.mkCluster(t, "conn", clusters.VisibilityAssigned)
	p := admin()

	res, err := f.svc.TestConnection(f.ctx, p, "conn")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("want ok, got %+v", res)
	}
	// state must not be persisted by the test
	got, _ := f.repo.GetByNameOrID(f.ctx, "conn")
	if got.State != clusters.StateUnreachable || got.LastSyncAt != nil {
		t.Fatalf("test must not persist state: %+v", got)
	}

	// failing factory -> error_code, still no state change
	f2 := newFixture(t, &fakeFactory{openErr: fmt.Errorf("dial refused")})
	f2.mkCluster(t, "conn2", clusters.VisibilityAssigned)
	res, err = f2.svc.TestConnection(f2.ctx, p, "conn2")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.ErrorCode == "" {
		t.Fatalf("want error_code, got %+v", res)
	}
	got, _ = f2.repo.GetByNameOrID(f2.ctx, "conn2")
	if got.State != clusters.StateUnreachable {
		t.Fatalf("state changed: %+v", got)
	}
}

// --- assignments ---------------------------------------------------------------

func TestAssignUnassign(t *testing.T) {
	f := newFixture(t, nil)
	p := admin()
	tn := f.mkTenant(t, "t-a", tenants.StateActive)
	c := f.mkCluster(t, "asg", clusters.VisibilityAssigned)

	err := f.svc.Assign(f.ctx, p, "asg", tn.Slug,
		clusters.AssignmentDefaults{AllowedPartitions: []string{"batch"}})
	if err != nil {
		t.Fatal(err)
	}
	as, _, err := f.repo.ListAssignments(f.ctx, tenants.PlatformScope(),
		c.ID, clusters.Page{})
	if err != nil || len(as) != 1 || as[0].Source != clusters.SourceManual ||
		as[0].Defaults.AllowedPartitions[0] != "batch" {
		t.Fatalf("assign: %v %+v", err, as)
	}

	if err := f.svc.Unassign(f.ctx, p, "asg", tn.Slug); err != nil {
		t.Fatal(err)
	}
	as, _, _ = f.repo.ListAssignments(f.ctx, tenants.PlatformScope(),
		c.ID, clusters.Page{})
	if len(as) != 0 {
		t.Fatalf("unassign left rows: %+v", as)
	}

	// all_tenants refuses manual assignment manipulation
	ca := f.mkCluster(t, "asg-all", clusters.VisibilityAllTenants)
	err = f.svc.Assign(f.ctx, p, "asg-all", tn.Slug, clusters.AssignmentDefaults{})
	if !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want conflict for all_tenants assign, got %v", err)
	}
	err = f.svc.Unassign(f.ctx, p, "asg-all", tn.Slug)
	if !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("want conflict for all_tenants unassign, got %v", err)
	}
	_ = ca
}

// --- tenant-facing views --------------------------------------------------------

func TestTenantVisibleViews(t *testing.T) {
	f := newFixture(t, nil)
	p := admin()
	ta := f.mkTenant(t, "vis-a", tenants.StateActive)
	tb := f.mkTenant(t, "vis-b", tenants.StateActive)
	c := f.mkCluster(t, "vis", clusters.VisibilityAssigned)

	// unassigned: A sees nothing
	listA, err := f.svc.ListVisible(f.ctx, p, tcFor(ta, []string{"researcher"}))
	if err != nil || len(listA) != 0 {
		t.Fatalf("unassigned list: %v %d", err, len(listA))
	}
	if err := f.svc.Assign(f.ctx, p, "vis", ta.Slug,
		clusters.AssignmentDefaults{AllowedPartitions: []string{"gpu"}}); err != nil {
		t.Fatal(err)
	}

	// seed capabilities + partitions via RecordSyncResult
	_, err = f.repo.RecordSyncResult(f.ctx, c.ID, clusters.SyncResult{
		OK: true, At: time.Now(),
		Capabilities: &slurm.Capabilities{
			SlurmVersion: "26.05.0", APIVersion: "v0.0.45",
			Partitions: []slurm.Partition{
				{Name: "gpu", Nodes: 4}, {Name: "batch", Nodes: 8},
			},
			GRESTypes:   []string{"gpu:h100"},
			NodeSummary: map[string]int{"idle": 10, "alloc": 2},
		},
		Partitions: []slurm.Partition{
			{Name: "gpu", Nodes: 4}, {Name: "batch", Nodes: 8},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	listA, err = f.svc.ListVisible(f.ctx, p, tcFor(ta, []string{"researcher"}))
	if err != nil || len(listA) != 1 {
		t.Fatalf("assigned list: %v %d", err, len(listA))
	}
	s := listA[0]
	if s.Name != "vis" || s.SlurmVersion != "26.05.0" {
		t.Fatalf("summary: %+v", s)
	}
	// allowed_partitions filters the partition names
	if len(s.Partitions) != 1 || s.Partitions[0] != "gpu" {
		t.Fatalf("filtered partitions: %v", s.Partitions)
	}
	// B (not assigned) still sees nothing
	listB, _ := f.svc.ListVisible(f.ctx, p, tcFor(tb, []string{"researcher"}))
	if len(listB) != 0 {
		t.Fatalf("B should see none: %+v", listB)
	}

	// partitions endpoint filtered the same way
	recs, err := f.svc.ListPartitions(f.ctx, p, tcFor(ta, []string{"researcher"}), "vis")
	if err != nil || len(recs) != 1 || recs[0].Name != "gpu" {
		t.Fatalf("partitions: %v %+v", err, recs)
	}
	// unassigned tenant -> 404
	_, err = f.svc.ListPartitions(f.ctx, p, tcFor(tb, []string{"researcher"}), "vis")
	if !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("want notfound, got %v", err)
	}
}

// --- authorization matrix --------------------------------------------------------

func TestAuthorizationMatrix(t *testing.T) {
	f := newFixture(t, nil)
	tn := f.mkTenant(t, "mx", tenants.StateActive)
	f.mkCluster(t, "mx-c", clusters.VisibilityAssigned)
	if err := f.svc.Assign(f.ctx, admin(), "mx-c", tn.Slug,
		clusters.AssignmentDefaults{}); err != nil {
		t.Fatal(err)
	}

	mkP := func(roles ...string) authn.Principal {
		return authn.Principal{UserID: uuid.Must(uuid.NewV7()),
			Kind: authn.KindUser, PlatformRoles: roles}
	}
	member := func(roles []string) tenants.TenantContext {
		return tcFor(tn, roles)
	}

	cases := []struct {
		name string
		run  func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error
		// admin, auditor, tenant-admin, operator, researcher, viewer, non-member
		want [7]bool
	}{
		{"Create", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			_, err := f.svc.Create(ctx, p, clustersvc.CreateInput{
				Name: "mx-" + uuid.NewString()[:8], DisplayName: "x",
				BaseURL: "https://203.0.113.10/", APIVersion: "v0.0.45",
				IdentityMode: "service", ServiceUser: "custos",
				TokenRef:   secrets.Reference{Provider: "file", Path: "t"},
				Visibility: "assigned",
			})
			return err
		}, [7]bool{true, false, false, false, false, false, false}},
		{"List", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			_, _, err := f.svc.List(ctx, p, clusters.Page{})
			return err
		}, [7]bool{true, true, false, false, false, false, false}},
		{"Get", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			_, err := f.svc.Get(ctx, p, "mx-c")
			return err
		}, [7]bool{true, true, false, false, false, false, false}},
		{"Update", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			n := "y"
			_, err := f.svc.Update(ctx, p, "mx-c", clustersvc.UpdateInput{
				DisplayName: &n, Version: 1})
			return err
		}, [7]bool{true, false, false, false, false, false, false}},
		{"Disable", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			_, err := f.svc.Disable(ctx, p, "mx-c")
			return err
		}, [7]bool{true, false, false, false, false, false, false}},
		{"TestConnection", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			_, err := f.svc.TestConnection(ctx, p, "mx-c")
			return err
		}, [7]bool{true, false, false, false, false, false, false}},
		{"Assign", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			return f.svc.Assign(ctx, p, "mx-c", tn.Slug, clusters.AssignmentDefaults{})
		}, [7]bool{true, false, false, false, false, false, false}},
		{"Unassign", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			return f.svc.Unassign(ctx, p, "mx-c", tn.Slug)
		}, [7]bool{true, false, false, false, false, false, false}},
		{"ListAssignments", func(ctx context.Context, p authn.Principal, _ tenants.TenantContext) error {
			_, _, err := f.svc.ListAssignments(ctx, p, "mx-c", clusters.Page{})
			return err
		}, [7]bool{true, false, false, false, false, false, false}},
		{"ListVisible", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, err := f.svc.ListVisible(ctx, p, tc)
			return err
		}, [7]bool{true, true, true, true, true, true, false}},
		{"GetVisible", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, err := f.svc.GetVisible(ctx, p, tc, "mx-c")
			return err
		}, [7]bool{true, true, true, true, true, true, false}},
		{"ListPartitions", func(ctx context.Context, p authn.Principal, tc tenants.TenantContext) error {
			_, err := f.svc.ListPartitions(ctx, p, tc, "mx-c")
			return err
		}, [7]bool{true, true, true, true, true, true, false}},
	}

	for _, c := range cases {
		callers := []struct {
			p  authn.Principal
			tc tenants.TenantContext
		}{
			{mkP("platform-admin"), member(nil)},
			{mkP("platform-auditor"), member(nil)},
			{mkP(), member([]string{"tenant-admin"})},
			{mkP(), member([]string{"tenant-operator"})},
			{mkP(), member([]string{"researcher"})},
			{mkP(), member([]string{"viewer"})},
			{mkP(), member(nil)},
		}
		for i, caller := range callers {
			ctx := tenants.WithTenantContext(f.ctx, caller.tc)
			err := c.run(ctx, caller.p, caller.tc)
			allowed := err == nil || !apperr.Is(err, apperr.Forbidden)
			if allowed != c.want[i] {
				t.Errorf("%s caller %d: allowed=%v want=%v (err=%v)",
					c.name, i, allowed, c.want[i], err)
			}
		}
	}
}
