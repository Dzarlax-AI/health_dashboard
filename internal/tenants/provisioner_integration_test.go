package tenants

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestProvisionerIsolationAndIdempotency(t *testing.T) {
	dsn := testdb.DSN(t)
	registryDSN := os.Getenv("REGISTRY_TEST_DSN")
	if registryDSN == "" {
		t.Fatal("HEALTH_DB_TESTS=1 requires REGISTRY_TEST_DSN for registry-role denial proof")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	requireDisposableProvisioningDB(t, ctx, admin)

	prefix := fmt.Sprintf("p%d", os.Getpid())
	previous := SecretVersion{Version: 1, Secret: []byte("disposable-test-master-secret-v1-32-bytes")}
	deriver := CredentialDeriver{Current: SecretVersion{Version: 2, Secret: []byte("disposable-test-master-secret-v2-32-bytes")}, Previous: &previous}
	reg, err := registry.New(ctx, registryDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	if err := reg.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	ua, opa, err := reg.ReserveUser(ctx, registry.CreateUserReq{Username: prefix + "a", SchemaName: prefix + "_a", Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ub, opb, err := reg.ReserveUser(ctx, registry.CreateUserReq{Username: prefix + "b", SchemaName: prefix + "_b", Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reg.DeleteUser(context.Background(), ua.Username)
		_ = reg.DeleteUser(context.Background(), ub.Username)
	})
	a := TenantSpec{TenantID: opa.TenantID, OperationID: opa.OperationID, SchemaName: opa.SchemaName, DBRole: opa.DBRole, CredentialVersion: opa.CredentialVersion}
	b := TenantSpec{TenantID: opb.TenantID, OperationID: opb.OperationID, SchemaName: opb.SchemaName, DBRole: opb.DBRole, CredentialVersion: opb.CredentialVersion}
	p, err := NewProvisioner(ctx, dsn, dsn, deriver, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	var owned []TenantSpec
	t.Cleanup(func() {
		for _, spec := range owned {
			if err := p.cleanupOwnedFixture(context.Background(), spec); err != nil {
				t.Errorf("cleanup owned fixture %s: %v", spec.SchemaName, err)
			}
		}
	})
	for _, spec := range []TenantSpec{a, b} {
		if err := reg.AdvanceProvisioning(ctx, spec.OperationID, registry.ProvisioningStatePending, registry.ProvisioningStateProvisioning, ""); err != nil {
			t.Fatal(err)
		}
		if err := p.EnsureTenant(ctx, spec); err != nil {
			t.Fatalf("ensure %s: %v", spec.SchemaName, err)
		}
		owned = append(owned, spec)
		if err := p.EnsureTenant(ctx, spec); err != nil {
			t.Fatalf("idempotent ensure %s: %v", spec.SchemaName, err)
		}
		var markerTenant, markerOperation uuid.UUID
		var contractVersion int
		var contractChecksum string
		if err := admin.QueryRow(ctx, "SELECT tenant_id,operation_id,schema_contract_version,schema_contract_checksum FROM "+pgxIdent(spec.SchemaName)+"."+pgxIdent(storage.TenantIdentityTable)+" WHERE singleton=true").Scan(&markerTenant, &markerOperation, &contractVersion, &contractChecksum); err != nil {
			t.Fatalf("read permanent marker for %s: %v", spec.SchemaName, err)
		}
		if markerTenant != spec.TenantID || markerOperation != spec.OperationID || contractVersion != storage.SchemaContractVersion || contractChecksum != storage.SchemaContractChecksum() {
			t.Fatalf("permanent marker for %s is stale", spec.SchemaName)
		}
	}
	uc, opc, err := reg.ReserveUser(ctx, registry.CreateUserReq{Username: prefix + "c", SchemaName: prefix + "_c", Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	c := TenantSpec{TenantID: opc.TenantID, OperationID: opc.OperationID, SchemaName: opc.SchemaName, DBRole: opc.DBRole, CredentialVersion: opc.CredentialVersion}
	if err := reg.AdvanceProvisioning(ctx, c.OperationID, registry.ProvisioningStatePending, registry.ProvisioningStateProvisioning, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE ROLE "+pgxIdent(c.DBRole)+" LOGIN"); err != nil {
		t.Fatal(err)
	}
	if err := p.setRoleMarker(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgxIdent(c.SchemaName)+" AUTHORIZATION "+pgxIdent(c.DBRole)); err != nil {
		t.Fatal(err)
	}
	if err := p.ensureMarker(ctx, c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "REVOKE SELECT ON health_registry.users FROM "+pgxIdent(c.DBRole))
		_, _ = admin.Exec(context.Background(), "REVOKE USAGE ON SCHEMA health_registry FROM "+pgxIdent(c.DBRole))
		if err := p.cleanupOwnedFixture(context.Background(), c); err != nil {
			t.Errorf("cleanup owned fixture %s: %v", c.SchemaName, err)
		}
		_ = reg.DeleteUser(context.Background(), uc.Username)
	})
	if _, err := admin.Exec(ctx, "GRANT USAGE ON SCHEMA health_registry TO "+pgxIdent(c.DBRole)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "GRANT SELECT ON health_registry.users TO "+pgxIdent(c.DBRole)); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureTenant(ctx, c); err == nil {
		t.Fatal("provisioning accepted restricted-login isolation failure")
	}
	var permanentMarkerExists bool
	if err := admin.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, c.SchemaName+"."+storage.TenantIdentityTable).Scan(&permanentMarkerExists); err != nil {
		t.Fatal(err)
	}
	if permanentMarkerExists {
		t.Fatal("permanent marker was written before restricted isolation proof")
	}
	if _, err := admin.Exec(ctx, "REVOKE SELECT ON health_registry.users FROM "+pgxIdent(c.DBRole)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "REVOKE USAGE ON SCHEMA health_registry FROM "+pgxIdent(c.DBRole)); err != nil {
		t.Fatal(err)
	}
	missingIndex := pgxIdent(a.SchemaName) + `.idx_hourly_date`
	if _, err := admin.Exec(ctx, "DROP INDEX "+missingIndex); err != nil {
		t.Fatal(err)
	}
	if err := p.VerifyTenant(ctx, a); err == nil {
		t.Fatal("tenant verification accepted missing required index")
	}
	if err := p.EnsureTenant(ctx, a); err != nil {
		t.Fatalf("repair required index: %v", err)
	}
	var passwordBefore, passwordAfter string
	if err := admin.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, a.DBRole).Scan(&passwordBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "COMMENT ON ROLE "+pgxIdent(a.DBRole)+` IS 'mismatched-fixture-marker'`); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureTenant(ctx, a); err == nil {
		t.Fatal("duplicate role with mismatched marker was adopted")
	}
	if err := admin.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, a.DBRole).Scan(&passwordAfter); err != nil {
		t.Fatal(err)
	}
	if passwordAfter != passwordBefore {
		t.Fatal("mismatched duplicate role credential was mutated before ownership proof")
	}
	if err := p.setRoleMarker(ctx, a); err != nil {
		t.Fatal(err)
	}
	markerTable := pgxIdent(a.SchemaName) + `.` + pgxIdent(provisionMarkerTable)
	if _, err := admin.Exec(ctx, "UPDATE "+markerTable+" SET operation_id=$1", uuid.New()); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureTenant(ctx, a); err == nil {
		t.Fatal("duplicate schema with mismatched marker was adopted")
	}
	if _, err := admin.Exec(ctx, "UPDATE "+markerTable+" SET operation_id=$1", a.OperationID); err != nil {
		t.Fatal(err)
	}
	aPool, err := p.openTenantPool(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	defer aPool.Close()
	if _, err := aPool.Exec(ctx, `CREATE TABLE own_object(id int)`); err != nil {
		t.Fatalf("own DDL: %v", err)
	}
	if _, err := aPool.Exec(ctx, `ALTER TABLE own_object ADD COLUMN value text`); err != nil {
		t.Fatalf("own ALTER: %v", err)
	}
	if _, err := aPool.Exec(ctx, `SELECT * FROM own_object`); err != nil {
		t.Fatalf("own SELECT: %v", err)
	}
	assertSQLState(t, aPool, `SELECT count(*) FROM `+pgxIdent(b.SchemaName)+`.metric_points`, "42501")
	assertSQLState(t, aPool, `INSERT INTO `+pgxIdent(b.SchemaName)+`.metric_points(health_record_id,metric_name,date) VALUES(1,'x','x')`, "42501")
	assertSQLState(t, aPool, `SELECT count(*) FROM health_registry.users`, "42501")
	assertSQLState(t, aPool, `INSERT INTO health_registry.users(username) VALUES ('nope')`, "42501")
	assertSQLState(t, aPool, `CREATE TABLE `+pgxIdent(b.SchemaName)+`.injected(id int)`, "42501")
	registryPool, err := pgxpool.New(ctx, registryDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer registryPool.Close()
	assertSQLState(t, registryPool, `SELECT count(*) FROM `+pgxIdent(a.SchemaName)+`.metric_points`, "42501")
	assertSQLState(t, registryPool, `INSERT INTO `+pgxIdent(a.SchemaName)+`.metric_points(health_record_id,metric_name,date) VALUES(1,'x','x')`, "42501")
	assertSQLState(t, registryPool, `CREATE TABLE `+pgxIdent(a.SchemaName)+`.registry_injected(id int)`, "42501")
	for _, op := range []registry.ProvisioningOperation{opa, opb} {
		op.State = registry.ProvisioningStateProvisioning
		if err := reg.ActivateProvisioned(ctx, op, registry.SchemaContractMetadata{Version: storage.SchemaContractVersion, Checksum: storage.SchemaContractChecksum()}); err != nil {
			t.Fatal(err)
		}
	}
	activeA, err := reg.GetBySchema(ctx, a.SchemaName)
	if err != nil || activeA.SchemaContractVersion != storage.SchemaContractVersion || activeA.SchemaContractChecksum != storage.SchemaContractChecksum() {
		t.Fatalf("active registry contract = %+v, %v", activeA, err)
	}
	var bPassword string
	if err := admin.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, b.DBRole).Scan(&bPassword); err != nil {
		t.Fatal(err)
	}
	badSpec := a
	badSpec.DBRole = b.DBRole
	if err := p.RotateCredential(ctx, badSpec, 2); err == nil {
		t.Fatal("rotation accepted mismatched role/spec")
	}
	var bPasswordAfter string
	if err := admin.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, b.DBRole).Scan(&bPasswordAfter); err != nil {
		t.Fatal(err)
	}
	if bPasswordAfter != bPassword {
		t.Fatal("mismatched spec mutated arbitrary existing role")
	}
	if err := p.RotateCredential(ctx, a, 2); !errors.Is(err, ErrCredentialRotationDeferred) {
		t.Fatalf("valid Task 3 rotation error=%v", err)
	}
	if err := admin.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, a.DBRole).Scan(&passwordAfter); err != nil {
		t.Fatal(err)
	}
	if passwordAfter != passwordBefore {
		t.Fatal("deferred rotation changed password")
	}
}

func TestLegacySetupRestartReconcilesExactReservation(t *testing.T) {
	dsn := testdb.DSN(t)
	ctx := context.Background()
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
	if err := reg.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("legacy%d", os.Getpid())
	schema := name + "_schema"
	u, _, err := reg.ReserveFirstUser(ctx, registry.CreateUserReq{Username: name, SchemaName: schema, Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = testdb.DropSchema(context.Background(), admin, schema)
		_ = reg.DeleteUser(context.Background(), name)
	})
	restarted := NewLegacySetup(reg, dsn)
	if err := restarted.ReconcileNonterminal(ctx); err != nil {
		t.Fatal(err)
	}
	active, err := reg.GetByUsername(ctx, name)
	if err != nil || active.TenantID != u.TenantID {
		t.Fatalf("reconciled user=%v err=%v", active, err)
	}
	if err := restarted.ReconcileNonterminal(ctx); err != nil {
		t.Fatalf("repeated restart: %v", err)
	}
	if _, _, err := reg.ReserveFirstUser(ctx, registry.CreateUserReq{Username: name + "other", Password: "fixture"}); !errors.Is(err, registry.ErrSetupClosed) {
		t.Fatalf("conflicting setup claimed reservation: %v", err)
	}
}

func requireDisposableProvisioningDB(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var marker bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM health_test_metadata WHERE key='disposable_database' AND value='true')`).Scan(&marker); err != nil || !marker {
		t.Fatalf("HEALTH_DB_TESTS=1 requires disposable database marker: %v", err)
	}
	var users int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM health_registry.users`).Scan(&users); err != nil || users != 0 {
		t.Fatalf("destructive provisioning test requires empty registry (users=%d, err=%v)", users, err)
	}
	// Provisioning operations intentionally outlive deleted users in production.
	// Disposable integration fixtures delete their users during cleanup, so clear
	// the orphaned operation history before the next destructive test starts.
	if _, err := pool.Exec(ctx, `DELETE FROM health_registry.tenant_provisioning_operations`); err != nil {
		t.Fatalf("clear disposable provisioning operations: %v", err)
	}
}

func assertSQLState(t *testing.T, pool *pgxpool.Pool, sql, want string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != want {
		t.Fatalf("%s: error=%v, want SQLSTATE %s", sql, err, want)
	}
}

func pgxIdent(s string) string { return `"` + s + `"` }
