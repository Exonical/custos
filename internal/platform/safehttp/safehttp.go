// Package safehttp builds SSRF-resistant HTTP clients for tenant-controlled endpoints.
package safehttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/Exonical/custos/internal/platform/apperr"
)

// DialPolicy controls which resolved addresses may be dialed.
type DialPolicy struct {
	AllowPrivate, AllowLoopback, AllowHTTP bool
	DenyCIDRs                              []netip.Prefix
}

var alwaysDenied = []netip.Prefix{netip.MustParsePrefix("169.254.169.254/32"), netip.MustParsePrefix("fd00:ec2::254/128")}

// New constructs a TLS 1.3 client with pinned CA support, no redirects and
// DNS-rebinding-resistant dialing. Tenant-controlled endpoints must use it.
func New(rawURL string, caPEM []byte, policy DialPolicy) (*http.Client, error) {
	return NewWithCertificate(rawURL, caPEM, nil, policy)
}

// NewWithCertificate additionally configures an optional mTLS certificate.
func NewWithCertificate(rawURL string, caPEM []byte, cert *tls.Certificate, policy DialPolicy) (*http.Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, apperr.New(apperr.Validation, "ENDPOINT_INVALID", "endpoint is not a valid URL")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !policy.AllowHTTP || !policy.AllowLoopback {
			return nil, apperr.New(apperr.Validation, "ENDPOINT_SCHEME_INVALID", "endpoint must use https")
		}
	default:
		return nil, apperr.New(apperr.Validation, "ENDPOINT_SCHEME_INVALID", "endpoint must use https")
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if len(caPEM) > 0 {
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(caPEM) {
			return nil, apperr.New(apperr.Validation, "CA_BUNDLE_INVALID", "CA bundle contains no parseable certificates")
		}
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}
	if cert != nil {
		tlsCfg.Certificates = []tls.Certificate{*cert}
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{TLSClientConfig: tlsCfg, ResponseHeaderTimeout: 30 * time.Second, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, fmt.Errorf("safe dial: %w", e)
		}
		first, e := VetHost(ctx, host, policy, u.Scheme == "http")
		if e != nil {
			return nil, e
		}
		return d.DialContext(ctx, network, net.JoinHostPort(first.String(), port))
	}}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
		return apperr.New(apperr.Forbidden, "REDIRECT_REFUSED", "endpoint redirects are refused")
	}}, nil
}

// VetHost resolves and vets every answer; one denied answer fails closed.
func VetHost(ctx context.Context, host string, policy DialPolicy, httpOnlyLoopback bool) (netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, apperr.Wrap(err, apperr.Unavailable, "ENDPOINT_UNAVAILABLE", "endpoint DNS unavailable")
	}
	var first netip.Addr
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		a = a.Unmap()
		if !allowed(a, policy, httpOnlyLoopback) {
			return netip.Addr{}, apperr.New(apperr.Forbidden, "ENDPOINT_DENIED", "resolved address is denied")
		}
		if !first.IsValid() {
			first = a
		}
	}
	if !first.IsValid() {
		return netip.Addr{}, apperr.New(apperr.Forbidden, "ENDPOINT_DENIED", "no resolved address is permitted")
	}
	return first, nil
}
func allowed(a netip.Addr, p DialPolicy, httpOnly bool) bool {
	for _, n := range p.DenyCIDRs {
		if n.Contains(a) {
			return false
		}
	}
	for _, n := range alwaysDenied {
		if n.Contains(a) {
			return false
		}
	}
	if httpOnly {
		return a.IsLoopback()
	}
	switch {
	case a.IsLoopback():
		return p.AllowLoopback
	case a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast(), a.IsMulticast(), a.IsUnspecified():
		return false
	case a.IsPrivate():
		return p.AllowPrivate
	default:
		return true
	}
}
