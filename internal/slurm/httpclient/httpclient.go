// Package httpclient builds SSRF-safe HTTP clients for talking to
// slurmrestd (docs/slurm.md "SSRF protection", threat-model TM-08):
// TLS 1.3 minimum, no redirects, pinned CA, optional mTLS, and a dialer
// that re-checks resolved IPs against a policy before connecting — and
// dials the vetted IP rather than the hostname so DNS cannot rebind
// between check and dial.
package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/safehttp"
	"github.com/Exonical/custos/internal/slurm"
)

// DialPolicy controls which resolved addresses may be dialed.
type DialPolicy struct {
	// AllowPrivate permits RFC1918/ULA addresses (deployments whose
	// clusters live on private networks set this).
	AllowPrivate bool
	// AllowLoopback permits loopback (dev/test only).
	AllowLoopback bool
	// DenyCIDRs are always refused, checked first.
	DenyCIDRs []netip.Prefix
	// AllowHTTP permits http:// endpoints, but only to loopback targets
	// and only alongside AllowLoopback (dev).
	AllowHTTP bool
}

var alwaysDenied = []netip.Prefix{
	netip.MustParsePrefix("169.254.169.254/32"), // cloud metadata
	netip.MustParsePrefix("fd00:ec2::254/128"),  // cloud metadata v6
}

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
	return safehttp.NewWithCertificate(ep.BaseURL, ep.CABundlePEM, ep.ClientCert,
		safehttp.DialPolicy{AllowPrivate: policy.AllowPrivate,
			AllowLoopback: policy.AllowLoopback, AllowHTTP: policy.AllowHTTP,
			DenyCIDRs: policy.DenyCIDRs})
}

// lookupIP is the resolver used by vetDial; unexported and injectable so
// tests can return crafted address lists.
var lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// vetDial resolves host, vets every returned IP against policy, and dials
// the first address literal (defeats DNS rebinding between the check and
// the dial). It fails closed: a single denied address in the answer —
// e.g. a public/private mix — refuses the connection rather than
// narrowing to a permitted IP. httpOnlyLoopback additionally restricts
// plaintext endpoints to loopback targets.
func vetDial(ctx context.Context, d *net.Dialer, policy DialPolicy,
	network, addr string, httpOnlyLoopback bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("slurm dial: %w", err)
	}
	first, err := vetAddrs(ctx, host, policy, httpOnlyLoopback)
	if err != nil {
		return nil, err
	}
	return d.DialContext(ctx, network, net.JoinHostPort(first.String(), port))
}

// VetHost resolves host and applies the dial policy to every returned
// address without dialing — used at cluster registration as an early
// error. httpOnlyLoopback restricts plaintext endpoints to loopback.
func VetHost(ctx context.Context, host string, policy DialPolicy,
	httpOnlyLoopback bool) error {
	_, err := vetAddrs(ctx, host, policy, httpOnlyLoopback)
	return err
}

// vetAddrs resolves host and returns the first permitted address; any
// denied address fails closed.
func vetAddrs(ctx context.Context, host string, policy DialPolicy,
	httpOnlyLoopback bool) (netip.Addr, error) {
	ips, err := lookupIP(ctx, host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%w: %v", slurm.ErrUnavailable, err)
	}
	var first netip.Addr
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		a = a.Unmap()
		if !allowed(a, policy, httpOnlyLoopback) {
			return netip.Addr{}, apperr.New(apperr.Forbidden,
				"slurm.dial_denied",
				"a resolved address is denied by the cluster dial policy")
		}
		if !first.IsValid() {
			first = a
		}
	}
	if !first.IsValid() {
		return netip.Addr{}, apperr.New(apperr.Forbidden, "slurm.dial_denied",
			"no resolved address is permitted by the cluster dial policy")
	}
	return first, nil
}

func allowed(a netip.Addr, policy DialPolicy, httpOnlyLoopback bool) bool {
	for _, p := range policy.DenyCIDRs {
		if p.Contains(a) {
			return false
		}
	}
	for _, p := range alwaysDenied {
		if p.Contains(a) {
			return false
		}
	}
	if httpOnlyLoopback {
		return a.IsLoopback()
	}
	switch {
	case a.IsLoopback():
		return policy.AllowLoopback
	case a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast(), a.IsMulticast(),
		a.IsUnspecified():
		return false
	case a.IsPrivate():
		return policy.AllowPrivate
	default:
		return true
	}
}
