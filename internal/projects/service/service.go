// Package service implements the project application service:
// authorization, archived-state gating, membership management, cluster
// bindings and audit for the /projects API.
package service

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/tenants"
)

// Service is the project application service.
type Service struct {
	repo       projects.Repository
	members    projects.MembershipRepository
	bindings   projects.BindingRepository
	tenantRepo tenants.Repository
	clusters   clusters.Repository
	az         authz.Authorizer
	rec        audit.Recorder
}

// NewService wires the project service.
func NewService(repo projects.Repository, members projects.MembershipRepository,
	bindings projects.BindingRepository, tenantRepo tenants.Repository,
	crepo clusters.Repository,
	az authz.Authorizer, rec audit.Recorder) *Service {
	return &Service{repo: repo, members: members, bindings: bindings,
		tenantRepo: tenantRepo, clusters: crepo, az: az, rec: rec}
}

// CreateProject is the POST /projects body.
type CreateProject struct {
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Settings    map[string]any `json:"settings,omitempty"`
}

// UpdateProject is the PATCH /projects/{project} body.
type UpdateProject struct {
	Name        *string         `json:"name,omitempty"`
	Description *string         `json:"description,omitempty"`
	Settings    *map[string]any `json:"settings,omitempty"`
	Version     int             `json:"version"`
}

// UpsertMember is the POST/PATCH members body.
type UpsertMember struct {
	UserID uuid.UUID `json:"user_id"`
	Roles  []string  `json:"roles"`
}

// UpsertBinding is the POST/PATCH cluster-bindings body.
type UpsertBinding struct {
	ClusterID         uuid.UUID `json:"cluster_id"`
	SlurmAccount      string    `json:"slurm_account"`
	DefaultPartition  string    `json:"default_partition,omitempty"`
	AllowedPartitions []string  `json:"allowed_partitions,omitempty"`
	DefaultQoS        string    `json:"default_qos,omitempty"`
	AllowedQoS        []string  `json:"allowed_qos,omitempty"`
	Enabled           *bool     `json:"enabled,omitempty"`
	Version           int       `json:"version,omitempty"`
}

func actorOf(p authn.Principal) audit.Actor {
	t := audit.ActorUser
	if p.Kind == authn.KindService {
		t = audit.ActorService
	}
	return audit.Actor{Type: t, ID: p.UserID.String()}
}

func (s *Service) audit(ctx context.Context, p authn.Principal, tenantID uuid.UUID,
	action, targetType, targetID string, details map[string]any) {
	if s.rec == nil {
		return
	}
	_ = s.rec.Record(ctx, audit.Event{
		Actor:    actorOf(p),
		Action:   action,
		Target:   audit.Target{Type: targetType, ID: targetID},
		Result:   audit.ResultAllow,
		TenantID: &tenantID,
		Details:  details,
	})
}

func res(tc tenants.TenantContext, pc projects.ProjectContext) authz.Resource {
	return authz.Resource{
		Kind:      "project",
		ID:        pc.Project.ID.String(),
		TenantID:  tc.Tenant.ID.String(),
		ProjectID: pc.Project.ID.String(),
	}
}

// collectionRes is the resource for tenant-level project collection
// actions (create, list).
func collectionRes(tc tenants.TenantContext) authz.Resource {
	return authz.Resource{Kind: "project", TenantID: tc.Tenant.ID.String()}
}

// hasProjectRole reports whether pc's membership carries a project role.
func hasProjectRole(pc projects.ProjectContext) bool {
	return pc.Membership != nil && len(pc.Membership.Roles) > 0
}

// canRead: project.read at tenant level OR any project membership.
func (s *Service) canRead(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext) error {
	if err := authz.Require(ctx, s.az, p, authz.ProjectRead, res(tc, pc), s.rec); err == nil {
		return nil
	}
	if hasProjectRole(pc) {
		return nil // membership alone grants project.read via project roles
	}
	return apperr.New(apperr.Forbidden, "FORBIDDEN", "forbidden")
}

// mutable gates mutations on project.manage + active state.
func (s *Service) mutable(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext) error {
	if pc.Project.State == projects.StateArchived {
		return apperr.New(apperr.Conflict, "PROJECT_STATE", "project is archived")
	}
	return authz.Require(ctx, s.az, p, authz.ProjectManage, res(tc, pc), s.rec)
}

// Create makes a project (tenant project.create); the creator becomes
// a project-admin member automatically.
func (s *Service) Create(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, in CreateProject) (projects.Project, error) {
	if err := authz.Require(ctx, s.az, p, authz.ProjectCreate, collectionRes(tc), s.rec); err != nil {
		return projects.Project{}, err
	}
	proj := projects.Project{
		ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID,
		Slug: in.Slug, Name: in.Name, Description: in.Description,
		State: projects.StateActive,
	}
	if proj.Settings == nil {
		proj.Settings = map[string]any{}
	}
	scope := tenants.ScopeFor(&tc)
	if err := s.repo.Create(ctx, scope, proj); err != nil {
		return projects.Project{}, err
	}
	if err := s.members.UpsertMembership(ctx, scope, projects.Membership{
		TenantID: tc.Tenant.ID, ProjectID: proj.ID, UserID: p.UserID,
		Roles: []string{"project-admin"}, Source: "manual",
	}); err != nil {
		return projects.Project{}, err
	}
	s.audit(ctx, p, tc.Tenant.ID, "project.created", "project", proj.ID.String(),
		map[string]any{"slug": proj.Slug})
	return proj, nil
}

// List returns all projects for project.read principals, else only the
// caller's member projects.
func (s *Service) List(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, page tenants.Page) ([]projects.Project, string, error) {
	scope := tenants.ScopeFor(&tc)
	if err := authz.Require(ctx, s.az, p, authz.ProjectRead, collectionRes(tc), s.rec); err == nil {
		return s.repo.List(ctx, scope, tc.Tenant.ID, page)
	}
	items, err := s.repo.ListForUser(ctx, scope, tc.Tenant.ID, p.UserID)
	if err != nil {
		return nil, "", err
	}
	return items, "", nil
}

// Get returns the resolved project (tenant project.read or membership).
func (s *Service) Get(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext) (projects.Project, error) {
	if err := s.canRead(ctx, p, tc, pc); err != nil {
		return projects.Project{}, err
	}
	return pc.Project, nil
}

// Update patches name/description/settings (project.manage; archived
// projects may still be patched to allow unarchive-adjacent edits?
// No — archived blocks mutations; use Unarchive first).
func (s *Service) Update(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext, in UpdateProject) (projects.Project, error) {
	if err := s.mutable(ctx, p, tc, pc); err != nil {
		return projects.Project{}, err
	}
	proj := pc.Project
	var changed []string
	if in.Name != nil {
		proj.Name = *in.Name
		changed = append(changed, "name")
	}
	if in.Description != nil {
		proj.Description = *in.Description
		changed = append(changed, "description")
	}
	if in.Settings != nil {
		proj.Settings = *in.Settings
		changed = append(changed, "settings")
	}
	proj.Version = in.Version
	if err := s.repo.Update(ctx, tenants.ScopeFor(&tc), proj); err != nil {
		return projects.Project{}, err
	}
	if len(changed) > 0 {
		s.audit(ctx, p, tc.Tenant.ID, "project.updated", "project", proj.ID.String(),
			map[string]any{"fields": changed})
	}
	return proj, nil
}

// setState flips active/archived under project.manage.
func (s *Service) setState(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext,
	to projects.State, action string) (projects.Project, error) {
	if err := authz.Require(ctx, s.az, p, authz.ProjectManage, res(tc, pc), s.rec); err != nil {
		return projects.Project{}, err
	}
	proj := pc.Project
	if proj.State == to {
		return proj, nil
	}
	proj.State = to
	proj.Version = pc.Project.Version
	if err := s.repo.Update(ctx, tenants.ScopeFor(&tc), proj); err != nil {
		return projects.Project{}, err
	}
	s.audit(ctx, p, tc.Tenant.ID, action, "project", proj.ID.String(), nil)
	return proj, nil
}

// Archive marks the project archived (project.manage).
func (s *Service) Archive(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext) (projects.Project, error) {
	return s.setState(ctx, p, tc, pc, projects.StateArchived, "project.archived")
}

// Unarchive reactivates an archived project (project.manage).
func (s *Service) Unarchive(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext) (projects.Project, error) {
	return s.setState(ctx, p, tc, pc, projects.StateActive, "project.unarchived")
}

// --- members -------------------------------------------------------------

// AddMember grants project roles (project.members.manage); the user
// must be a tenant member.
func (s *Service) AddMember(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext,
	in UpsertMember) (projects.Membership, error) {
	if err := s.mutable(ctx, p, tc, pc); err != nil {
		return projects.Membership{}, err
	}
	if err := authz.Require(ctx, s.az, p, authz.ProjectMembersManage, res(tc, pc), s.rec); err != nil {
		return projects.Membership{}, err
	}
	if len(in.Roles) == 0 {
		return projects.Membership{}, apperr.New(apperr.Validation, "ROLES_REQUIRED", "roles required")
	}
	for _, r := range in.Roles {
		if !authz.ValidProjectRole(r) {
			return projects.Membership{}, apperr.New(apperr.Validation, "ROLE_INVALID", "unknown project role "+r)
		}
	}
	if _, err := s.tenantRepo.GetMembership(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, in.UserID); err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return projects.Membership{}, apperr.New(apperr.Validation, "NOT_TENANT_MEMBER",
				"user is not a tenant member")
		}
		return projects.Membership{}, err
	}
	m := projects.Membership{
		TenantID: tc.Tenant.ID, ProjectID: pc.Project.ID,
		UserID: in.UserID, Roles: in.Roles, Source: "manual",
	}
	if err := s.members.UpsertMembership(ctx, tenants.ScopeFor(&tc), m); err != nil {
		return projects.Membership{}, err
	}
	s.audit(ctx, p, tc.Tenant.ID, "project.member.added", "project_membership",
		pc.Project.ID.String()+"/"+in.UserID.String(), map[string]any{"roles": in.Roles})
	return m, nil
}

// UpdateRoles replaces a member's roles (project.members.manage); last
// project-admin is protected.
func (s *Service) UpdateRoles(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext,
	userID uuid.UUID, roles []string) (projects.Membership, error) {
	if err := s.mutable(ctx, p, tc, pc); err != nil {
		return projects.Membership{}, err
	}
	if err := authz.Require(ctx, s.az, p, authz.ProjectMembersManage, res(tc, pc), s.rec); err != nil {
		return projects.Membership{}, err
	}
	scope := tenants.ScopeFor(&tc)
	m, err := s.members.GetMembership(ctx, scope, pc.Project.ID, userID)
	if err != nil {
		return projects.Membership{}, err
	}
	for _, r := range roles {
		if !authz.ValidProjectRole(r) {
			return projects.Membership{}, apperr.New(apperr.Validation, "ROLE_INVALID", "unknown project role "+r)
		}
	}
	if len(roles) == 0 {
		return projects.Membership{}, apperr.New(apperr.Validation, "ROLES_REQUIRED", "roles required")
	}
	if slices.Contains(m.Roles, "project-admin") && !slices.Contains(roles, "project-admin") {
		n, err := s.members.CountProjectAdmins(ctx, scope, pc.Project.ID)
		if err != nil {
			return projects.Membership{}, err
		}
		if n <= 1 {
			return projects.Membership{}, apperr.New(apperr.Conflict, "LAST_ADMIN",
				"cannot remove the last project-admin")
		}
	}
	m.Roles = roles
	if err := s.members.UpsertMembership(ctx, scope, m); err != nil {
		return projects.Membership{}, err
	}
	s.audit(ctx, p, tc.Tenant.ID, "project.member.updated", "project_membership",
		pc.Project.ID.String()+"/"+userID.String(), map[string]any{"roles": roles})
	return m, nil
}

// RemoveMember deletes a membership (project.members.manage); last
// project-admin is protected.
func (s *Service) RemoveMember(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext, userID uuid.UUID) error {
	if err := s.mutable(ctx, p, tc, pc); err != nil {
		return err
	}
	if err := authz.Require(ctx, s.az, p, authz.ProjectMembersManage, res(tc, pc), s.rec); err != nil {
		return err
	}
	scope := tenants.ScopeFor(&tc)
	m, err := s.members.GetMembership(ctx, scope, pc.Project.ID, userID)
	if err != nil {
		return err
	}
	if slices.Contains(m.Roles, "project-admin") {
		n, err := s.members.CountProjectAdmins(ctx, scope, pc.Project.ID)
		if err != nil {
			return err
		}
		if n <= 1 {
			return apperr.New(apperr.Conflict, "LAST_ADMIN", "cannot remove the last project-admin")
		}
	}
	if err := s.members.DeleteMembership(ctx, scope, pc.Project.ID, userID); err != nil {
		return err
	}
	s.audit(ctx, p, tc.Tenant.ID, "project.member.removed", "project_membership",
		pc.Project.ID.String()+"/"+userID.String(), nil)
	return nil
}

// ListMembers pages project memberships (project.read or membership).
func (s *Service) ListMembers(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext,
	page tenants.Page) ([]projects.Membership, string, error) {
	if err := s.canRead(ctx, p, tc, pc); err != nil {
		return nil, "", err
	}
	return s.members.ListMemberships(ctx, tenants.ScopeFor(&tc), pc.Project.ID, page)
}

// --- bindings ------------------------------------------------------------

// validateBinding checks the cluster assignment constraints.
func (s *Service) validateBinding(ctx context.Context, scope tenants.Scope,
	tenantID uuid.UUID, in UpsertBinding) error {
	a, err := s.clusters.GetAssignment(ctx, scope, in.ClusterID, tenantID)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return apperr.New(apperr.Validation, "CLUSTER_NOT_ASSIGNED",
				"cluster is not assigned to this tenant")
		}
		return err
	}
	if al := a.Defaults.AllowedPartitions; len(al) > 0 {
		for _, p := range in.AllowedPartitions {
			if !slices.Contains(al, p) {
				return apperr.New(apperr.Validation, "PARTITION_NOT_ALLOWED",
					"partition "+p+" is not allowed by the cluster assignment")
			}
		}
	}
	if in.DefaultPartition != "" && len(in.AllowedPartitions) > 0 &&
		!slices.Contains(in.AllowedPartitions, in.DefaultPartition) {
		return apperr.New(apperr.Validation, "PARTITION_NOT_ALLOWED",
			"default_partition must be in allowed_partitions")
	}
	if pre := a.Defaults.DefaultAccountPrefix; pre != "" &&
		!strings.HasPrefix(in.SlurmAccount, pre) {
		return apperr.New(apperr.Validation, "ACCOUNT_PREFIX",
			"slurm_account must start with the assignment prefix "+pre)
	}
	if in.DefaultQoS != "" && len(in.AllowedQoS) > 0 &&
		!slices.Contains(in.AllowedQoS, in.DefaultQoS) {
		return apperr.New(apperr.Validation, "QOS_NOT_ALLOWED",
			"default_qos must be in allowed_qos")
	}
	return nil
}

// CreateBinding adds a cluster binding (project.manage).
func (s *Service) CreateBinding(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext,
	in UpsertBinding) (projects.ClusterBinding, error) {
	if err := s.mutable(ctx, p, tc, pc); err != nil {
		return projects.ClusterBinding{}, err
	}
	scope := tenants.ScopeFor(&tc)
	if err := s.validateBinding(ctx, scope, tc.Tenant.ID, in); err != nil {
		return projects.ClusterBinding{}, err
	}
	b := projects.ClusterBinding{
		ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID,
		ProjectID: pc.Project.ID, ClusterID: in.ClusterID,
		SlurmAccount: in.SlurmAccount, DefaultPartition: in.DefaultPartition,
		AllowedPartitions: in.AllowedPartitions, DefaultQoS: in.DefaultQoS,
		AllowedQoS: in.AllowedQoS, Enabled: true,
	}
	if in.Enabled != nil {
		b.Enabled = *in.Enabled
	}
	if err := s.bindings.CreateBinding(ctx, scope, b); err != nil {
		return projects.ClusterBinding{}, err
	}
	s.audit(ctx, p, tc.Tenant.ID, "project.binding.created", "project_cluster_binding",
		b.ID.String(), map[string]any{"cluster_id": b.ClusterID.String()})
	return b, nil
}

// GetBinding returns a binding (project.read or membership).
func (s *Service) GetBinding(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext,
	bindingID uuid.UUID) (projects.ClusterBinding, error) {
	if err := s.canRead(ctx, p, tc, pc); err != nil {
		return projects.ClusterBinding{}, err
	}
	return s.bindings.GetBinding(ctx, tenants.ScopeFor(&tc), pc.Project.ID, bindingID)
}

// ListBindings returns the project's bindings (project.read or membership).
func (s *Service) ListBindings(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext) ([]projects.ClusterBinding, error) {
	if err := s.canRead(ctx, p, tc, pc); err != nil {
		return nil, err
	}
	return s.bindings.ListBindings(ctx, tenants.ScopeFor(&tc), pc.Project.ID)
}

// UpdateBinding patches a binding (project.manage; revalidates
// assignment constraints).
func (s *Service) UpdateBinding(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext,
	bindingID uuid.UUID, in UpsertBinding) (projects.ClusterBinding, error) {
	if err := s.mutable(ctx, p, tc, pc); err != nil {
		return projects.ClusterBinding{}, err
	}
	scope := tenants.ScopeFor(&tc)
	b, err := s.bindings.GetBinding(ctx, scope, pc.Project.ID, bindingID)
	if err != nil {
		return projects.ClusterBinding{}, err
	}
	if in.ClusterID != uuid.Nil {
		b.ClusterID = in.ClusterID
	}
	if in.SlurmAccount != "" {
		b.SlurmAccount = in.SlurmAccount
	}
	if in.DefaultPartition != "" {
		b.DefaultPartition = in.DefaultPartition
	}
	if in.AllowedPartitions != nil {
		b.AllowedPartitions = in.AllowedPartitions
	}
	if in.DefaultQoS != "" {
		b.DefaultQoS = in.DefaultQoS
	}
	if in.AllowedQoS != nil {
		b.AllowedQoS = in.AllowedQoS
	}
	if in.Enabled != nil {
		b.Enabled = *in.Enabled
	}
	if err := s.validateBinding(ctx, scope, tc.Tenant.ID, UpsertBinding{
		ClusterID: b.ClusterID, SlurmAccount: b.SlurmAccount,
		DefaultPartition: b.DefaultPartition, AllowedPartitions: b.AllowedPartitions,
		DefaultQoS: b.DefaultQoS, AllowedQoS: b.AllowedQoS,
	}); err != nil {
		return projects.ClusterBinding{}, err
	}
	b.Version = in.Version
	if err := s.bindings.UpdateBinding(ctx, scope, b); err != nil {
		return projects.ClusterBinding{}, err
	}
	s.audit(ctx, p, tc.Tenant.ID, "project.binding.updated", "project_cluster_binding",
		b.ID.String(), nil)
	return b, nil
}

// DeleteBinding removes a binding (project.manage).
func (s *Service) DeleteBinding(ctx context.Context, p authn.Principal,
	tc tenants.TenantContext, pc projects.ProjectContext, bindingID uuid.UUID) error {
	if err := s.mutable(ctx, p, tc, pc); err != nil {
		return err
	}
	if err := s.bindings.DeleteBinding(ctx, tenants.ScopeFor(&tc), pc.Project.ID, bindingID); err != nil {
		return err
	}
	s.audit(ctx, p, tc.Tenant.ID, "project.binding.deleted", "project_cluster_binding",
		bindingID.String(), nil)
	return nil
}

// ResolveBinding returns the admission.Binding for (project, cluster);
// disabled bindings are invisible.
func (s *Service) ResolveBinding(ctx context.Context, scope tenants.Scope,
	projectID, clusterID uuid.UUID) (admission.Binding, error) {
	b, err := s.bindings.GetBindingByCluster(ctx, scope, projectID, clusterID)
	if err != nil {
		return admission.Binding{}, err
	}
	if !b.Enabled {
		return admission.Binding{}, apperr.New(apperr.NotFound, "NOT_FOUND", "binding disabled")
	}
	return admission.Binding{
		Account: b.SlurmAccount, DefaultPartition: b.DefaultPartition,
		AllowedPartitions: b.AllowedPartitions, AllowedQoS: b.AllowedQoS,
		DefaultQoS: b.DefaultQoS,
	}, nil
}
