// Package policysync reconciles Slurm accounting policy from Custos bindings.
package policysync

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Exonical/custos/internal/allocations"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/policysync/kind"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/slurm"
)

// Kind is the policy drift worker kind.
const Kind = kind.PolicySync
const interval = 15 * time.Minute

// ErrNoAccounting identifies clusters that do not enable slurmdbd.
var ErrNoAccounting = errors.New("cluster accounting is disabled")

// Summary reports the latest policy reconciliation state for a cluster.
type Summary struct {
	ClusterID     uuid.UUID  `json:"cluster_id"`
	Mode          string     `json:"mode"`
	CheckedAt     *time.Time `json:"checked_at"`
	LastAppliedAt *time.Time `json:"last_applied_at,omitempty"`
	Bindings      int        `json:"bindings"`
	Drifted       int        `json:"drifted"`
	Unknown       int        `json:"unknown"`
	Errors        int        `json:"errors"`
	OpsApplied    int        `json:"ops_applied"`
	OpsFailed     int        `json:"ops_failed"`
	LastError     string     `json:"last_error,omitempty"`
}

// AllocationReader provides active binding allocations to policy reconciliation.
type AllocationReader interface {
	ActiveForBinding(context.Context, uuid.UUID, time.Time) ([]allocations.Allocation, error)
}

// Deps wires policy persistence, Slurm clients, audit, metrics and configuration.
type Deps struct {
	Clusters    clusters.Repository
	Bindings    projects.BindingRepository
	Factory     slurm.Factory
	Audit       audit.Recorder
	Pool        *pgxpool.Pool
	Metrics     *Metrics
	Allocations AllocationReader
	ConfigMode  string
	Now         func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

// Metrics records the number of bindings currently reporting drift.
type Metrics struct {
	gauge  metric.Int64ObservableGauge
	ops    metric.Int64Counter
	mu     sync.Mutex
	counts map[string]int64
}

// NewMetrics registers policy drift and policy operation instruments.
func NewMetrics(mp metric.MeterProvider) *Metrics {
	m := &Metrics{counts: map[string]int64{}}
	if mp != nil {
		meter := mp.Meter("custos/policy")
		g, _ := meter.Int64ObservableGauge("custos_policy_drift_bindings")
		m.gauge = g
		m.ops, _ = meter.Int64Counter("custos_policy_ops_total")
		_, _ = mp.Meter("custos/policy").RegisterCallback(func(_ context.Context, o metric.Observer) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			for name, count := range m.counts {
				o.ObserveInt64(g, count, metric.WithAttributes(attribute.String("cluster", name)))
			}
			return nil
		}, g)
	}
	return m
}
func (m *Metrics) set(name string, n int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.counts[name] = n
	m.mu.Unlock()
}
func (m *Metrics) recordOp(ctx context.Context, cluster, op, result string) {
	if m == nil || m.ops == nil {
		return
	}
	m.ops.Add(ctx, 1, metric.WithAttributes(attribute.String("cluster", cluster), attribute.String("op", op), attribute.String("result", result)))
}

// Handler returns the per-cluster policy reconciliation chain.
func Handler(d Deps) workqueue.Handler {
	return func(ctx context.Context, it workqueue.Item) error {
		id, err := uuid.Parse(trimCluster(it.Key))
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
		if err = d.Check(ctx, id); errors.Is(err, ErrNoAccounting) {
			return nil
		} else if err != nil {
			return err
		}
		return workqueue.RescheduleAt(d.now().Add(interval))
	}
}
func trimCluster(key string) string {
	if len(key) > 8 && key[:8] == "cluster:" {
		return key[8:]
	}
	return key
}

// Bootstrap enqueues one policy reconciliation per non-disabled cluster.
func Bootstrap(ctx context.Context, ex workqueue.Execer, repo clusters.Repository) error {
	cs, err := repo.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if c.State == clusters.StateDisabled {
			continue
		}
		if _, err = workqueue.Enqueue(ctx, ex, workqueue.EnqueueRequest{Kind: Kind, Key: "cluster:" + c.ID.String()}); err != nil {
			return err
		}
	}
	return nil
}

// Check reconciles one cluster according to its effective policy-management mode.
func (d Deps) Check(ctx context.Context, id uuid.UUID) error { return d.Reconcile(ctx, id) }

// Compare returns every known mismatch for one binding.
func Compare(b projects.ClusterBinding, accounts map[string]bool, associationCount int, qos, partitions map[string]bool) []projects.DriftItem {
	var drift []projects.DriftItem
	if !accounts[b.SlurmAccount] {
		drift = append(drift, projects.DriftItem{Code: "ACCOUNT_MISSING", Detail: "account is absent from slurmdbd"})
	}
	if associationCount == 0 {
		drift = append(drift, projects.DriftItem{Code: "NO_ASSOCIATIONS", Detail: "no cluster/account association exists"})
	}
	for _, name := range b.AllowedQoS {
		if !qos[name] {
			drift = append(drift, projects.DriftItem{Code: "QOS_UNKNOWN", Detail: "allowed QoS is absent from slurmdbd: " + name})
		}
	}
	for _, name := range b.AllowedPartitions {
		if !partitions[name] {
			drift = append(drift, projects.DriftItem{Code: "PARTITION_UNKNOWN", Detail: "allowed partition is absent from cluster: " + name})
		}
	}
	if b.DefaultQoS != "" && !qos[b.DefaultQoS] {
		drift = append(drift, projects.DriftItem{Code: "DEFAULT_QOS_UNKNOWN", Detail: "default QoS is absent from slurmdbd: " + b.DefaultQoS})
	}
	if b.DefaultPartition != "" && !partitions[b.DefaultPartition] {
		drift = append(drift, projects.DriftItem{Code: "DEFAULT_PARTITION_UNKNOWN", Detail: "default partition is absent from cluster: " + b.DefaultPartition})
	}
	return drift
}

func (d Deps) markUnknown(ctx context.Context, id uuid.UUID, bindings []projects.ClusterBinding, upstream error) error {
	now := d.now()
	var drifted int64
	for _, b := range bindings {
		old, e := d.Bindings.BindingByID(ctx, b.ID)
		if e != nil {
			return e
		}
		if len(old.Drift) > 0 {
			drifted++
		}
		if _, e = d.Bindings.SetBindingDrift(ctx, b.ID, "unknown", PreserveDrift(old.Drift), now); e != nil {
			return e
		}
	}
	if c, e := d.Clusters.GetByNameOrID(ctx, id.String()); e == nil {
		d.Metrics.set(c.Name, drifted)
	}
	return d.statusRun(ctx, id, now, upstream.Error(), nil, 0, 0)
}

// PreserveDrift carries a previous finding across an unavailable check.
func PreserveDrift(previous []projects.DriftItem) []projects.DriftItem {
	return append([]projects.DriftItem(nil), previous...)
}

// TransitionAudit returns the audit event for a meaningful drift transition.
func TransitionAudit(old projects.ClusterBinding, state string) string {
	if state == "drift" && old.DriftState == "ok" {
		return "binding.drift_detected"
	}
	if state == "ok" && (old.DriftState == "drift" || len(old.Drift) > 0) {
		return "binding.drift_cleared"
	}
	return ""
}
func (d Deps) setBinding(ctx context.Context, b projects.ClusterBinding, state string, drift []projects.DriftItem, now time.Time) error {
	old, err := d.Bindings.BindingByID(ctx, b.ID)
	if err != nil {
		return err
	}
	updated, err := d.Bindings.SetBindingDrift(ctx, b.ID, state, drift, now)
	if err != nil {
		return err
	}
	action := TransitionAudit(old, state)
	if action != "" && d.Audit != nil {
		_ = d.Audit.Record(ctx, audit.Event{Actor: audit.Actor{Type: audit.ActorSystem, ID: "custos"}, TenantID: &updated.TenantID, Action: action, Target: audit.Target{Type: "project_cluster_binding", ID: b.ID.String()}, Result: audit.ResultAllow, Details: map[string]any{"codes": driftCodes(drift)}})
	}
	return nil
}
func driftCodes(d []projects.DriftItem) []string {
	out := make([]string, 0, len(d))
	for _, x := range d {
		out = append(out, x.Code)
	}
	return out
}
func nilError(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SummaryService provides policy-sync summaries and dry-run plans.
type SummaryService struct{ deps Deps }

// NewSummaryService wires read and planning dependencies.
func NewSummaryService(d Deps) *SummaryService { return &SummaryService{deps: d} }

// Summary returns the current binding drift counts and last policy run.
func (s *SummaryService) Summary(ctx context.Context, id uuid.UUID) (Summary, error) {
	out, err := SummaryFor(ctx, s.deps.Pool, s.deps.Bindings, id)
	if err != nil {
		return out, err
	}
	c, err := s.deps.Clusters.GetByNameOrID(ctx, id.String())
	if err != nil {
		return out, err
	}
	out.Mode = effectiveMode(c, s.deps.ConfigMode)
	return out, nil
}

// Plan returns current policy operations without applying writes.
func (s *SummaryService) Plan(ctx context.Context, id uuid.UUID) (PlanResponse, error) {
	return s.deps.Preview(ctx, id)
}

// SummaryFor loads a cluster drift summary from binding and status rows.
func SummaryFor(ctx context.Context, pool *pgxpool.Pool, bindings projects.BindingRepository, id uuid.UUID) (Summary, error) {
	out := Summary{ClusterID: id}
	rows, err := bindings.ListBindingsByCluster(ctx, id)
	if err != nil {
		return out, err
	}
	out.Bindings = len(rows)
	for _, b := range rows {
		if b.DriftState == "drift" {
			out.Drifted++
		}
		if b.DriftState == "unknown" {
			out.Unknown++
		}
		if b.DriftState == "error" {
			out.Errors++
		}
		if b.DriftCheckedAt != nil && (out.CheckedAt == nil || b.DriftCheckedAt.After(*out.CheckedAt)) {
			v := *b.DriftCheckedAt
			out.CheckedAt = &v
		}
	}
	err = db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if e := db.SetPlatformScope(ctx, tx); e != nil {
			return e
		}
		var checked *time.Time
		e := tx.QueryRow(ctx, `SELECT checked_at,coalesce(last_error,''),last_applied_at,ops_applied,ops_failed FROM policy_sync_status WHERE cluster_id=$1`, id).Scan(&checked, &out.LastError, &out.LastAppliedAt, &out.OpsApplied, &out.OpsFailed)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e == nil && checked != nil {
			out.CheckedAt = checked
		}
		return e
	})
	return out, err
}
