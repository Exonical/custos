package main

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authz"
	clusters "github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/otel"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/envcheck"
	"github.com/Exonical/custos/internal/validation/pipeline"
	vpolicy "github.com/Exonical/custos/internal/validation/policy"
	vpolicypg "github.com/Exonical/custos/internal/validation/postgres"
	"github.com/Exonical/custos/internal/validation/sbatchscan"
	"github.com/Exonical/custos/internal/validation/shellcheck"
	"github.com/Exonical/custos/internal/validation/shsyntax"
)

// validationDeps bundles the script-validation stack shared by the API
// server and the worker (engine task.admit runs the same pipeline).
type validationDeps struct {
	Pipeline *pipeline.Pipeline
	VPolicy  *vpolicy.Service
	Store    *vpolicypg.ValidationStore
	Metrics  *pipeline.Metrics
}

// newValidationDeps builds the in-process validator pipeline plus its
// persistence/policy services (docs/script-validation.md).
func newValidationDeps(cfg config.Config, pool *pgxpool.Pool,
	clusterRepo clusters.Repository, prov *otel.Providers,
	recorder audit.Recorder) (validationDeps, error) {
	validators := []validation.ScriptValidator{
		shsyntax.Validator{}, sbatchscan.Validator{}, envcheck.Validator{},
	}
	if cfg.Validation.Shellcheck.Enabled {
		sc, err := shellcheck.New(shellcheck.Config{
			Endpoint: cfg.Validation.Shellcheck.Endpoint,
			Timeout:  cfg.Validation.Shellcheck.Timeout,
		})
		if err != nil {
			return validationDeps{}, fmt.Errorf("shellcheck config: %w", err)
		}
		validators = append(validators, sc)
	}
	pipe := pipeline.New(validators)
	pipe.UnavailableSeverity = validation.Severity(
		cfg.Validation.UnavailableSeverity)
	vpolSvc := vpolicy.NewService(vpolicy.Deps{
		Store:    vpolicypg.NewPolicyStore(pool),
		Clusters: clusterRepo,
		AZ:       authz.RBAC{}, Audit: recorder,
	})
	return validationDeps{
		Pipeline: pipe,
		VPolicy:  vpolSvc,
		Store:    vpolicypg.NewValidationStore(pool),
		Metrics:  pipeline.NewMetrics(prov.Meter),
	}, nil
}
