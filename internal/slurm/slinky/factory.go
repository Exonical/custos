// Package slinky builds Slurm port implementations for registered
// clusters: it resolves credentials through secrets.Resolver at Open
// time, builds the SSRF-safe transport, and selects the adapter by
// APIVersion.
package slinky

import (
	"context"
	"crypto/tls"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/httpclient"
	v0045 "github.com/Exonical/custos/internal/slurm/slinky/v0045"
)

// Factory implements slurm.Factory.
type Factory struct {
	resolver secrets.Resolver
	policy   httpclient.DialPolicy
}

var _ slurm.Factory = (*Factory)(nil)

// NewFactory returns a Factory resolving credentials via resolver and
// dialing under policy.
func NewFactory(resolver secrets.Resolver,
	policy httpclient.DialPolicy) *Factory {
	return &Factory{resolver: resolver, policy: policy}
}

// Open implements slurm.Factory. The token is resolved here and passed to
// the adapter as a secrets.Value — never stored as a string.
func (f *Factory) Open(ctx context.Context, c slurm.ClusterConfig) (
	slurm.Cluster, slurm.Accounting, error) {
	if c.IdentityMode == "impersonate" {
		return nil, nil, apperr.New(apperr.Invalid,
			"slurm.identity_mode_unsupported",
			"per-user impersonation is implemented in M4")
	}
	if c.APIVersion != v0045.APIVersion {
		return nil, nil, apperr.New(apperr.Invalid,
			"slurm.api_version_unsupported",
			"no adapter for slurmrestd API "+c.APIVersion)
	}
	tok, err := f.resolver.Resolve(ctx, c.TokenRef)
	if err != nil {
		return nil, nil, err
	}
	ep := slurm.Endpoint{BaseURL: c.BaseURL, CABundlePEM: c.CABundle}
	if c.ClientCertRef != nil {
		bundle, err := f.resolver.Resolve(ctx, *c.ClientCertRef)
		if err != nil {
			return nil, nil, err
		}
		cert, err := tls.X509KeyPair(bundle.Reveal(), bundle.Reveal())
		if err != nil {
			return nil, nil, apperr.New(apperr.Invalid,
				"slurm.client_cert_invalid",
				"client certificate bundle is not a PEM cert+key pair")
		}
		ep.ClientCert = &cert
	}
	hc, err := httpclient.New(ep, f.policy)
	if err != nil {
		return nil, nil, err
	}
	client, err := v0045.New(ep, slurm.Credential{
		UserName: c.ServiceUser, Token: tok,
	}, hc)
	if err != nil {
		return nil, nil, err
	}
	return client, client, nil
}
