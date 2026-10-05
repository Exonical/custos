package users_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/users"
)

// syncRepo is an in-memory Repository covering the Provision path; other
// methods are unused and panic via the nil embedded interface.
type syncRepo struct {
	users.Repository
	user    users.User
	outcome users.SyncOutcome
}

func (r *syncRepo) UpsertByIdentity(context.Context, users.User) (users.User, bool, error) {
	return r.user, false, nil
}

func (r *syncRepo) PlatformRoles(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}

func (r *syncRepo) SyncIDPClaims(context.Context, uuid.UUID, map[string][]string,
	[]byte, time.Time) (users.SyncOutcome, error) {
	return r.outcome, nil
}

// failingRec fails every Record call after counting it.
type failingRec struct {
	mu    sync.Mutex
	calls []string
}

func (f *failingRec) Record(_ context.Context, e audit.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, e.Action)
	return errors.New("audit sink down")
}

func TestReconcileAuditFailureLoggedAndCounted(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	tid := uuid.Must(uuid.NewV7())
	repo := &syncRepo{
		user: users.User{ID: uid},
		outcome: users.SyncOutcome{Events: []users.SyncEvent{
			{Action: "membership.granted", TenantID: tid, UserID: uid, Roles: []string{"member"}},
			{Action: "group.member.added", TenantID: tid, UserID: uid, GroupID: uuid.Must(uuid.NewV7())},
		}},
	}
	rec := &failingRec{}
	var logs bytes.Buffer
	reader := sdkmetric.NewManualReader()
	svc := users.NewService(repo, rec,
		users.WithGroupsClaim("groups"),
		users.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
		users.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))

	p := authn.Principal{Issuer: "https://idp", Subject: "s1", Kind: authn.KindUser,
		Groups: []string{"hpc"}}
	if _, err := svc.Provision(context.Background(), p); err != nil {
		t.Fatalf("Provision must not fail on reconcile audit errors: %v", err)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("every event must still be attempted, got %v", rec.calls)
	}
	if n := strings.Count(logs.String(), "claim reconciliation audit failed"); n != 2 {
		t.Fatalf("warn logs = %d, want 2:\n%s", n, logs.String())
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "custos_claims_sync_total" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				v, _ := dp.Attributes.Value("result")
				got[v.AsString()] += dp.Value
			}
		}
	}
	if got["ok"] != 1 || got["audit_error"] != 2 {
		t.Fatalf("custos_claims_sync_total = %v, want ok=1 audit_error=2", got)
	}
}
