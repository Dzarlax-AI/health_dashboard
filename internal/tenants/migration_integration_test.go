package tenants

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
	"health-receiver/internal/testdb"
)

func TestTenantMigrationApplyVerifyRollbackIntegration(t *testing.T) {
	if os.Getenv("HEALTH_DB_TESTS") != "1" {
		t.Skip("set HEALTH_DB_TESTS=1 for destructive disposable-DB migration test")
	}
	ctx := context.Background()
	dsn := testdb.DSN(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	requireDisposableProvisioningDB(t, ctx, admin)
	registryDSN := os.Getenv("REGISTRY_TEST_DSN")
	if registryDSN == "" {
		t.Fatal("HEALTH_DB_TESTS=1 requires REGISTRY_TEST_DSN")
	}
	reg, err := registry.New(ctx, registryDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	if err = reg.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("migration%d", os.Getpid())
	user, _, err := reg.ReserveUser(ctx, registry.CreateUserReq{Username: name, SchemaName: name + "_schema", Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	otherSchema := name + "_other"
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgxIdent(otherSchema)+" CASCADE")
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgxIdent(user.SchemaName)+" CASCADE")
		_, _ = admin.Exec(context.Background(), "DROP OWNED BY "+pgxIdent(user.DBRole))
		_, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgxIdent(user.DBRole))
		_ = reg.DeleteUser(context.Background(), name)
	})
	legacy := NewLegacySetup(reg, dsn)
	if err = legacy.ReconcileNonterminal(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgxIdent(otherSchema)); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"CREATE TABLE " + pgxIdent(otherSchema) + `.metric_points(id integer)`, "CREATE TABLE " + pgxIdent(otherSchema) + `.settings(key text,value text)`} {
		if _, err = admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	deriver := CredentialDeriver{Current: SecretVersion{Version: 1, Secret: []byte("disposable-migration-secret-32-bytes")}}
	migrator, err := NewMigrator(ctx, dsn, credentialFreeTestDSN(t, dsn), deriver)
	if err != nil {
		t.Fatal(err)
	}
	defer migrator.Close()
	inventory, err := migrator.Inventory(ctx, user.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.ApplyRestrictedTenant(ctx, inventory, otherSchema); err != nil {
		t.Fatal(err)
	}
	active, err := reg.GetBySchema(ctx, user.SchemaName)
	if err != nil || !active.DBIsolationReady {
		t.Fatalf("active metadata after apply = %+v, %v", active, err)
	}
	if err = migrator.RestoreTenant(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	restored, err := reg.GetBySchema(ctx, user.SchemaName)
	if err != nil || restored.DBIsolationReady {
		t.Fatalf("metadata after rollback = %+v, %v", restored, err)
	}
}
