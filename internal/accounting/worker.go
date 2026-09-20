package accounting

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/slurm"
)

// Work item kinds.
const (
	KindCollect       = "accounting.collect"
	KindAggregate     = "usage.aggregate"
	collectInterval   = 5 * time.Minute
	aggregateInterval = time.Hour
)

// WorkerDeps wires accounting handlers.
type WorkerDeps struct {
	Repo     Repository
	Clusters clusters.Repository
	Factory  slurm.Factory
	Metrics  *Metrics
	Now      func() time.Time
}

func (d WorkerDeps) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

// Metrics contains bounded accounting worker instruments.
type Metrics struct {
	records      metric.Int64Counter
	lag          metric.Float64Histogram
	unattributed metric.Int64Counter
}

// NewMetrics registers accounting worker instruments.
func NewMetrics(mp metric.MeterProvider) *Metrics {
	m := &Metrics{}
	if mp == nil {
		return m
	}
	meter := mp.Meter("custos/accounting")
	m.records, _ = meter.Int64Counter("custos_accounting_records_total")
	m.lag, _ = meter.Float64Histogram("custos_accounting_lag_seconds")
	m.unattributed, _ = meter.Int64Counter("custos_accounting_unattributed_total")
	return m
}
func (m *Metrics) collected(ctx context.Context, cluster string, r StoreResult, lag float64) {
	if m == nil {
		return
	}
	attrs := metric.WithAttributes(attribute.String("cluster", cluster))
	if m.records != nil {
		m.records.Add(ctx, r.Inserted, attrs)
	}
	if m.lag != nil {
		m.lag.Record(ctx, lag, attrs)
	}
	if m.unattributed != nil {
		if r.UnattributedAmbiguous > 0 {
			m.unattributed.Add(ctx, r.UnattributedAmbiguous, metric.WithAttributes(attribute.String("reason", "ambiguous_account")))
		}
		if r.UnattributedMissing > 0 {
			m.unattributed.Add(ctx, r.UnattributedMissing, metric.WithAttributes(attribute.String("reason", "no_binding")))
		}
	}
}

// Collect returns the periodic per-cluster collector.
func Collect(d WorkerDeps) workqueue.Handler {
	return func(ctx context.Context, it workqueue.Item) error {
		id, err := clusterID(it)
		if err != nil {
			return err
		}
		c, err := d.Clusters.GetByNameOrID(ctx, id.String())
		if err != nil {
			return err
		}
		if c.State == clusters.StateDisabled {
			return nil
		}
		_, acct, err := d.Factory.Open(ctx, c.SlurmConfig())
		if err != nil {
			_ = d.Repo.RecordError(ctx, id, err)
			return workqueue.RescheduleAt(d.now().Add(collectInterval))
		}
		if acct == nil {
			return workqueue.RescheduleAt(d.now().Add(collectInterval))
		}
		now := d.now()
		wm, err := d.Repo.Watermark(ctx, id)
		if err != nil {
			return err
		}
		if wm.Watermark.Before(now.Add(-48 * time.Hour)) {
			wm.Watermark = now.Add(-24 * time.Hour)
		}
		cursor := wm.Watermark
		maxSeen := wm.Watermark
		for chunk := 0; chunk < 20; chunk++ {
			since := cursor.Add(-2 * time.Hour)
			until := since.Add(24 * time.Hour)
			if until.After(now) {
				until = now
			}
			records, err := acct.GetJobRecords(ctx, slurm.JobRecordFilter{Since: &since, Until: &until})
			if err != nil {
				_ = d.Repo.RecordError(ctx, id, err)
				return workqueue.RescheduleAt(now.Add(collectInterval))
			}
			derived := make([]Record, 0, len(records))
			for _, record := range records {
				if record.EndTime.IsZero() {
					continue
				}
				derived = append(derived, Derive(id, record, now))
				if record.EndTime.After(maxSeen) {
					maxSeen = record.EndTime
				}
			}
			stored, err := d.Repo.Store(ctx, id, derived, maxSeen)
			if err != nil {
				_ = d.Repo.RecordError(ctx, id, err)
				return workqueue.RescheduleAt(now.Add(collectInterval))
			}
			d.Metrics.collected(ctx, c.Name, stored, now.Sub(maxSeen).Seconds())
			if !until.Before(now) {
				break
			}
			cursor = until
		}
		return workqueue.RescheduleAt(now.Add(collectInterval))
	}
}

// Aggregate returns the hourly dirty-day aggregator.
func Aggregate(d WorkerDeps) workqueue.Handler {
	return func(ctx context.Context, _ workqueue.Item) error {
		if _, err := d.Repo.AggregateDirty(ctx, 64); err != nil {
			return err
		}
		return workqueue.RescheduleAt(d.now().Add(aggregateInterval))
	}
}
func clusterID(it workqueue.Item) (uuid.UUID, error) {
	var p struct {
		ClusterID string `json:"cluster_id"`
	}
	if len(it.Payload) > 0 {
		_ = json.Unmarshal(it.Payload, &p)
	}
	if p.ClusterID != "" {
		return uuid.Parse(p.ClusterID)
	}
	if len(it.Key) > 8 && it.Key[:8] == "cluster:" {
		return uuid.Parse(it.Key[8:])
	}
	return uuid.Parse(it.Key)
}

// Bootstrap enqueues collector chains and the aggregate singleton.
func Bootstrap(ctx context.Context, ex workqueue.Execer, repo clusters.Repository) error {
	list, err := repo.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.State == clusters.StateDisabled {
			continue
		}
		if _, err = workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{Kind: KindCollect, Key: "cluster:" + c.ID.String(), Payload: map[string]string{"cluster_id": c.ID.String()}}); err != nil {
			return err
		}
	}
	_, err = workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{Kind: KindAggregate, Key: "singleton"})
	return err
}
