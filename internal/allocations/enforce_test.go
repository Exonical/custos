package allocations_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/allocations"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
)

type enforceRepo struct {
	allocations.Repository
	active    []allocations.Allocation
	activeErr error
	inTx      allocations.CheckResult
	inTxErr   error
}

func (r *enforceRepo) Active(context.Context, tenants.Scope, uuid.UUID, time.Time) ([]allocations.Allocation, error) {
	return r.active, r.activeErr
}

func (r *enforceRepo) InFlight(context.Context, tenants.Scope, uuid.UUID) (map[string]float64, error) {
	return nil, nil
}

func (r *enforceRepo) CheckInTx(context.Context, any, uuid.UUID, map[string]float64) (allocations.CheckResult, error) {
	return r.inTx, r.inTxErr
}

func newEnforcement(r *enforceRepo) *allocations.Enforcement {
	return allocations.NewEnforcement(allocations.NewService(r, nil, nil, nil), tenants.PlatformScope(), uuid.New())
}

func TestEnforcementDisabled(t *testing.T) {
	e := allocations.NewEnforcement(nil, tenants.PlatformScope(), uuid.New())
	if e.Enabled() {
		t.Fatal("nil service reported enabled")
	}
	if d, w := e.Allocate(context.Background())(admission.ResolvedResources{}); d != nil || w != nil {
		t.Fatalf("disabled allocate = %+v, %+v", d, w)
	}
	if err := e.Guard(nil)(context.Background(), nil); err != nil {
		t.Fatalf("disabled guard = %v", err)
	}
	if d := e.Denial(); d != nil {
		t.Fatalf("disabled denial = %+v", d)
	}
	e.ObserveDenial(context.Background())
	e.Admitted(context.Background(), authn.Principal{}, uuid.New())
}

func TestEnforcementAllocateCheckFailure(t *testing.T) {
	e := newEnforcement(&enforceRepo{activeErr: errors.New("db down")})
	d, w := e.Allocate(context.Background())(admission.ResolvedResources{})
	if d == nil || d.Code != "ALLOCATION_CHECK_FAILED" || d.Field != "allocation" ||
		d.Severity != validation.SeverityError || w != nil {
		t.Fatalf("check failure = %+v, %+v", d, w)
	}
	if e.Denial() != nil {
		t.Fatal("check failure must not record an allocation denial")
	}
}

func TestEnforcementAllocateSoft(t *testing.T) {
	e := newEnforcement(&enforceRepo{active: []allocations.Allocation{{
		Name: "gpu", Unit: "gpu_hours", Enforcement: "soft", LimitAmount: 1, ConsumedAmount: 1,
	}}})
	d, w := e.Allocate(context.Background())(admission.ResolvedResources{Nodes: 1, GPUCount: 1, WalltimeSeconds: 3600})
	if d != nil || len(w) != 1 || len(e.Result().Soft) != 1 {
		t.Fatalf("soft allocate = %+v, %+v, %+v", d, w, e.Result())
	}
	e.Admitted(context.Background(), authn.Principal{}, uuid.New())
}

func TestEnforcementGuard(t *testing.T) {
	denial := &admission.Denial{Code: "ALLOCATION_EXHAUSTED", Field: "allocation", Message: "cpu_hours exhausted"}
	e := newEnforcement(&enforceRepo{inTx: allocations.CheckResult{Denial: denial}})
	err := e.Guard(map[string]float64{"cpu_hours": 1})(context.Background(), nil)
	if !apperr.Is(err, apperr.Validation) {
		t.Fatalf("guard err = %v", err)
	}
	if got := e.Denial(); got != denial {
		t.Fatalf("denial = %+v", got)
	}
	e.ObserveDenial(context.Background())

	txErr := errors.New("tx failed")
	e = newEnforcement(&enforceRepo{inTxErr: txErr})
	if err := e.Guard(nil)(context.Background(), nil); !errors.Is(err, txErr) {
		t.Fatalf("guard err = %v", err)
	}
	if e.Denial() != nil {
		t.Fatal("tx error must not record an allocation denial")
	}
}
