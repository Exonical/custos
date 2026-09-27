// Package policysync detects read-only drift between Custos bindings and Slurm.
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

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/slurm"
)

// Kind is the policy drift worker kind.
const Kind = "policy.sync"
const interval = 15 * time.Minute

// ErrNoAccounting identifies clusters that do not enable slurmdbd.
var ErrNoAccounting = errors.New("cluster accounting is disabled")

// Summary reports the latest read-only drift check for a cluster.
type Summary struct {
	ClusterID uuid.UUID  `json:"cluster_id"`
	CheckedAt *time.Time `json:"checked_at"`
	Bindings  int        `json:"bindings"`
	Drifted   int        `json:"drifted"`
	Unknown   int        `json:"unknown"`
	LastError string     `json:"last_error,omitempty"`
}

// Deps wires policy sync persistence, Slurm reads, and audit/metrics.
type Deps struct {
	Clusters clusters.Repository
	Bindings projects.BindingRepository
	Factory  slurm.Factory
	Audit    audit.Recorder
	Pool     *pgxpool.Pool
	Metrics  *Metrics
	Now      func() time.Time
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
	mu     sync.Mutex
	counts map[string]int64
}

// NewMetrics registers the policy drift gauge.
func NewMetrics(mp metric.MeterProvider) *Metrics {
	m := &Metrics{counts: map[string]int64{}}
	if mp != nil {
		g, _ := mp.Meter("custos/policy").Int64ObservableGauge("custos_policy_drift_bindings")
		m.gauge = g
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

// Handler returns the per-cluster read-only drift chain.
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

// Bootstrap enqueues one policy drift check per non-disabled cluster.
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

// Check refreshes every binding on a cluster without changing Slurm state.
func (d Deps) Check(ctx context.Context, id uuid.UUID) error {
	c, err := d.Clusters.GetByNameOrID(ctx, id.String())
	if err != nil {
		return err
	}
	if c.State == clusters.StateDisabled {
		return nil
	}
	bindings, err := d.Bindings.ListBindingsByCluster(ctx, id)
	if err != nil {
		return err
	}
	_, acct, err := d.Factory.Open(ctx, c.SlurmConfig())
	if err != nil {
		return d.markUnknown(ctx, id, bindings, err)
	}
	if acct == nil {
		return ErrNoAccounting
	}
	accounts, err := acct.GetAccounts(ctx)
	if err != nil {
		return d.markUnknown(ctx, id, bindings, err)
	}
	qos, err := acct.GetQoS(ctx)
	if err != nil {
		return d.markUnknown(ctx, id, bindings, err)
	}
	if c.Capabilities == nil {
		return d.markUnknown(ctx, id, bindings, errors.New("cluster capability snapshot unavailable"))
	}
	accountSet := map[string]bool{}
	for _, a := range accounts {
		accountSet[a.Name] = true
	}
	qosSet := map[string]bool{}
	for _, q := range qos {
		qosSet[q.Name] = true
	}
	partitions := map[string]bool{}
	for _, p := range c.Capabilities.Partitions {
		partitions[p.Name] = true
	}
	var drifted int64
	for _, b := range bindings {
		assocs, e := acct.GetAssociations(ctx, slurm.AssociationFilter{Accounts: []string{b.SlurmAccount}, Clusters: []string{c.Name}})
		if e != nil {
			return d.markUnknown(ctx, id, bindings, e)
		}
		drift := Compare(b, accountSet, len(assocs), qosSet, partitions)
		state := "ok"
		if len(drift) > 0 {
			state = "drift"
			drifted++
		}
		if err = d.setBinding(ctx, b, state, drift, d.now()); err != nil {
			return err
		}
	}
	d.Metrics.set(c.Name, drifted)
	return d.status(ctx, id, d.now(), "")
}

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
	return d.status(ctx, id, now, upstream.Error())
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
func (d Deps) status(ctx context.Context, id uuid.UUID, checked time.Time, lastError string) error {
	return db.WithTx(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO policy_sync_status(cluster_id,checked_at,last_error) VALUES($1,$2,$3) ON CONFLICT(cluster_id) DO UPDATE SET checked_at=excluded.checked_at,last_error=excluded.last_error`, id, checked, nilError(lastError))
		return db.MapError(err)
	})
}
func nilError(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SummaryService provides read-only per-cluster drift summaries.
type SummaryService struct {
	pool     *pgxpool.Pool
	bindings projects.BindingRepository
}

// NewSummaryService wires the summary's database dependencies.
func NewSummaryService(pool *pgxpool.Pool, b projects.BindingRepository) *SummaryService {
	return &SummaryService{pool: pool, bindings: b}
}

// Summary returns current binding drift counts and the last check status.
func (s *SummaryService) Summary(ctx context.Context, id uuid.UUID) (Summary, error) {
	return SummaryFor(ctx, s.pool, s.bindings, id)
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
		e := tx.QueryRow(ctx, `SELECT checked_at,coalesce(last_error,'') FROM policy_sync_status WHERE cluster_id=$1`, id).Scan(&checked, &out.LastError)
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
