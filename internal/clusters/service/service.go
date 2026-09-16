// Package service holds the clusters use-cases: platform registry CRUD,
// connection testing, tenant assignments, and tenant-visible views.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/httpclient"
	"github.com/Exonical/custos/internal/tenants"
)

// KindClusterSync is the workqueue kind for cluster sync items.
const KindClusterSync = "cluster.sync"

var supportedAPIVersions = map[string]bool{"v0.0.45": true, "v0.0.44": true}

// Deps wires the service.
type Deps struct {
	Repository clusters.Repository
	Tenants    tenants.Repository
	Factory    slurm.Factory
	Authorizer authz.Authorizer
	Recorder   audit.Recorder
	DialPolicy httpclient.DialPolicy
	Resolver   secrets.Resolver
	Enqueuer   workqueue.Execer // pool; used for sync enqueue outside txs
}

// Service implements cluster use-cases.
type Service struct {
	repo     clusters.Repository
	tenants  tenants.Repository
	factory  slurm.Factory
	az       authz.Authorizer
	rec      audit.Recorder
	policy   httpclient.DialPolicy
	resolver secrets.Resolver
	enq      workqueue.Execer
}

// New builds the service.
func New(d Deps) *Service {
	return &Service{repo: d.Repository, tenants: d.Tenants,
		factory: d.Factory, az: d.Authorizer, rec: d.Recorder,
		policy: d.DialPolicy, resolver: d.Resolver, enq: d.Enqueuer}
}

func actorOf(p authn.Principal) audit.Actor {
	t := audit.ActorUser
	if p.Kind == authn.KindService {
		t = audit.ActorService
	}
	return audit.Actor{Type: t, ID: p.UserID.String()}
}

func (s *Service) audit(ctx context.Context, p authn.Principal, action,
	targetID string, result string, details map[string]any) {
	if s.rec == nil {
		return
	}
	_ = s.rec.Record(ctx, audit.Event{
		Actor:   actorOf(p),
		Action:  action,
		Target:  audit.Target{Type: "cluster", ID: targetID},
		Result:  result,
		Details: details,
	})
}

func (s *Service) check(ctx context.Context, p authn.Principal,
	a authz.Action, r authz.Resource) error {
	d, err := s.az.Check(ctx, p, a, r)
	if err != nil {
		return err
	}
	if !d.Allow {
		return apperr.New(apperr.Forbidden, "AUTHZ_DENIED", d.Reason)
	}
	return nil
}

// --- platform: registry ----------------------------------------------------

// CreateInput is the platform cluster registration payload.
type CreateInput struct {
	Name          string
	DisplayName   string
	BaseURL       string
	APIVersion    string
	CABundlePEM   string
	IdentityMode  string
	ServiceUser   string
	TokenRef      secrets.Reference
	ClientCertRef *secrets.Reference
	Visibility    string
}

// Create registers a cluster (platform cluster.manage).
func (s *Service) Create(ctx context.Context, p authn.Principal,
	in CreateInput) (clusters.Cluster, error) {
	if err := s.check(ctx, p, authz.ClusterManage,
		authz.Resource{Kind: "platform"}); err != nil {
		return clusters.Cluster{}, err
	}
	if !supportedAPIVersions[in.APIVersion] {
		return clusters.Cluster{}, apperr.New(apperr.Validation,
			"slurm.api_version", "unsupported api_version")
	}
	if err := s.validateEndpoint(ctx, in.BaseURL); err != nil {
		return clusters.Cluster{}, err
	}
	if err := s.validateTokenRef(ctx, in.TokenRef); err != nil {
		return clusters.Cluster{}, err
	}
	c := clusters.Cluster{
		ID:            uuid.Must(uuid.NewV7()),
		Name:          in.Name,
		DisplayName:   in.DisplayName,
		BaseURL:       in.BaseURL,
		APIVersion:    in.APIVersion,
		CABundlePEM:   in.CABundlePEM,
		IdentityMode:  clusters.IdentityMode(in.IdentityMode),
		ServiceUser:   in.ServiceUser,
		TokenRef:      in.TokenRef,
		ClientCertRef: in.ClientCertRef,
		Visibility:    clusters.Visibility(in.Visibility),
		State:         clusters.StateUnreachable,
	}
	if c.IdentityMode == "" {
		c.IdentityMode = clusters.IdentityService
	}
	if c.Visibility == "" {
		c.Visibility = clusters.VisibilityAssigned
	}
	if err := s.repo.Create(ctx, c); err != nil {
		return clusters.Cluster{}, err
	}
	s.enqueueSync(ctx, c.ID, time.Now())
	s.audit(ctx, p, "cluster.created", c.ID.String(), audit.ResultAllow,
		map[string]any{"name": c.Name})
	return c, nil
}

// UpdateInput is the PATCH body (nil = unchanged).
type UpdateInput struct {
	DisplayName   *string
	BaseURL       *string
	APIVersion    *string
	CABundlePEM   *string
	IdentityMode  *string
	ServiceUser   *string
	TokenRef      *secrets.Reference
	ClientCertRef **secrets.Reference
	Visibility    *string
	Version       int
}

// Update patches a cluster with optimistic version checking.
func (s *Service) Update(ctx context.Context, p authn.Principal, ref string,
	in UpdateInput) (clusters.Cluster, error) {
	c, err := s.getPlatform(ctx, p, ref, authz.ClusterManage)
	if err != nil {
		return clusters.Cluster{}, err
	}
	resync := false
	if in.BaseURL != nil && *in.BaseURL != c.BaseURL {
		if err := s.validateEndpoint(ctx, *in.BaseURL); err != nil {
			return clusters.Cluster{}, err
		}
		c.BaseURL, resync = *in.BaseURL, true
	}
	if in.TokenRef != nil {
		if err := s.validateTokenRef(ctx, *in.TokenRef); err != nil {
			return clusters.Cluster{}, err
		}
		c.TokenRef, resync = *in.TokenRef, true
	}
	if in.DisplayName != nil {
		c.DisplayName = *in.DisplayName
	}
	if in.APIVersion != nil && *in.APIVersion != c.APIVersion {
		if !supportedAPIVersions[*in.APIVersion] {
			return clusters.Cluster{}, apperr.New(apperr.Validation,
				"slurm.api_version", "unsupported api_version")
		}
		c.APIVersion, resync = *in.APIVersion, true
	}
	if in.CABundlePEM != nil {
		c.CABundlePEM, resync = *in.CABundlePEM, true
	}
	if in.IdentityMode != nil {
		c.IdentityMode, resync = clusters.IdentityMode(*in.IdentityMode), true
	}
	if in.ServiceUser != nil {
		c.ServiceUser, resync = *in.ServiceUser, true
	}
	if in.ClientCertRef != nil {
		c.ClientCertRef, resync = *in.ClientCertRef, true
	}
	if in.Visibility != nil {
		c.Visibility = clusters.Visibility(*in.Visibility)
	}
	c.Version = in.Version
	if err := s.repo.Update(ctx, c); err != nil {
		return clusters.Cluster{}, err
	}
	if resync {
		s.enqueueSync(ctx, c.ID, time.Now())
	}
	s.audit(ctx, p, "cluster.updated", c.ID.String(), audit.ResultAllow, nil)
	return c, nil
}

// Get returns one cluster (cluster.manage or cluster.read).
func (s *Service) Get(ctx context.Context, p authn.Principal,
	ref string) (clusters.Cluster, error) {
	return s.getPlatform(ctx, p, ref, authz.ClusterRead)
}

// List returns a page of clusters (cluster.manage or cluster.read).
func (s *Service) List(ctx context.Context, p authn.Principal,
	page clusters.Page) ([]clusters.Cluster, string, error) {
	if err := s.check(ctx, p, authz.ClusterRead,
		authz.Resource{Kind: "platform"}); err != nil {
		return nil, "", err
	}
	return s.repo.List(ctx, page)
}

// Disable flips state to disabled (no hard delete in v1).
func (s *Service) Disable(ctx context.Context, p authn.Principal,
	ref string) (clusters.Cluster, error) {
	c, err := s.getPlatform(ctx, p, ref, authz.ClusterManage)
	if err != nil {
		return clusters.Cluster{}, err
	}
	if c.State == clusters.StateDisabled {
		return c, nil
	}
	if err := s.repo.SetState(ctx, c.ID, clusters.StateDisabled); err != nil {
		return clusters.Cluster{}, err
	}
	c.State = clusters.StateDisabled
	s.audit(ctx, p, "cluster.disabled", c.ID.String(), audit.ResultAllow, nil)
	return c, nil
}

// ConnTestResult is the test-connection response DTO.
type ConnTestResult struct {
	OK           bool     `json:"ok"`
	LatencyMS    int64    `json:"latency_ms,omitempty"`
	SlurmVersion string   `json:"slurm_version,omitempty"`
	APIVersion   string   `json:"api_version,omitempty"`
	Partitions   []string `json:"partitions,omitempty"`
	ErrorCode    string   `json:"error_code,omitempty"`
}

// TestConnection opens the cluster, pings, reads capabilities. It does
// not persist sync state.
func (s *Service) TestConnection(ctx context.Context, p authn.Principal,
	ref string) (ConnTestResult, error) {
	c, err := s.getPlatform(ctx, p, ref, authz.ClusterManage)
	if err != nil {
		return ConnTestResult{}, err
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res := s.testConnection(tctx, c)
	s.audit(ctx, p, "cluster.connection_tested", c.ID.String(),
		audit.ResultAllow, map[string]any{"ok": res.OK})
	return res, nil
}

func (s *Service) testConnection(ctx context.Context,
	c clusters.Cluster) ConnTestResult {
	cl, _, err := s.factory.Open(ctx, c.SlurmConfig())
	if err != nil {
		return ConnTestResult{ErrorCode: codeOf(err)}
	}
	if closer, ok := cl.(interface{ Close() }); ok {
		closer.Close()
	}
	ping, err := cl.Ping(ctx)
	if err != nil {
		return ConnTestResult{ErrorCode: codeOf(err)}
	}
	caps, err := cl.Capabilities(ctx)
	if err != nil {
		return ConnTestResult{ErrorCode: codeOf(err)}
	}
	res := ConnTestResult{
		OK:           ping.Responding,
		LatencyMS:    ping.Latency.Milliseconds(),
		SlurmVersion: caps.SlurmVersion,
		APIVersion:   caps.APIVersion,
	}
	for _, p := range caps.Partitions {
		res.Partitions = append(res.Partitions, p.Name)
	}
	return res
}

// --- platform: assignments --------------------------------------------------

// Assign creates or updates a manual assignment (platform cluster.assign).
func (s *Service) Assign(ctx context.Context, p authn.Principal, clusterRef,
	tenantRef string, defaults clusters.AssignmentDefaults) error {
	c, err := s.getPlatform(ctx, p, clusterRef, authz.ClusterAssign)
	if err != nil {
		return err
	}
	if c.Visibility == clusters.VisibilityAllTenants {
		return apperr.New(apperr.Conflict, "CLUSTER_VISIBILITY",
			"assignments are managed automatically for all_tenants clusters")
	}
	t, err := s.tenants.GetBySlugOrID(ctx, tenants.PlatformScope(), tenantRef)
	if err != nil {
		return err
	}
	err = s.repo.UpsertAssignment(ctx, tenants.PlatformScope(),
		clusters.Assignment{ClusterID: c.ID, TenantID: t.ID,
			Source: clusters.SourceManual, Defaults: defaults})
	if err != nil {
		return err
	}
	s.audit(ctx, p, "cluster.assigned", c.ID.String(), audit.ResultAllow,
		map[string]any{"tenant": t.Slug})
	return nil
}

// Unassign removes a manual assignment.
func (s *Service) Unassign(ctx context.Context, p authn.Principal, clusterRef,
	tenantRef string) error {
	c, err := s.getPlatform(ctx, p, clusterRef, authz.ClusterAssign)
	if err != nil {
		return err
	}
	if c.Visibility == clusters.VisibilityAllTenants {
		return apperr.New(apperr.Conflict, "CLUSTER_VISIBILITY",
			"assignments are managed automatically for all_tenants clusters")
	}
	t, err := s.tenants.GetBySlugOrID(ctx, tenants.PlatformScope(), tenantRef)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteAssignment(ctx, tenants.PlatformScope(),
		c.ID, t.ID); err != nil {
		return err
	}
	s.audit(ctx, p, "cluster.unassigned", c.ID.String(), audit.ResultAllow,
		map[string]any{"tenant": t.Slug})
	return nil
}

// ListAssignments lists assignment rows for a cluster.
func (s *Service) ListAssignments(ctx context.Context, p authn.Principal,
	clusterRef string, page clusters.Page) ([]clusters.Assignment, string, error) {
	c, err := s.getPlatform(ctx, p, clusterRef, authz.ClusterAssign)
	if err != nil {
		return nil, "", err
	}
	return s.repo.ListAssignments(ctx, tenants.PlatformScope(), c.ID, page)
}

// --- tenant-facing ----------------------------------------------------------

// ClusterSummary is the tenant-visible cluster view — never base_url,
// token_ref, ca_bundle, or other platform configuration.
type ClusterSummary struct {
	ID           string
	Name         string
	DisplayName  string
	State        clusters.State
	SlurmVersion string
	Partitions   []string
	GRESTypes    []string
	NodeSummary  map[string]int
	Defaults     clusters.AssignmentDefaults
}

// ListVisible lists clusters assigned to the tenant (tenant cluster.read).
func (s *Service) ListVisible(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext) ([]ClusterSummary, error) {
	if err := s.check(ctx, p, authz.ClusterRead, authz.Resource{
		Kind: "tenant", ID: tc.Tenant.ID.String(),
		TenantID: tc.Tenant.ID.String()}); err != nil {
		return nil, err
	}
	cs, assigns, err := s.repo.ListVisibleForTenant(ctx,
		tenants.ScopeFor(&tc), tc.Tenant.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ClusterSummary, 0, len(cs))
	for _, c := range cs {
		out = append(out, summarize(c, assigns[c.ID]))
	}
	return out, nil
}

// GetVisible returns one assigned cluster.
func (s *Service) GetVisible(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, clusterRef string) (ClusterSummary, error) {
	list, err := s.ListVisible(ctx, p, tc)
	if err != nil {
		return ClusterSummary{}, err
	}
	for _, c := range list {
		if c.ID == clusterRef || c.Name == clusterRef {
			return c, nil
		}
	}
	return ClusterSummary{}, apperr.New(apperr.NotFound, "NOT_FOUND",
		"cluster not found")
}

// ListPartitions returns partition records for an assigned cluster,
// filtered by the assignment's allowed_partitions when set.
func (s *Service) ListPartitions(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, clusterRef string) ([]clusters.PartitionRecord, error) {
	sum, err := s.GetVisible(ctx, p, tc, clusterRef)
	if err != nil {
		return nil, err
	}
	recs, err := s.repo.ListPartitions(ctx, uuid.MustParse(sum.ID))
	if err != nil {
		return nil, err
	}
	allowed := sum.Defaults.AllowedPartitions
	if len(allowed) == 0 {
		return recs, nil
	}
	out := recs[:0]
	for _, r := range recs {
		if slices.Contains(allowed, r.Name) {
			out = append(out, r)
		}
	}
	return out, nil
}

func summarize(c clusters.Cluster, a clusters.Assignment) ClusterSummary {
	sum := ClusterSummary{
		ID: c.ID.String(), Name: c.Name, DisplayName: c.DisplayName,
		State: c.State, Defaults: a.Defaults,
	}
	if c.Capabilities != nil {
		sum.SlurmVersion = c.Capabilities.SlurmVersion
		sum.GRESTypes = c.Capabilities.GRESTypes
		sum.NodeSummary = c.Capabilities.NodeSummary
		for _, pt := range c.Capabilities.Partitions {
			if len(a.Defaults.AllowedPartitions) == 0 ||
				slices.Contains(a.Defaults.AllowedPartitions, pt.Name) {
				sum.Partitions = append(sum.Partitions, pt.Name)
			}
		}
	}
	return sum
}

// --- helpers ----------------------------------------------------------------

// EnqueueSync enqueues a cluster.sync item (key cluster:<id>).
func (s *Service) EnqueueSync(ctx context.Context, ex workqueue.Execer,
	clusterID uuid.UUID, runAt time.Time) error {
	_, err := workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{
		Kind:  KindClusterSync,
		Key:   "cluster:" + clusterID.String(),
		RunAt: runAt,
	})
	return err
}

func (s *Service) enqueueSync(ctx context.Context, id uuid.UUID, at time.Time) {
	if s.enq == nil {
		return
	}
	_ = s.EnqueueSync(ctx, s.enq, id, at)
}

func (s *Service) getPlatform(ctx context.Context, p authn.Principal,
	ref string, a authz.Action) (clusters.Cluster, error) {
	c, err := s.repo.GetByNameOrID(ctx, ref)
	if err != nil {
		return c, err
	}
	if err := s.check(ctx, p, a, authz.Resource{
		Kind: "cluster", ID: c.ID.String()}); err != nil {
		return clusters.Cluster{}, err
	}
	return c, nil
}

// validateEndpoint parses base_url and vets the resolved host at
// registration time (early SSRF check; the transport re-vets per dial).
func (s *Service) validateEndpoint(ctx context.Context, baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return apperr.New(apperr.Validation, "slurm.base_url",
			"invalid base_url")
	}
	httpOnly := false
	switch u.Scheme {
	case "https":
	case "http":
		httpOnly = true
		if !s.policy.AllowHTTP {
			return apperr.New(apperr.Validation, "slurm.base_url",
				"http endpoints require dev dial policy")
		}
	default:
		return apperr.New(apperr.Validation, "slurm.base_url",
			"base_url must be https")
	}
	if err := httpclient.VetHost(ctx, u.Hostname(), s.policy, httpOnly); err != nil {
		// Registration-time denial is a validation failure (422). The
		// transport keeps Forbidden for per-dial enforcement.
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Kind == apperr.Forbidden {
			return apperr.New(apperr.Validation, ae.Code, ae.Message)
		}
		return err
	}
	return nil
}

func (s *Service) validateTokenRef(ctx context.Context,
	ref secrets.Reference) error {
	v, err := s.resolver.Resolve(ctx, ref)
	if err != nil {
		var ae *apperr.Error
		code := "secrets.unresolvable"
		if errors.As(err, &ae) && ae.Code != "" {
			code = ae.Code
		}
		return apperr.New(apperr.Validation, code,
			fmt.Sprintf("token_ref not resolvable: %v", ae))
	}
	v.Wipe()
	return nil
}

func codeOf(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return "internal"
}
