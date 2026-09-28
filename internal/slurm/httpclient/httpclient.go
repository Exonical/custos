// Package httpclient builds SSRF-safe HTTP clients for talking to
// slurmrestd (docs/slurm.md "SSRF protection", threat-model TM-08).
// Clients are built by safehttp.NewWithCertificate — TLS 1.3 minimum, no
// redirects, pinned CA, optional mTLS, and a dialer that vets every
// resolved IP and dials the vetted literal so DNS cannot rebind between
// check and dial. This package adds Slurm endpoint validation and maps
// safehttp's vetting errors onto Slurm error codes.
package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/safehttp"
	"github.com/Exonical/custos/internal/slurm"
)

// DialPolicy controls which resolved addresses may be dialed.
type DialPolicy = safehttp.DialPolicy

// New builds an *http.Client for ep under policy. The client never
// follows redirects and never uses InsecureSkipVerify.
func New(ep slurm.Endpoint, policy DialPolicy) (*http.Client, error) {
	u, err := url.Parse(ep.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, apperr.New(apperr.Invalid, "slurm.endpoint_invalid", "cluster endpoint is not a valid URL")
	}
	if u.Scheme == "http" && (!policy.AllowHTTP || !policy.AllowLoopback) {
		return nil, apperr.New(apperr.Invalid, "slurm.endpoint_scheme", "cluster endpoint must use https")
	}
	return safehttp.NewWithCertificate(ep.BaseURL, ep.CABundlePEM, ep.ClientCert, policy)
}

// VetHost resolves host and applies the dial policy to every returned
// address without dialing — used at cluster registration as an early
// error. httpOnlyLoopback restricts plaintext endpoints to loopback.
// Resolution failures wrap slurm.ErrUnavailable; policy denials are
// Forbidden with code "slurm.dial_denied".
func VetHost(ctx context.Context, host string, policy DialPolicy,
	httpOnlyLoopback bool) error {
	_, err := safehttp.VetHost(ctx, host, policy, httpOnlyLoopback)
	switch {
	case err == nil:
		return nil
	case apperr.Is(err, apperr.Forbidden):
		return apperr.Wrap(err, apperr.Forbidden, "slurm.dial_denied",
			"a resolved address is denied by the cluster dial policy")
	default:
		return fmt.Errorf("%w: %v", slurm.ErrUnavailable, err)
	}
}
