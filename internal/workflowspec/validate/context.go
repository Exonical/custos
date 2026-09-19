package validate

import (
	"fmt"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// Context supplies everything steps 5-8 need from the outside world;
// the workflows service builds it per request.
type Context struct {
	// ShellAllowed is effective ValidationPolicy.allowShellTasks —
	// there is deliberately no workflow.shell permission (publish is
	// the gate).
	ShellAllowed bool
	// ResourcePolicy is the effective tenant ∩ project policy.
	ResourcePolicy admission.ResourcePolicy
	// Cluster resolves a named placement cluster to the project
	// binding and the cluster's capability snapshot; ok=false means no
	// enabled binding exists.
	Cluster func(name string) (admission.Binding, validation.ClusterSnapshot, bool)
	// SecretReference resolves a tenant SecretReference name. It validates
	// bindings for M6-B while static validation keeps spec.secrets fail-closed.
	SecretReference func(name string) bool
	// DefaultCluster is the placement used when neither the task nor
	// the workflow names one ("" → no capability checks possible).
	DefaultCluster string
}

// Contextual runs steps 5-8: type permitted, resources satisfiable,
// secrets available, placement bound. Steps 1-4 must have run first.
func Contextual(w workflowspec.Workflow, ctx Context) []FieldError {
	var errs []FieldError
	if ctx.SecretReference != nil {
		for handle, use := range w.Spec.Secrets {
			if !ctx.SecretReference(use.Ref) {
				errs = append(errs, FieldError{Path: "spec.secrets." + handle + ".ref",
					Code: "SECRET_REFERENCE_NOT_FOUND", Message: "secret reference not found"})
			}
		}
	}
	for i, t := range w.Spec.Tasks {
		base := fmt.Sprintf("spec.tasks[%d]", i)
		ty := t.Type
		if ty == "" {
			ty = TypeBatch
		}
		// Step 5: permitted types. Reserved types already failed
		// statically; shell is the only permission-gated type.
		if ty == TypeShell && !ctx.ShellAllowed {
			errs = append(errs, FieldError{Path: base + ".type", Code: "SHELL_NOT_ALLOWED",
				Message: "shell tasks require allowShellTasks in the effective validation policy"})
		}
		if !slurmBacked(t) || ty == TypeCondition {
			continue
		}
		// Step 8: placement.
		cluster := w.Spec.Placement
		if t.Placement != nil {
			cluster = t.Placement
		}
		clusterName := ctx.DefaultCluster
		if cluster != nil && cluster.Cluster != "" {
			clusterName = cluster.Cluster
		}
		var (
			bind admission.Binding
			caps validation.ClusterSnapshot
			ok   bool
		)
		if clusterName != "" && ctx.Cluster != nil {
			bind, caps, ok = ctx.Cluster(clusterName)
			if !ok {
				errs = append(errs, FieldError{Path: base + ".placement.cluster",
					Code:    "PLACEMENT_CLUSTER_UNBOUND",
					Message: "cluster " + clusterName + " has no enabled project binding"})
			}
		}
		// Step 6: resources. A task declaring none inherits the
		// submission-time defaults; nothing to check here.
		if emptyResources(t.Resources) {
			continue
		}
		res, resErrs := t.Resources.Resolve(base + ".resources")
		errs = append(errs, resErrs...)
		if len(resErrs) == 0 {
			if d := admission.CheckResourcePolicy(res, ctx.ResourcePolicy); d != nil {
				errs = append(errs, FieldError{Path: base + ".resources." + d.Field,
					Code: d.Code, Message: d.Message})
			}
			if ok {
				part := t.Partition
				if part == "" && w.Spec.Defaults != nil {
					part = w.Spec.Defaults.Partition
				}
				qos := t.QoS
				if qos == "" && w.Spec.Defaults != nil {
					qos = w.Spec.Defaults.QoS
				}
				if _, _, _, d := admission.CheckEntitlement(bind, part, qos); d != nil {
					errs = append(errs, FieldError{Path: base + "." + d.Field,
						Code: d.Code, Message: d.Message})
				}
				part2 := part
				if part2 == "" {
					part2 = bind.DefaultPartition
				}
				if d := admission.CheckAdmission(res, part2, caps); d != nil {
					errs = append(errs, FieldError{Path: base + ".resources." + d.Field,
						Code: d.Code, Message: d.Message})
				}
			}
		}
	}
	return errs
}
