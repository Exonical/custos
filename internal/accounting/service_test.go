package accounting

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/projects"
	"github.com/Exonical/custos/internal/tenants"
)

type repoFake struct {
	rows []Daily
	q    UsageQuery
}

func (f *repoFake) Watermark(context.Context, uuid.UUID) (Watermark, error) { return Watermark{}, nil }
func (f *repoFake) Store(context.Context, uuid.UUID, []Record, time.Time) (StoreResult, error) {
	return StoreResult{}, nil
}
func (f *repoFake) RecordError(context.Context, uuid.UUID, error) error { return nil }
func (f *repoFake) AggregateDirty(context.Context, int) (int, error)    { return 0, nil }
func (f *repoFake) ListDirty(context.Context, int) ([]DirtyDay, error)  { return nil, nil }
func (f *repoFake) ListUsage(_ context.Context, _ tenants.Scope, q UsageQuery) ([]Daily, error) {
	f.q = q
	return f.rows, nil
}
func (f *repoFake) Top(context.Context, tenants.Scope, TopQuery) ([]TopRow, error) { return nil, nil }
func (f *repoFake) ClusterStatus(context.Context, uuid.UUID) (Watermark, int64, error) {
	return Watermark{}, 0, nil
}

type projectFake struct{ members []projects.Membership }

func (projectFake) GetMembership(context.Context, tenants.Scope, uuid.UUID, uuid.UUID) (projects.Membership, error) {
	return projects.Membership{}, nil
}
func (projectFake) ListMemberships(context.Context, tenants.Scope, uuid.UUID, tenants.Page) ([]projects.Membership, string, error) {
	return nil, "", nil
}
func (projectFake) UpsertMembership(context.Context, tenants.Scope, projects.Membership) error {
	return nil
}
func (projectFake) DeleteMembership(context.Context, tenants.Scope, uuid.UUID, uuid.UUID) error {
	return nil
}
func (projectFake) CountProjectAdmins(context.Context, tenants.Scope, uuid.UUID) (int, error) {
	return 0, nil
}
func (f projectFake) ListMembershipsForUser(context.Context, tenants.Scope, uuid.UUID, uuid.UUID) ([]projects.Membership, error) {
	return f.members, nil
}
func (projectFake) ListAllForUser(context.Context, uuid.UUID) ([]projects.MembershipRef, error) {
	return nil, nil
}

func TestUsageScopeRangeAndPagination(t *testing.T) {
	tid, uid, pid, cid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	day := time.Now().UTC().Truncate(24 * time.Hour)
	repo := &repoFake{rows: []Daily{
		{ID: uuid.New(), TenantID: &tid, ProjectID: &pid, UserID: &uid, ClusterID: cid, Day: day, Jobs: 1, CPUSeconds: 3600},
		{ID: uuid.New(), TenantID: &tid, ProjectID: &pid, UserID: &uid, ClusterID: uuid.New(), Day: day, Jobs: 2, CPUSeconds: 7200},
	}}
	svc := NewService(repo, projectFake{members: []projects.Membership{{
		ProjectID: pid, Roles: []string{"project-admin"},
	}}}, authz.RBAC{})
	tc := tenants.TenantContext{Tenant: tenants.Tenant{ID: tid}, Membership: &tenants.Membership{
		UserID: uid, Roles: []string{"researcher"},
	}}
	ctx := tenants.WithTenantContext(context.Background(), tc)
	p := authn.Principal{UserID: uid}
	res, err := svc.Usage(ctx, p, tc, UsageRequest{From: day.Add(-time.Hour),
		To: day.Add(24 * time.Hour), GroupBy: "cluster", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.NextCursor == "" || repo.q.TenantWide ||
		repo.q.UserID != uid || len(repo.q.ProjectIDs) != 1 {
		t.Fatalf("result=%+v query=%+v", res, repo.q)
	}
	res2, err := svc.Usage(ctx, p, tc, UsageRequest{From: day.Add(-time.Hour),
		To: day.Add(24 * time.Hour), GroupBy: "cluster", Limit: 1, Cursor: res.NextCursor})
	if err != nil || len(res2.Items) != 1 {
		t.Fatalf("page2=%+v %v", res2, err)
	}
	if _, err = svc.Usage(ctx, p, tc, UsageRequest{From: day.Add(-401 * 24 * time.Hour),
		To: day, GroupBy: "user"}); err == nil {
		t.Fatal("400-day cap not enforced")
	}
	tc.Membership.Roles = []string{"tenant-admin"}
	ctx = tenants.WithTenantContext(context.Background(), tc)
	if _, err = svc.Usage(ctx, p, tc, UsageRequest{From: day.Add(-time.Hour),
		To: day.Add(time.Hour), GroupBy: "user"}); err != nil || !repo.q.TenantWide {
		t.Fatalf("tenant scope: %v %+v", err, repo.q)
	}
}
