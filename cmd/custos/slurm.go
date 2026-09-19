package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"

	"go.opentelemetry.io/otel/metric"

	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/secrets/openbao"
	"github.com/Exonical/custos/internal/slurm/httpclient"
	"github.com/Exonical/custos/internal/slurm/slinky"
)

// slurmDeps builds the secret resolver + SSRF dial policy + adapter
// factory shared by serve and worker.
type slurmDeps struct {
	Resolver secrets.Multi
	OpenBao  *openbao.Provider
	Policy   httpclient.DialPolicy
	Factory  *slinky.Factory
}

func newSlurmDeps(ctx context.Context, cfg config.Config, logger *slog.Logger,
	mp metric.MeterProvider) (slurmDeps, error) {
	file, err := secrets.NewFileResolver(cfg.Secrets.FileRoots...)
	if err != nil {
		return slurmDeps{}, fmt.Errorf("secrets file resolver: %w", err)
	}
	resolver := secrets.Multi{"file": file}
	var bao *openbao.Provider
	if b := cfg.Secrets.OpenBao; b != nil {
		var oidc *openbao.OIDCClientCredentials
		if o := b.Auth.JWT.OIDCClientCredentials; o != nil {
			oidc = &openbao.OIDCClientCredentials{TokenURL: o.TokenURL,
				ClientID: o.ClientID, ClientSecret: o.ClientSecret, Scopes: o.Scopes}
		}
		bao, err = openbao.New(ctx, openbao.Config{
			Address: b.Address, CAFile: b.CAFile, Namespace: b.Namespace,
			Timeout: b.Timeout, DevMode: cfg.DevMode,
			Auth: openbao.AuthConfig{Method: b.Auth.Method,
				JWT: openbao.JWTAuth{Role: b.Auth.JWT.Role,
					TokenFile: b.Auth.JWT.TokenFile, OIDCClientCredentials: oidc},
				AppRole: openbao.AppRoleAuth{RoleID: b.Auth.AppRole.RoleID,
					SecretIDFile: b.Auth.AppRole.SecretIDFile}},
		}, logger, openbao.NewMetrics(mp, "platform-openbao"))
		if err != nil {
			return slurmDeps{}, fmt.Errorf("openbao provider: %w", err)
		}
		resolver["openbao"] = bao
	}
	p := httpclient.DialPolicy{
		AllowPrivate:  cfg.Slurm.DialPolicy.AllowPrivate,
		AllowLoopback: cfg.Slurm.DialPolicy.AllowLoopback,
		AllowHTTP:     cfg.Slurm.DialPolicy.AllowHTTP,
	}
	for _, c := range cfg.Slurm.DialPolicy.DenyCIDRs {
		pfx, err := netip.ParsePrefix(c)
		if err != nil {
			if bao != nil {
				_ = bao.Close()
			}
			return slurmDeps{}, fmt.Errorf("slurm.dial_policy.deny_cidrs: %w", err)
		}
		p.DenyCIDRs = append(p.DenyCIDRs, pfx)
	}
	return slurmDeps{Resolver: resolver, OpenBao: bao, Policy: p,
		Factory: slinky.NewFactory(resolver, p)}, nil
}
