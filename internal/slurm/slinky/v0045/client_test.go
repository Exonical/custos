package v0045

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Exonical/custos/internal/slurm"
	api "github.com/SlinkyProject/slurm-client/api/v0045"
)

func TestAPIErrorForbiddenClassification(t *testing.T) {
	str := func(s string) *string { return &s }
	num := func(n int32) *int32 { return &n }
	cases := []struct {
		name string
		err  api.V0045OpenapiError
		want error
	}{
		{"access denied errno", api.V0045OpenapiError{Error: str("Unexpected failure"), ErrorNumber: num(eslurmAccessDenied)}, slurm.ErrForbidden},
		{"errno without text", api.V0045OpenapiError{ErrorNumber: num(eslurmAccessDenied)}, slurm.ErrForbidden},
		{"permission in error", api.V0045OpenapiError{Error: str("Access/permission denied"), ErrorNumber: num(1)}, slurm.ErrForbidden},
		{"not authorized in description", api.V0045OpenapiError{Error: str("rejected"), Description: str("User is Not Authorized to modify account")}, slurm.ErrForbidden},
		{"access denied in description", api.V0045OpenapiError{Description: str("ACCESS DENIED")}, slurm.ErrForbidden},
		{"unrelated rejection", api.V0045OpenapiError{Error: str("Invalid account specified"), ErrorNumber: num(2045)}, slurm.ErrRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := api.V0045OpenapiErrors{tc.err}
			if got := apiError(&errs, http.StatusInternalServerError); !errors.Is(got, tc.want) {
				t.Fatalf("apiError=%v, want %v", got, tc.want)
			}
		})
	}
}
