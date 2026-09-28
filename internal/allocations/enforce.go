package allocations

import (
	"context"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
)

// Enforcement runs allocation enforcement for one admission attempt:
// the pre-build Check, the in-transaction CheckInTx recheck, and the
// metrics/audit bookkeeping around them. A nil Service disables
// enforcement and every method becomes a no-op.
type Enforcement struct {
	svc       *Service
	scope     tenants.Scope
	bindingID uuid.UUID
	result    CheckResult
}

// NewEnforcement starts allocation enforcement against bindingID.
func NewEnforcement(s *Service, scope tenants.Scope, bindingID uuid.UUID) *Enforcement {
	return &Enforcement{svc: s, scope: scope, bindingID: bindingID}
}

// Enabled reports whether allocation enforcement is active.
func (e *Enforcement) Enabled() bool { return e.svc != nil }

// Result returns the most recent allocation check result.
func (e *Enforcement) Result() CheckResult { return e.result }

// Allocate returns an admission.BuildInput.Allocate hook that checks
// the resolved request against active allocations.
func (e *Enforcement) Allocate(ctx context.Context) func(admission.ResolvedResources) (*admission.Denial, []admission.Warning) {
	return func(rr admission.ResolvedResources) (*admission.Denial, []admission.Warning) {
		if e.svc == nil {
			return nil, nil
		}
		result, err := e.svc.Check(ctx, e.scope, e.bindingID, rr)
		if err != nil {
			return &admission.Denial{Code: "ALLOCATION_CHECK_FAILED", Field: "allocation",
				Message: "allocation check unavailable", Severity: validation.SeverityError}, nil
		}
		e.result = result
		return result.Denial, result.Warnings
	}
}

// Guard returns a persist-transaction guard that rechecks estimate
// against allocations and in-flight jobs, failing on a hard denial.
func (e *Enforcement) Guard(estimate map[string]float64) func(context.Context, db.Tx) error {
	return func(ctx context.Context, tx db.Tx) error {
		if e.svc == nil {
			return nil
		}
		checked, err := e.svc.CheckInTx(ctx, tx, e.bindingID, estimate)
		if err != nil {
			return err
		}
		e.result = checked
		if checked.Denial != nil {
			return apperr.New(apperr.Validation, checked.Denial.Code, checked.Denial.Message)
		}
		return nil
	}
}

// Denial returns the hard denial from the most recent allocation
// check, or nil when enforcement is disabled or the check allowed.
func (e *Enforcement) Denial() *admission.Denial {
	if e.svc == nil {
		return nil
	}
	return e.result.Denial
}

// ObserveDenial records the allocation denial metric when the most
// recent check denied.
func (e *Enforcement) ObserveDenial(ctx context.Context) {
	if e.Denial() != nil {
		e.svc.Observe(ctx, e.result)
	}
}

// Admitted records metrics and soft-overrun audit events for a
// successfully persisted admission.
func (e *Enforcement) Admitted(ctx context.Context, p authn.Principal, tenantID uuid.UUID) {
	if e.svc == nil {
		return
	}
	e.svc.Observe(ctx, e.result)
	if len(e.result.Soft) > 0 {
		e.svc.AuditSoft(ctx, p, tenantID, e.result)
	}
}
