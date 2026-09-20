package accounting

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/tenants"
)

// Service authorizes and answers accounting queries.
type Service struct {
	repo     Repository
	projects projects.MembershipRepository
	az       authz.Authorizer
}

// NewService returns an accounting query service.
func NewService(repo Repository, p projects.MembershipRepository, az authz.Authorizer) *Service {
	return &Service{repo: repo, projects: p, az: az}
}

// UsageRequest is a bounded grouped usage query.
type UsageRequest struct {
	From, To             time.Time
	GroupBy              string
	ProjectID, ClusterID *uuid.UUID
	Limit                int
	Cursor               string
}

// UsageRow is one grouped API result.
type UsageRow struct {
	Key                                                            string `json:"key"`
	Jobs                                                           int64  `json:"jobs"`
	Failed                                                         int64  `json:"failed"`
	CPUHours, GPUHours, NodeHours, MemGBHours, WaitHours, RunHours float64
	EnergyJoules                                                   *int64 `json:"energy_joules,omitempty"`
	WaitP50, WaitP90, WaitP99, RunP50, RunP90, RunP99              *float64
}

// UsageResult is a page of grouped usage.
type UsageResult struct {
	Items      []UsageRow `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

//nolint:revive // Public methods mirror accounting API operations.
func (s *Service) scope(ctx context.Context, p authn.Principal, tc tenants.TenantContext) (bool, []uuid.UUID, error) {
	res := authz.Resource{Kind: "tenant", ID: tc.Tenant.ID.String(),
		TenantID: tc.Tenant.ID.String(), OwnerID: p.UserID.String()}
	if d, e := s.az.Check(ctx, p, authz.AccountingReadTenant, res); e == nil && d.Allow {
		return true, nil, nil
	}
	if d, e := s.az.Check(ctx, p, authz.AccountingReadSelf, res); e != nil || !d.Allow {
		return false, nil, apperr.New(apperr.Forbidden, "FORBIDDEN", "accounting access denied")
	}
	members, err := s.projects.ListMembershipsForUser(ctx, tenants.ScopeFor(&tc), tc.Tenant.ID, p.UserID)
	if err != nil {
		return false, nil, err
	}
	var ids []uuid.UUID
	for _, m := range members {
		for _, role := range m.Roles {
			if slices.Contains(authz.Permissions(role), authz.AccountingReadProject) {
				ids = append(ids, m.ProjectID)
				break
			}
		}
	}
	return false, ids, nil
}
func validateRange(from, to time.Time) error {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return apperr.New(apperr.Invalid, "ACCOUNTING_RANGE_INVALID", "from and to are required")
	}
	if to.Sub(from) > 400*24*time.Hour {
		return apperr.New(apperr.Invalid, "ACCOUNTING_RANGE_TOO_LARGE", "accounting range must not exceed 400 days")
	}
	return nil
}

//nolint:revive // Public methods mirror accounting API operations.
func (s *Service) Usage(ctx context.Context, p authn.Principal, tc tenants.TenantContext, in UsageRequest) (UsageResult, error) {
	if err := validateRange(in.From, in.To); err != nil {
		return UsageResult{}, err
	}
	if !slices.Contains([]string{"user", "project", "cluster", "account", "partition", "day"}, in.GroupBy) {
		return UsageResult{}, apperr.New(apperr.Invalid, "ACCOUNTING_GROUP_INVALID", "invalid group_by")
	}
	wide, projects, err := s.scope(ctx, p, tc)
	if err != nil {
		return UsageResult{}, err
	}
	rows, err := s.repo.ListUsage(ctx, tenants.ScopeFor(&tc), UsageQuery{TenantID: tc.Tenant.ID, From: in.From, To: in.To, ProjectID: in.ProjectID, ClusterID: in.ClusterID, UserID: p.UserID, ProjectIDs: projects, TenantWide: wide})
	if err != nil {
		return UsageResult{}, err
	}
	group := map[string]*UsageRow{}
	for _, d := range rows {
		key := groupKey(in.GroupBy, d)
		g := group[key]
		if g == nil {
			g = &UsageRow{Key: key}
			group[key] = g
		}
		g.Jobs += d.Jobs
		g.Failed += d.Failed
		g.CPUHours += float64(d.CPUSeconds) / 3600
		g.GPUHours += float64(d.GPUSeconds) / 3600
		g.NodeHours += float64(d.NodeSeconds) / 3600
		g.MemGBHours += float64(d.MemGBSeconds) / 3600
		g.WaitHours += float64(d.WaitSecondsSum) / 3600
		g.RunHours += float64(d.RunSecondsSum) / 3600
		if d.EnergyJoules != nil {
			if g.EnergyJoules == nil {
				z := int64(0)
				g.EnergyJoules = &z
			}
			*g.EnergyJoules += *d.EnergyJoules
		}
		g.WaitP50 = maxf(g.WaitP50, d.WaitP50)
		g.WaitP90 = maxf(g.WaitP90, d.WaitP90)
		g.WaitP99 = maxf(g.WaitP99, d.WaitP99)
		g.RunP50 = maxf(g.RunP50, d.RunP50)
		g.RunP90 = maxf(g.RunP90, d.RunP90)
		g.RunP99 = maxf(g.RunP99, d.RunP99)
	}
	keys := make([]string, 0, len(group))
	for k := range group {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	after := ""
	if in.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(in.Cursor)
		if e != nil {
			return UsageResult{}, apperr.New(apperr.Invalid, "CURSOR_INVALID", "invalid cursor")
		}
		after = string(b)
	}
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	out := UsageResult{}
	for _, k := range keys {
		if k <= after {
			continue
		}
		if len(out.Items) == limit {
			out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(out.Items[len(out.Items)-1].Key))
			break
		}
		out.Items = append(out.Items, *group[k])
	}
	return out, nil
}
func groupKey(by string, d Daily) string {
	switch by {
	case "user":
		if d.UserID != nil {
			return d.UserID.String()
		}
		return "unattributed"
	case "project":
		if d.ProjectID != nil {
			return d.ProjectID.String()
		}
		return "unattributed"
	case "cluster":
		return d.ClusterID.String()
	case "account":
		return d.Account
	case "partition":
		return d.Partition
	default:
		return d.Day.UTC().Format("2006-01-02")
	}
}
func maxf(a, b *float64) *float64 {
	if b == nil {
		return a
	}
	if a == nil || *b > *a {
		v := *b
		return &v
	}
	return a
}

// TopRequest is a bounded ranking query.
type TopRequest struct {
	From, To   time.Time
	Metric, By string
	Limit      int
}

//nolint:revive // Public methods mirror accounting API operations.
func (s *Service) Top(ctx context.Context, p authn.Principal, tc tenants.TenantContext, in TopRequest) ([]TopRow, error) {
	if !slices.Contains([]string{"cpu_seconds", "gpu_seconds", "jobs"}, in.Metric) || !slices.Contains([]string{"user", "project"}, in.By) {
		return nil, apperr.New(apperr.Invalid, "ACCOUNTING_TOP_INVALID", "invalid top query")
	}
	res, err := s.Usage(ctx, p, tc, UsageRequest{From: in.From, To: in.To, GroupBy: in.By, Limit: 10000})
	if err != nil {
		return nil, err
	}
	out := make([]TopRow, 0, len(res.Items))
	for _, r := range res.Items {
		var v int64
		switch in.Metric {
		case "jobs":
			v = r.Jobs
		case "cpu_seconds":
			v = int64(r.CPUHours * 3600)
		case "gpu_seconds":
			v = int64(r.GPUHours * 3600)
		}
		out = append(out, TopRow{Key: r.Key, Value: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value == out[j].Value {
			return out[i].Key < out[j].Key
		}
		return out[i].Value > out[j].Value
	})
	limit := in.Limit
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

//nolint:revive // Public methods mirror accounting API operations.
func (s *Service) ClusterStatus(ctx context.Context, p authn.Principal, clusterID uuid.UUID) (Watermark, int64, error) {
	d, e := s.az.Check(ctx, p, authz.ClusterRead, authz.Resource{Kind: "cluster", ID: clusterID.String()})
	if e != nil || !d.Allow {
		return Watermark{}, 0, apperr.New(apperr.Forbidden, "FORBIDDEN", "cluster read denied")
	}
	return s.repo.ClusterStatus(ctx, clusterID)
}

// ParseTime parses an RFC3339 accounting bound.
func ParseTime(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid RFC3339 time")
	}
	return t.UTC(), nil
}
