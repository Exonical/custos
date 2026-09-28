package slurm_test

import (
	"testing"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/slurm"
)

func TestClassifyForbidden(t *testing.T) {
	if got := slurm.Classify(slurm.ErrForbidden); got != apperr.Forbidden {
		t.Fatalf("Classify(ErrForbidden)=%v", got)
	}
}

func TestClassifyRejectedAsValidation(t *testing.T) {
	if got := slurm.Classify(slurm.ErrRejected); got != apperr.Validation {
		t.Fatalf("Classify(ErrRejected)=%v", got)
	}
}
