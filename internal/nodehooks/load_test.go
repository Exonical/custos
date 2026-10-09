package nodehooks_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/nodehooks"
	"github.com/Exonical/custos/internal/platform/apperr"
)

type fakeReader struct {
	st  nodehooks.Stored
	err error
}

func (f fakeReader) GetConfig(context.Context, uuid.UUID) (nodehooks.Stored, error) {
	return f.st, f.err
}

func TestLoadIsolation(t *testing.T) {
	ctx := context.Background()
	a := uuid.MustParse(tenantA)

	t.Run("read error is returned for retry", func(t *testing.T) {
		boom := errors.New("connection reset")
		iso, err := nodehooks.LoadIsolation(ctx, fakeReader{err: boom}, uuid.New(), a, "tenant-a")
		if !errors.Is(err, boom) || iso != nil {
			t.Fatalf("iso=%v err=%v, want the read error and no snapshot", iso, err)
		}
	})
	t.Run("not found uses the default", func(t *testing.T) {
		nf := apperr.New(apperr.NotFound, "NOT_FOUND", "none")
		iso, err := nodehooks.LoadIsolation(ctx, fakeReader{err: nf}, uuid.New(), a, "tenant-a")
		if err != nil || iso.Mode != "namespace" || len(iso.Mounts) != 0 {
			t.Fatalf("iso=%+v err=%v", iso, err)
		}
	})
	t.Run("nil reader uses the default", func(t *testing.T) {
		iso, err := nodehooks.LoadIsolation(ctx, nil, uuid.New(), a, "tenant-a")
		if err != nil || iso.Mode != "namespace" {
			t.Fatalf("iso=%+v err=%v", iso, err)
		}
	})
	t.Run("stored tenant_exclusive config", func(t *testing.T) {
		cfg := goodConfig()
		cfg.IsolationMode = nodehooks.ModeTenantExclusive
		iso, err := nodehooks.LoadIsolation(ctx, fakeReader{st: nodehooks.Stored{Config: cfg}},
			uuid.New(), a, "tenant-a")
		if err != nil || iso.Mode != "tenant_exclusive" || iso.Mechanism != "mcs_label" ||
			iso.TenantSlug != "tenant-a" || len(iso.Mounts) != 2 {
			t.Fatalf("iso=%+v err=%v", iso, err)
		}
	})
}
