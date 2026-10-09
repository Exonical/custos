// Package service implements the platform-admin node-config use cases
// (config CRUD, bundle download, node tokens, node status) and the
// token-authenticated node pull (ADR-032).
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/nodehooks"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/tenants"
)

// Warning codes (non-blocking findings returned with the configuration).
const (
	WarnSharedServiceUser         = "SHARED_SERVICE_USER"
	WarnNamespaceVersion          = "NAMESPACE_REQUIRES_SLURM_25_11"
	WarnTenantExclusiveSharedUser = "TENANT_EXCLUSIVE_SHARED_USER"
)

// TokenPrefix marks node pull tokens.
const TokenPrefix = "cnt_"

// writeInterval bounds last_used_at / node-status writes per node.
const writeInterval = 30 * time.Second

var (
	tokenNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	nodeNameRE  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	versionRE   = regexp.MustCompile(`(\d+)\.(\d+)`)
)

// Deps wires the service.
type Deps struct {
	Repo     nodehooks.Repository
	Clusters clusters.Repository
	Tenants  tenants.Repository
	Bindings projects.BindingRepository
	AZ       authz.Authorizer
	Audit    audit.Recorder
	Logger   *slog.Logger
	Now      func() time.Time
}

// Service implements the node-config use cases.
type Service struct {
	d Deps
}

// New builds the service.
func New(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

// Warning is a non-blocking finding.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// View is the admin view of a cluster's node configuration.
type View struct {
	ClusterID     uuid.UUID
	Config        nodehooks.Config
	Revision      int64
	ContentSHA256 string
	Version       int
	UpdatedAt     *time.Time
	UpdatedBy     *uuid.UUID
	Warnings      []Warning
}

// Bundle is a rendered archive.
type Bundle struct {
	Filename string
	Data     []byte
	ETag     string // hex sha256 of Data
	Revision int64
}

// NewToken is returned once on creation.
type NewToken struct {
	Token  nodehooks.Token
	Secret string
}

// NodeStatusView is one node's sync state.
type NodeStatusView struct {
	NodeName     string
	Revision     int64
	BundleSHA256 string
	FetchedAt    time.Time
	Stale        bool
}

// NodeStatusResult lists nodes against the bundle served right now.
type NodeStatusResult struct {
	CurrentRevision     int64
	CurrentBundleSHA256 string
	Items               []NodeStatusView
}

// PullResult is the outcome of a node pull.
type PullResult struct {
	NotModified bool
	Bundle      Bundle
}

func (s *Service) check(ctx context.Context, p authn.Principal, c clusters.Cluster) error {
	d, err := s.d.AZ.Check(ctx, p, authz.ClusterManage,
		authz.Resource{Kind: "cluster", ID: c.ID.String()})
	if err != nil {
		return err
	}
	if !d.Allow {
		return apperr.New(apperr.Forbidden, "AUTHZ_DENIED", d.Reason)
	}
	return nil
}

func (s *Service) cluster(ctx context.Context, p authn.Principal, ref string) (clusters.Cluster, error) {
	c, err := s.d.Clusters.GetByNameOrID(ctx, ref)
	if err != nil {
		return c, err
	}
	return c, s.check(ctx, p, c)
}

func actorOf(p authn.Principal) audit.Actor {
	t := audit.ActorUser
	if p.Kind == authn.KindService {
		t = audit.ActorService
	}
	return audit.Actor{Type: t, ID: p.UserID.String()}
}

func (s *Service) audit(ctx context.Context, p authn.Principal, action string,
	c clusters.Cluster, details map[string]any) {
	audit.RecordBestEffort(ctx, s.d.Logger, s.d.Audit, audit.Event{
		Actor:   actorOf(p),
		Action:  action,
		Target:  audit.Target{Type: "cluster", ID: c.ID.String()},
		Result:  audit.ResultAllow,
		Details: details,
	})
}

func (s *Service) stored(ctx context.Context, c clusters.Cluster) (nodehooks.Stored, error) {
	st, err := s.d.Repo.GetConfig(ctx, c.ID)
	if apperr.Is(err, apperr.NotFound) {
		return nodehooks.Stored{ClusterID: c.ID, Config: nodehooks.Normalize(nodehooks.Default())}, nil
	}
	return st, err
}

// GetConfig returns the configuration with warnings (cluster.manage).
func (s *Service) GetConfig(ctx context.Context, p authn.Principal, ref string) (View, error) {
	c, err := s.cluster(ctx, p, ref)
	if err != nil {
		return View{}, err
	}
	st, err := s.stored(ctx, c)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, c, st)
}

func (s *Service) view(ctx context.Context, c clusters.Cluster, st nodehooks.Stored) (View, error) {
	warns, err := s.warnings(ctx, c, st.Config)
	if err != nil {
		return View{}, err
	}
	v := View{ClusterID: c.ID, Config: st.Config, Revision: st.Revision,
		ContentSHA256: st.ContentSHA256, Version: st.Version, Warnings: warns,
		UpdatedBy: st.UpdatedBy}
	if v.ContentSHA256 == "" {
		v.ContentSHA256 = nodehooks.ContentSHA256(st.Config)
	}
	if !st.UpdatedAt.IsZero() {
		t := st.UpdatedAt
		v.UpdatedAt = &t
	}
	return v, nil
}

// PutConfig validates and stores the configuration (cluster.manage).
// version 0 creates it; otherwise it must match the stored version.
func (s *Service) PutConfig(ctx context.Context, p authn.Principal, ref string,
	in nodehooks.Config, version int) (View, error) {
	c, err := s.cluster(ctx, p, ref)
	if err != nil {
		return View{}, err
	}
	cfg := nodehooks.Normalize(in)
	errs := nodehooks.Validate(cfg)
	errs = append(errs, s.tenantErrors(ctx, c, cfg)...)
	if len(errs) > 0 {
		return View{}, validationError(errs)
	}
	hash := nodehooks.ContentSHA256(cfg)
	uid := p.UserID
	st, err := s.d.Repo.PutConfig(ctx, c.ID, cfg, hash, version, &uid)
	if err != nil {
		return View{}, err
	}
	s.audit(ctx, p, "cluster.node_config.updated", c, auditDetails(st))
	return s.view(ctx, c, st)
}

func auditDetails(st nodehooks.Stored) map[string]any {
	hooks := make([]map[string]any, 0, len(st.Config.Hooks))
	for _, h := range st.Config.Hooks {
		sum := sha256.Sum256([]byte(h.Script))
		hooks = append(hooks, map[string]any{
			"name": h.Name, "phase": h.Phase, "order": h.Order,
			"sha256": hex.EncodeToString(sum[:]),
		})
	}
	shared := make([]string, 0, len(st.Config.SharedMounts))
	for _, m := range st.Config.SharedMounts {
		shared = append(shared, m.Name)
	}
	return map[string]any{
		"revision":       st.Revision,
		"content_sha256": st.ContentSHA256,
		"isolation_mode": st.Config.IsolationMode,
		"shared_mounts":  shared,
		"tenant_mounts":  len(st.Config.TenantMounts),
		"hooks":          hooks,
	}
}

func validationError(errs []nodehooks.FieldError) error {
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Path < errs[j].Path })
	details := make([]apperr.Detail, 0, len(errs))
	for _, e := range errs {
		details = append(details, apperr.Detail{Field: e.Path, Reason: e.Message})
	}
	code := errs[0].Code
	for _, e := range errs[1:] {
		if e.Code != code {
			code = nodehooks.CodeConfigInvalid
			break
		}
	}
	return &apperr.Error{Kind: apperr.Validation, Code: code,
		Message: "node configuration is invalid", Details: details}
}

// tenantErrors checks that every tenant exists and may use the cluster.
func (s *Service) tenantErrors(ctx context.Context, c clusters.Cluster, cfg nodehooks.Config) []nodehooks.FieldError {
	var errs []nodehooks.FieldError
	seen := map[string]string{}
	for i, m := range cfg.TenantMounts {
		path := fmt.Sprintf("tenant_mounts[%d].tenant", i)
		if msg, done := seen[m.Tenant]; done {
			if msg != "" {
				errs = append(errs, nodehooks.FieldError{Path: path, Code: nodehooks.CodeMountInvalid, Message: msg})
			}
			continue
		}
		msg := s.tenantProblem(ctx, c, m.Tenant)
		seen[m.Tenant] = msg
		if msg != "" {
			errs = append(errs, nodehooks.FieldError{Path: path, Code: nodehooks.CodeMountInvalid, Message: msg})
		}
	}
	return errs
}

func (s *Service) tenantProblem(ctx context.Context, c clusters.Cluster, tenantID string) string {
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return "" // reported by static validation
	}
	t, err := s.d.Tenants.GetBySlugOrID(ctx, tenants.PlatformScope(), id.String())
	if err != nil || t.State == tenants.StateDeleted || t.State == tenants.StateDeleting {
		return "tenant does not exist"
	}
	if c.Visibility == clusters.VisibilityAllTenants {
		return ""
	}
	if _, err := s.d.Clusters.GetAssignment(ctx, tenants.PlatformScope(), c.ID, id); err != nil {
		return "tenant is not assigned to this cluster"
	}
	return ""
}

func (s *Service) warnings(ctx context.Context, c clusters.Cluster, cfg nodehooks.Config) ([]Warning, error) {
	warns := []Warning{}
	if cfg.IsolationMode == nodehooks.ModeNamespace && !slurmAtLeast(c, 25, 11) {
		ver := "unknown"
		if c.Capabilities != nil && c.Capabilities.SlurmVersion != "" {
			ver = c.Capabilities.SlurmVersion
		}
		warns = append(warns, Warning{Code: WarnNamespaceVersion,
			Message: "namespace mode needs Slurm 25.11 or newer with namespace/linux; cluster reports " + ver})
	}
	userMechanism := cfg.IsolationMode == nodehooks.ModeTenantExclusive &&
		cfg.TenantExclusiveMechanism == nodehooks.MechanismUser
	if (len(cfg.TenantMounts) > 0 || userMechanism) && c.IdentityMode == clusters.IdentityService {
		bindings, err := s.d.Bindings.ListBindingsByCluster(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		distinct := map[uuid.UUID]bool{}
		for _, b := range bindings {
			distinct[b.TenantID] = true
		}
		if len(distinct) >= 2 && userMechanism {
			warns = append(warns, Warning{Code: WarnTenantExclusiveSharedUser,
				Message: fmt.Sprintf("tenant_exclusive with mechanism user needs one Slurm user per tenant, but bindings of %d tenants share the service user %q; jobs of different tenants can co-locate, the prolog will refuse them and drain the node", len(distinct), c.ServiceUser)})
		}
		if len(distinct) >= 2 && len(cfg.TenantMounts) > 0 {
			warns = append(warns, Warning{Code: WarnSharedServiceUser,
				Message: fmt.Sprintf("bindings of %d tenants share the Slurm service user %q; "+
					"jobs of different tenants run as the same OS user and tenant NFS isolation "+
					"cannot rely on file ownership", len(distinct), c.ServiceUser)})
		}
	}
	return warns, nil
}

func slurmAtLeast(c clusters.Cluster, major, minor int) bool {
	if c.Capabilities == nil {
		return false
	}
	m := versionRE.FindStringSubmatch(c.Capabilities.SlurmVersion)
	if m == nil {
		return false
	}
	mj, _ := strconv.Atoi(m[1])
	mn, _ := strconv.Atoi(m[2])
	return mj > major || (mj == major && mn >= minor)
}

// render builds the bundle for the cluster's current configuration.
func (s *Service) render(ctx context.Context, c clusters.Cluster) (Bundle, error) {
	st, err := s.stored(ctx, c)
	if err != nil {
		return Bundle{}, err
	}
	bindings, err := s.d.Bindings.ListBindingsByCluster(ctx, c.ID)
	if err != nil {
		return Bundle{}, err
	}
	in := nodehooks.RenderInput{
		Cluster: c.Name, Revision: st.Revision, Config: st.Config,
		TenantSlugs: map[string]string{},
	}
	for _, b := range bindings {
		in.Bindings = append(in.Bindings, nodehooks.Binding{
			TenantID: b.TenantID.String(), Account: b.SlurmAccount})
	}
	for _, m := range st.Config.TenantMounts {
		if _, done := in.TenantSlugs[m.Tenant]; done {
			continue
		}
		id, perr := uuid.Parse(m.Tenant)
		if perr != nil {
			continue
		}
		t, terr := s.d.Tenants.GetBySlugOrID(ctx, tenants.PlatformScope(), id.String())
		if terr != nil {
			if apperr.Is(terr, apperr.NotFound) {
				continue
			}
			return Bundle{}, terr
		}
		if t.State == tenants.StateDeleted || t.State == tenants.StateDeleting {
			continue
		}
		in.TenantSlugs[m.Tenant] = t.Slug
	}
	r, err := nodehooks.Render(in)
	if err != nil {
		return Bundle{}, err
	}
	data, err := nodehooks.Archive(r.Files)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		Filename: fmt.Sprintf("custos-node-%s-r%d.tar.gz", c.Name, st.Revision),
		Data:     data, ETag: nodehooks.BundleSHA256(data), Revision: st.Revision,
	}, nil
}

// Bundle renders the download bundle (cluster.manage).
func (s *Service) Bundle(ctx context.Context, p authn.Principal, ref string) (Bundle, error) {
	c, err := s.cluster(ctx, p, ref)
	if err != nil {
		return Bundle{}, err
	}
	return s.render(ctx, c)
}

// CreateToken mints a node pull token; the secret is returned once.
func (s *Service) CreateToken(ctx context.Context, p authn.Principal, ref, name string) (NewToken, error) {
	c, err := s.cluster(ctx, p, ref)
	if err != nil {
		return NewToken{}, err
	}
	if !tokenNameRE.MatchString(name) {
		return NewToken{}, &apperr.Error{Kind: apperr.Validation, Code: "NODE_TOKEN_INVALID",
			Message: "invalid token name", Details: []apperr.Detail{{Field: "name",
				Reason: "must match ^[a-z0-9][a-z0-9-]{0,62}$"}}}
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return NewToken{}, err
	}
	secret := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	uid := p.UserID
	t := nodehooks.Token{ID: uuid.Must(uuid.NewV7()), ClusterID: c.ID, Name: name,
		CreatedAt: s.d.Now().UTC(), CreatedBy: &uid}
	if err := s.d.Repo.CreateToken(ctx, t, hashToken(secret)); err != nil {
		return NewToken{}, err
	}
	s.audit(ctx, p, "cluster.node_token.created", c,
		map[string]any{"token_id": t.ID.String(), "name": name})
	return NewToken{Token: t, Secret: secret}, nil
}

// ListTokens lists token metadata (never secrets).
func (s *Service) ListTokens(ctx context.Context, p authn.Principal, ref string) ([]nodehooks.Token, error) {
	c, err := s.cluster(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	return s.d.Repo.ListTokens(ctx, c.ID)
}

// RevokeToken revokes a token.
func (s *Service) RevokeToken(ctx context.Context, p authn.Principal, ref string, id uuid.UUID) error {
	c, err := s.cluster(ctx, p, ref)
	if err != nil {
		return err
	}
	if err := s.d.Repo.RevokeToken(ctx, c.ID, id); err != nil {
		return err
	}
	s.audit(ctx, p, "cluster.node_token.revoked", c, map[string]any{"token_id": id.String()})
	return nil
}

// NodeStatus lists the revision each node last fetched.
func (s *Service) NodeStatus(ctx context.Context, p authn.Principal, ref string) (NodeStatusResult, error) {
	c, err := s.cluster(ctx, p, ref)
	if err != nil {
		return NodeStatusResult{}, err
	}
	cur, err := s.render(ctx, c)
	if err != nil {
		return NodeStatusResult{}, err
	}
	rows, err := s.d.Repo.ListStatus(ctx, c.ID)
	if err != nil {
		return NodeStatusResult{}, err
	}
	out := NodeStatusResult{CurrentRevision: cur.Revision, CurrentBundleSHA256: cur.ETag,
		Items: make([]NodeStatusView, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, NodeStatusView{NodeName: r.NodeName, Revision: r.Revision,
			BundleSHA256: r.BundleSHA256, FetchedAt: r.FetchedAt, Stale: r.BundleSHA256 != cur.ETag})
	}
	return out, nil
}

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func errNodeAuth() error {
	return apperr.New(apperr.Unauthenticated, "NODE_TOKEN_INVALID", "invalid node token")
}

// Pull serves the node bundle for a token. The token hash is always
// computed and looked up, so malformed credentials take the same path as
// unknown ones. ifNoneMatch is the raw If-None-Match header.
func (s *Service) Pull(ctx context.Context, secret, node, ifNoneMatch string) (PullResult, error) {
	tok, err := s.d.Repo.LookupToken(ctx, hashToken(secret))
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return PullResult{}, errNodeAuth()
		}
		return PullResult{}, err
	}
	if !nodeNameRE.MatchString(node) {
		return PullResult{}, apperr.New(apperr.Invalid, "NODE_NAME_INVALID", "invalid X-Custos-Node header")
	}
	c, err := s.d.Clusters.GetByNameOrID(ctx, tok.ClusterID.String())
	if err != nil {
		return PullResult{}, err
	}
	if c.State == clusters.StateDisabled {
		return PullResult{}, apperr.New(apperr.Forbidden, "CLUSTER_DISABLED", "cluster is disabled")
	}
	b, err := s.render(ctx, c)
	if err != nil {
		return PullResult{}, err
	}
	if err := s.d.Repo.TouchToken(ctx, tok.ID, writeInterval); err != nil {
		s.d.Logger.WarnContext(ctx, "node token touch failed", "error", err)
	}
	tid := tok.ID
	if err := s.d.Repo.UpsertStatus(ctx, nodehooks.NodeStatus{ClusterID: c.ID,
		NodeName: node, TokenID: &tid, Revision: b.Revision, BundleSHA256: b.ETag}, writeInterval); err != nil {
		s.d.Logger.WarnContext(ctx, "node status write failed", "error", err)
	}
	return PullResult{NotModified: etagMatches(ifNoneMatch, b.ETag), Bundle: b}, nil
}

func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		part = strings.Trim(part, `"`)
		if part != "" && part == etag {
			return true
		}
	}
	return false
}
