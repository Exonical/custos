// Package v0045 implements the slurm.Cluster and slurm.Accounting ports
// against slurmrestd's data_parser v0.0.45 API using the generated Slinky
// client (docs/slurm.md). This is the only package (with its tests) that
// may import github.com/SlinkyProject/slurm-client.
package v0045

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/SlinkyProject/slurm-client/api/v0045"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/slurm"
)

// APIVersion is the slurmrestd data_parser version this adapter serves.
const APIVersion = "v0.0.45"

// Client implements slurm.Cluster and slurm.Accounting over one
// slurmrestd endpoint.
type Client struct {
	api api.ClientWithResponsesInterface
}

var (
	_ slurm.Cluster    = (*Client)(nil)
	_ slurm.Accounting = (*Client)(nil)
)

// New builds a client. hc must already be SSRF-vetted (see
// internal/slurm/httpclient). The credential is read at request time via
// a request editor — the token is never stored as a string and never
// logged.
func New(ep slurm.Endpoint, cred slurm.Credential, hc *http.Client) (*Client, error) {
	auth := func(_ context.Context, req *http.Request) error {
		req.Header.Set("X-SLURM-USER-NAME", cred.UserName)
		req.Header.Set("X-SLURM-USER-TOKEN", string(cred.Token.Reveal()))
		return nil
	}
	c, err := api.NewClientWithResponses(ep.BaseURL,
		api.WithHTTPClient(hc),
		api.WithRequestEditorFn(auth))
	if err != nil {
		return nil, fmt.Errorf("v0045 client: %w", err)
	}
	return &Client{api: c}, nil
}

// --- helpers -----------------------------------------------------------

func strV(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func i32s(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}

func i32u(v *api.V0045Uint32NoValStruct) int32 {
	if v == nil || v.Set == nil || !*v.Set || v.Number == nil {
		return 0
	}
	return *v.Number
}

func i32(v *int32) int {
	if v == nil {
		return 0
	}
	return int(*v)
}

func i64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func u64v(v *api.V0045Uint64NoValStruct) int64 {
	if v == nil || v.Set == nil || !*v.Set || v.Number == nil {
		return 0
	}
	return *v.Number
}

func u32v(v *api.V0045Uint32NoValStruct) int {
	if v == nil || v.Set == nil || !*v.Set || v.Number == nil {
		return 0
	}
	return int(*v.Number)
}

// durVal converts a NoVal seconds field to *time.Duration (nil when
// unset or infinite).
func durVal(v *api.V0045Uint32NoValStruct) *time.Duration {
	if v == nil || v.Set == nil || !*v.Set || v.Number == nil {
		return nil
	}
	if v.Infinite != nil && *v.Infinite {
		return nil
	}
	d := time.Duration(*v.Number) * time.Second
	return &d
}

func tsVal(v *api.V0045Uint64NoValStruct) time.Time {
	if v == nil || v.Set == nil || !*v.Set || v.Number == nil {
		return time.Time{}
	}
	return time.Unix(*v.Number, 0).UTC()
}

func csv(v *api.V0045CsvString) []string {
	if v == nil {
		return nil
	}
	return *v
}

// csvSplit splits a comma-separated *string field.
func csvSplit(s *string) []string {
	if s == nil || *s == "" {
		return nil
	}
	return strings.Split(*s, ",")
}

// checkMeta rejects responses that provably come from a different
// data_parser version. A missing plugin block cannot confirm a version
// and is tolerated (mapping is defensive anyway).
func checkMeta(meta *api.V0045OpenapiMeta) error {
	if meta == nil || meta.Plugin == nil || meta.Plugin.DataParser == nil {
		return nil
	}
	if !strings.Contains(*meta.Plugin.DataParser, APIVersion) {
		return apperr.New(apperr.Invalid, "slurm.api_version_mismatch",
			"slurmrestd reported data_parser "+
				strV(meta.Plugin.DataParser)+", want "+APIVersion)
	}
	return nil
}

func slurmRelease(meta *api.V0045OpenapiMeta) string {
	if meta == nil || meta.Slurm == nil || meta.Slurm.Release == nil {
		return ""
	}
	return *meta.Slurm.Release
}

// apiError merges slurmrestd's errors[]/warnings[] into one error. Tokens
// are never included — slurmrestd error strings are not user-controlled
// secret material, but only the code, short error, and source are kept.
func apiError(errs *api.V0045OpenapiErrors, status int) error {
	if errs != nil && len(*errs) > 0 {
		parts := make([]string, 0, len(*errs))
		for _, e := range *errs {
			code := i32(e.ErrorNumber)
			switch {
			case e.Error != nil && e.ErrorNumber != nil:
				parts = append(parts, fmt.Sprintf("%s (%d)", *e.Error, code))
			case e.Error != nil:
				parts = append(parts, *e.Error)
			case e.Description != nil:
				parts = append(parts, *e.Description)
			default:
				parts = append(parts, fmt.Sprintf("error %d", code))
			}
		}
		base := fmt.Errorf("%w: %s", slurm.ErrUnavailable,
			strings.Join(parts, "; "))
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			base = fmt.Errorf("%w: %s", slurm.ErrUnauthorized,
				strings.Join(parts, "; "))
		}
		if status == http.StatusNotFound {
			base = fmt.Errorf("%w: %s", slurm.ErrNotFound,
				strings.Join(parts, "; "))
		}
		return base
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: status %d", slurm.ErrUnauthorized, status)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: status %d", slurm.ErrNotFound, status)
	case status >= 500 || status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: status %d", slurm.ErrUnavailable, status)
	case status >= 400:
		return apperr.New(apperr.Invalid, "slurm.request_rejected",
			fmt.Sprintf("slurmrestd rejected the request: status %d", status))
	default:
		return nil
	}
}

// unavailable wraps a transport-level failure.
func unavailable(err error) error {
	return fmt.Errorf("%w: %v", slurm.ErrUnavailable, err)
}

// noBody is the error for a nil JSON payload.
var errEmpty = errors.New("slurmrestd returned no JSON body")
