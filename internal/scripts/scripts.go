// Package scripts is the content-addressed script store. Bodies are
// keyed by (tenant, sha256), immutable, and re-verified on read —
// depguard keeps internal/admission and internal/submission from
// importing this package; the only place bytes meet the wrapper is
// internal/jobs/worker.
package scripts

import (
	"context"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// Store is the script persistence port.
type Store interface {
	// Put validates the body against validation limits, computes its
	// digest and stores it (idempotent: same digest = same bytes).
	Put(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID,
		lang workflowspec.Language, body []byte,
		createdBy uuid.UUID) (validation.Digest, error)
	// Get returns the stored body after re-verifying its sha256 against
	// the requested digest.
	Get(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID,
		digest validation.Digest) ([]byte, error)
}
