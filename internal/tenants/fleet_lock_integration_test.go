package tenants

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
	"health-receiver/internal/testdb"
)

func TestFleetMigrationLockSerializesReservationAndProvisioning(t *testing.T) {
	if os.Getenv("HEALTH_DB_TESTS") != "1" {
		t.Skip("set HEALTH_DB_TESTS=1 for disposable-DB advisory lock test")
	}
	ctx := context.Background()
	dsn := testdb.DSN(t)
	registryDSN := os.Getenv("REGISTRY_TEST_DSN")
	if registryDSN == "" {
		t.Fatal("HEALTH_DB_TESTS=1 requires REGISTRY_TEST_DSN")
	}
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	requireDisposableProvisioningDB(t, ctx, admin)

	reg, err := registry.New(ctx, registryDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	if err := reg.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	deriver := CredentialDeriver{Current: SecretVersion{Version: 1, Secret: []byte("fleet-lock-integration-secret-32-bytes")}}
	migrator, err := NewMigratorWithRegistryLock(ctx, dsn, registryDSN, credentialFreeTestDSN(t, dsn), deriver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(migrator.Close)

	lock, err := migrator.AcquireFleetMigrationLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("fleetlock%d", os.Getpid())
	t.Cleanup(func() { _ = reg.DeleteUser(context.Background(), username) })

	reservationDone := make(chan error, 1)
	reservationCtx, cancelReservation := context.WithTimeout(ctx, 3*time.Second)
	defer cancelReservation()
	go func() {
		_, _, reserveErr := reg.ReserveUser(reservationCtx, registry.CreateUserReq{Username: username, Password: "fixture"})
		reservationDone <- reserveErr
	}()
	assertStillBlocked(t, reservationDone, "tenant reservation")
	if err := lock.Release(); err != nil {
		t.Fatalf("release exclusive fleet lock: %v", err)
	}
	if err := <-reservationDone; err != nil {
		t.Fatalf("reservation after migration unlock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("idempotent exclusive release: %v", err)
	}

	guard, err := reg.AcquireProvisioningGuard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	migrationDone := make(chan struct {
		lock *FleetMigrationLock
		err  error
	}, 1)
	migrationCtx, cancelMigration := context.WithTimeout(ctx, 3*time.Second)
	defer cancelMigration()
	go func() {
		acquired, acquireErr := migrator.AcquireFleetMigrationLock(migrationCtx)
		migrationDone <- struct {
			lock *FleetMigrationLock
			err  error
		}{acquired, acquireErr}
	}()
	select {
	case result := <-migrationDone:
		if result.lock != nil {
			_ = result.lock.Release()
		}
		t.Fatalf("fleet migration acquired while provisioning guard was held: %v", result.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("release provisioning guard: %v", err)
	}
	result := <-migrationDone
	if result.err != nil {
		t.Fatalf("migration lock after provisioning unlock: %v", result.err)
	}
	if err := result.lock.Release(); err != nil {
		t.Fatalf("release second migration lock: %v", err)
	}
}

func assertStillBlocked(t *testing.T, done <-chan error, operation string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s completed while exclusive fleet lock was held: %v", operation, err)
	case <-time.After(150 * time.Millisecond):
	}
}
