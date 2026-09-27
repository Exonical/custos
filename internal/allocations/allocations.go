// Package allocations owns project binding budgets and admission checks.
package allocations

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/tenants"
)

// Allocation is a budget for a project-cluster binding.
type Allocation struct {
	ID, TenantID, ProjectID, BindingID uuid.UUID
	Name, Unit, Enforcement            string
	LimitAmount, ConsumedAmount        float64
	PeriodStart, PeriodEnd             time.Time
	ConsumedAsOf                       *time.Time
	Version                            int
	CreatedAt, UpdatedAt               time.Time
	CreatedBy                          uuid.UUID
}

// Repository persists allocations and their consumption/reservations.
type Repository interface {
	Create(context.Context, tenants.Scope, Allocation) error
	Get(context.Context, tenants.Scope, uuid.UUID, uuid.UUID) (Allocation, error)
	List(context.Context, tenants.Scope, uuid.UUID) ([]Allocation, error)
	ListTenant(context.Context, tenants.Scope, uuid.UUID) ([]Allocation, error)
	Update(context.Context, tenants.Scope, Allocation) error
	Delete(context.Context, tenants.Scope, uuid.UUID, uuid.UUID) error
	Active(context.Context, tenants.Scope, uuid.UUID, time.Time) ([]Allocation, error)
	InFlight(context.Context, tenants.Scope, uuid.UUID) (map[string]float64, error)
	Refresh(context.Context, *uuid.UUID) (int, error)
	CheckInTx(context.Context, any, uuid.UUID, map[string]float64) (CheckResult, error)
}

// CheckResult reports allocation decisions for one admission attempt.
type CheckResult struct {
	Estimate   map[string]float64
	Warnings   []admission.Warning
	Denial     *admission.Denial
	DenialUnit string
	Soft       []Allocation
}

// Check applies active allocation limits to consumed, reserved, and estimated cost.
func Check(items []Allocation, reserved map[string]float64, estimate map[string]float64) CheckResult {
	out := CheckResult{Estimate: estimate}
	for _, a := range items {
		e := admission.CostMilli(estimate[a.Unit])
		total := a.ConsumedAmount + reserved[a.Unit] + e
		if total <= a.LimitAmount {
			continue
		}
		if a.Enforcement == "hard" {
			out.DenialUnit = a.Unit
			out.Denial = &admission.Denial{Code: "ALLOCATION_EXHAUSTED", Field: "allocation", Message: fmt.Sprintf("allocation %s (%s): limit %.3f, consumed %.3f, reserved %.3f, estimate %.3f", a.Name, a.Unit, a.LimitAmount, a.ConsumedAmount, reserved[a.Unit], e)}
			return out
		}
		out.Soft = append(out.Soft, a)
		out.Warnings = append(out.Warnings, admission.Warning{Severity: "WARN", Code: "ALLOCATION_SOFT_EXCEEDED", Allocation: a.Name, Unit: a.Unit, Message: a.Name + " soft allocation exceeded"})
	}
	return out
}
