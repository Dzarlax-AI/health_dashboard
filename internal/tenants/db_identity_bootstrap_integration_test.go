package tenants

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
)

func TestDatabaseIdentityTemporaryPrivilegeFlow(t *testing.T) {
	if os.Getenv("HEALTH_DB_TESTS") != "1" {
		t.Skip("set HEALTH_DB_TESTS=1 for disposable database identity test")
	}
	dsn := os.Getenv("DB_IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("DB_IDENTITY_TEST_DSN is not configured")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	var disposable bool
	if err = root.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.health_test_metadata WHERE key='disposable_database' AND value='true')`).Scan(&disposable); err != nil || !disposable {
		t.Fatalf("DB_IDENTITY_TEST_DSN must carry the disposable marker: %v", err)
	}
	tenantID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	tenantRole := TenantRoleName(tenantID)
	resetDatabaseIdentityFixtures(t, ctx, root, tenantRole)
	t.Cleanup(func() { resetDatabaseIdentityFixtures(t, context.Background(), root, tenantRole) })
	for _, role := range []string{DatabaseAdminRole, DatabaseRegistryRole, legacyDatabaseRole} {
		var exists bool
		if err = root.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, role).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("disposable identity cluster is dirty: fixed role %s exists", role)
		}
	}
	if _, err = root.Exec(ctx, `CREATE ROLE health_user LOGIN; CREATE SCHEMA legacy_identity AUTHORIZATION health_user; CREATE SCHEMA health_registry AUTHORIZATION health_user`); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, `CREATE ROLE health_admin LOGIN CREATEROLE; CREATE ROLE health_registry LOGIN; REVOKE health_admin FROM postgres; REVOKE health_registry FROM postgres; COMMENT ON ROLE health_admin IS 'health-db-identity-v1'; COMMENT ON ROLE health_registry IS 'health-db-identity-v1'; GRANT health_user TO health_admin WITH ADMIN FALSE, INHERIT FALSE, SET TRUE`); err != nil {
		t.Fatal(err)
	}
	var fixtureDatabase string
	if err = root.QueryRow(ctx, `SELECT format('%I',current_database())`).Scan(&fixtureDatabase); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, `REVOKE ALL ON DATABASE `+fixtureDatabase+` FROM health_admin; REVOKE ALL ON DATABASE `+fixtureDatabase+` FROM health_registry; GRANT CONNECT ON DATABASE `+fixtureDatabase+` TO health_admin`); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, `SET ROLE health_user;
		CREATE TABLE legacy_identity.sample(id integer);
		CREATE TABLE health_registry.legacy_registry(id integer);
		INSERT INTO health_registry.legacy_registry VALUES (7);
		CREATE TABLE health_registry.users(username text PRIMARY KEY,schema_name text UNIQUE NOT NULL,api_key text UNIQUE NOT NULL,password_hash text NOT NULL,email text UNIQUE,is_admin boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL DEFAULT now());
		CREATE TABLE health_registry.global_settings(key text PRIMARY KEY,value text NOT NULL DEFAULT '',updated_at timestamptz NOT NULL DEFAULT now());
		CREATE TABLE health_registry.tenant_provisioning_operations(operation_id uuid PRIMARY KEY,tenant_id uuid NOT NULL,username text NOT NULL,schema_name text NOT NULL,db_role text NOT NULL,credential_version integer NOT NULL DEFAULT 1,state text NOT NULL,error text,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),CONSTRAINT tenant_provisioning_operations_state_check CHECK (state IN ('pending','provisioning','active','failed')));
		CREATE SEQUENCE health_registry.legacy_registry_seq;
		CREATE VIEW health_registry.legacy_registry_view AS SELECT id FROM health_registry.legacy_registry;
		CREATE FUNCTION health_registry.legacy_registry_fn() RETURNS integer LANGUAGE sql SECURITY DEFINER SET search_path TO pg_catalog,health_registry AS 'SELECT 7';
		CREATE DOMAIN health_registry.legacy_registry_domain AS text CHECK (VALUE <> '');
		GRANT USAGE ON SCHEMA health_registry TO PUBLIC;
		GRANT CREATE ON SCHEMA health_registry TO health_admin;
		GRANT SELECT ON health_registry.legacy_registry TO PUBLIC;
		GRANT SELECT ON health_registry.legacy_registry TO health_admin WITH GRANT OPTION;
		GRANT EXECUTE ON FUNCTION health_registry.legacy_registry_fn() TO health_admin;
		GRANT USAGE ON DOMAIN health_registry.legacy_registry_domain TO health_admin;
		ALTER DEFAULT PRIVILEGES GRANT SELECT ON TABLES TO PUBLIC;
		ALTER DEFAULT PRIVILEGES IN SCHEMA health_registry GRANT INSERT ON TABLES TO PUBLIC;
		RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, `ALTER SEQUENCE health_registry.legacy_registry_seq OWNER TO postgres; GRANT USAGE,SELECT,UPDATE ON SEQUENCE health_registry.legacy_registry_seq TO health_user; GRANT SELECT ON SEQUENCE health_registry.legacy_registry_seq TO health_admin`); err != nil {
		t.Fatal(err)
	}

	bootstrap, err := OpenDatabaseIdentityBootstrap(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	manifest, err := bootstrap.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotFixture(t, manifest)
	manifestPath := filepath.Join(t.TempDir(), "database-identity-manifest.json")
	if err = WriteDatabaseIdentityManifest(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	writtenManifest, err := ReadDatabaseIdentityManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotFixture(t, writtenManifest)
	if _, err = root.Exec(ctx, `GRANT UPDATE ON health_registry.legacy_registry TO health_admin`); err != nil {
		t.Fatal(err)
	}
	if err = bootstrap.Bootstrap(ctx, manifest, " rejected admin password ", " rejected registry password "); err == nil {
		t.Fatal("bootstrap accepted catalog drift from its manifest")
	}
	var rejectedOwner string
	if err = root.QueryRow(ctx, `SELECT owner.rolname FROM pg_namespace n JOIN pg_roles owner ON owner.oid=n.nspowner WHERE n.nspname='health_registry'`).Scan(&rejectedOwner); err != nil {
		t.Fatal(err)
	}
	rejectedSnapshot, snapshotErr := bootstrap.Snapshot(ctx)
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	var rejectedAdminCreate bool
	if err = root.QueryRow(ctx, `SELECT has_database_privilege('health_admin',current_database(),'CREATE')`).Scan(&rejectedAdminCreate); err != nil {
		t.Fatal(err)
	}
	if rejectedOwner != legacyDatabaseRole || rejectedAdminCreate ||
		!reflect.DeepEqual(effectiveACLSet(rejectedSnapshot.DatabaseGrants), effectiveACLSet(manifest.DatabaseGrants)) ||
		!reflect.DeepEqual(rejectedSnapshot.FixedRoles, manifest.FixedRoles) {
		t.Fatalf("rejected bootstrap partially mutated owner=%s admin_create=%v", rejectedOwner, rejectedAdminCreate)
	}
	if _, err = root.Exec(ctx, `REVOKE UPDATE ON health_registry.legacy_registry FROM health_admin`); err != nil {
		t.Fatal(err)
	}
	if err = bootstrap.Bootstrap(ctx, manifest, " admin password with spaces ", " registry password with spaces "); err != nil {
		t.Fatal(err)
	}
	if err = bootstrap.Bootstrap(ctx, manifest, " admin password with spaces ", " registry password with spaces "); err != nil {
		t.Fatalf("idempotent bootstrap: %v", err)
	}

	adminCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminCfg.ConnConfig.User, adminCfg.ConnConfig.Password = DatabaseAdminRole, " admin password with spaces "
	adminCfg.MaxConns, adminCfg.MinConns = 1, 0
	admin, err := pgxpool.NewWithConfig(ctx, adminCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err = admin.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	adminDSN := identityTestDSN(t, dsn, DatabaseAdminRole, " admin password with spaces ")
	registryDSN := identityTestDSN(t, dsn, DatabaseRegistryRole, " registry password with spaces ")
	deriver := CredentialDeriver{Current: SecretVersion{Version: 1, Secret: []byte("database-identity-test-secret-32-bytes")}}
	assertRedactedIdentityRejection(t, dsn, func() error {
		_, openErr := NewFixedIdentityVerifier(ctx, dsn, registryDSN)
		return openErr
	})
	assertRedactedIdentityRejection(t, dsn, func() error {
		_, openErr := NewFixedIdentityVerifier(ctx, adminDSN, dsn)
		return openErr
	})
	assertRedactedIdentityRejection(t, dsn, func() error {
		_, openErr := NewProvisioner(ctx, dsn, identityTestDSN(t, dsn, "", ""), deriver, nil)
		return openErr
	})
	assertRedactedIdentityRejection(t, dsn, func() error {
		_, openErr := NewMigratorWithRegistryLock(ctx, dsn, registryDSN, identityTestDSN(t, dsn, "", ""), deriver)
		return openErr
	})
	assertRedactedIdentityRejection(t, dsn, func() error {
		_, openErr := NewMigratorWithRegistryLock(ctx, adminDSN, dsn, identityTestDSN(t, dsn, "", ""), deriver)
		return openErr
	})
	wrongRegistry, openErr := registry.New(ctx, dsn)
	if openErr != nil {
		t.Fatal(openErr)
	}
	identityErr := wrongRegistry.RequireExactIdentity(ctx, DatabaseRegistryRole)
	if identityErr == nil || strings.Contains(identityErr.Error(), "postgres") || strings.Contains(identityErr.Error(), "test-root") {
		t.Fatalf("runtime registry identity rejection=%v", identityErr)
	}
	assertRedactedIdentityRejection(t, dsn, func() error {
		_, isolatedErr := registry.NewWithExpectedIdentity(ctx, dsn, DatabaseRegistryRole)
		return isolatedErr
	})
	impersonationURL, parseErr := url.Parse(dsn)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	query := impersonationURL.Query()
	query.Set("options", "-c session_authorization=health_registry")
	impersonationURL.RawQuery = query.Encode()
	assertRedactedIdentityRejection(t, impersonationURL.String(), func() error {
		_, isolatedErr := registry.NewWithExpectedIdentity(ctx, impersonationURL.String(), DatabaseRegistryRole)
		return isolatedErr
	})
	assertRedactedIdentityRejection(t, dsn, func() error {
		_, provisionErr := NewProvisioner(ctx, adminDSN, identityTestDSN(t, dsn, "", ""), deriver, wrongRegistry)
		return provisionErr
	})
	wrongRegistry.Close()

	if _, err = admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{tenantRole}.Sanitize()+" LOGIN"); err != nil {
		t.Fatal(err)
	}
	assertCanonicalMembershipRows(t, ctx, root, tenantRole)

	// The tempting direct transfer is denied even when health_admin can SET both
	// roles: the old owner's schema context and the new owner's CREATE rights are
	// independently required by PostgreSQL 16.
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, "GRANT "+pgx.Identifier{tenantRole}.Sanitize()+" TO health_admin WITH INHERIT FALSE, SET TRUE")
	if err == nil {
		_, err = tx.Exec(ctx, "ALTER TABLE legacy_identity.sample OWNER TO "+pgx.Identifier{tenantRole}.Sanitize())
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("direct transfer error=%v, want 42501", err)
	}
	_ = tx.Rollback(ctx)
	assertCanonicalMembershipRows(t, ctx, root, tenantRole)

	tx, err = admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inventory := TenantInventory{Schema: "legacy_identity", Role: tenantRole, SchemaOwner: legacyDatabaseRole, Objects: []OwnedObject{{Kind: "TABLE", Name: "sample", Owner: legacyDatabaseRole}}}
	if err = transferLegacyTenantOwnership(ctx, tx, inventory); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var schemaOwner, tableOwner string
	if err = root.QueryRow(ctx, `SELECT r.rolname FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname='legacy_identity'`).Scan(&schemaOwner); err != nil {
		t.Fatal(err)
	}
	if err = root.QueryRow(ctx, `SELECT r.rolname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_roles r ON r.oid=c.relowner WHERE n.nspname='legacy_identity' AND c.relname='sample'`).Scan(&tableOwner); err != nil {
		t.Fatal(err)
	}
	if schemaOwner != tenantRole || tableOwner != tenantRole {
		t.Fatalf("owners schema=%s table=%s want=%s", schemaOwner, tableOwner, tenantRole)
	}
	if _, err = root.Exec(ctx, "SET ROLE "+pgx.Identifier{tenantRole}.Sanitize()+`; CREATE TABLE legacy_identity.metric_points(id integer); CREATE TABLE legacy_identity.settings(key text PRIMARY KEY,value text); RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	assertCanonicalMembershipRows(t, ctx, root, tenantRole)
	var tenantCreate, legacyCreate bool
	if err = root.QueryRow(ctx, `SELECT has_database_privilege($1,current_database(),'CREATE')`, tenantRole).Scan(&tenantCreate); err != nil {
		t.Fatal(err)
	}
	if tenantCreate {
		t.Fatal("transient tenant database CREATE survived commit")
	}
	if err = root.QueryRow(ctx, `SELECT has_database_privilege('health_user',current_database(),'CREATE')`).Scan(&legacyCreate); err != nil {
		t.Fatal(err)
	}
	if legacyCreate {
		t.Fatal("transient legacy database CREATE survived commit")
	}
	assertNoLegacySchemaCreateGrant(t, ctx, root, "legacy_identity", tenantRole)

	// Every temporary privilege is transaction-scoped: an injected rollback
	// restores the legacy owners and leaves no database/schema/membership ACL.
	if _, err = root.Exec(ctx, `CREATE SCHEMA legacy_rollback AUTHORIZATION health_user; SET ROLE health_user; CREATE TABLE legacy_rollback.sample(id integer); RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	tx, err = admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rollbackInventory := TenantInventory{Schema: "legacy_rollback", Role: tenantRole, SchemaOwner: legacyDatabaseRole, Objects: []OwnedObject{{Kind: "TABLE", Name: "sample", Owner: legacyDatabaseRole}}}
	if err = transferLegacyTenantOwnership(ctx, tx, rollbackInventory); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertNoLegacySchemaCreateGrant(t, ctx, root, "legacy_rollback", tenantRole)
	assertCanonicalMembershipRows(t, ctx, root, tenantRole)
	if err = root.QueryRow(ctx, `SELECT r.rolname FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname='legacy_rollback'`).Scan(&schemaOwner); err != nil {
		t.Fatal(err)
	}
	if schemaOwner != legacyDatabaseRole {
		t.Fatalf("rollback schema owner=%s want=%s", schemaOwner, legacyDatabaseRole)
	}
	if err = root.QueryRow(ctx, `SELECT has_database_privilege($1,current_database(),'CREATE')`, tenantRole).Scan(&tenantCreate); err != nil || tenantCreate {
		t.Fatalf("rollback tenant database CREATE=%v err=%v", tenantCreate, err)
	}
	if err = root.QueryRow(ctx, `SELECT has_database_privilege('health_user',current_database(),'CREATE')`).Scan(&legacyCreate); err != nil || legacyCreate {
		t.Fatalf("rollback legacy database CREATE=%v err=%v", legacyCreate, err)
	}

	// A callback failure rolls back the second SET row and leaves the automatic
	// canonical membership byte-for-byte intact.
	tx, err = admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected")
	if err = withTemporaryAdminSet(ctx, tx, tenantRole, func() error { return injected }); !errors.Is(err, injected) {
		t.Fatalf("failure=%v", err)
	}
	_ = tx.Rollback(ctx)
	assertCanonicalMembershipRows(t, ctx, root, tenantRole)

	reg, err := registry.New(ctx, identityTestDSN(t, dsn, DatabaseRegistryRole, " registry password with spaces "))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	if err = reg.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err = reg.EnsureSchema(ctx); err != nil {
		t.Fatalf("idempotent registry schema: %v", err)
	}
	if err = bootstrap.Bootstrap(ctx, manifest, " admin password with spaces ", " registry password with spaces "); err != nil {
		t.Fatalf("bootstrap retry after registry initialization: %v", err)
	}
	var retryGrantedCanonicalDML bool
	if err = root.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND c.relname='sessions' AND g.rolname='health_user')`).Scan(&retryGrantedCanonicalDML); err != nil {
		t.Fatal(err)
	}
	if retryGrantedCanonicalDML {
		t.Fatal("bootstrap retry granted health_user authority on canonical post-manifest tables")
	}
	if _, err = root.Exec(ctx, `INSERT INTO health_registry.users(username,schema_name,api_key,password_hash,tenant_id,db_role,db_credential_version,provisioning_state) VALUES('legacyidentity','legacy_identity','legacy-api','legacy-hash',$1,$2,1,'active')`, tenantID, tenantRole); err != nil {
		t.Fatal(err)
	}
	user, op, err := reg.ReserveUser(ctx, registry.CreateUserReq{Username: "identityprovision", SchemaName: "provision_identity", Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	spec := TenantSpec{TenantID: op.TenantID, OperationID: op.OperationID, SchemaName: op.SchemaName, DBRole: op.DBRole, CredentialVersion: op.CredentialVersion}
	if err = reg.AdvanceProvisioning(ctx, spec.OperationID, registry.ProvisioningStatePending, registry.ProvisioningStateProvisioning, ""); err != nil {
		t.Fatal(err)
	}
	p, err := NewProvisioner(ctx, identityTestDSN(t, dsn, DatabaseAdminRole, " admin password with spaces "), identityTestDSN(t, dsn, "", ""), deriver, reg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var provisioningUser string
	if err = p.admin.QueryRow(ctx, `SELECT current_user`).Scan(&provisioningUser); err != nil {
		t.Fatal(err)
	}
	if provisioningUser != DatabaseAdminRole {
		t.Fatalf("provisioner user=%s", provisioningUser)
	}
	if err = p.EnsureTenant(ctx, spec); err != nil {
		rows, _ := root.Query(ctx, `SELECT granted.rolname,member.rolname,grantor.rolname,m.admin_option,m.inherit_option,m.set_option FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member JOIN pg_roles grantor ON grantor.oid=m.grantor WHERE granted.rolname=$1 OR member.rolname=$1 ORDER BY 1,2,3`, spec.DBRole)
		if rows != nil {
			for rows.Next() {
				var a, b, c string
				var d, e, f bool
				_ = rows.Scan(&a, &b, &c, &d, &e, &f)
				t.Logf("membership role=%s member=%s grantor=%s admin=%v inherit=%v set=%v", a, b, c, d, e, f)
			}
			rows.Close()
		}
		t.Fatal(err)
	}
	if err = p.EnsureTenant(ctx, spec); err != nil {
		t.Fatalf("idempotent EnsureTenant: %v", err)
	}
	assertCanonicalMembershipRows(t, ctx, root, user.DBRole)

	verifier, err := NewFixedIdentityVerifier(ctx, identityTestDSN(t, dsn, DatabaseAdminRole, " admin password with spaces "), identityTestDSN(t, dsn, DatabaseRegistryRole, " registry password with spaces "))
	if err != nil {
		t.Fatal(err)
	}
	defer verifier.Close()
	result, err := verifier.Verify(ctx, true)
	if err != nil || result.Status != AuditStatusPass {
		t.Fatalf("fixed identity verification=%+v err=%v", result, err)
	}
	result, err = verifier.Verify(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != AuditStatusFail || !fixedFindingCode(result, "fixed_role_membership_drift") {
		t.Fatalf("undeclared bridge verification=%+v", result)
	}
	if _, err = root.Exec(ctx, `ALTER ROLE health_registry CONNECTION LIMIT 2`); err != nil {
		t.Fatal(err)
	}
	result, verifyErr := verifier.Verify(ctx, true)
	if _, err = root.Exec(ctx, `ALTER ROLE health_registry CONNECTION LIMIT -1`); err != nil {
		t.Fatal(err)
	}
	if verifyErr != nil {
		t.Fatal(verifyErr)
	}
	if result.Status != AuditStatusFail || !fixedFindingCode(result, "fixed_role_attributes_drift") {
		t.Fatalf("role drift verification=%+v", result)
	}

	if _, err = root.Exec(ctx, "SET ROLE "+pgx.Identifier{spec.DBRole}.Sanitize()+`; CREATE SEQUENCE provision_identity.identity_seq; CREATE FUNCTION provision_identity.identity_fn() RETURNS integer LANGUAGE sql AS 'SELECT 1'; CREATE DOMAIN provision_identity.identity_domain AS text; RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	assertFixedIdentityDrift(t, ctx, root, verifier, `GRANT USAGE ON SCHEMA provision_identity TO health_registry`, `REVOKE USAGE ON SCHEMA provision_identity FROM health_registry`, "registry_tenant_schema_privilege")
	assertFixedIdentityDrift(t, ctx, root, verifier, `GRANT SELECT ON provision_identity.settings TO health_registry`, `REVOKE SELECT ON provision_identity.settings FROM health_registry`, "registry_tenant_relation_privilege")
	assertFixedIdentityDrift(t, ctx, root, verifier, `GRANT USAGE ON SEQUENCE provision_identity.identity_seq TO health_registry`, `REVOKE USAGE ON SEQUENCE provision_identity.identity_seq FROM health_registry`, "registry_tenant_relation_privilege")
	assertFixedIdentityDrift(t, ctx, root, verifier, `GRANT EXECUTE ON FUNCTION provision_identity.identity_fn() TO health_registry`, `REVOKE EXECUTE ON FUNCTION provision_identity.identity_fn() FROM health_registry`, "registry_tenant_routine_privilege")
	assertFixedIdentityDrift(t, ctx, root, verifier, `GRANT USAGE ON TYPE provision_identity.identity_domain TO health_registry`, `REVOKE USAGE ON TYPE provision_identity.identity_domain FROM health_registry`, "registry_tenant_type_privilege")
	assertFixedIdentityDrift(t, ctx, root, verifier,
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+pgx.Identifier{spec.DBRole}.Sanitize()+` IN SCHEMA provision_identity GRANT SELECT ON TABLES TO health_registry`,
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+pgx.Identifier{spec.DBRole}.Sanitize()+` IN SCHEMA provision_identity REVOKE SELECT ON TABLES FROM health_registry`,
		"registry_tenant_default_acl_privilege")
	assertFixedIdentityDrift(t, ctx, root, verifier,
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+pgx.Identifier{spec.DBRole}.Sanitize()+` GRANT SELECT ON TABLES TO health_registry`,
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+pgx.Identifier{spec.DBRole}.Sanitize()+` REVOKE SELECT ON TABLES FROM health_registry`,
		"registry_tenant_default_acl_privilege")

	// PostgreSQL 16 requires role-global default ACL exceptions to remove the
	// built-in PUBLIC defaults. Restoring either default must be detected, and
	// re-revoking it must return the verifier to its exact protected state.
	assertFixedIdentityDrift(t, ctx, root, verifier,
		`ALTER DEFAULT PRIVILEGES FOR ROLE health_registry GRANT EXECUTE ON FUNCTIONS TO PUBLIC`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE health_registry REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC`,
		"registry_default_acl_protection_missing", "registry_public_default_acl_access")
	assertFixedIdentityDrift(t, ctx, root, verifier,
		`ALTER DEFAULT PRIVILEGES FOR ROLE health_registry GRANT USAGE ON TYPES TO PUBLIC`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE health_registry REVOKE USAGE ON TYPES FROM PUBLIC`,
		"registry_default_acl_protection_missing", "registry_public_default_acl_access")

	var databaseName string
	if err = root.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	database := pgx.Identifier{databaseName}.Sanitize()
	assertFixedIdentityDrift(t, ctx, root, verifier,
		"GRANT CONNECT ON DATABASE "+database+` TO health_registry WITH GRANT OPTION`,
		"REVOKE ALL ON DATABASE "+database+` FROM health_registry; GRANT CONNECT ON DATABASE `+database+` TO health_registry`,
		"fixed_database_grants_drift")
	assertFixedIdentityDrift(t, ctx, root, verifier,
		`COMMENT ON ROLE health_registry IS 'drift'`,
		`COMMENT ON ROLE health_registry IS '`+databaseIdentityMarker+`'`,
		"fixed_role_marker_mismatch")
	assertFixedIdentityDrift(t, ctx, root, verifier,
		`ALTER TABLE health_registry.global_settings OWNER TO postgres`,
		`ALTER TABLE health_registry.global_settings OWNER TO health_registry`,
		"registry_relation_owner_drift")
	assertFixedIdentityDrift(t, ctx, root, verifier,
		`GRANT SELECT ON health_registry.global_settings TO PUBLIC`,
		`REVOKE SELECT ON health_registry.global_settings FROM PUBLIC`,
		"registry_public_relation_access")

	assertFixedIdentityDrift(t, ctx, root, verifier,
		"REVOKE "+pgx.Identifier{spec.DBRole}.Sanitize()+` FROM health_admin`,
		"GRANT "+pgx.Identifier{spec.DBRole}.Sanitize()+` TO health_admin WITH ADMIN TRUE, INHERIT FALSE, SET FALSE`,
		"fixed_role_membership_drift")

	wrongMetadataRole := TenantRoleName(uuid.MustParse("33333333-3333-4333-8333-333333333333"))
	if _, err = root.Exec(ctx, `UPDATE health_registry.users SET db_role=$1 WHERE username=$2`, wrongMetadataRole, user.Username); err != nil {
		t.Fatal(err)
	}
	result, verifyErr = verifier.Verify(ctx, true)
	if _, restoreErr := root.Exec(ctx, `UPDATE health_registry.users SET db_role=$1 WHERE username=$2`, spec.DBRole, user.Username); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if verifyErr != nil || result.Status != AuditStatusFail || !fixedFindingCode(result, "registry_tenant_metadata_invalid") {
		t.Fatalf("invalid registry metadata verification=%+v err=%v", result, verifyErr)
	}

	// Exercise live read, write, and DDL probes together; every statement is
	// rolled back by the verifier, then every grant is explicitly removed.
	assertFixedIdentityDrift(t, ctx, root, verifier,
		`GRANT USAGE,CREATE ON SCHEMA provision_identity TO health_registry; GRANT SELECT ON provision_identity.metric_points TO health_registry; GRANT INSERT ON provision_identity.settings TO health_registry`,
		`REVOKE INSERT ON provision_identity.settings FROM health_registry; REVOKE SELECT ON provision_identity.metric_points FROM health_registry; REVOKE USAGE,CREATE ON SCHEMA provision_identity FROM health_registry`,
		"registry_tenant_access_allowed")

	orphanRole := "health_t_22222222222242228222222222222222"
	if _, err = admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{orphanRole}.Sanitize()+" LOGIN"); err != nil {
		t.Fatal(err)
	}
	result, verifyErr = verifier.Verify(ctx, true)
	if _, cleanupErr := admin.Exec(ctx, "DROP ROLE "+pgx.Identifier{orphanRole}.Sanitize()); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if verifyErr != nil || result.Status != AuditStatusFail || !fixedFindingCode(result, "fixed_role_membership_drift") {
		t.Fatalf("orphan membership verification=%+v err=%v", result, verifyErr)
	}

	result, err = verifier.Verify(ctx, true)
	if err != nil || result.Status != AuditStatusPass {
		t.Fatalf("post-negative fixed identity verification=%+v err=%v", result, err)
	}
	if _, err = root.Exec(ctx, `DELETE FROM health_registry.users WHERE schema_name='legacy_identity'; DROP SCHEMA legacy_identity CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, "REVOKE "+pgx.Identifier{tenantRole}.Sanitize()+" FROM health_admin"); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, "DROP ROLE "+pgx.Identifier{tenantRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	lockTx, err := root.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lockTx.Exec(ctx, databaseIdentityAdvisoryLockSQL); err != nil {
		t.Fatal(err)
	}
	lockedCtx, cancelLocked := context.WithTimeout(ctx, 150*time.Millisecond)
	lockedFinalizeErr := bootstrap.Finalize(lockedCtx, manifest)
	cancelLocked()
	if rollbackErr := lockTx.Rollback(ctx); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if !errors.Is(lockedFinalizeErr, context.DeadlineExceeded) {
		t.Fatalf("finalize did not wait on shared identity lock: %v", lockedFinalizeErr)
	}
	if err = bootstrap.Finalize(ctx, manifest); err == nil {
		t.Fatal("finalize succeeded while provisioning state was not active")
	}
	if _, err = root.Exec(ctx, `UPDATE health_registry.users SET provisioning_state='active',db_isolation_ready=true`); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, `UPDATE health_registry.tenant_provisioning_operations SET state='active' WHERE operation_id=$1`, op.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, `DROP INDEX health_registry.idx_registry_sessions_expires`); err != nil {
		t.Fatal(err)
	}
	if err = bootstrap.Finalize(ctx, manifest); err == nil {
		t.Fatal("finalize accepted a canonical table with a missing required index")
	}
	if _, err = root.Exec(ctx, `CREATE INDEX idx_registry_sessions_expires ON health_registry.sessions (expires_at)`); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, `SET ROLE health_registry; CREATE TABLE health_registry.unexpected_finalize_object(id integer); RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	if err = bootstrap.Finalize(ctx, manifest); err == nil {
		t.Fatal("finalize accepted an unknown registry addition")
	}
	if _, err = root.Exec(ctx, `DROP TABLE health_registry.unexpected_finalize_object`); err != nil {
		t.Fatal(err)
	}
	if err = bootstrap.Finalize(ctx, manifest); err != nil {
		t.Fatalf("finalize fixed identities: %v", err)
	}
	if err = bootstrap.Finalize(ctx, manifest); err != nil {
		t.Fatalf("idempotent finalize fixed identities: %v", err)
	}
	assertFinalizedLegacyAuthorityRemoved(t, ctx, bootstrap)
	result, err = verifier.Verify(ctx, false)
	if err != nil || result.Status != AuditStatusPass {
		t.Fatalf("authoritative post-finalize verification=%+v err=%v", result, err)
	}
	productionMigrator, err := NewMigratorWithRegistryLock(ctx, adminDSN, registryDSN, identityTestDSN(t, dsn, "", ""), deriver)
	if err != nil {
		t.Fatal(err)
	}
	defer productionMigrator.Close()
	canonicalSchemas, err := productionMigrator.CanonicalSchemas(ctx)
	if err != nil || len(canonicalSchemas) != 1 || canonicalSchemas[0] != spec.SchemaName {
		t.Fatalf("production identity canonical schemas=%v err=%v", canonicalSchemas, err)
	}
	if _, err = root.Exec(ctx, `DROP FUNCTION provision_identity.identity_fn(); DROP DOMAIN provision_identity.identity_domain; DROP SEQUENCE provision_identity.identity_seq`); err != nil {
		t.Fatal(err)
	}
	productionInventory, err := productionMigrator.Inventory(ctx, spec.SchemaName)
	if err != nil || productionInventory.Role != spec.DBRole || productionInventory.Registry.Schema != spec.SchemaName {
		t.Fatalf("production identity inventory=%+v err=%v", productionInventory, err)
	}
	if err = productionMigrator.ApplyRestrictedTenant(ctx, productionInventory, ""); err != nil {
		t.Fatalf("production identity contract reconciliation: %v", err)
	}
	auditResult, err := productionMigrator.AuditFleet(ctx)
	if err != nil || auditResult.Status != AuditStatusPass {
		t.Fatalf("production identity audit=%+v err=%v", auditResult, err)
	}
	versionOne := deriver.Current
	versionTwo := SecretVersion{Version: 2, Secret: []byte("database-identity-rotated-test-secret")}
	rotationDeriver := CredentialDeriver{Current: versionTwo, Previous: &versionOne}
	rotationMigrator, err := NewMigratorWithRegistryLock(ctx, adminDSN, registryDSN, identityTestDSN(t, dsn, "", ""), rotationDeriver)
	if err != nil {
		t.Fatal(err)
	}
	rotationInventory, err := rotationMigrator.Inventory(ctx, spec.SchemaName)
	if err != nil {
		rotationMigrator.Close()
		t.Fatal(err)
	}
	if err = rotationMigrator.RotateTenantCredential(ctx, rotationInventory, 1, 2, ""); err != nil {
		rotationMigrator.Close()
		t.Fatalf("production identity credential rotation: %v", err)
	}
	rotationMigrator.Close()

	returnDeriver := CredentialDeriver{Current: versionOne, Previous: &versionTwo}
	returnMigrator, err := NewMigratorWithRegistryLock(ctx, adminDSN, registryDSN, identityTestDSN(t, dsn, "", ""), returnDeriver)
	if err != nil {
		t.Fatal(err)
	}
	rotatedInventory, err := returnMigrator.Inventory(ctx, spec.SchemaName)
	if err != nil || rotatedInventory.CredentialVersion != 2 {
		returnMigrator.Close()
		t.Fatalf("production identity rotated inventory=%+v err=%v", rotatedInventory, err)
	}
	if err = returnMigrator.RotateTenantCredential(ctx, rotatedInventory, 2, 1, ""); err != nil {
		returnMigrator.Close()
		t.Fatalf("production identity credential return rotation: %v", err)
	}
	returnMigrator.Close()

	productionRollbackInventory, err := productionMigrator.Inventory(ctx, spec.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	if err = productionMigrator.RestoreTenant(ctx, productionRollbackInventory); err != nil {
		t.Fatalf("production identity rollback: %v", err)
	}
	if err = productionMigrator.RestoreTenant(ctx, productionRollbackInventory); err != nil {
		t.Fatalf("production identity rollback retry: %v", err)
	}
	partialInventory, err := productionMigrator.Inventory(ctx, spec.SchemaName)
	if err != nil || partialInventory.Registry.IsolationReady {
		t.Fatalf("production identity disabled partial state=%+v err=%v", partialInventory.Registry, err)
	}
	if err = productionMigrator.ApplyRestrictedTenant(ctx, partialInventory, ""); err != nil {
		t.Fatalf("production identity partial-state recovery: %v", err)
	}
	auditResult, err = productionMigrator.AuditFleet(ctx)
	if err != nil || auditResult.Status != AuditStatusPass {
		t.Fatalf("production identity recovered audit=%+v err=%v", auditResult, err)
	}
	missingBridgeInventory, err := productionMigrator.Inventory(ctx, spec.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	missingBridgeInventory.SchemaOwner = legacyDatabaseRole
	for idx := range missingBridgeInventory.Objects {
		missingBridgeInventory.Objects[idx].Owner = legacyDatabaseRole
	}
	if err = productionMigrator.RestoreTenant(ctx, missingBridgeInventory); !errors.Is(err, ErrLegacyRollbackBridgeMissing) {
		t.Fatalf("post-finalize legacy rollback error=%v, want missing bridge", err)
	}
	failClosedInventory, err := productionMigrator.Inventory(ctx, spec.SchemaName)
	if err != nil || failClosedInventory.Registry.IsolationReady {
		t.Fatalf("missing-bridge rollback did not fail closed: registry=%+v err=%v", failClosedInventory.Registry, err)
	}
	if err = productionMigrator.ApplyRestrictedTenant(ctx, failClosedInventory, ""); err != nil {
		t.Fatalf("recover missing-bridge fail-closed state: %v", err)
	}
	// sessions is the one canonical relation created after the manifest. Its
	// acceptance proves the positive finalize path; remove it before the strict
	// existing-schema rollback, which correctly treats every addition as ambiguous.
	if _, err = root.Exec(ctx, `DROP TABLE health_registry.sessions`); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, `SET ROLE health_registry; CREATE TABLE health_registry.rollback_ambiguous_addition(id integer); RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	if _, err = bootstrap.Rollback(ctx, manifest); err == nil {
		t.Fatal("rollback accepted a post-manifest registry addition")
	}
	var ownerAfterAdditionFailure string
	if err = root.QueryRow(ctx, `SELECT owner.rolname FROM pg_namespace n JOIN pg_roles owner ON owner.oid=n.nspowner WHERE n.nspname='health_registry'`).Scan(&ownerAfterAdditionFailure); err != nil {
		t.Fatal(err)
	}
	if ownerAfterAdditionFailure != DatabaseRegistryRole {
		t.Fatalf("ambiguous-addition rollback partially committed schema owner=%s", ownerAfterAdditionFailure)
	}
	if _, err = root.Exec(ctx, `DROP TABLE health_registry.rollback_ambiguous_addition`); err != nil {
		t.Fatal(err)
	}
	targetMismatch := manifest
	targetMismatch.Target.DatabaseOID++
	targetMismatch, _ = sealDatabaseIdentityManifest(targetMismatch)
	if _, err = bootstrap.Rollback(ctx, targetMismatch); err == nil {
		t.Fatal("target-mismatched rollback succeeded")
	}
	if _, err = root.Exec(ctx, `SET ROLE health_registry; ALTER DEFAULT PRIVILEGES IN SCHEMA health_registry GRANT SELECT ON TABLES TO PUBLIC; RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	if _, err = bootstrap.Rollback(ctx, manifest); err == nil {
		t.Fatal("rollback accepted an unexpected default ACL key")
	}
	var ownerAfterDefaultACLFailure string
	if err = root.QueryRow(ctx, `SELECT owner.rolname FROM pg_namespace n JOIN pg_roles owner ON owner.oid=n.nspowner WHERE n.nspname='health_registry'`).Scan(&ownerAfterDefaultACLFailure); err != nil {
		t.Fatal(err)
	}
	if ownerAfterDefaultACLFailure != DatabaseRegistryRole {
		t.Fatalf("default ACL failure partially committed schema owner=%s", ownerAfterDefaultACLFailure)
	}
	if _, err = root.Exec(ctx, `SET ROLE health_registry; ALTER DEFAULT PRIVILEGES IN SCHEMA health_registry REVOKE SELECT ON TABLES FROM PUBLIC; RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	failedReplay := manifest
	failedReplay.CatalogObjects = append([]DatabaseIdentityCatalogObject(nil), manifest.CatalogObjects...)
	failedReplay.CatalogObjects[0].Owner = "missing_rollback_owner"
	failedReplay, _ = sealDatabaseIdentityManifest(failedReplay)
	if _, err = bootstrap.Rollback(ctx, failedReplay); err == nil {
		t.Fatal("injected rollback replay succeeded")
	}
	var ownerAfterFailedReplay string
	if err = root.QueryRow(ctx, `SELECT owner.rolname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_roles owner ON owner.oid=c.relowner WHERE n.nspname='health_registry' AND c.relname='legacy_registry'`).Scan(&ownerAfterFailedReplay); err != nil {
		t.Fatal(err)
	}
	if ownerAfterFailedReplay != DatabaseRegistryRole {
		t.Fatalf("failed replay partially committed owner=%s", ownerAfterFailedReplay)
	}
	if _, err = bootstrap.Rollback(ctx, manifest); err != nil {
		t.Fatalf("rollback fixed identities: %v", err)
	}
	assertManifestPrestateRestored(t, ctx, root, bootstrap, manifest)
	if _, err = bootstrap.Rollback(ctx, manifest); err != nil {
		t.Fatalf("idempotent rollback fixed identities: %v", err)
	}
	assertManifestPrestateRestored(t, ctx, root, bootstrap, manifest)

	// Clean-install rollback cannot delete post-bootstrap application data or
	// password-bearing roles. It retains both, removes isolation-only authority,
	// and leaves the fixed roles dormant; repeating it has the same result.
	resetDatabaseIdentityFixtures(t, ctx, root, tenantRole)
	if _, err = root.Exec(ctx, `CREATE ROLE health_user LOGIN`); err != nil {
		t.Fatal(err)
	}
	cleanManifest, err := bootstrap.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleanManifest.RegistrySchemaExisted {
		t.Fatal("clean-install snapshot unexpectedly found health_registry")
	}
	if err = bootstrap.Bootstrap(ctx, cleanManifest, " clean admin password ", " clean registry password "); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Exec(ctx, `SET ROLE health_registry; CREATE TABLE health_registry.clean_install_data(id integer PRIMARY KEY); INSERT INTO health_registry.clean_install_data VALUES (9); RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		rollbackResult, rollbackErr := bootstrap.Rollback(ctx, cleanManifest)
		if rollbackErr != nil {
			t.Fatalf("clean-install rollback attempt %d: %v", attempt, rollbackErr)
		}
		if len(rollbackResult.RetainedArtifacts) != 3 {
			t.Fatalf("clean-install retained artifacts=%v", rollbackResult.RetainedArtifacts)
		}
		var rowCount int
		if err = root.QueryRow(ctx, `SELECT count(*) FROM health_registry.clean_install_data WHERE id=9`).Scan(&rowCount); err != nil || rowCount != 1 {
			t.Fatalf("clean-install data not retained count=%d err=%v", rowCount, err)
		}
		var adminLogin, registryLogin bool
		if err = root.QueryRow(ctx, `SELECT
			(SELECT rolcanlogin FROM pg_roles WHERE rolname='health_admin'),
			(SELECT rolcanlogin FROM pg_roles WHERE rolname='health_registry')`).Scan(&adminLogin, &registryLogin); err != nil {
			t.Fatal(err)
		}
		var isolationGrantCount int
		if err = root.QueryRow(ctx, `SELECT count(*) FROM pg_database d CROSS JOIN LATERAL aclexplode(coalesce(d.datacl,acldefault('d',d.datdba))) a JOIN pg_roles grantee ON grantee.oid=a.grantee WHERE d.datname=current_database() AND ((grantee.rolname='health_admin' AND a.privilege_type='CREATE') OR grantee.rolname='health_registry')`).Scan(&isolationGrantCount); err != nil {
			t.Fatal(err)
		}
		if adminLogin || registryLogin || isolationGrantCount != 0 {
			t.Fatalf("clean-install authority survived attempt=%d admin_login=%v registry_login=%v explicit_grants=%d", attempt, adminLogin, registryLogin, isolationGrantCount)
		}
		var legacySchemaAccess, legacyObjectACL bool
		if err = root.QueryRow(ctx, `SELECT has_schema_privilege('health_user','health_registry','USAGE') OR has_schema_privilege('health_user','health_registry','CREATE'), EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) a JOIN pg_roles g ON g.oid=a.grantee WHERE n.nspname='health_registry' AND g.rolname='health_user')`).Scan(&legacySchemaAccess, &legacyObjectACL); err != nil {
			t.Fatal(err)
		}
		if legacySchemaAccess || legacyObjectACL {
			t.Fatalf("clean-install legacy compatibility survived attempt=%d schema=%v object=%v", attempt, legacySchemaAccess, legacyObjectACL)
		}
	}
}

func assertFinalizedLegacyAuthorityRemoved(t *testing.T, ctx context.Context, bootstrap *DatabaseIdentityBootstrap) {
	t.Helper()
	after, err := bootstrap.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNoLegacy := func(scope string, values []DatabaseIdentityACL) {
		for _, acl := range values {
			if acl.Grantee == legacyDatabaseRole {
				t.Fatalf("finalize retained health_user %s ACL: %+v", scope, acl)
			}
		}
	}
	assertNoLegacy("schema", after.RegistrySchemaACL)
	for _, object := range after.CatalogObjects {
		assertNoLegacy(object.Kind+" "+object.Name, object.ACL)
	}
	for _, d := range after.DefaultACLs {
		if d.Schema == "health_registry" {
			assertNoLegacy("default ACL", d.ACL)
		}
	}
	for _, membership := range after.Memberships {
		if membership.Granted == legacyDatabaseRole && membership.Member == DatabaseAdminRole {
			t.Fatalf("finalize retained legacy bridge: %+v", membership)
		}
	}
}

func assertManifestPrestateRestored(t *testing.T, ctx context.Context, root *pgxpool.Pool, bootstrap *DatabaseIdentityBootstrap, before DatabaseIdentityManifest) {
	t.Helper()
	after, err := bootstrap.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.RegistrySchemaOwner != before.RegistrySchemaOwner || !reflect.DeepEqual(effectiveACLSet(after.RegistrySchemaACL), effectiveACLSet(before.RegistrySchemaACL)) {
		t.Fatalf("schema state after rollback owner=%s acl=%+v", after.RegistrySchemaOwner, after.RegistrySchemaACL)
	}
	afterObjects := map[string]DatabaseIdentityCatalogObject{}
	for _, object := range after.CatalogObjects {
		afterObjects[object.Kind+"\x00"+object.Name+"\x00"+object.IdentityArgs] = object
	}
	for _, want := range before.CatalogObjects {
		got, ok := afterObjects[want.Kind+"\x00"+want.Name+"\x00"+want.IdentityArgs]
		if !ok || got.Owner != want.Owner || got.SecurityDefiner != want.SecurityDefiner || !reflect.DeepEqual(got.RoleConfig, want.RoleConfig) || !reflect.DeepEqual(effectiveACLSet(got.ACL), effectiveACLSet(want.ACL)) {
			t.Fatalf("catalog object not restored want=%+v got=%+v", want, got)
		}
	}
	if !reflect.DeepEqual(defaultACLEffectiveSet(after.DefaultACLs), defaultACLEffectiveSet(before.DefaultACLs)) {
		t.Fatalf("default ACLs not restored want=%+v got=%+v", before.DefaultACLs, after.DefaultACLs)
	}
	if !reflect.DeepEqual(membershipEffectiveSet(after.Memberships), membershipEffectiveSet(before.Memberships)) {
		t.Fatalf("memberships not restored want=%+v got=%+v", before.Memberships, after.Memberships)
	}
	if !reflect.DeepEqual(effectiveACLSet(after.DatabaseGrants), effectiveACLSet(before.DatabaseGrants)) || !reflect.DeepEqual(after.FixedRoles, before.FixedRoles) {
		t.Fatalf("fixed role/database state not restored roles=%+v grants=%+v", after.FixedRoles, after.DatabaseGrants)
	}
	var rows int
	if err = root.QueryRow(ctx, `SELECT count(*) FROM health_registry.legacy_registry WHERE id=7`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("registry data changed rows=%d err=%v", rows, err)
	}
}

func effectiveACLSet(values []DatabaseIdentityACL) map[string]bool {
	out := map[string]bool{}
	for _, acl := range values {
		out[acl.Grantee+"\x00"+acl.Privilege+fmt.Sprint(acl.Grantable)] = true
	}
	return out
}

func defaultACLEffectiveSet(values []DatabaseIdentityDefaultACL) map[string]bool {
	out := map[string]bool{}
	for _, d := range values {
		for key := range effectiveACLSet(d.ACL) {
			out[d.Owner+"\x00"+d.Schema+"\x00"+d.ObjectType+"\x00"+key] = true
		}
	}
	return out
}

func membershipEffectiveSet(values []DatabaseIdentityMembership) map[string]bool {
	out := map[string]bool{}
	for _, membership := range values {
		out[fmt.Sprintf("%s\x00%s\x00%s\x00%t\x00%t\x00%t", membership.Granted, membership.Member, membership.Grantor, membership.AdminOption, membership.InheritOption, membership.SetOption)] = true
	}
	return out
}

func fixedFindingCode(r FixedIdentityResult, code string) bool {
	for _, f := range r.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func assertSnapshotFixture(t *testing.T, manifest DatabaseIdentityManifest) {
	t.Helper()
	if !manifest.RegistrySchemaExisted || manifest.RegistrySchemaOwner != legacyDatabaseRole {
		t.Fatalf("registry snapshot header=%+v", manifest)
	}
	owners := map[string]string{}
	objects := map[string]DatabaseIdentityCatalogObject{}
	for _, owner := range manifest.CatalogObjects {
		owners[owner.Name] = owner.Kind
		objects[owner.Name] = owner
	}
	if owners["legacy_registry"] != "TABLE" || owners["legacy_registry_seq"] != "SEQUENCE" {
		t.Fatalf("registry snapshot owners=%+v", manifest.CatalogObjects)
	}
	if owners["legacy_registry_view"] != "VIEW" || owners["legacy_registry_fn"] != "FUNCTION" || owners["legacy_registry_domain"] != "DOMAIN" {
		t.Fatalf("registry snapshot missing view/routine/domain: %+v", manifest.CatalogObjects)
	}
	if routine := objects["legacy_registry_fn"]; !routine.SecurityDefiner || !safeSecurityDefinerSearchPath(routine.RoleConfig) {
		t.Fatalf("security-definer metadata=%+v", routine)
	}
	grants := map[string]bool{}
	for name, object := range objects {
		for _, grant := range object.ACL {
			if grant.Grantee == legacyDatabaseRole {
				grants[objectGrantClass(object.Kind)+":"+name+":"+grant.Privilege] = true
			}
		}
	}
	for _, expected := range []string{
		"TABLE:legacy_registry:SELECT",
		"TABLE:legacy_registry:INSERT",
		"SEQUENCE:legacy_registry_seq:USAGE",
		"SEQUENCE:legacy_registry_seq:SELECT",
		"SEQUENCE:legacy_registry_seq:UPDATE",
	} {
		if !grants[expected] {
			t.Fatalf("registry snapshot missing %s: %+v", expected, manifest.CatalogObjects)
		}
	}
	if !identityACLContains(manifest.RegistrySchemaACL, "PUBLIC", "USAGE", false) || !identityACLContains(manifest.RegistrySchemaACL, DatabaseAdminRole, "CREATE", false) {
		t.Fatalf("registry schema ACL=%+v", manifest.RegistrySchemaACL)
	}
	defaultKeys := map[string]bool{}
	for _, d := range manifest.DefaultACLs {
		defaultKeys[d.Owner+":"+d.Schema+":"+d.ObjectType] = true
	}
	if !defaultKeys[legacyDatabaseRole+"::r"] || !defaultKeys[legacyDatabaseRole+":health_registry:r"] {
		t.Fatalf("registry default ACLs=%+v", manifest.DefaultACLs)
	}
	if len(manifest.Memberships) != 1 || manifest.Memberships[0].Granted != legacyDatabaseRole || manifest.Memberships[0].Member != DatabaseAdminRole || manifest.Memberships[0].AdminOption || manifest.Memberships[0].InheritOption || !manifest.Memberships[0].SetOption {
		t.Fatalf("legacy bridge snapshot=%+v", manifest.Memberships)
	}
	for _, role := range manifest.FixedRoles {
		if !role.Existed {
			t.Fatalf("preexisting fixed role not captured: %+v", role)
		}
	}
	if !identityACLContains(manifest.DatabaseGrants, DatabaseAdminRole, "CONNECT", false) || identityACLContains(manifest.DatabaseGrants, DatabaseRegistryRole, "CONNECT", false) {
		t.Fatalf("preexisting fixed database grants=%+v", manifest.DatabaseGrants)
	}
}

func identityACLContains(values []DatabaseIdentityACL, grantee, privilege string, grantable bool) bool {
	for _, value := range values {
		if value.Grantee == grantee && value.Privilege == privilege && value.Grantable == grantable {
			return true
		}
	}
	return false
}

func assertRedactedIdentityRejection(t *testing.T, secret string, open func() error) {
	t.Helper()
	err := open()
	if err == nil {
		t.Fatal("wrong fixed database identity accepted")
	}
	for _, unsafe := range []string{secret, "test-root", "postgres://postgres"} {
		if unsafe != "" && strings.Contains(err.Error(), unsafe) {
			t.Fatalf("identity error leaked connection details: %v", err)
		}
	}
}

func assertFixedIdentityDrift(t *testing.T, ctx context.Context, root *pgxpool.Pool, verifier *FixedIdentityVerifier, setup, restore string, codes ...string) {
	t.Helper()
	if len(codes) == 0 {
		t.Fatal("drift assertion requires at least one finding code")
	}
	if _, err := root.Exec(ctx, setup); err != nil {
		t.Fatalf("set up %s drift: %v", codes[0], err)
	}
	result, verifyErr := verifier.Verify(ctx, true)
	if _, err := root.Exec(ctx, restore); err != nil {
		t.Fatalf("restore %s drift: %v", codes[0], err)
	}
	if verifyErr != nil {
		t.Fatalf("verify %s drift: %v", codes[0], verifyErr)
	}
	for _, code := range codes {
		if result.Status != AuditStatusFail || !fixedFindingCode(result, code) {
			t.Fatalf("%s drift verification=%+v", code, result)
		}
	}
}

func assertNoLegacySchemaCreateGrant(t *testing.T, ctx context.Context, root *pgxpool.Pool, schema, tenantRole string) {
	t.Helper()
	var exists bool
	if err := root.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM pg_namespace n
			CROSS JOIN LATERAL aclexplode(n.nspacl) a
			JOIN pg_roles grantee ON grantee.oid=a.grantee
			JOIN pg_roles grantor ON grantor.oid=a.grantor
			WHERE n.nspname=$1 AND grantee.rolname=$2
			  AND grantor.rolname='health_user' AND a.privilege_type='CREATE'
		)`, schema, tenantRole).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatalf("transient health_user schema CREATE grant survived for %s", schema)
	}
}

func identityTestDSN(t *testing.T, dsn, user, password string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if user == "" {
		u.User = nil
	} else {
		u.User = url.UserPassword(user, password)
	}
	return u.String()
}

func resetDatabaseIdentityFixtures(t *testing.T, ctx context.Context, root *pgxpool.Pool, tenantRole string) {
	t.Helper()
	_, _ = root.Exec(ctx, `DROP SCHEMA IF EXISTS provision_identity CASCADE; DROP SCHEMA IF EXISTS legacy_identity CASCADE; DROP SCHEMA IF EXISTS legacy_rollback CASCADE; DROP SCHEMA IF EXISTS health_registry CASCADE`)
	for _, stmt := range []string{
		"REVOKE " + pgx.Identifier{tenantRole}.Sanitize() + " FROM health_admin",
		"REVOKE " + pgx.Identifier{tenantRole}.Sanitize() + " FROM health_user",
		`REVOKE health_user FROM health_admin`,
	} {
		_, _ = root.Exec(ctx, stmt)
	}
	roles := []string{tenantRole}
	rows, err := root.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname LIKE 'health_t_%'`)
	if err == nil {
		for rows.Next() {
			var role string
			if rows.Scan(&role) == nil {
				roles = append(roles, role)
			}
		}
		rows.Close()
	}
	roles = append(roles, DatabaseRegistryRole, DatabaseAdminRole, legacyDatabaseRole)
	seen := map[string]bool{}
	for _, role := range roles {
		if seen[role] {
			continue
		}
		seen[role] = true
		_, _ = root.Exec(ctx, "REVOKE "+pgx.Identifier{role}.Sanitize()+" FROM health_admin")
		_, _ = root.Exec(ctx, "REVOKE "+pgx.Identifier{role}.Sanitize()+" FROM health_user")
		_, _ = root.Exec(ctx, "DROP OWNED BY "+pgx.Identifier{role}.Sanitize())
		_, _ = root.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize())
	}
}

func assertCanonicalMembershipRows(t *testing.T, ctx context.Context, root *pgxpool.Pool, role string) {
	t.Helper()
	rows, err := root.Query(ctx, `SELECT granted.rolname,member.rolname,m.admin_option,m.inherit_option,m.set_option FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member WHERE granted.rolname=$1 OR (member.rolname=$1 AND granted.rolname<>'health_user') ORDER BY 1,2`, role)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []MembershipRecord
	for rows.Next() {
		var x MembershipRecord
		if err = rows.Scan(&x.Role, &x.Member, &x.AdminOption, &x.InheritOption, &x.SetOption); err != nil {
			t.Fatal(err)
		}
		got = append(got, x)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !canonicalTenantMemberships(role, got) {
		t.Fatalf("membership rows=%+v", got)
	}
}
