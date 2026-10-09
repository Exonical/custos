package admission

import "slices"

// Node isolation modes and tenant-exclusive mechanisms (ADR-032). They
// mirror internal/nodehooks, which admission cannot import.
const (
	IsolationNamespace       = "namespace"
	IsolationTenantExclusive = "tenant_exclusive"
	IsolationNodeExclusive   = "node_exclusive"

	MechanismMCSLabel = "mcs_label"
	MechanismUser     = "user"
)

// Slurm --exclusive/--exclusive=user/--exclusive=mcs values.
const (
	SharedNone = "none"
	SharedUser = "user"
	SharedMCS  = "mcs"
)

// NodeMount is one node mount visible to the task at the same path.
type NodeMount struct {
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
	Shared   bool   `json:"shared,omitempty"`
}

// NodeIsolation is the cluster's node isolation configuration reduced to
// what one tenant may see: shared mounts plus this tenant's mounts only.
type NodeIsolation struct {
	Mode       string
	Mechanism  string
	TenantSlug string
	Mounts     []NodeMount
}

// IsolationSpec freezes the node isolation decision on the execution spec.
type IsolationSpec struct {
	Mode     string      `json:"mode"`
	Shared   string      `json:"shared,omitempty"`
	MCSLabel string      `json:"mcs_label,omitempty"`
	Mounts   []NodeMount `json:"mounts,omitempty"`
}

// resolveIsolation derives the frozen isolation block. Exclusivity
// imposed by the cluster mode is admin policy and is not subject to the
// resource policy's AllowExclusive, which only gates the user's request.
// It returns nil when there is nothing to record.
func resolveIsolation(node *NodeIsolation, userExclusive bool) *IsolationSpec {
	if node == nil {
		node = &NodeIsolation{Mode: IsolationNamespace}
	}
	out := &IsolationSpec{Mode: node.Mode, Mounts: slices.Clone(node.Mounts)}
	switch node.Mode {
	case IsolationNodeExclusive:
		out.Shared = SharedNone
	case IsolationTenantExclusive:
		switch node.Mechanism {
		case MechanismUser:
			out.Shared = SharedUser
		default:
			out.Shared = SharedMCS
			out.MCSLabel = node.TenantSlug
		}
	}
	if userExclusive {
		out.Shared = SharedNone
	}
	if out.Shared == "" && out.MCSLabel == "" && len(out.Mounts) == 0 {
		return nil
	}
	return out
}
