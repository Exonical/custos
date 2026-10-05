package slurm_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/slurm"
)

func TestCustosJobCorrelation(t *testing.T) {
	id := uuid.MustParse("0190a6f0-1c2b-7d3e-8f40-5a6b7c8d9e0f")
	if got := slurm.CustosJobName(id); got != "custos-0190a6f0-1c2b-7d3e-8f40-5a6b7c8d9e0f" {
		t.Fatalf("name %q", got)
	}
	if got := slurm.CustosJobComment(id, "train"); got != "custos:0190a6f0-1c2b-7d3e-8f40-5a6b7c8d9e0f/train" {
		t.Fatalf("comment %q", got)
	}
	if got, ok := slurm.ParseCustosJobName(slurm.CustosJobName(id)); !ok || got != id {
		t.Fatalf("parse round trip: %v %v", got, ok)
	}
	for _, name := range []string{"", "custos-", "custos-nope", "external", "xcustos-" + id.String()} {
		if got, ok := slurm.ParseCustosJobName(name); ok || got != uuid.Nil {
			t.Fatalf("parse %q: %v %v", name, got, ok)
		}
	}
}
