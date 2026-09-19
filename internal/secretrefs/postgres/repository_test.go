package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/db/dbtest"
	"github.com/Exonical/custos/internal/secretrefs"
	secretpg "github.com/Exonical/custos/internal/secretrefs/postgres"
	"github.com/Exonical/custos/internal/tenants"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
func seed(t *testing.T, p *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tid := uuid.Must(uuid.NewV7())
	uid := uuid.Must(uuid.NewV7())
	pid := uuid.Must(uuid.NewV7())
	if _, err := p.Exec(ctx, `INSERT INTO users(id,issuer,subject,kind) VALUES($1,'i',$2,'user')`, uid, uid.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO tenants(id,slug,name,state,settings) VALUES($1,$2,'t','active','{}')`, tid, "t-"+tid.String()[28:]); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO projects(id,tenant_id,slug,name,state,settings) VALUES($1,$2,$3,'p','active','{}')`, pid, tid, "p-"+pid.String()[28:]); err != nil {
		t.Fatal(err)
	}
	return tid, uid, pid
}
func TestRLSAndProjectTenantTrigger(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.URL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ta, ua, pa := seed(t, admin)
	tb, _, pb := seed(t, admin)
	repo := secretpg.New(admin)
	ca := secretrefs.Connector{ID: uuid.Must(uuid.NewV7()), TenantID: ta, Name: "default", Kind: "platform-openbao", State: "active", Config: map[string]any{}, CreatedBy: ua}
	if err := repo.CreateConnector(ctx, tenants.PlatformScope(), ca); err != nil {
		t.Fatal(err)
	}
	cb := ca
	cb.ID = uuid.Must(uuid.NewV7())
	cb.TenantID = tb
	cb.Name = "other"
	if err := repo.CreateConnector(ctx, tenants.PlatformScope(), cb); err != nil {
		t.Fatal(err)
	}
	app := dbtest.AppPoolOn(t, dsn)
	defer app.Close()
	err = db.WithTx(ctx, app, func(tx pgx.Tx) error {
		if err := db.SetTenant(ctx, tx, ta); err != nil {
			return err
		}
		for table, want := range map[string]int{"secret_connectors": 1, "secret_references": 0} {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
				return err
			}
			if n != want {
				t.Fatalf("%s count=%d want %d", table, n, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := secretrefs.Reference{ID: uuid.Must(uuid.NewV7()), TenantID: ta, ProjectID: &pb, Name: "bad", ConnectorID: ca.ID, Namespace: "custos/tenants/" + ta.String(), Mount: "kv", Path: "x", Key: "value", Kind: "generic", CreatedBy: ua}
	if err := secretpg.New(app).CreateReference(ctx, tenants.TenantScope(ta), bad); err == nil {
		t.Fatal("foreign project accepted")
	}
	good := bad
	good.ID = uuid.Must(uuid.NewV7())
	good.ProjectID = &pa
	good.Name = "good"
	if err := secretpg.New(app).CreateReference(ctx, tenants.TenantScope(ta), good); err != nil {
		t.Fatalf("own project rejected: %v", err)
	}
}
