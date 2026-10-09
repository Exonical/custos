package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/nodehooks"
	"github.com/Exonical/custos/internal/nodehooks/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/tenants"
)

// --- fakes ---

type fakeRepo struct {
	mu       sync.Mutex
	stored   *nodehooks.Stored
	putErr   error
	puts     int
	tokens   map[string]nodehooks.Token // by token sha256
	hashes   []string
	revoked  map[uuid.UUID]bool
	statuses []nodehooks.NodeStatus
}

func newRepo() *fakeRepo {
	return &fakeRepo{tokens: map[string]nodehooks.Token{}, revoked: map[uuid.UUID]bool{}}
}

func (r *fakeRepo) GetConfig(_ context.Context, _ uuid.UUID) (nodehooks.Stored, error) {
	if r.stored == nil {
		return nodehooks.Stored{}, apperr.New(apperr.NotFound, "NOT_FOUND", "not found")
	}
	return *r.stored, nil
}

func (r *fakeRepo) PutConfig(_ context.Context, id uuid.UUID, cfg nodehooks.Config,
	sha string, ver int, by *uuid.UUID) (nodehooks.Stored, error) {
	if r.putErr != nil {
		return nodehooks.Stored{}, r.putErr
	}
	r.puts++
	rev := int64(1)
	if r.stored != nil {
		rev = r.stored.Revision + 1
	}
	st := nodehooks.Stored{ClusterID: id, Config: cfg, Revision: rev, ContentSHA256: sha,
		Version: ver + 1, UpdatedAt: time.Unix(1, 0), UpdatedBy: by}
	r.stored = &st
	return st, nil
}

func (r *fakeRepo) CreateToken(_ context.Context, t nodehooks.Token, sha string) error {
	r.hashes = append(r.hashes, sha)
	r.tokens[sha] = t
	return nil
}
func (r *fakeRepo) ListTokens(context.Context, uuid.UUID) ([]nodehooks.Token, error) { return nil, nil }
func (r *fakeRepo) RevokeToken(_ context.Context, _, id uuid.UUID) error {
	r.revoked[id] = true
	return nil
}
func (r *fakeRepo) LookupToken(_ context.Context, sha string) (nodehooks.Token, error) {
	t, ok := r.tokens[sha]
	if !ok || r.revoked[t.ID] {
		return nodehooks.Token{}, apperr.New(apperr.NotFound, "NOT_FOUND", "not found")
	}
	return t, nil
}
func (r *fakeRepo) TouchToken(context.Context, uuid.UUID, time.Duration) error { return nil }
func (r *fakeRepo) UpsertStatus(_ context.Context, s nodehooks.NodeStatus, _ time.Duration) error {
	r.statuses = append(r.statuses, s)
	return nil
}
func (r *fakeRepo) ListStatus(context.Context, uuid.UUID) ([]nodehooks.NodeStatus, error) {
	return r.statuses, nil
}

type fakeClusters struct {
	clusters.Repository // unused methods panic on nil
	c                   clusters.Cluster
	assigned            map[uuid.UUID]bool
}

func (f *fakeClusters) GetByNameOrID(_ context.Context, ref string) (clusters.Cluster, error) {
	if ref != f.c.Name && ref != f.c.ID.String() {
		return clusters.Cluster{}, apperr.New(apperr.NotFound, "NOT_FOUND", "cluster not found")
	}
	return f.c, nil
}

func (f *fakeClusters) GetAssignment(_ context.Context, _ tenants.Scope, _, tenantID uuid.UUID) (clusters.Assignment, error) {
	if !f.assigned[tenantID] {
		return clusters.Assignment{}, apperr.New(apperr.NotFound, "NOT_FOUND", "no assignment")
	}
	return clusters.Assignment{TenantID: tenantID}, nil
}

type fakeTenants struct {
	tenants.Repository
	byID map[string]tenants.Tenant
}

func (f *fakeTenants) GetBySlugOrID(_ context.Context, _ tenants.Scope, ref string) (tenants.Tenant, error) {
	t, ok := f.byID[ref]
	if !ok {
		return tenants.Tenant{}, apperr.New(apperr.NotFound, "NOT_FOUND", "tenant not found")
	}
	return t, nil
}

type fakeBindings struct {
	projects.BindingRepository
	list []projects.ClusterBinding
}

func (f *fakeBindings) ListBindingsByCluster(context.Context, uuid.UUID) ([]projects.ClusterBinding, error) {
	return f.list, nil
}

type recorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recorder) Record(_ context.Context, e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

// --- harness ---

type harness struct {
	svc      *service.Service
	repo     *fakeRepo
	clusters *fakeClusters
	tenants  *fakeTenants
	bindings *fakeBindings
	rec      *recorder
	admin    authn.Principal
	tenantA  uuid.UUID
	tenantB  uuid.UUID
}

func newHarness() *harness {
	h := &harness{
		repo: newRepo(),
		clusters: &fakeClusters{
			c: clusters.Cluster{ID: uuid.New(), Name: "c1", State: clusters.StateActive,
				IdentityMode: clusters.IdentityService, ServiceUser: "custos",
				Visibility:   clusters.VisibilityAssigned,
				Capabilities: &slurm.Capabilities{SlurmVersion: "26.05.4"}},
			assigned: map[uuid.UUID]bool{},
		},
		tenants:  &fakeTenants{byID: map[string]tenants.Tenant{}},
		bindings: &fakeBindings{},
		rec:      &recorder{},
		admin:    authn.Principal{UserID: uuid.New(), PlatformRoles: []string{"platform-admin"}},
		tenantA:  uuid.New(),
		tenantB:  uuid.New(),
	}
	h.tenants.byID[h.tenantA.String()] = tenants.Tenant{ID: h.tenantA, Slug: "ta", State: tenants.StateActive}
	h.tenants.byID[h.tenantB.String()] = tenants.Tenant{ID: h.tenantB, Slug: "tb", State: tenants.StateActive}
	h.clusters.assigned[h.tenantA] = true
	h.clusters.assigned[h.tenantB] = true
	h.svc = service.New(service.Deps{Repo: h.repo, Clusters: h.clusters, Tenants: h.tenants,
		Bindings: h.bindings, AZ: authz.RBAC{}, Audit: h.rec})
	return h
}

func (h *harness) config() nodehooks.Config {
	return nodehooks.Config{
		IsolationMode: "namespace", MountTimeoutSeconds: 30,
		SharedMounts: []nodehooks.Mount{{Name: "apps", FSType: "nfs4",
			Source: "server:/hpc/apps", Target: "/apps", Options: []string{"ro"}}},
		TenantMounts: []nodehooks.TenantMount{{Tenant: h.tenantA.String(), Name: "data",
			FSType: "nfs4", Source: "server:/tenantA/flight_data", Target: "/mnt/data"}},
		Hooks: []nodehooks.Hook{{Name: "metrics", Phase: "prolog", Order: 1,
			Script: "#!/bin/bash\necho TOP-SECRET-BODY\n"}},
	}
}

func bind(tenant uuid.UUID, account string) projects.ClusterBinding {
	return projects.ClusterBinding{ID: uuid.New(), TenantID: tenant, SlurmAccount: account, Enabled: true}
}

func code(t *testing.T, err error) string {
	t.Helper()
	var ae *apperr.Error
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errorsAs(err, &ae) {
		t.Fatalf("not an apperr: %v", err)
	}
	return ae.Code
}

func errorsAs(err error, target **apperr.Error) bool {
	for err != nil {
		if ae, ok := err.(*apperr.Error); ok {
			*target = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// --- PutConfig ---

func TestPutConfigValidationDetails(t *testing.T) {
	h := newHarness()
	cfg := h.config()
	cfg.SharedMounts[0].Target = "/etc/shadow"
	cfg.Hooks[0].Script = "echo nope"
	_, err := h.svc.PutConfig(context.Background(), h.admin, "c1", cfg, 0)
	var ae *apperr.Error
	if err == nil || !errorsAs(err, &ae) || ae.Kind != apperr.Validation {
		t.Fatalf("want validation error, got %v", err)
	}
	if ae.Code != nodehooks.CodeConfigInvalid {
		t.Fatalf("mixed mount+hook errors use %s, got %s", nodehooks.CodeConfigInvalid, ae.Code)
	}
	fields := map[string]bool{}
	for _, d := range ae.Details {
		fields[d.Field] = true
	}
	if !fields["shared_mounts[0].target"] || !fields["hooks[0].script"] {
		t.Fatalf("details = %+v", ae.Details)
	}
	if h.repo.puts != 0 {
		t.Fatal("invalid config must not be stored")
	}
}

func TestPutConfigSingleCategoryCode(t *testing.T) {
	h := newHarness()
	cfg := h.config()
	cfg.SharedMounts[0].Target = "/etc/shadow"
	_, err := h.svc.PutConfig(context.Background(), h.admin, "c1", cfg, 0)
	if got := code(t, err); got != nodehooks.CodeMountInvalid {
		t.Fatalf("code = %s", got)
	}
	cfg = h.config()
	cfg.Hooks[0].Phase = "boot"
	_, err = h.svc.PutConfig(context.Background(), h.admin, "c1", cfg, 0)
	if got := code(t, err); got != nodehooks.CodeHookInvalid {
		t.Fatalf("code = %s", got)
	}
}

func TestPutConfigTenantChecks(t *testing.T) {
	ctx := context.Background()
	t.Run("unknown tenant", func(t *testing.T) {
		h := newHarness()
		cfg := h.config()
		cfg.TenantMounts[0].Tenant = uuid.NewString()
		_, err := h.svc.PutConfig(ctx, h.admin, "c1", cfg, 0)
		if got := code(t, err); got != nodehooks.CodeMountInvalid {
			t.Fatalf("code = %s", got)
		}
	})
	t.Run("deleted tenant", func(t *testing.T) {
		h := newHarness()
		d := h.tenants.byID[h.tenantA.String()]
		d.State = tenants.StateDeleted
		h.tenants.byID[h.tenantA.String()] = d
		_, err := h.svc.PutConfig(ctx, h.admin, "c1", h.config(), 0)
		if got := code(t, err); got != nodehooks.CodeMountInvalid {
			t.Fatalf("code = %s", got)
		}
	})
	t.Run("not assigned", func(t *testing.T) {
		h := newHarness()
		delete(h.clusters.assigned, h.tenantA)
		_, err := h.svc.PutConfig(ctx, h.admin, "c1", h.config(), 0)
		if got := code(t, err); got != nodehooks.CodeMountInvalid {
			t.Fatalf("code = %s", got)
		}
	})
	t.Run("not assigned but all_tenants visibility passes", func(t *testing.T) {
		h := newHarness()
		delete(h.clusters.assigned, h.tenantA)
		h.clusters.c.Visibility = clusters.VisibilityAllTenants
		if _, err := h.svc.PutConfig(ctx, h.admin, "c1", h.config(), 0); err != nil {
			t.Fatalf("all_tenants cluster must accept tenant: %v", err)
		}
	})
}

func TestPutConfigVersionConflictPassthrough(t *testing.T) {
	h := newHarness()
	h.repo.putErr = apperr.New(apperr.Conflict, "VERSION_CONFLICT", "node config version conflict")
	_, err := h.svc.PutConfig(context.Background(), h.admin, "c1", h.config(), 3)
	if got := code(t, err); got != "VERSION_CONFLICT" {
		t.Fatalf("code = %s", got)
	}
	if len(h.rec.events) != 0 {
		t.Fatal("failed update must not be audited as updated")
	}
}

func TestPutConfigAuditCarriesHookHashNotBody(t *testing.T) {
	h := newHarness()
	if _, err := h.svc.PutConfig(context.Background(), h.admin, "c1", h.config(), 0); err != nil {
		t.Fatal(err)
	}
	if len(h.rec.events) != 1 || h.rec.events[0].Action != "cluster.node_config.updated" {
		t.Fatalf("events = %+v", h.rec.events)
	}
	b, _ := json.Marshal(h.rec.events[0])
	s := string(b)
	if strings.Contains(s, "TOP-SECRET-BODY") || strings.Contains(s, "#!/bin/bash") {
		t.Fatalf("audit leaked script body: %s", s)
	}
	hooks, _ := h.rec.events[0].Details["hooks"].([]map[string]any)
	if len(hooks) != 1 || hooks[0]["name"] != "metrics" || len(hooks[0]["sha256"].(string)) != 64 {
		t.Fatalf("hook audit = %+v", h.rec.events[0].Details["hooks"])
	}
	if h.rec.events[0].Details["revision"] != int64(1) {
		t.Fatalf("revision = %v", h.rec.events[0].Details["revision"])
	}
}

// --- warnings ---

func warningCodes(v service.View) map[string]bool {
	out := map[string]bool{}
	for _, w := range v.Warnings {
		out[w.Code] = true
	}
	return out
}

func TestWarningsSharedServiceUser(t *testing.T) {
	ctx := context.Background()
	h := newHarness()
	if _, err := h.svc.PutConfig(ctx, h.admin, "c1", h.config(), 0); err != nil {
		t.Fatal(err)
	}
	h.bindings.list = []projects.ClusterBinding{bind(h.tenantA, "a1")}
	v, _ := h.svc.GetConfig(ctx, h.admin, "c1")
	if warningCodes(v)[service.WarnSharedServiceUser] {
		t.Fatal("one tenant must not warn")
	}
	h.bindings.list = append(h.bindings.list, bind(h.tenantB, "b1"))
	v, _ = h.svc.GetConfig(ctx, h.admin, "c1")
	if !warningCodes(v)[service.WarnSharedServiceUser] {
		t.Fatal("two tenants on one service user must warn")
	}
	h.clusters.c.IdentityMode = clusters.IdentityImpersonate
	v, _ = h.svc.GetConfig(ctx, h.admin, "c1")
	if warningCodes(v)[service.WarnSharedServiceUser] {
		t.Fatal("impersonation mode must not warn")
	}
	h.clusters.c.IdentityMode = clusters.IdentityService
	empty := h.config()
	empty.TenantMounts = nil
	if _, err := h.svc.PutConfig(ctx, h.admin, "c1", empty, 1); err != nil {
		t.Fatal(err)
	}
	v, _ = h.svc.GetConfig(ctx, h.admin, "c1")
	if warningCodes(v)[service.WarnSharedServiceUser] {
		t.Fatal("no tenant mounts must not warn")
	}
}

func TestWarningsNamespaceVersion(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		caps *slurm.Capabilities
		mode string
		warn bool
	}{
		{"unknown (no capabilities)", nil, "namespace", true},
		{"unknown (empty version)", &slurm.Capabilities{}, "namespace", true},
		{"25.05", &slurm.Capabilities{SlurmVersion: "25.05.3"}, "namespace", true},
		{"25.11", &slurm.Capabilities{SlurmVersion: "25.11.0"}, "namespace", false},
		{"26.05", &slurm.Capabilities{SlurmVersion: "26.05.4"}, "namespace", false},
		{"24.11 prefixed", &slurm.Capabilities{SlurmVersion: "slurm 24.11.2"}, "namespace", true},
		{"old but host mode", &slurm.Capabilities{SlurmVersion: "23.02.0"}, "tenant_exclusive", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			h.clusters.c.Capabilities = tc.caps
			cfg := h.config()
			cfg.IsolationMode = tc.mode
			if _, err := h.svc.PutConfig(ctx, h.admin, "c1", cfg, 0); err != nil {
				t.Fatal(err)
			}
			v, err := h.svc.GetConfig(ctx, h.admin, "c1")
			if err != nil {
				t.Fatal(err)
			}
			if got := warningCodes(v)[service.WarnNamespaceVersion]; got != tc.warn {
				t.Fatalf("warn = %v, want %v (%+v)", got, tc.warn, v.Warnings)
			}
		})
	}
}

// --- tokens and pull ---

func (h *harness) token(t *testing.T) service.NewToken {
	t.Helper()
	nt, err := h.svc.CreateToken(context.Background(), h.admin, "c1", "rack1")
	if err != nil {
		t.Fatal(err)
	}
	return nt
}

func TestCreateToken(t *testing.T) {
	h := newHarness()
	a, b := h.token(t), h.token(t)
	if !strings.HasPrefix(a.Secret, "cnt_") || len(a.Secret) != len("cnt_")+43 {
		t.Fatalf("secret = %q", a.Secret)
	}
	if a.Secret == b.Secret {
		t.Fatal("tokens must be unique")
	}
	for _, hsh := range h.repo.hashes {
		if len(hsh) != 64 || strings.Contains(hsh, "cnt_") {
			t.Fatalf("repo must only see sha256 hex, got %q", hsh)
		}
		if hsh == a.Secret || hsh == b.Secret {
			t.Fatal("secret stored in plaintext")
		}
	}
	for _, name := range []string{"", "Bad Name", "UPPER", "-lead", strings.Repeat("a", 64)} {
		_, err := h.svc.CreateToken(context.Background(), h.admin, "c1", name)
		if got := code(t, err); got != "NODE_TOKEN_INVALID" {
			t.Fatalf("name %q: code %s", name, got)
		}
	}
}

func TestPullAuthAndValidation(t *testing.T) {
	ctx := context.Background()
	h := newHarness()
	nt := h.token(t)

	for _, secret := range []string{"", "garbage", "cnt_" + strings.Repeat("A", 43)} {
		_, err := h.svc.Pull(ctx, secret, "c1", "")
		if got := code(t, err); got != "NODE_TOKEN_INVALID" {
			t.Fatalf("secret %q: %s", secret, got)
		}
		var ae *apperr.Error
		errorsAs(err, &ae)
		if ae.Kind != apperr.Unauthenticated {
			t.Fatalf("kind = %v", ae.Kind)
		}
	}
	for _, node := range []string{"", "bad node", "a/b", strings.Repeat("n", 129)} {
		_, err := h.svc.Pull(ctx, nt.Secret, node, "")
		if got := code(t, err); got != "NODE_NAME_INVALID" {
			t.Fatalf("node %q: %s", node, got)
		}
	}
	h.clusters.c.State = clusters.StateDisabled
	_, err := h.svc.Pull(ctx, nt.Secret, "c1", "")
	if got := code(t, err); got != "CLUSTER_DISABLED" {
		t.Fatalf("disabled: %s", got)
	}
	h.clusters.c.State = clusters.StateActive

	if err := h.svc.RevokeToken(ctx, h.admin, "c1", nt.Token.ID); err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.Pull(ctx, nt.Secret, "c1", "")
	var ae *apperr.Error
	if !errorsAs(err, &ae) || ae.Kind != apperr.Unauthenticated {
		t.Fatalf("revoked token: %v", err)
	}
}

func TestPullConditionalAndStatus(t *testing.T) {
	ctx := context.Background()
	h := newHarness()
	if _, err := h.svc.PutConfig(ctx, h.admin, "c1", h.config(), 0); err != nil {
		t.Fatal(err)
	}
	h.bindings.list = []projects.ClusterBinding{bind(h.tenantA, "a1")}
	nt := h.token(t)

	res, err := h.svc.Pull(ctx, nt.Secret, "node-1", "")
	if err != nil || res.NotModified || len(res.Bundle.ETag) != 64 || res.Bundle.Revision != 1 {
		t.Fatalf("first pull: %+v %v", res, err)
	}
	etag := res.Bundle.ETag
	if len(h.repo.statuses) != 1 || h.repo.statuses[0].BundleSHA256 != etag ||
		h.repo.statuses[0].NodeName != "node-1" || h.repo.statuses[0].Revision != 1 ||
		h.repo.statuses[0].TokenID == nil || *h.repo.statuses[0].TokenID != nt.Token.ID {
		t.Fatalf("status = %+v", h.repo.statuses)
	}

	for name, header := range map[string]string{
		"quoted": `"` + etag + `"`, "weak": `W/"` + etag + `"`,
		"list": `"deadbeef", ` + `"` + etag + `"`, "bare": etag,
	} {
		r, err := h.svc.Pull(ctx, nt.Secret, "node-1", header)
		if err != nil || !r.NotModified {
			t.Fatalf("%s: want 304, got %+v %v", name, r, err)
		}
	}
	for _, header := range []string{`"` + strings.Repeat("0", 64) + `"`, "", "*"} {
		r, err := h.svc.Pull(ctx, nt.Secret, "node-1", header)
		if err != nil || r.NotModified {
			t.Fatalf("header %q must not match: %+v %v", header, r, err)
		}
	}

	// Binding change -> new ETag without a revision bump -> node is stale.
	h.bindings.list = append(h.bindings.list, bind(h.tenantA, "a2"))
	st, err := h.svc.NodeStatus(ctx, h.admin, "c1")
	if err != nil || st.CurrentRevision != 1 || st.CurrentBundleSHA256 == etag ||
		len(st.Items) != len(h.repo.statuses) {
		t.Fatalf("status = %+v %v", st, err)
	}
	for _, it := range st.Items {
		if !it.Stale {
			t.Fatalf("node must be stale after binding change: %+v", it)
		}
	}
	r, _ := h.svc.Pull(ctx, nt.Secret, "node-1", "")
	last := h.repo.statuses[len(h.repo.statuses)-1]
	if last.BundleSHA256 != r.Bundle.ETag || r.Bundle.ETag == etag {
		t.Fatalf("status must carry the new ETag: %+v", last)
	}
}

// --- authorization ---

func TestNonPlatformAdminForbidden(t *testing.T) {
	ctx := context.Background()
	h := newHarness()
	nobody := authn.Principal{UserID: uuid.New()}
	tenantAdmin := authn.Principal{UserID: uuid.New(), PlatformRoles: []string{"platform-auditor"}}
	for _, p := range []authn.Principal{nobody, tenantAdmin} {
		checks := map[string]func() error{
			"get":    func() error { _, err := h.svc.GetConfig(ctx, p, "c1"); return err },
			"put":    func() error { _, err := h.svc.PutConfig(ctx, p, "c1", h.config(), 0); return err },
			"bundle": func() error { _, err := h.svc.Bundle(ctx, p, "c1"); return err },
			"create": func() error { _, err := h.svc.CreateToken(ctx, p, "c1", "x"); return err },
			"list":   func() error { _, err := h.svc.ListTokens(ctx, p, "c1"); return err },
			"revoke": func() error { return h.svc.RevokeToken(ctx, p, "c1", uuid.New()) },
			"status": func() error { _, err := h.svc.NodeStatus(ctx, p, "c1"); return err },
		}
		for name, fn := range checks {
			var ae *apperr.Error
			err := fn()
			if !errorsAs(err, &ae) || ae.Kind != apperr.Forbidden {
				t.Fatalf("%s as %v: %v", name, p.PlatformRoles, err)
			}
		}
	}
	if h.repo.puts != 0 || len(h.repo.hashes) != 0 {
		t.Fatal("forbidden calls must not write")
	}
}
