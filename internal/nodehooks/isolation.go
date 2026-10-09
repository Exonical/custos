package nodehooks

import (
	"slices"
	"sort"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
)

// Isolation reduces the cluster node configuration to what one tenant's
// job may see: every shared mount plus that tenant's mounts, sorted by
// target. Other tenants' mounts never appear in the result.
func Isolation(cfg Config, tenantID uuid.UUID, tenantSlug string) *admission.NodeIsolation {
	cfg = Normalize(cfg)
	out := &admission.NodeIsolation{
		Mode: cfg.IsolationMode, Mechanism: cfg.TenantExclusiveMechanism,
		TenantSlug: tenantSlug, Mounts: []admission.NodeMount{},
	}
	for _, m := range cfg.SharedMounts {
		out.Mounts = append(out.Mounts, admission.NodeMount{
			Target: m.Target, ReadOnly: slices.Contains(m.Options, "ro"), Shared: true})
	}
	want := tenantID.String()
	for _, m := range cfg.TenantMounts {
		if m.Tenant != want {
			continue
		}
		out.Mounts = append(out.Mounts, admission.NodeMount{
			Target: m.Target, ReadOnly: slices.Contains(m.Options, "ro")})
	}
	sort.SliceStable(out.Mounts, func(i, j int) bool { return out.Mounts[i].Target < out.Mounts[j].Target })
	return out
}
