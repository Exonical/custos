package allocations

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/tenants"
)

const policySyncKind = "policy.sync"

// Service implements allocation management and admission checks.
type Service struct {
	repo     Repository
	bindings projects.BindingRepository
	members  projects.MembershipRepository
	az       authz.Authorizer
	audit    audit.Recorder
	denials  metric.Int64Counter
	soft     metric.Int64Counter
	enq      workqueue.Execer
}

// NewService wires the allocation repository, authorization, and audit dependencies.
func NewService(r Repository, b projects.BindingRepository, az authz.Authorizer, a audit.Recorder, members ...projects.MembershipRepository) *Service {
	s := &Service{repo: r, bindings: b, az: az, audit: a}
	if len(members) > 0 {
		s.members = members[0]
	}
	return s
}

// SetEnqueuer installs the shared policy.sync workqueue executor.
func (s *Service) SetEnqueuer(ex workqueue.Execer) { s.enq = ex }
func (s *Service) enqueuePolicySync(ctx context.Context, clusterID uuid.UUID) error {
	if s.enq == nil {
		return nil
	}
	_, err := workqueue.Enqueue(ctx, s.enq, workqueue.EnqueueRequest{Kind: policySyncKind, Key: "cluster:" + clusterID.String()})
	return err
}

// SetMeterProvider installs allocation enforcement counters.
func (s *Service) SetMeterProvider(mp metric.MeterProvider) {
	if mp == nil {
		return
	}
	m := mp.Meter("custos/allocations")
	s.denials, _ = m.Int64Counter("custos_allocation_denials_total")
	s.soft, _ = m.Int64Counter("custos_allocation_soft_exceeded_total")
}

// Observe increments counters for allocation denials and soft overruns.
func (s *Service) Observe(ctx context.Context, result CheckResult) {
	if result.Denial != nil && s.denials != nil {
		s.denials.Add(ctx, 1, metric.WithAttributes(attribute.String("unit", result.DenialUnit)))
	}
	if s.soft != nil {
		for _, a := range result.Soft {
			s.soft.Add(ctx, 1, metric.WithAttributes(attribute.String("unit", a.Unit)))
		}
	}
}
func allocRes(t, p uuid.UUID) authz.Resource {
	return authz.Resource{Kind: "project", ID: p.String(), TenantID: t.String(), ProjectID: p.String()}
}
func (s *Service) require(ctx context.Context, p authn.Principal, a authz.Action, tc tenants.TenantContext, project uuid.UUID) error {
	return authz.Require(ctx, s.az, p, a, allocRes(tc.Tenant.ID, project), s.audit)
}
func (s *Service) record(ctx context.Context, p authn.Principal, a Allocation, event string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: p.UserID.String()}, TenantID: &a.TenantID, Action: event, Target: audit.Target{Type: "allocation", ID: a.ID.String()}, Result: audit.ResultAllow, Details: map[string]any{"project_id": a.ProjectID.String(), "binding_id": a.BindingID.String(), "unit": a.Unit}})
}

// CreateInput contains client-supplied allocation fields.
type CreateInput struct {
	BindingID   uuid.UUID `json:"binding_id"`
	Name        string    `json:"name"`
	Unit        string    `json:"unit"`
	LimitAmount float64   `json:"limit_amount"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	Enforcement string    `json:"enforcement"`
}

// UpdateInput contains optional allocation changes and the expected version.
type UpdateInput struct {
	Name        *string    `json:"name,omitempty"`
	Unit        *string    `json:"unit,omitempty"`
	LimitAmount *float64   `json:"limit_amount,omitempty"`
	PeriodStart *time.Time `json:"period_start,omitempty"`
	PeriodEnd   *time.Time `json:"period_end,omitempty"`
	Enforcement *string    `json:"enforcement,omitempty"`
	Version     int        `json:"version"`
}

func valid(a Allocation) bool {
	return a.Name != "" && a.LimitAmount >= 0 && a.PeriodEnd.After(a.PeriodStart) && (a.Unit == "cpu_hours" || a.Unit == "gpu_hours" || a.Unit == "node_hours") && (a.Enforcement == "hard" || a.Enforcement == "soft")
}

// Create creates and audits a binding allocation.
func (s *Service) Create(ctx context.Context, p authn.Principal, tc tenants.TenantContext, pc projects.ProjectContext, in CreateInput) (Allocation, error) {
	if err := s.require(ctx, p, authz.AllocationManage, tc, pc.Project.ID); err != nil {
		return Allocation{}, err
	}
	b, err := s.bindings.GetBinding(ctx, tenants.ScopeFor(&tc), pc.Project.ID, in.BindingID)
	if err != nil {
		return Allocation{}, err
	}
	a := Allocation{ID: uuid.Must(uuid.NewV7()), TenantID: tc.Tenant.ID, ProjectID: pc.Project.ID, BindingID: b.ID, Name: in.Name, Unit: in.Unit, LimitAmount: in.LimitAmount, PeriodStart: in.PeriodStart, PeriodEnd: in.PeriodEnd, Enforcement: in.Enforcement, Version: 1, CreatedBy: p.UserID}
	if !valid(a) {
		return Allocation{}, apperr.New(apperr.Validation, "ALLOCATION_INVALID", "allocation fields are invalid")
	}
	if err := s.repo.Create(ctx, tenants.ScopeFor(&tc), a); err != nil {
		return Allocation{}, err
	}
	created, err := s.repo.Get(ctx, tenants.ScopeFor(&tc), pc.Project.ID, a.ID)
	if err != nil {
		return Allocation{}, err
	}
	if err := s.enqueuePolicySync(ctx, b.ClusterID); err != nil {
		return Allocation{}, err
	}
	s.record(ctx, p, created, "allocation.created")
	return created, nil
}

// List returns allocations for a project after authorization.
func (s *Service) List(ctx context.Context, p authn.Principal, tc tenants.TenantContext, pc projects.ProjectContext) ([]Allocation, error) {
	if err := s.require(ctx, p, authz.AllocationRead, tc, pc.Project.ID); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, tenants.ScopeFor(&tc), pc.Project.ID)
}

// Get returns one project allocation after authorization.
func (s *Service) Get(ctx context.Context, p authn.Principal, tc tenants.TenantContext, pc projects.ProjectContext, id uuid.UUID) (Allocation, error) {
	if err := s.require(ctx, p, authz.AllocationRead, tc, pc.Project.ID); err != nil {
		return Allocation{}, err
	}
	return s.repo.Get(ctx, tenants.ScopeFor(&tc), pc.Project.ID, id)
}

// Update applies an optimistic allocation update and audits it.
func (s *Service) Update(ctx context.Context, p authn.Principal, tc tenants.TenantContext, pc projects.ProjectContext, id uuid.UUID, in UpdateInput) (Allocation, error) {
	if err := s.require(ctx, p, authz.AllocationManage, tc, pc.Project.ID); err != nil {
		return Allocation{}, err
	}
	a, err := s.repo.Get(ctx, tenants.ScopeFor(&tc), pc.Project.ID, id)
	if err != nil {
		return a, err
	}
	if in.Name != nil {
		a.Name = *in.Name
	}
	if in.Unit != nil {
		a.Unit = *in.Unit
	}
	if in.LimitAmount != nil {
		a.LimitAmount = *in.LimitAmount
	}
	if in.PeriodStart != nil {
		a.PeriodStart = *in.PeriodStart
	}
	if in.PeriodEnd != nil {
		a.PeriodEnd = *in.PeriodEnd
	}
	if in.Enforcement != nil {
		a.Enforcement = *in.Enforcement
	}
	binding, err := s.bindings.GetBinding(ctx, tenants.ScopeFor(&tc), pc.Project.ID, a.BindingID)
	if err != nil {
		return a, err
	}
	a.Version = in.Version
	if !valid(a) {
		return a, apperr.New(apperr.Validation, "ALLOCATION_INVALID", "allocation fields are invalid")
	}
	if err := s.repo.Update(ctx, tenants.ScopeFor(&tc), a); err != nil {
		return a, err
	}
	if err := s.enqueuePolicySync(ctx, binding.ClusterID); err != nil {
		return a, err
	}
	s.record(ctx, p, a, "allocation.updated")
	return s.repo.Get(ctx, tenants.ScopeFor(&tc), pc.Project.ID, id)
}

// Delete removes and audits a project allocation.
func (s *Service) Delete(ctx context.Context, p authn.Principal, tc tenants.TenantContext, pc projects.ProjectContext, id uuid.UUID) error {
	if err := s.require(ctx, p, authz.AllocationManage, tc, pc.Project.ID); err != nil {
		return err
	}
	a, err := s.repo.Get(ctx, tenants.ScopeFor(&tc), pc.Project.ID, id)
	if err != nil {
		return err
	}
	binding, err := s.bindings.GetBinding(ctx, tenants.ScopeFor(&tc), pc.Project.ID, a.BindingID)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, tenants.ScopeFor(&tc), pc.Project.ID, id); err != nil {
		return err
	}
	if err := s.enqueuePolicySync(ctx, binding.ClusterID); err != nil {
		return err
	}
	s.record(ctx, p, a, "allocation.deleted")
	return nil
}

// AuditSoft records one event for each soft allocation overrun.
func (s *Service) AuditSoft(ctx context.Context, p authn.Principal, tid uuid.UUID, result CheckResult) {
	for _, a := range result.Soft {
		if s.audit != nil {
			_ = s.audit.Record(ctx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: p.UserID.String()}, TenantID: &tid, Action: "allocation.soft_exceeded", Target: audit.Target{Type: "allocation", ID: a.ID.String()}, Result: audit.ResultAllow, Details: map[string]any{"allocation": a.Name, "unit": a.Unit}})
		}
	}
}

// ListTenant returns visible allocations across a tenant's projects.
func (s *Service) ListTenant(ctx context.Context, p authn.Principal, tc tenants.TenantContext) ([]Allocation, error) {
	items, err := s.repo.ListTenant(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID)
	if err != nil {
		return nil, err
	}
	projectRoles := map[uuid.UUID][]string{}
	if s.members != nil {
		memberships, e := s.members.ListMembershipsForUser(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, p.UserID)
		if e != nil {
			return nil, e
		}
		for _, m := range memberships {
			projectRoles[m.ProjectID] = m.Roles
		}
	}
	out := items[:0]
	for _, a := range items {
		checkCtx := ctx
		if roles, ok := projectRoles[a.ProjectID]; ok {
			checkCtx = authz.WithProjectRoles(checkCtx, a.ProjectID.String(), roles)
		}
		d, e := s.az.Check(checkCtx, p, authz.AllocationRead, allocRes(tc.Tenant.ID, a.ProjectID))
		if e == nil && d.Allow {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		allowed := false
		for projectID, roles := range projectRoles {
			checkCtx := authz.WithProjectRoles(ctx, projectID.String(), roles)
			d, e := s.az.Check(checkCtx, p, authz.AllocationRead, allocRes(tc.Tenant.ID, projectID))
			if e == nil && d.Allow {
				allowed = true
				break
			}
		}
		if !allowed {
			if err := authz.Require(ctx, s.az, p, authz.AllocationRead, authz.Resource{Kind: "tenant", ID: tc.Tenant.ID.String(), TenantID: tc.Tenant.ID.String()}, s.audit); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// Check applies active budgets with current in-flight reservations.
func (s *Service) Check(ctx context.Context, scope tenants.Scope, bindingID uuid.UUID, req admission.ResolvedResources) (CheckResult, error) {
	active, err := s.repo.Active(ctx, scope, bindingID, time.Now())
	if err != nil {
		return CheckResult{}, err
	}
	reserved, err := s.repo.InFlight(ctx, scope, bindingID)
	if err != nil {
		return CheckResult{}, err
	}
	estimate := admission.EstimateCost(req)
	return Check(active, reserved, estimate), nil
}

// CheckInTx rechecks allocations and in-flight jobs inside the job-persist transaction.
func (s *Service) CheckInTx(ctx context.Context, tx db.Tx, bindingID uuid.UUID, estimate map[string]float64) (CheckResult, error) {
	return s.repo.CheckInTx(ctx, tx, bindingID, estimate)
}

// ActiveForBinding returns active allocations for policy reconciliation.
func (s *Service) ActiveForBinding(ctx context.Context, bindingID uuid.UUID, now time.Time) ([]Allocation, error) {
	return s.repo.Active(ctx, tenants.PlatformScope(), bindingID, now)
}

// Refresh materializes usage consumption for allocations, optionally by cluster.
func (s *Service) Refresh(ctx context.Context, clusterID *uuid.UUID) (int, error) {
	return s.repo.Refresh(ctx, clusterID)
}
