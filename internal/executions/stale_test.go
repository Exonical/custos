package executions_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Exonical/custos/internal/executions"
	"github.com/Exonical/custos/internal/platform/apperr"
)

func TestIsStale(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"sentinel", executions.ErrTransitionStale, true},
		{"wrapped sentinel", fmt.Errorf("op: %w", executions.ErrTransitionStale), true},
		{"unique violation conflict", apperr.New(apperr.Conflict, "conflict", "resource conflict"), false},
		{"idempotency conflict", apperr.New(apperr.Conflict, "IDEMPOTENCY_MISMATCH", "mismatch"), false},
		{"stale code other kind", apperr.New(apperr.Internal, "TRANSITION_STALE", "x"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := executions.IsStale(tt.err); got != tt.want {
				t.Fatalf("IsStale(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
