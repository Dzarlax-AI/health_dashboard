package tenants

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestTenantMigrationApplyVerifyRollbackIntegration(t *testing.T) {
	if os.Getenv("HEALTH_DB_TESTS") != "1" {
		t.Skip("set HEALTH_DB_TESTS=1 for destructive disposable-DB migration test")
	}
	ctx := context.Background()
	dsn := testdb.DSN(t)
	adminDSN, registryDSN := requireFixedIdentityTestDSNs(t)
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
	legacyDSN := createLegacyOwnerTestIdentity(t, ctx, admin, dsn)
	legacy := NewLegacySetup(reg, legacyDSN)
	if err = legacy.ReconcileNonterminal(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "DROP INDEX "+pgxIdent(user.SchemaName)+".idx_hourly_date"); err != nil {
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
	previousSecret := SecretVersion{Version: 1, Secret: []byte("disposable-migration-secret-32-bytes")}
	deriver := CredentialDeriver{
		Current:  SecretVersion{Version: 2, Secret: []byte("rotated-migration-secret-32-bytes!!")},
		Previous: &previousSecret,
	}
	migrator, err := NewMigratorWithRegistryLock(ctx, adminDSN, registryDSN, credentialFreeTestDSN(t, dsn), deriver)
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
	probeResult, err := migrator.VerifyRestrictedTenantAll(ctx, inventory, []string{otherSchema})
	if err != nil {
		t.Fatalf("all-pairs restricted verification: %v", err)
	}
	if probeResult.Total != 6 || probeResult.Denied != 6 || probeResult.RegistryFailures != 0 || probeResult.CrossTenantFailures != 0 || probeResult.OperationalFailures != 0 {
		t.Fatalf("all-pairs probe result = %+v, want 6/6 denied", probeResult)
	}
	var repairedIndex bool
	if err = admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=$1 AND indexname='idx_hourly_date')`, user.SchemaName).Scan(&repairedIndex); err != nil || !repairedIndex {
		t.Fatalf("shared contract did not repair missing index: present=%v err=%v", repairedIndex, err)
	}
	active, err := reg.GetBySchema(ctx, user.SchemaName)
	if err != nil || !active.DBIsolationReady || active.SchemaContractVersion != storage.SchemaContractVersion || active.SchemaContractChecksum != storage.SchemaContractChecksum() {
		t.Fatalf("active metadata after apply = %+v, %v", active, err)
	}
	marker, err := storage.ReadTenantIdentityMarker(ctx, admin, user.SchemaName)
	if err != nil || marker.TenantID != user.TenantID || marker.SchemaContractVersion != storage.SchemaContractVersion || marker.SchemaContractChecksum != storage.SchemaContractChecksum() {
		t.Fatalf("permanent marker after migration = %+v, %v", marker, err)
	}
	casInventory, err := migrator.Inventory(ctx, user.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	staleCASInventory := casInventory
	staleCASInventory.Registry.ContractVersion = nil
	staleCASInventory.Registry.ContractChecksum = nil
	if err := migrator.advanceRegistryContract(ctx, staleCASInventory, true); err != nil {
		t.Fatalf("zero-row registry CAS did not accept exact landed target: %v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE health_registry.users SET schema_contract_checksum=$2 WHERE schema_name=$1`, user.SchemaName, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if err := migrator.advanceRegistryContract(ctx, casInventory, true); !errors.Is(err, registry.ErrProvisioningStateConflict) {
		t.Fatalf("registry contract CAS accepted metadata drift: %v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE health_registry.users SET schema_contract_version=$2,schema_contract_checksum=$3 WHERE schema_name=$1`, user.SchemaName, storage.SchemaContractVersion, storage.SchemaContractChecksum()); err != nil {
		t.Fatal(err)
	}
	rotationInventory, err := migrator.Inventory(ctx, user.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.RotateTenantCredential(ctx, rotationInventory, 1, 2, otherSchema); err != nil {
		t.Fatalf("rotate tenant credential: %v", err)
	}
	// Model an ambiguous CAS response where another writer already landed the
	// exact target metadata. Retrying from the stale inventory must preserve the
	// target password instead of restoring the old credential.
	if err = migrator.RotateTenantCredential(ctx, rotationInventory, 1, 2, otherSchema); err != nil {
		t.Fatalf("same-target credential rotation retry: %v", err)
	}
	rotated, err := reg.GetBySchema(ctx, user.SchemaName)
	if err != nil || rotated.DBCredentialVersion != 2 {
		t.Fatalf("metadata after rotation = %+v, %v", rotated, err)
	}
	rotationProbe := rotationInventory
	rotationProbe.CredentialVersion = 2
	rotationProbe.Registry.CredentialVersion = 2
	if err = migrator.VerifyRestrictedTenant(ctx, rotationProbe, otherSchema); err != nil {
		t.Fatalf("target credential after same-target retry: %v", err)
	}
	// Keep the pre-cutover ownership/ACL inventory for rollback; only its
	// registry credential CAS version advances during rotation.
	inventory.CredentialVersion = 2
	inventory.Registry.CredentialVersion = 2
	if err = migrator.RestoreTenant(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	assertCanonicalMembershipRows(t, ctx, admin, user.DBRole)
	if err = migrator.RestoreTenant(ctx, inventory); err != nil {
		t.Fatalf("retry rollback for cutover-created marker: %v", err)
	}
	assertCanonicalMembershipRows(t, ctx, admin, user.DBRole)
	restored, err := reg.GetBySchema(ctx, user.SchemaName)
	if err != nil || restored.DBIsolationReady || restored.SchemaContractVersion != 0 || restored.SchemaContractChecksum != "" {
		t.Fatalf("metadata after rollback = %+v, %v", restored, err)
	}
	var markerExists bool
	if err = admin.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, user.SchemaName+"."+storage.TenantIdentityTable).Scan(&markerExists); err != nil || markerExists {
		t.Fatalf("cutover-only marker after rollback: exists=%v err=%v", markerExists, err)
	}
	freshInventory, err := migrator.Inventory(ctx, user.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	if freshInventory.Registry.ContractVersion != nil || freshInventory.Registry.ContractChecksum != nil || freshInventory.Registry.IsolationReady {
		t.Fatalf("fresh rollback inventory did not preserve NULL/NULL: %+v", freshInventory.Registry)
	}
	if err = migrator.ApplyRestrictedTenant(ctx, freshInventory, otherSchema); err != nil {
		t.Fatalf("reapply after rollback: %v", err)
	}
	reapplied, err := reg.GetBySchema(ctx, user.SchemaName)
	if err != nil || !reapplied.DBIsolationReady || reapplied.SchemaContractVersion != storage.SchemaContractVersion || reapplied.SchemaContractChecksum != storage.SchemaContractChecksum() {
		t.Fatalf("metadata after reapply = %+v, %v", reapplied, err)
	}
	currentInventory, err := migrator.Inventory(ctx, user.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	if currentInventory.Marker == nil || currentInventory.Marker.ContractVersion == nil || currentInventory.Marker.ContractChecksum == nil {
		t.Fatalf("current marker missing from inventory: %+v", currentInventory.Marker)
	}
	if err = migrator.RestoreTenant(ctx, currentInventory); err != nil {
		t.Fatalf("restore existing current marker: %v", err)
	}
	if err = migrator.RestoreTenant(ctx, currentInventory); err != nil {
		t.Fatalf("retry restore existing current marker: %v", err)
	}
	currentRestored, err := reg.GetBySchema(ctx, user.SchemaName)
	if err != nil || currentRestored.DBIsolationReady || currentRestored.SchemaContractVersion != storage.SchemaContractVersion || currentRestored.SchemaContractChecksum != storage.SchemaContractChecksum() {
		t.Fatalf("current contract metadata after rollback = %+v, %v", currentRestored, err)
	}
	marker, err = storage.ReadTenantIdentityMarker(ctx, admin, user.SchemaName)
	if err != nil || marker.SchemaContractVersion != storage.SchemaContractVersion || marker.SchemaContractChecksum != storage.SchemaContractChecksum() {
		t.Fatalf("existing current marker after rollback = %+v, %v", marker, err)
	}
	if _, err = admin.Exec(ctx, `UPDATE health_registry.users SET db_isolation_ready=true,schema_contract_version=$2,schema_contract_checksum=$3 WHERE schema_name=$1`, user.SchemaName, storage.SchemaContractVersion, storage.SchemaContractChecksum()); err != nil {
		t.Fatal(err)
	}
	legacyMarkerInventory := currentInventory
	legacyMarkerInventory.Registry.ContractVersion = nil
	legacyMarkerInventory.Registry.ContractChecksum = nil
	legacyMarkerInventory.Marker.ContractVersion = nil
	legacyMarkerInventory.Marker.ContractChecksum = nil
	if err = migrator.RestoreTenant(ctx, legacyMarkerInventory); err != nil {
		t.Fatalf("restore pre-existing legacy NULL marker: %v", err)
	}
	if err = migrator.RestoreTenant(ctx, legacyMarkerInventory); err != nil {
		t.Fatalf("retry restore pre-existing legacy NULL marker: %v", err)
	}
	if _, err = admin.Exec(ctx, "UPDATE "+pgxIdent(user.SchemaName)+"."+pgxIdent(storage.TenantIdentityTable)+` SET schema_contract_version=$1,schema_contract_checksum=$2 WHERE singleton=true`, storage.SchemaContractVersion, storage.SchemaContractChecksum()); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE health_registry.users SET db_isolation_ready=true,schema_contract_version=$2,schema_contract_checksum=$3 WHERE schema_name=$1`, user.SchemaName, storage.SchemaContractVersion, storage.SchemaContractChecksum()); err != nil {
		t.Fatal(err)
	}
	olderVersion, olderChecksum := storage.SchemaContractVersion-1, strings.Repeat("a", 64)
	olderInventory := currentInventory
	olderInventory.Registry.ContractVersion = &olderVersion
	olderInventory.Registry.ContractChecksum = &olderChecksum
	olderInventory.Marker.ContractVersion = &olderVersion
	olderInventory.Marker.ContractChecksum = &olderChecksum
	if err = migrator.RestoreTenant(ctx, olderInventory); err != nil {
		t.Fatalf("restore pre-existing older marker: %v", err)
	}
	if err = migrator.RestoreTenant(ctx, olderInventory); err != nil {
		t.Fatalf("retry restore pre-existing older marker: %v", err)
	}
	for _, column := range []string{"schema_contract_version", "schema_contract_checksum"} {
		if _, err = admin.Exec(ctx, "ALTER TABLE "+pgxIdent(user.SchemaName)+"."+pgxIdent(storage.TenantIdentityTable)+" ALTER COLUMN "+pgxIdent(column)+" DROP NOT NULL"); err != nil {
			t.Fatal(err)
		}
	}
	const previousContractVersion = 3
	const previousContractChecksum = "962b237cf8b54bd857aa123b5cf4e764b274d4b4c19ede00244971e455d2f45e"
	if _, err = admin.Exec(ctx, "UPDATE "+pgxIdent(user.SchemaName)+"."+pgxIdent(storage.TenantIdentityTable)+` SET schema_contract_version=$1,schema_contract_checksum=$2 WHERE singleton=true`, previousContractVersion, previousContractChecksum); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE health_registry.users SET db_isolation_ready=true,schema_contract_version=$2,schema_contract_checksum=$3 WHERE schema_name=$1`, user.SchemaName, previousContractVersion, previousContractChecksum); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "UPDATE "+pgxIdent(user.SchemaName)+`.source_epochs SET end_date='2025-12-31',description='closed production epoch',detected_by='automatic' WHERE epoch_id='initial'`); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "INSERT INTO "+pgxIdent(user.SchemaName)+`.source_epochs(epoch_id,start_date,end_date,kind,description,detected_by,confirmed) VALUES('contract_v4_next','2026-01-01',NULL,'source_epoch','current production epoch','automatic',true)`); err != nil {
		t.Fatal(err)
	}
	contractInventory, err := migrator.Inventory(ctx, user.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	cancelledRegistryCtx, cancelRegistry := context.WithCancel(ctx)
	cancelRegistry()
	if err = migrator.advanceRegistryContract(cancelledRegistryCtx, contractInventory, true); err == nil {
		t.Fatal("cancelled registry contract CAS unexpectedly succeeded")
	}
	unchangedAfterCancel, err := reg.GetBySchema(ctx, user.SchemaName)
	if err != nil || unchangedAfterCancel.SchemaContractVersion != previousContractVersion || unchangedAfterCancel.SchemaContractChecksum != previousContractChecksum {
		t.Fatalf("cancelled registry CAS changed contract metadata: %+v, %v", unchangedAfterCancel, err)
	}
	if err = migrator.MigrateTenantContract(ctx, contractInventory, []string{otherSchema}); err != nil {
		t.Fatalf("explicit contract migration: %v", err)
	}
	currentContractInventory, err := migrator.Inventory(ctx, user.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.MigrateTenantContract(ctx, currentContractInventory, []string{otherSchema}); err != nil {
		t.Fatalf("idempotent explicit contract migration retry: %v", err)
	}
	currentContract, err := reg.GetBySchema(ctx, user.SchemaName)
	if err != nil || !currentContract.DBIsolationReady || currentContract.SchemaContractVersion != storage.SchemaContractVersion || currentContract.SchemaContractChecksum != storage.SchemaContractChecksum() {
		t.Fatalf("explicit contract migration metadata = %+v, %v", currentContract, err)
	}
}
