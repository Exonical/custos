package workflows

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// ScriptStore is the script-bytes read port the snapshot needs.
type ScriptStore interface {
	Get(ctx context.Context, scope tenants.Scope, tenantID uuid.UUID,
		digest validation.Digest) ([]byte, error)
}

// ClusterSource resolves a placement cluster by name or id.
type ClusterSource interface {
	GetByNameOrID(ctx context.Context, nameOrID string) (clusters.Cluster, error)
}

// ClusterSnapshot projects cluster capabilities into the validation
// snapshot (shared by the workflow service and the execution engine).
func ClusterSnapshot(c *clusters.Cluster) validation.ClusterSnapshot {
	var snap validation.ClusterSnapshot
	if c == nil {
		return snap
	}
	snap.Software = c.SoftwareModules
	snap.ContainerRuntime = c.ContainerRuntime
	if c.Capabilities == nil {
		return snap
	}
	snap.GRESTypes = c.Capabilities.GRESTypes
	snap.QoS = c.Capabilities.QoSNames
	snap.MaxWalltime = map[string]time.Duration{}
	for _, pt := range c.Capabilities.Partitions {
		snap.Partitions = append(snap.Partitions, pt.Name)
		if pt.MaxTime != nil {
			snap.MaxWalltime[pt.Name] = *pt.MaxTime
		}
	}
	return snap
}

// TaskSnapshot resolves a task's validation input: script bytes,
// effective resources/env/software and the placement cluster. Shared
// by the publish gate (internal/workflows/service) and the
// execution engine's task.admit handler.
func TaskSnapshot(ctx context.Context, scope tenants.Scope,
	scripts ScriptStore, clustersSrc ClusterSource,
	tenantID uuid.UUID, spec workflowspec.Workflow,
	task workflowspec.Task) (validation.Input, *clusters.Cluster, error) {
	if task.Script == nil {
		return validation.Input{}, nil, apperr.New(apperr.Invalid,
			"TASK_NO_SCRIPT", "task has no script payload")
	}
	var (
		digest validation.Digest
		body   []byte
		err    error
	)
	if task.Script.HasInline() {
		body = []byte(task.Script.Inline)
		digest = validation.DigestOf(body)
	} else {
		digest, err = validation.ParseDigest(task.Script.Digest)
		if err != nil {
			return validation.Input{}, nil, apperr.New(apperr.Invalid,
				"SCRIPT_REF", "script ref must be sha256:<hex>")
		}
		body, err = scripts.Get(ctx, scope, tenantID, digest)
		if err != nil {
			if apperr.Is(err, apperr.NotFound) {
				return validation.Input{}, nil, apperr.New(
					apperr.Validation, "SCRIPT_UNKNOWN",
					"script digest is not stored in this tenant")
			}
			return validation.Input{}, nil, err
		}
	}
	var res workflowspec.Resources
	if !task.Resources.Empty() || task.Multinode != nil {
		var resErrs []workflowspec.FieldError
		res, resErrs = task.ResolveResources("resources")
		if len(resErrs) > 0 {
			return validation.Input{}, nil, apperr.New(apperr.Validation,
				resErrs[0].Code, resErrs[0].Message)
		}
	}
	env := map[string]string{}
	if spec.Spec.Defaults != nil {
		for k, v := range spec.Spec.Defaults.Env {
			env[k] = v
		}
	}
	for k, v := range task.Env {
		env[k] = v
	}
	clusterName := ""
	if spec.Spec.Placement != nil {
		clusterName = spec.Spec.Placement.Cluster
	}
	if task.Placement != nil && task.Placement.Cluster != "" {
		clusterName = task.Placement.Cluster
	}
	var (
		cluster *clusters.Cluster
		snap    *validation.ClusterSnapshot
	)
	if clusterName != "" {
		c, err := clustersSrc.GetByNameOrID(ctx, clusterName)
		if err != nil {
			if apperr.Is(err, apperr.NotFound) {
				return validation.Input{}, nil, apperr.New(
					apperr.Validation, "CLUSTER_UNKNOWN",
					"cluster is not registered")
			}
			return validation.Input{}, nil, err
		}
		cluster = &c
		s := ClusterSnapshot(cluster)
		if task.Image == nil {
			s.ContainerRuntime = nil
		}
		snap = &s
	}
	return validation.Input{
		Language: task.Script.EffectiveLanguage(), Script: body, Digest: digest,
		Resources: res, Environment: env, Software: task.Software,
		Cluster: snap, Container: task.Image != nil,
	}, cluster, nil
}
