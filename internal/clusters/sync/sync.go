// Package sync implements the cluster.sync worker: a self-rescheduling
// chain per cluster that refreshes capabilities, partitions, and state
// hysteresis, and reconciles all_tenants auto-assignments
// (docs/workers.md "Cluster state reconciler").
package sync

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/health"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/tenants"
)

// Kind is the workqueue kind.
const Kind = "cluster.sync"

const keyPrefix = "cluster:"

var allStates = []clusters.State{
	clusters.StateActive, clusters.StateDegraded,
	clusters.StateUnreachable, clusters.StateDisabled,
}

// Metrics holds the cluster.sync instruments (per-cluster state map +
// counter).
type Metrics struct {
	total metric.Int64Counter

	mu     sync.Mutex
	states map[string]clusters.State // cluster name -> last recorded state
}

// NewMetrics registers custos_cluster_sync_total and
// custos_cluster_state{cluster,state} on mp. The cluster label is the
// cluster name (bounded: tens, never a UUID — names are slugs).
func NewMetrics(mp metric.MeterProvider) *Metrics {
	m := &Metrics{states: map[string]clusters.State{}}
	if mp == nil {
		return m
	}
	meter := mp.Meter("custos/clusters")
	m.total, _ = meter.Int64Counter("custos_cluster_sync_total",
		metric.WithDescription("cluster.sync results"))
	g, _ := meter.Int64ObservableGauge("custos_cluster_state",
		metric.WithDescription("cluster state (1 per current state)"))
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		for name, st := range m.states {
			for _, s := range allStates {
				var v int64
				if s == st {
					v = 1
				}
				o.ObserveInt64(g, v, metric.WithAttributes(
					attribute.String("cluster", name),
					attribute.String("state", string(s))))
			}
		}
		return nil
	}, g)
	return m
}

// record notes a sync result for metrics.
func (m *Metrics) record(ctx context.Context, name, result string,
	st clusters.State) {
	if m.total != nil {
		m.total.Add(ctx, 1, metric.WithAttributes(
			attribute.String("cluster", name),
			attribute.String("result", result)))
	}
	m.mu.Lock()
	m.states[name] = st
	m.mu.Unlock()
}

// nextInterval computes the self-reschedule delay: interval ±10% jitter;
// unreachable clusters back off to 5× interval capped at 10 min.
func nextInterval(base time.Duration, st clusters.State) time.Duration {
	d := base
	if st == clusters.StateUnreachable {
		d = 5 * base
		if d > 10*time.Minute {
			d = 10 * time.Minute
		}
	}
	var b [8]byte
	_, _ = crand.Read(b[:])
	jitter := 0.9 + 0.2*float64(binary.LittleEndian.Uint64(b[:])%1000)/1000
	return time.Duration(float64(d) * jitter)
}

// Handler returns the cluster.sync workqueue handler. Slurm errors are
// recorded via RecordSyncResult and the handler returns nil — the next
// scheduled run is the retry; only infrastructure (DB) errors propagate.
func Handler(repo clusters.Repository, factory slurm.Factory,
	interval time.Duration, m *Metrics) workqueue.Handler {
	return func(ctx context.Context, it workqueue.Item) error {
		id, err := uuid.Parse(it.Key[len(keyPrefix):])
		if err != nil {
			return fmt.Errorf("cluster.sync key %q: %w", it.Key, err)
		}
		c, err := repo.GetByNameOrID(ctx, id.String())
		if err != nil {
			return err // DB error -> queue retry
		}
		if c.State == clusters.StateDisabled {
			return nil // stop the chain
		}
		now := time.Now()
		res := clusters.SyncResult{At: now}
		cl, _, err := factory.Open(ctx, c.SlurmConfig())
		if err != nil {
			res.Err = err.Error()
		} else {
			var pingErr, capErr error
			if _, pingErr = cl.Ping(ctx); pingErr == nil {
				var caps slurm.Capabilities
				caps, capErr = cl.Capabilities(ctx)
				if capErr == nil {
					res.OK = true
					res.Capabilities = &caps
					res.Partitions = caps.Partitions
				}
			}
			if e := errors.Join(pingErr, capErr); e != nil {
				res.Err = e.Error()
			}
		}
		state, recErr := repo.RecordSyncResult(ctx, id, res)
		if recErr != nil {
			return recErr
		}
		if res.OK && c.Visibility == clusters.VisibilityAllTenants {
			if err := reconcileAuto(ctx, repo, c.ID); err != nil {
				return err
			}
		}
		result := "ok"
		if !res.OK {
			result = "error"
		}
		if m != nil {
			m.record(ctx, c.Name, result, state)
		}
		// Self-reschedule: the next run is the retry. RescheduleAt keeps
		// the same row; Enqueue with this (kind,key) would be deduped
		// against the still-leased item.
		return workqueue.RescheduleAt(now.Add(nextInterval(interval, state)))
	}
}

// reconcileAuto upserts source=auto rows for qualifying tenants
// (active/suspended) and drops auto rows for tenants that no longer
// qualify. Manual rows are never touched.
func reconcileAuto(ctx context.Context, repo clusters.Repository,
	clusterID uuid.UUID) error {
	assign, drop, err := repo.AutoAssignTenants(ctx, clusterID)
	if err != nil {
		return err
	}
	ps := tenants.PlatformScope()
	for _, tid := range assign {
		if err := repo.UpsertAssignment(ctx, ps, clusters.Assignment{
			ClusterID: clusterID, TenantID: tid,
			Source: clusters.SourceAuto,
		}); err != nil {
			return err
		}
	}
	for _, tid := range drop {
		if err := repo.DeleteAssignment(ctx, ps, clusterID, tid); err != nil {
			return err
		}
	}
	return nil
}

// Bootstrap enqueues cluster.sync for every non-disabled cluster at
// startup; dedupe makes it idempotent.
func Bootstrap(ctx context.Context, ex workqueue.Execer,
	repo clusters.Repository) error {
	cs, err := repo.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if c.State == clusters.StateDisabled {
			continue
		}
		if _, err := workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{
			Kind: Kind, Key: keyPrefix + c.ID.String(),
			RunAt: time.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// UnreachableChecker is the optional readiness checker: fails when any
// non-disabled cluster is unreachable (readiness reports "degraded",
// still HTTP 200).
func UnreachableChecker(repo clusters.Repository) health.Checker {
	return checker{repo: repo}
}

type checker struct{ repo clusters.Repository }

func (checker) Name() string { return "clusters" }

func (c checker) Check(ctx context.Context) error {
	cs, err := c.repo.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, cl := range cs {
		if cl.State != clusters.StateDisabled &&
			cl.State == clusters.StateUnreachable {
			return apperr.New(apperr.Unavailable, "cluster.unreachable",
				"cluster "+cl.Name+" unreachable")
		}
	}
	return nil
}
