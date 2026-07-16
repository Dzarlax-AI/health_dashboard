package tenants

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestAuditFleetThreeTenantIntegration(t *testing.T) {
	if os.Getenv("HEALTH_DB_TESTS") != "1" {
		t.Skip("set HEALTH_DB_TESTS=1 for destructive disposable-DB fleet audit test")
	}
	ctx := context.Background()
	dsn := testdb.DSN(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	registryDSN := os.Getenv("REGISTRY_TEST_DSN")
	if registryDSN == "" {
		t.Fatal("HEALTH_DB_TESTS=1 requires REGISTRY_TEST_DSN")
	}
	requireSameDisposableRegistryTarget(t, ctx, admin, registryDSN)
	requireDisposableProvisioningDB(t, ctx, admin)
	reg, err := registry.New(ctx, registryDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	if err = reg.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("fleet%d", os.Getpid())
	legacy := NewLegacySetup(reg, dsn)
	users := make([]*registry.User, 0, 3)
	for n := 0; n < 3; n++ {
		name := fmt.Sprintf("%s%d", prefix, n)
		user, createErr := legacy.CreateTenant(ctx, registry.CreateUserReq{Username: name, SchemaName: name + "_schema", Password: "fixture"})
		if createErr != nil {
			t.Fatal(createErr)
		}
		users = append(users, user)
	}
	t.Cleanup(func() {
		for _, u := range users {
			_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgxIdent(u.SchemaName)+" CASCADE")
			_, _ = admin.Exec(context.Background(), "DROP OWNED BY "+pgxIdent(u.DBRole))
			_, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgxIdent(u.DBRole))
			_ = reg.DeleteUser(context.Background(), u.Username)
		}
	})
	deriver := CredentialDeriver{Current: SecretVersion{Version: 1, Secret: []byte("fleet-audit-disposable-secret-32-bytes")}}
	migrator, err := NewMigrator(ctx, dsn, credentialFreeTestDSN(t, dsn), deriver)
	if err != nil {
		t.Fatal(err)
	}
	defer migrator.Close()
	for idx, u := range users {
		inventory, inventoryErr := migrator.Inventory(ctx, u.SchemaName)
		if inventoryErr != nil {
			t.Fatal(inventoryErr)
		}
		other := users[(idx+1)%len(users)].SchemaName
		if applyErr := migrator.ApplyRestrictedTenant(ctx, inventory, other); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	result, auditErr := migrator.AuditFleet(ctx)
	if auditErr != nil {
		t.Fatal(auditErr)
	}
	if result.Status != AuditStatusPass || result.Counts.Markers != 3 || result.Counts.Roles != 3 || result.Probes.Attempted != 27 || result.Probes.Denied != 27 || result.Probes.Failed != 0 {
		t.Fatalf("clean fleet audit=%+v", result)
	}
	// Exercise both supported legacy marker shapes across a real multi-tenant
	// progression. The structural digest must remain stable after tenant zero is
	// upgraded so tenant one and the final proof can continue.
	for _, column := range []string{"schema_contract_version", "schema_contract_checksum"} {
		if _, err = admin.Exec(ctx, "ALTER TABLE "+pgxIdent(users[0].SchemaName)+"."+pgxIdent(storage.TenantIdentityTable)+" DROP COLUMN "+pgxIdent(column)); err != nil {
			t.Fatal(err)
		}
		if _, err = admin.Exec(ctx, "ALTER TABLE "+pgxIdent(users[1].SchemaName)+"."+pgxIdent(storage.TenantIdentityTable)+" ALTER COLUMN "+pgxIdent(column)+" DROP NOT NULL"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = admin.Exec(ctx, "UPDATE "+pgxIdent(users[1].SchemaName)+"."+pgxIdent(storage.TenantIdentityTable)+` SET schema_contract_version=NULL,schema_contract_checksum=NULL WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE health_registry.users SET schema_contract_version=NULL,schema_contract_checksum=NULL WHERE schema_name=ANY($1)`, []string{users[0].SchemaName, users[1].SchemaName}); err != nil {
		t.Fatal(err)
	}
	migrationFleet, err := migrator.PrepareContractMigrationFleet(ctx)
	if err != nil {
		t.Fatalf("legacy fleet preflight: %v", err)
	}
	ordered := append([]TenantInventory(nil), migrationFleet.Inventories...)
	for left, right := 0, len(ordered)-1; left < right; left, right = left+1, right-1 {
		ordered[left], ordered[right] = ordered[right], ordered[left]
	}
	for _, original := range ordered {
		validated, validateErr := migrator.ValidateContractMigrationFleet(ctx, migrationFleet.Digest)
		if validateErr != nil {
			t.Fatalf("legacy fleet structural validation: %v", validateErr)
		}
		var fresh TenantInventory
		for _, candidate := range validated.Inventories {
			if candidate.Schema == original.Schema {
				fresh = candidate
			}
		}
		if fresh.Schema == "" {
			t.Fatal("fresh legacy migration inventory missing")
		}
		if err = migrator.MigrateTenantContract(ctx, fresh, validated.PeerSchemas); err != nil {
			t.Fatalf("legacy fleet tenant migration: %v", err)
		}
	}
	if _, err = migrator.ValidateContractMigrationFleet(ctx, migrationFleet.Digest); err != nil {
		t.Fatalf("legacy fleet final structural validation: %v", err)
	}
	result, auditErr = migrator.AuditFleet(ctx)
	if auditErr != nil || result.Status != AuditStatusPass {
		t.Fatalf("legacy fleet final audit=%+v err=%v", result, auditErr)
	}
	assertNoAuditArtifacts(t, ctx, admin, users)
	migrator.auditBeforeEndSnapshot = func(hookCtx context.Context) error {
		_, hookErr := migrator.admin.Exec(hookCtx, `UPDATE health_registry.users SET schema_contract_checksum='changed-during-audit' WHERE schema_name=$1`, users[0].SchemaName)
		return hookErr
	}
	changed, changedErr := migrator.AuditFleet(ctx)
	migrator.auditBeforeEndSnapshot = nil
	if changedErr != nil {
		t.Fatal(changedErr)
	}
	if !findingCodes(changed.Findings)["inventory_changed"] {
		t.Fatalf("hook did not invalidate inventory: %+v", changed)
	}
	if _, err = admin.Exec(ctx, `UPDATE health_registry.users SET schema_contract_checksum=$2 WHERE schema_name=$1`, users[0].SchemaName, storage.SchemaContractChecksum()); err != nil {
		t.Fatal(err)
	}
	var adminRole string
	if err = admin.QueryRow(ctx, `SELECT current_user`).Scan(&adminRole); err != nil {
		t.Fatal(err)
	}
	migrator.auditBeforeEndSnapshot = func(hookCtx context.Context) error {
		if _, hookErr := migrator.admin.Exec(hookCtx, "GRANT SELECT ON "+pgxIdent(users[1].SchemaName)+".metric_points TO PUBLIC"); hookErr != nil {
			return hookErr
		}
		_, hookErr := migrator.admin.Exec(hookCtx, "ALTER TABLE "+pgxIdent(users[1].SchemaName)+".settings OWNER TO "+pgxIdent(adminRole))
		return hookErr
	}
	catalogChanged, catalogChangedErr := migrator.AuditFleet(ctx)
	migrator.auditBeforeEndSnapshot = nil
	if catalogChangedErr != nil {
		t.Fatal(catalogChangedErr)
	}
	if !findingCodes(catalogChanged.Findings)["inventory_changed"] {
		t.Fatalf("catalog drift not detected: %+v", catalogChanged)
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+pgxIdent(users[1].SchemaName)+".settings OWNER TO "+pgxIdent(users[1].DBRole)); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "REVOKE SELECT ON "+pgxIdent(users[1].SchemaName)+".metric_points FROM PUBLIC"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "GRANT USAGE ON SCHEMA "+pgxIdent(users[1].SchemaName)+" TO "+pgxIdent(users[0].DBRole)); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "GRANT SELECT ON "+pgxIdent(users[1].SchemaName)+".metric_points TO "+pgxIdent(users[0].DBRole)); err != nil {
		t.Fatal(err)
	}
	accessAllowed, accessErr := migrator.AuditFleet(ctx)
	if accessErr != nil {
		t.Fatal(accessErr)
	}
	if !findingCodes(accessAllowed.Findings)["cross_tenant_access_allowed"] || accessAllowed.Probes.Attempted != 27 || accessAllowed.Probes.Failed < 1 {
		t.Fatalf("cross grant not detected: %+v", accessAllowed)
	}
	assertNoAuditArtifacts(t, ctx, admin, users)
	if _, err = admin.Exec(ctx, "REVOKE SELECT ON "+pgxIdent(users[1].SchemaName)+".metric_points FROM "+pgxIdent(users[0].DBRole)); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "REVOKE USAGE ON SCHEMA "+pgxIdent(users[1].SchemaName)+" FROM "+pgxIdent(users[0].DBRole)); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "DROP TABLE "+pgxIdent(users[0].SchemaName)+".__tenant_identity"); err != nil {
		t.Fatal(err)
	}
	markerless, markerlessErr := migrator.AuditFleet(ctx)
	if markerlessErr != nil {
		t.Fatalf("missing marker is logical: %v", markerlessErr)
	}
	if !findingCodes(markerless.Findings)["registry_marker_missing"] || markerless.Probes.Attempted != 27 {
		t.Fatalf("markerless active schema omitted from peers/probes: %+v", markerless)
	}
	if err = storage.EnsureTenantIdentityMarker(ctx, admin, users[0].SchemaName, users[0].TenantID, users[0].TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+pgxIdent(users[0].SchemaName)+".__tenant_identity OWNER TO "+pgxIdent(users[0].DBRole)); err != nil {
		t.Fatal(err)
	}
	markerTable := pgxIdent(users[0].SchemaName) + ".__tenant_identity"
	if _, err = admin.Exec(ctx, "ALTER TABLE "+markerTable+" ALTER COLUMN tenant_id TYPE text USING tenant_id::text"); err != nil {
		t.Fatal(err)
	}
	wrongTenantType, wrongTenantTypeErr := migrator.AuditFleet(ctx)
	if wrongTenantTypeErr != nil {
		t.Fatalf("snapshot-known tenant_id type corruption became operational: %v", wrongTenantTypeErr)
	}
	if !findingCodes(wrongTenantType.Findings)["marker_column_type_mismatch"] || wrongTenantType.Probes.Attempted != 27 {
		t.Fatalf("tenant_id type corruption audit=%+v", wrongTenantType)
	}
	assertNoAuditArtifacts(t, ctx, admin, users)
	if _, err = admin.Exec(ctx, "ALTER TABLE "+markerTable+" ALTER COLUMN tenant_id TYPE uuid USING tenant_id::uuid"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+markerTable+" SET UNLOGGED"); err != nil {
		t.Fatal(err)
	}
	unlogged, auditErr := migrator.AuditFleet(ctx)
	if auditErr != nil || !findingCodes(unlogged.Findings)["marker_relation_persistence_invalid"] {
		t.Fatalf("unlogged marker audit=%+v err=%v", unlogged, auditErr)
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+markerTable+" SET LOGGED"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+markerTable+" ADD COLUMN unexpected_marker_data text"); err != nil {
		t.Fatal(err)
	}
	extraColumn, auditErr := migrator.AuditFleet(ctx)
	if auditErr != nil || !findingCodes(extraColumn.Findings)["marker_column_unexpected"] {
		t.Fatalf("extra marker column audit=%+v err=%v", extraColumn, auditErr)
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+markerTable+" DROP COLUMN unexpected_marker_data"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"ALTER TABLE " + markerTable + " DROP CONSTRAINT IF EXISTS __tenant_identity_pkey", "ALTER TABLE " + markerTable + " DROP CONSTRAINT IF EXISTS __tenant_identity_singleton_check", "ALTER TABLE " + markerTable + " ALTER COLUMN singleton DROP DEFAULT", "ALTER TABLE " + markerTable + " ALTER COLUMN singleton TYPE text USING singleton::text"} {
		if _, err = admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	wrongSingletonType, wrongSingletonTypeErr := migrator.AuditFleet(ctx)
	if wrongSingletonTypeErr != nil {
		t.Fatalf("snapshot-known singleton type corruption became operational: %v", wrongSingletonTypeErr)
	}
	if !findingCodes(wrongSingletonType.Findings)["marker_column_type_mismatch"] || wrongSingletonType.Probes.Attempted != 27 {
		t.Fatalf("singleton type corruption audit=%+v", wrongSingletonType)
	}
	assertNoAuditArtifacts(t, ctx, admin, users)
	if _, err = admin.Exec(ctx, "DROP TABLE "+markerTable); err != nil {
		t.Fatal(err)
	}
	if err = storage.EnsureTenantIdentityMarker(ctx, admin, users[0].SchemaName, users[0].TenantID, users[0].TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+markerTable+" OWNER TO "+pgxIdent(users[0].DBRole)); err != nil {
		t.Fatal(err)
	}

	orphan := prefix + "_orphan"
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgxIdent(orphan)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgxIdent(orphan)+" CASCADE") })
	for _, sql := range []string{"CREATE TABLE " + pgxIdent(orphan) + ".__tenant_identity(singleton boolean PRIMARY KEY,tenant_id uuid NOT NULL,operation_id uuid NOT NULL,schema_contract_version integer,schema_contract_checksum text)", "CREATE TABLE " + pgxIdent(orphan) + ".metric_points(id integer)", "CREATE TABLE " + pgxIdent(orphan) + ".settings(key text,value text)"} {
		if _, err = admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	many := prefix + "_many"
	wrongRelation := prefix + "_wrongrel"
	for _, schema := range []string{many, wrongRelation} {
		if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgxIdent(schema)); err != nil {
			t.Fatal(err)
		}
		schemaCopy := schema
		t.Cleanup(func() {
			_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgxIdent(schemaCopy)+" CASCADE")
		})
		for _, sql := range []string{"CREATE TABLE " + pgxIdent(schema) + ".metric_points(id integer)", "CREATE TABLE " + pgxIdent(schema) + ".settings(key text,value text)"} {
			if _, err = admin.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = admin.Exec(ctx, "CREATE TABLE "+pgxIdent(many)+".__tenant_identity(singleton boolean DEFAULT false,tenant_id uuid,operation_id uuid,schema_contract_version integer,schema_contract_checksum text)"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "INSERT INTO "+pgxIdent(many)+".__tenant_identity SELECT true,'11111111-1111-4111-8111-111111111111'::uuid,'22222222-2222-4222-8222-222222222222'::uuid,1,'x' FROM generate_series(1,3)"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "CREATE VIEW "+pgxIdent(wrongRelation)+".__tenant_identity AS SELECT true::boolean singleton,'33333333-3333-4333-8333-333333333333'::uuid tenant_id,'44444444-4444-4444-8444-444444444444'::uuid operation_id,1::integer schema_contract_version,'x'::text schema_contract_checksum WHERE false"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "ALTER TABLE "+pgxIdent(users[0].SchemaName)+".__tenant_identity DROP COLUMN schema_contract_checksum"); err != nil {
		t.Fatal(err)
	}
	malformed, malformedErr := migrator.AuditFleet(ctx)
	if malformedErr != nil {
		t.Fatalf("malformed markers are logical findings, got %v", malformedErr)
	}
	codes := findingCodes(malformed.Findings)
	for _, want := range []string{"marker_contract_columns_partial", "marker_column_missing", "marker_empty", "marker_multiple_rows", "marker_multiple_singleton_rows", "marker_singleton_primary_key_missing", "marker_singleton_check_missing", "marker_singleton_default_mismatch", "marker_relation_kind_invalid", "marker_registry_missing"} {
		if !codes[want] {
			t.Errorf("missing %s in %+v", want, malformed.Findings)
		}
	}
	if malformed.Counts.Markers != 6 || malformed.Counts.Roles != 3 || malformed.Probes.Attempted != 54 {
		t.Fatalf("malformed fleet counts/probes=%+v", malformed)
	}
	assertNoAuditArtifacts(t, ctx, admin, users)
}

func requireSameDisposableRegistryTarget(t *testing.T, ctx context.Context, admin *pgxpool.Pool, registryDSN string) {
	t.Helper()
	registryPool, err := pgxpool.New(ctx, registryDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer registryPool.Close()
	type identity struct {
		database string
		address  *string
		port     *int
		started  string
	}
	read := func(pool *pgxpool.Pool) (identity, error) {
		var x identity
		err := pool.QueryRow(ctx, `SELECT current_database(),inet_server_addr()::text,inet_server_port(),pg_postmaster_start_time()::text`).Scan(&x.database, &x.address, &x.port, &x.started)
		return x, err
	}
	a, err := read(admin)
	if err != nil {
		t.Fatal(err)
	}
	b, err := read(registryPool)
	if err != nil {
		t.Fatal(err)
	}
	address := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	port := func(v *int) int {
		if v == nil {
			return 0
		}
		return *v
	}
	if a.database != b.database || port(a.port) != port(b.port) || a.started != b.started || address(a.address) != address(b.address) {
		t.Fatalf("REGISTRY_TEST_DSN is not the disposable admin database")
	}
	var marker bool
	if err = registryPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM health_test_metadata WHERE key='disposable_database' AND value='true')`).Scan(&marker); err != nil || !marker {
		t.Fatalf("registry target lacks disposable marker: %v", err)
	}
	var usersTable *string
	if err = registryPool.QueryRow(ctx, `SELECT to_regclass('health_registry.users')::text`).Scan(&usersTable); err != nil {
		t.Fatal(err)
	}
	if usersTable != nil {
		var count int
		if err = registryPool.QueryRow(ctx, `SELECT count(*) FROM health_registry.users`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("registry target is not empty: count=%d err=%v", count, err)
		}
	}
}

func assertNoAuditArtifacts(t *testing.T, ctx context.Context, admin *pgxpool.Pool, users []*registry.User) {
	t.Helper()
	schemas := []string{"health_registry"}
	for _, u := range users {
		schemas = append(schemas, u.SchemaName)
	}
	var count int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relname='__isolation_probe__' AND n.nspname=ANY($1)`, schemas).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback-only probes left artifacts: count=%d err=%v", count, err)
	}
}
