package main

import (
	"fmt"
	"net/netip"

	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm/httpclient"
	"github.com/Exonical/custos/internal/slurm/slinky"
)

// slurmDeps builds the secret resolver + SSRF dial policy + adapter
// factory shared by serve and worker.
type slurmDeps struct {
	Resolver secrets.Resolver
	Policy   httpclient.DialPolicy
	Factory  *slinky.Factory
}

func newSlurmDeps(cfg config.Config) (slurmDeps, error) {
	file, err := secrets.NewFileResolver(cfg.Secrets.FileRoots...)
	if err != nil {
		return slurmDeps{}, fmt.Errorf("secrets file resolver: %w", err)
	}
	resolver := secrets.Multi{"file": file}
	p := httpclient.DialPolicy{
		AllowPrivate:  cfg.Slurm.DialPolicy.AllowPrivate,
		AllowLoopback: cfg.Slurm.DialPolicy.AllowLoopback,
		AllowHTTP:     cfg.Slurm.DialPolicy.AllowHTTP,
	}
	for _, c := range cfg.Slurm.DialPolicy.DenyCIDRs {
		pfx, err := netip.ParsePrefix(c)
		if err != nil {
			return slurmDeps{}, fmt.Errorf("slurm.dial_policy.deny_cidrs: %w", err)
		}
		p.DenyCIDRs = append(p.DenyCIDRs, pfx)
	}
	return slurmDeps{
		Resolver: resolver,
		Policy:   p,
		Factory:  slinky.NewFactory(resolver, p),
	}, nil
}
