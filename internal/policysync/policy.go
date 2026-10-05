package policysync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Exonical/custos/internal/allocations"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/slurm"
)

const maxOpsPerRun = 200

// PlanResponse is a dry-run policy plan returned by the cluster API.
type PlanResponse struct {
	ClusterID uuid.UUID `json:"cluster_id"`
	Mode      string    `json:"mode"`
	Ops       []Op      `json:"ops"`
}

type snapshot struct {
	cluster      clusters.Cluster
	bindings     []projects.ClusterBinding
	accounting   slurm.Accounting
	accounts     []slurm.Account
	associations []slurm.Association
	managed      []ManagedObject
	desired      DesiredState
	baseDrift    map[uuid.UUID][]projects.DriftItem
}

// Preview returns the current deterministic policy plan without applying writes.
func (d Deps) Preview(ctx context.Context, id uuid.UUID) (PlanResponse, error) {
	s, _, err := d.load(ctx, id)
	if err != nil {
		return PlanResponse{}, err
	}
	if s == nil {
		c, err := d.Clusters.GetByNameOrID(ctx, id.String())
		if err != nil {
			return PlanResponse{}, err
		}
		return PlanResponse{ClusterID: id, Mode: effectiveMode(c, d.ConfigMode), Ops: []Op{}}, nil
	}
	return PlanResponse{ClusterID: id, Mode: effectiveMode(s.cluster, d.ConfigMode), Ops: Plan(s.desired, s.observed())}, nil
}

// Reconcile executes the read/plan/apply/read policy-sync cycle.
func (d Deps) Reconcile(ctx context.Context, id uuid.UUID) error {
	markUnknown := func(bindings []projects.ClusterBinding, err error) error {
		if len(bindings) > 0 {
			return d.markUnknown(ctx, id, bindings, err)
		}
		return err
	}
	s, _, err := d.loadOrMark(ctx, id, markUnknown)
	if err != nil || s == nil {
		return err
	}
	mode := effectiveMode(s.cluster, d.ConfigMode)
	if mode == "report" {
		ops := Plan(s.desired, s.observed())
		return d.storeOutcome(ctx, s, mergeDrifts(s.baseDrift, DriftForOps(ops)), d.now(), "", nil, runStats{})
	}
	s, _, err = d.loadOrMark(ctx, id, markUnknown)
	if err != nil || s == nil {
		return err
	}
	mode = effectiveMode(s.cluster, d.ConfigMode)
	ops := Plan(s.desired, s.observed())
	if mode == "report" || len(ops) == 0 {
		return d.storeOutcome(ctx, s, mergeDrifts(s.baseDrift, DriftForOps(ops)), d.now(), "", nil, runStats{})
	}
	admin, ok := s.accounting.(slurm.AccountingAdmin)
	if !ok {
		return d.markForbidden(ctx, s, errors.New("slurmdbd policy write interface unavailable"), nil, runStats{Failed: 1})
	}
	var stats runStats
	lastApplied := (*time.Time)(nil)
	rejected := map[string]uuid.UUID{}
	for _, op := range boundedOps(ops, maxOpsPerRun) {
		if err := d.applyOp(ctx, admin, s.cluster.ID, s.managed, op); err != nil {
			stats.Failed++
			d.Metrics.recordOp(ctx, s.cluster.Name, string(op.Kind), "error")
			d.auditOp(ctx, s, op, auditResultError, policyErrorCode(err))
			if errors.Is(err, slurm.ErrForbidden) {
				return d.markForbidden(ctx, s, err, lastApplied, stats)
			}
			if errors.Is(err, slurm.ErrRejected) {
				if op.BindingID != nil {
					rejected[policyOpKey(op)] = *op.BindingID
				}
				continue
			}
			return d.markUnknownRun(ctx, id, s.bindings, err, lastApplied, stats)
		}
		stats.Applied++
		d.Metrics.recordOp(ctx, s.cluster.Name, string(op.Kind), "success")
		d.auditOp(ctx, s, op, auditResultAllow, "")
		t := d.now()
		lastApplied = &t
	}
	fresh, freshBindings, err := d.loadOrMark(ctx, id, func(_ []projects.ClusterBinding, err error) error {
		return d.markUnknownRun(ctx, id, s.bindings, err, lastApplied, stats)
	})
	if err != nil || fresh == nil {
		return err
	}
	remaining := Plan(fresh.desired, fresh.observed())
	drift := mergeDrifts(fresh.baseDrift, DriftForOps(remaining))
	for _, op := range remaining {
		if bindingID, ok := rejected[policyOpKey(op)]; ok {
			drift[bindingID] = appendDrift(drift[bindingID], projects.DriftItem{Code: "SLURM_REJECTED", Detail: "slurmdbd rejected this policy operation"})
		}
	}
	if len(freshBindings) > 0 {
		fresh.bindings = freshBindings
	}
	lastError := ""
	if stats.Failed > 0 {
		lastError = "one or more slurmdbd policy operations were rejected"
	}
	return d.storeOutcome(ctx, fresh, drift, d.now(), lastError, lastApplied, stats)
}

const (
	auditResultAllow = "allow"
	auditResultError = "error"
)

func effectiveMode(c clusters.Cluster, configured string) string {
	if c.PolicyManagement != "" && c.PolicyManagement != "inherit" {
		return c.PolicyManagement
	}
	if configured == "report" {
		return "report"
	}
	return "enforce"
}

func (s *snapshot) observed() ObservedState {
	return ObservedState{Accounts: s.accounts, Associations: s.associations, Managed: s.managed}
}

// loadOrMark loads a snapshot, passing load failures other than ErrNoAccounting
// to mark. A nil snapshot with a nil error means there is nothing to reconcile.
func (d Deps) loadOrMark(ctx context.Context, id uuid.UUID, mark func([]projects.ClusterBinding, error) error) (*snapshot, []projects.ClusterBinding, error) {
	s, bindings, err := d.load(ctx, id)
	if errors.Is(err, ErrNoAccounting) {
		return nil, bindings, ErrNoAccounting
	}
	if err != nil {
		return nil, bindings, mark(bindings, err)
	}
	return s, bindings, nil
}

func (d Deps) load(ctx context.Context, id uuid.UUID) (*snapshot, []projects.ClusterBinding, error) {
	c, err := d.Clusters.GetByNameOrID(ctx, id.String())
	if err != nil {
		return nil, nil, err
	}
	bindings, err := d.Bindings.ListBindingsByCluster(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if c.State == clusters.StateDisabled {
		return nil, bindings, nil
	}
	_, acct, err := d.Factory.Open(ctx, c.SlurmConfig())
	if err != nil {
		return nil, bindings, err
	}
	if acct == nil {
		return nil, bindings, ErrNoAccounting
	}
	accounts, err := acct.GetAccounts(ctx)
	if err != nil {
		return nil, bindings, err
	}
	qos, err := acct.GetQoS(ctx)
	if err != nil {
		return nil, bindings, err
	}
	if c.Capabilities == nil {
		return nil, bindings, errors.New("cluster capability snapshot unavailable")
	}
	managed, err := d.listManaged(ctx, id)
	if err != nil {
		return nil, bindings, err
	}
	accountNames := map[string]bool{}
	for _, b := range bindings {
		if b.Enabled {
			accountNames[b.SlurmAccount] = true
		}
	}
	for _, m := range managed {
		switch m.Kind {
		case "account":
			accountNames[m.Key] = true
		case "association":
			if name, _, ok := strings.Cut(m.Key, "|"); ok {
				accountNames[name] = true
			}
		}
	}
	accountFilter := make([]string, 0, len(accountNames))
	for name := range accountNames {
		accountFilter = append(accountFilter, name)
	}
	sort.Strings(accountFilter)
	var associations []slurm.Association
	if len(accountFilter) > 0 {
		associations, err = acct.GetAssociations(ctx, slurm.AssociationFilter{Accounts: accountFilter, Clusters: []string{c.Name}})
		if err != nil {
			return nil, bindings, err
		}
	}
	partitionSet := map[string]bool{}
	for _, p := range c.Capabilities.Partitions {
		partitionSet[p.Name] = true
	}
	inputs := make([]BindingInput, 0, len(bindings))
	for _, b := range bindings {
		if !b.Enabled {
			inputs = append(inputs, BindingInput{Binding: b})
			continue
		}
		tenantSlug, projectSlug, e := d.bindingLabels(ctx, b)
		if e != nil {
			return nil, bindings, e
		}
		var active []allocations.Allocation
		if d.Allocations != nil {
			active, e = d.Allocations.ActiveForBinding(ctx, b.ID, d.now())
			if e != nil {
				return nil, bindings, e
			}
		}
		inputs = append(inputs, BindingInput{Binding: b, TenantSlug: tenantSlug, ProjectSlug: projectSlug, HardAllocations: active})
	}
	desired, base := BuildDesired(c, inputs, qos, partitionSet)
	observed := ObservedState{Accounts: accounts, Associations: associations, Managed: managed}
	base = mergeDrifts(base, DefaultAssociationDrift(desired, observed))
	for _, b := range bindings {
		if !b.Enabled {
			continue
		}
		found := false
		for _, assoc := range associations {
			if assoc.Account == b.SlurmAccount && assoc.Cluster == c.Name {
				found = true
				break
			}
		}
		if !found {
			base[b.ID] = appendDrift(base[b.ID], projects.DriftItem{Code: "NO_ASSOCIATIONS", Detail: "no account association exists on the cluster"})
		}
	}
	return &snapshot{cluster: c, bindings: bindings, accounting: acct, accounts: accounts, associations: associations, managed: managed, desired: desired, baseDrift: base}, bindings, nil
}

func (d Deps) bindingLabels(ctx context.Context, b projects.ClusterBinding) (string, string, error) {
	var tenantSlug, projectSlug string
	err := db.WithTx(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT t.slug,p.slug FROM project_cluster_bindings b JOIN projects p ON p.id=b.project_id JOIN tenants t ON t.id=b.tenant_id WHERE b.id=$1`, b.ID).Scan(&tenantSlug, &projectSlug)
	})
	return tenantSlug, projectSlug, db.MapError(err)
}

func (d Deps) listManaged(ctx context.Context, id uuid.UUID) ([]ManagedObject, error) {
	var out []ManagedObject
	err := db.WithTx(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT kind,key,binding_id FROM slurm_managed_objects WHERE cluster_id=$1 ORDER BY kind,key`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m ManagedObject
			if err = rows.Scan(&m.Kind, &m.Key, &m.BindingID); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, db.MapError(err)
}

func (d Deps) applyOp(ctx context.Context, admin slurm.AccountingAdmin, clusterID uuid.UUID, managed []ManagedObject, op Op) error {
	switch op.Kind {
	case OpCreateAccount:
		if op.After == nil || op.After.Account == nil {
			return errors.New("create_account missing after account")
		}
		if err := admin.UpsertAccounts(ctx, []slurm.Account{*op.After.Account}); err != nil {
			return err
		}
		return d.putManaged(ctx, clusterID, "account", op.Key, op.BindingID)
	case OpUpsertAssociation:
		if op.After == nil || op.After.Association == nil {
			return errors.New("upsert_association missing after association")
		}
		assoc := *op.After.Association
		metadataOwned := op.Before == nil
		if !metadataOwned {
			for _, object := range managed {
				if object.Kind == "association" && object.Key == op.Key {
					metadataOwned = true
					break
				}
			}
		}
		if !metadataOwned {
			assoc.Comment = ""
			assoc.ParentAccount = ""
		}
		if err := admin.UpsertAssociations(ctx, []slurm.Association{assoc}); err != nil {
			return err
		}
		if op.Before == nil {
			return d.putManaged(ctx, clusterID, "association", op.Key, op.BindingID)
		}
		return nil
	case OpDeleteAssociation:
		if op.Before == nil || op.Before.Association == nil {
			return errors.New("delete_association missing before association")
		}
		a := op.Before.Association
		if err := admin.DeleteAssociation(ctx, slurm.AssociationKey{Account: a.Account, User: a.User, Cluster: a.Cluster, Partition: a.Partition}); err != nil {
			return err
		}
		return d.deleteManaged(ctx, clusterID, "association", op.Key)
	case OpDeleteAccount:
		if err := admin.DeleteAccount(ctx, op.Key); err != nil {
			return err
		}
		return d.deleteManaged(ctx, clusterID, "account", op.Key)
	default:
		return fmt.Errorf("unknown policy operation %q", op.Kind)
	}
}

func (d Deps) putManaged(ctx context.Context, clusterID uuid.UUID, kind, key string, bindingID *uuid.UUID) error {
	return db.WithTx(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO slurm_managed_objects(cluster_id,kind,key,binding_id) VALUES($1,$2,$3,$4) ON CONFLICT(cluster_id,kind,key) DO UPDATE SET binding_id=COALESCE(slurm_managed_objects.binding_id,excluded.binding_id)`, clusterID, kind, key, bindingID)
		return db.MapError(err)
	})
}
func (d Deps) deleteManaged(ctx context.Context, clusterID uuid.UUID, kind, key string) error {
	return db.WithTx(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM slurm_managed_objects WHERE cluster_id=$1 AND kind=$2 AND key=$3`, clusterID, kind, key)
		return db.MapError(err)
	})
}

func (d Deps) auditOp(ctx context.Context, s *snapshot, op Op, result, detail string) {
	if d.Audit == nil {
		return
	}
	var tid *uuid.UUID
	if op.BindingID != nil {
		for _, b := range s.bindings {
			if b.ID == *op.BindingID {
				id := b.TenantID
				tid = &id
				break
			}
		}
	}
	before, after := any(nil), any(nil)
	if op.Before != nil {
		before = op.Before
	}
	if op.After != nil {
		after = op.After
	}
	_ = d.Audit.Record(ctx, audit.Event{Actor: audit.Actor{Type: audit.ActorSystem, ID: "custos"}, TenantID: tid, Action: "slurm.policy_applied", Target: audit.Target{Type: string(op.Kind), ID: op.Key}, Result: result, Details: map[string]any{"cluster_id": s.cluster.ID, "op": op.Kind, "before": before, "after": after, "error_code": detail}})
}

func policyOpKey(op Op) string { return string(op.Kind) + "\x00" + op.Key }

func policyErrorCode(err error) string {
	if errors.Is(err, slurm.ErrForbidden) {
		return "PERMISSION_DENIED"
	}
	if errors.Is(err, slurm.ErrRejected) {
		return "SLURM_REJECTED"
	}
	if errors.Is(err, slurm.ErrUnavailable) {
		return "UPSTREAM_UNAVAILABLE"
	}
	return "POLICY_APPLY_FAILED"
}

// runStats counts the policy operations applied and failed during one reconcile run.
type runStats struct {
	Applied int
	Failed  int
}

func (d Deps) storeOutcome(ctx context.Context, s *snapshot, drifts map[uuid.UUID][]projects.DriftItem, checked time.Time, lastError string, lastApplied *time.Time, stats runStats) error {
	var drifted int64
	for _, b := range s.bindings {
		items := drifts[b.ID]
		state := "ok"
		if len(items) > 0 {
			state = "drift"
			drifted++
		}
		if err := d.setBinding(ctx, b, state, items, checked); err != nil {
			return err
		}
	}
	d.Metrics.set(s.cluster.Name, drifted)
	return d.statusRun(ctx, s.cluster.ID, checked, lastError, lastApplied, stats)
}

func (d Deps) markForbidden(ctx context.Context, s *snapshot, upstream error, lastApplied *time.Time, stats runStats) error {
	now := d.now()
	var failedBindings int64
	for _, b := range s.bindings {
		if !b.Enabled {
			continue
		}
		item := projects.DriftItem{Code: "PERMISSION_DENIED", Detail: "slurmdbd refused policy changes"}
		if _, err := d.Bindings.SetBindingDrift(ctx, b.ID, "error", []projects.DriftItem{item}, now); err != nil {
			return err
		}
		failedBindings++
	}
	d.Metrics.set(s.cluster.Name, failedBindings)
	return d.statusRun(ctx, s.cluster.ID, now, upstream.Error(), lastApplied, stats)
}

func (d Deps) markUnknownRun(ctx context.Context, id uuid.UUID, bindings []projects.ClusterBinding, upstream error, lastApplied *time.Time, stats runStats) error {
	if err := d.markUnknown(ctx, id, bindings, upstream); err != nil {
		return err
	}
	return d.statusRun(ctx, id, d.now(), upstream.Error(), lastApplied, stats)
}

func (d Deps) statusRun(ctx context.Context, id uuid.UUID, checked time.Time, lastError string, lastApplied *time.Time, stats runStats) error {
	return db.WithTx(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := db.SetPlatformScope(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO policy_sync_status(cluster_id,checked_at,last_error,last_applied_at,ops_applied,ops_failed) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(cluster_id) DO UPDATE SET checked_at=excluded.checked_at,last_error=excluded.last_error,last_applied_at=COALESCE(excluded.last_applied_at,policy_sync_status.last_applied_at),ops_applied=excluded.ops_applied,ops_failed=excluded.ops_failed`, id, checked, nilError(lastError), lastApplied, stats.Applied, stats.Failed)
		return db.MapError(err)
	})
}

func mergeDrifts(a, b map[uuid.UUID][]projects.DriftItem) map[uuid.UUID][]projects.DriftItem {
	out := map[uuid.UUID][]projects.DriftItem{}
	for id, items := range a {
		for _, item := range items {
			out[id] = appendDrift(out[id], item)
		}
	}
	for id, items := range b {
		for _, item := range items {
			out[id] = appendDrift(out[id], item)
		}
	}
	return out
}
