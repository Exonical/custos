package workflowspec_test

import (
	"testing"
	"time"

	"github.com/Exonical/custos/internal/workflowspec"
)

func TestResourcesValidate(t *testing.T) {
	ok := workflowspec.Resources{Nodes: 2, Walltime: workflowspec.Duration(time.Hour)}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.Walltime = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("zero walltime accepted")
	}
	bad = ok
	bad.GPU = &workflowspec.GPURequest{Type: "h100", Count: 0}
	if err := bad.Validate(); err == nil {
		t.Fatal("zero gpu count accepted")
	}
	bad = ok
	bad.Array = &workflowspec.ArraySpec{Start: 5, End: 2}
	if err := bad.Validate(); err == nil {
		t.Fatal("inverted array range accepted")
	}
	bad = ok
	bad.Nodes = -1
	if err := bad.Validate(); err == nil {
		t.Fatal("negative nodes accepted")
	}
}
