package api

import (
	"testing"

	"github.com/Exonical/custos/internal/executions"
)

func TestExecutionDTOExposesTestFlag(t *testing.T) {
	for _, tc := range []struct {
		test bool
		want bool
	}{
		{test: true, want: true},
		{test: false, want: false},
	} {
		dto := execDTO(executions.Execution{IsTest: tc.test})
		if dto["test"] != tc.want {
			t.Errorf("execution test = %v, want %v", dto["test"], tc.want)
		}
	}
}
