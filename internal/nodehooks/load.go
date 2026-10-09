package nodehooks

import (
	"context"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/platform/apperr"
)

// ConfigReader is the read side of the node configuration store used by
// admission.
type ConfigReader interface {
	GetConfig(ctx context.Context, clusterID uuid.UUID) (Stored, error)
}

// LoadIsolation resolves the node isolation snapshot for one tenant on a
// cluster. A cluster without a stored configuration uses the defaults; any
// other read error is returned so the caller retries rather than admitting
// without the isolation the admin configured. A nil reader means the node
// hooks feature is not wired and yields the default (namespace, no mounts).
func LoadIsolation(ctx context.Context, r ConfigReader, clusterID, tenantID uuid.UUID,
	tenantSlug string) (*admission.NodeIsolation, error) {
	cfg := Default()
	if r != nil {
		st, err := r.GetConfig(ctx, clusterID)
		switch {
		case err == nil:
			cfg = st.Config
		case apperr.Is(err, apperr.NotFound):
		default:
			return nil, err
		}
	}
	return Isolation(cfg, tenantID, tenantSlug), nil
}
