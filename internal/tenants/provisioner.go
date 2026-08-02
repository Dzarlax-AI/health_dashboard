package tenants

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

const provisionMarkerTable = "_health_tenant_provisioning"

var ErrCredentialRotationDeferred = errors.New("credential rotation is deferred until atomic cutover is implemented")

type TenantSpec struct {
	TenantID          uuid.UUID
	OperationID       uuid.UUID
	SchemaName        string
	DBRole            string
	CredentialVersion int
}

type Provisioner interface {
	EnsureTenant(context.Context, TenantSpec) error
	VerifyTenant(context.Context, TenantSpec) error
	Reconcile(context.Context, uuid.UUID) error
	RotateCredential(context.Context, TenantSpec, int) error
}

type TenantSetup interface {
	CreateFirstTenant(context.Context, registry.CreateUserReq) (*registry.User, error)
	CreateTenant(context.Context, registry.CreateUserReq) (*registry.User, error)
}

// AdminProvisioner is the only component that owns an administrative pool.
// The pool is one-connection, short-lived, and is never exposed to Manager or
// request contexts.
type AdminProvisioner struct {
	admin      *pgxpool.Pool
	tenantBase string
	deriver    CredentialDeriver
	registry   *registry.Registry
}

func NewProvisioner(ctx context.Context, adminDSN, tenantBase string, deriver CredentialDeriver, reg *registry.Registry) (*AdminProvisioner, error) {
	if err := deriver.validate(); err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(adminDSN)
	if err != nil {
		return nil, fmt.Errorf("parse provisioning database config: %w", err)
	}
	if err = secureFixedPoolConfig(cfg); err != nil {
		return nil, err
	}
	cfg.MaxConns, cfg.MinConns = 1, 0
	cfg.MaxConnIdleTime = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open provisioning database: %w", err)
	}
	if err := requireExactPoolIdentity(ctx, pool, DatabaseAdminRole); err != nil {
		pool.Close()
		return nil, err
	}
	if reg == nil {
		pool.Close()
		return nil, errors.New("provisioner registry is unavailable (details redacted)")
	}
	if err := reg.RequireExactIdentity(ctx, DatabaseRegistryRole); err != nil {
		pool.Close()
		return nil, err
	}
	return &AdminProvisioner{admin: pool, tenantBase: tenantBase, deriver: deriver, registry: reg}, nil
}

func (p *AdminProvisioner) Close() { p.admin.Close() }

func (p *AdminProvisioner) withProvisioningGuard(ctx context.Context, fn func() error) (err error) {
	if p.registry == nil {
		return errors.New("provisioner has no registry")
	}
	guard, err := p.registry.AcquireProvisioningGuard(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, guard.Release()) }()
	return fn()
}

func validateTenantSpec(spec TenantSpec) error {
	if spec.TenantID == uuid.Nil || spec.OperationID == uuid.Nil {
		return errors.New("tenant and operation IDs must not be nil")
	}
	if err := registry.ValidateSchemaName(spec.SchemaName); err != nil {
		return err
	}
	if spec.DBRole != TenantRoleName(spec.TenantID) || !tenantRolePattern.MatchString(spec.DBRole) {
		return errors.New("database role does not match immutable tenant ID")
	}
	if spec.CredentialVersion <= 0 {
		return errors.New("credential version must be positive")
	}
	return nil
}

func (p *AdminProvisioner) EnsureTenant(ctx context.Context, spec TenantSpec) error {
	if err := validateTenantSpec(spec); err != nil {
		return err
	}
	return p.withProvisioningGuard(ctx, func() error {
		return p.ensureTenant(ctx, spec)
	})
}

func (p *AdminProvisioner) ensureTenant(ctx context.Context, spec TenantSpec) error {
	password, err := p.deriver.Derive(spec.TenantID, spec.DBRole, spec.CredentialVersion)
	if err != nil {
		return err
	}
	role := pgx.Identifier{spec.DBRole}.Sanitize()
	schema := pgx.Identifier{spec.SchemaName}.Sanitize()
	if err := p.assertRegistryOperation(ctx, spec, registry.ProvisioningStateProvisioning); err != nil {
		return err
	}
	var schemaExists bool
	if err := p.admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, spec.SchemaName).Scan(&schemaExists); err != nil {
		return err
	}
	if schemaExists {
		if err := p.assertSchemaOwner(ctx, spec); err != nil {
			return err
		}
	}
	createdRole := false
	if _, err = p.admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN"); err == nil {
		createdRole = true
	} else if !sqlState(err, "42710") {
		return fmt.Errorf("create tenant role: %w", err)
	}
	if createdRole {
		if err := p.setRoleMarker(ctx, spec); err != nil {
			return err
		}
	} else if err := p.assertRoleMarker(ctx, spec); err != nil {
		return err
	}
	if err := p.assertRoleCatalog(ctx, spec.DBRole); err != nil {
		return err
	}
	if schemaExists {
		markerTx, beginErr := p.admin.Begin(ctx)
		if beginErr != nil {
			return beginErr
		}
		defer func() { _ = markerTx.Rollback(ctx) }()
		if err = withTemporaryAdminSet(ctx, markerTx, spec.DBRole, func() error {
			if _, setErr := markerTx.Exec(ctx, "SET LOCAL ROLE "+role); setErr != nil {
				return setErr
			}
			return p.assertSchemaMarker(ctx, markerTx, spec)
		}); err != nil {
			return err
		}
		if err = markerTx.Commit(ctx); err != nil {
			return err
		}
	}
	if err = p.setRolePassword(ctx, spec.DBRole, password); err != nil {
		return fmt.Errorf("set tenant credential: %w", err)
	}
	tx, err := p.admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = withTemporaryAdminSet(ctx, tx, spec.DBRole, func() error {
		if !schemaExists {
			if _, createErr := tx.Exec(ctx, "CREATE SCHEMA "+schema+" AUTHORIZATION "+role); createErr != nil {
				return fmt.Errorf("create tenant schema: %w", createErr)
			}
		}
		if _, createErr := tx.Exec(ctx, "SET LOCAL ROLE "+role); createErr != nil {
			return fmt.Errorf("enter transaction-scoped tenant owner role: %w", createErr)
		}
		if _, createErr := tx.Exec(ctx, "REVOKE ALL ON SCHEMA "+schema+" FROM PUBLIC"); createErr != nil {
			return fmt.Errorf("revoke public tenant schema access: %w", createErr)
		}
		if !schemaExists {
			if createErr := p.ensureMarker(ctx, tx, spec); createErr != nil {
				return createErr
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if err := p.assertSchemaOwner(ctx, spec); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		db, err := p.openProvisioningTenantDB(ctx, spec)
		if err != nil {
			return err
		}
		err = ensureTenantTables(ctx, db)
		db.Close()
		if err != nil {
			return fmt.Errorf("tenant table initialization pass %d: %w", i+1, err)
		}
	}
	if err := p.verifyRestrictedSchemaAndIsolation(ctx, spec); err != nil {
		return fmt.Errorf("pre-marker restricted tenant proof: %w", err)
	}
	markerDB, err := p.openProvisioningTenantDB(ctx, spec)
	if err != nil {
		return err
	}
	err = markerDB.EnsureTenantIdentity(ctx, spec.TenantID, spec.OperationID)
	markerDB.Close()
	if err != nil {
		return fmt.Errorf("ensure permanent tenant identity: %w", err)
	}
	return p.verifyTenantIdentity(ctx, spec)
}

func roleMarker(spec TenantSpec) string {
	return "health-tenant-v1:" + spec.TenantID.String() + ":" + spec.OperationID.String()
}

func (p *AdminProvisioner) setRoleMarker(ctx context.Context, spec TenantSpec) error {
	var statement string
	if err := p.admin.QueryRow(ctx, `SELECT format('COMMENT ON ROLE %I IS %L',$1::text,$2::text)`, spec.DBRole, roleMarker(spec)).Scan(&statement); err != nil {
		return err
	}
	_, err := p.admin.Exec(ctx, statement)
	return err
}

func (p *AdminProvisioner) assertRoleMarker(ctx context.Context, spec TenantSpec) error {
	var marker *string
	if err := p.admin.QueryRow(ctx, `SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=$1`, spec.DBRole).Scan(&marker); err != nil {
		return err
	}
	if marker == nil || *marker != roleMarker(spec) {
		return errors.New("existing tenant role marker does not match provisioning operation")
	}
	return nil
}

func (p *AdminProvisioner) assertRegistryOperation(ctx context.Context, spec TenantSpec, allowed ...registry.ProvisioningState) error {
	if p.registry == nil {
		return errors.New("provisioner requires registry operation proof")
	}
	op, err := p.registry.GetProvisioningOperation(ctx, spec.OperationID)
	if err != nil {
		return err
	}
	stateAllowed := false
	for _, state := range allowed {
		stateAllowed = stateAllowed || op.State == state
	}
	if op.TenantID != spec.TenantID || op.SchemaName != spec.SchemaName || op.DBRole != spec.DBRole || op.CredentialVersion != spec.CredentialVersion || !stateAllowed {
		return errors.New("registry operation does not match requested tenant provisioning")
	}
	return nil
}

func (p *AdminProvisioner) setRolePassword(ctx context.Context, role, password string) error {
	// PostgreSQL utility statements do not accept bind parameters directly.
	// Ask PostgreSQL format() to quote both values, then execute the resulting
	// catalog-safe statement on the private admin connection.
	var statement string
	if err := p.admin.QueryRow(ctx, `SELECT format('ALTER ROLE %I PASSWORD %L', $1::text, $2::text)`, role, password).Scan(&statement); err != nil {
		return err
	}
	_, err := p.admin.Exec(ctx, statement)
	return err
}

func (p *AdminProvisioner) assertRoleCatalog(ctx context.Context, role string) error {
	var login, superuser, createRole, createDB, bypassRLS bool
	var replication bool
	if err := p.admin.QueryRow(ctx, `SELECT rolcanlogin, rolsuper, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls FROM pg_roles WHERE rolname=$1`, role).Scan(&login, &superuser, &createRole, &createDB, &replication, &bypassRLS); err != nil {
		return fmt.Errorf("reconcile tenant role catalog: %w", err)
	}
	if !login || superuser || createRole || createDB || replication || bypassRLS {
		return errors.New("existing tenant role has unsafe catalog attributes")
	}
	var memberships, canonical int
	if err := p.admin.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE granted.rolname=$1 AND member.rolname='health_admin' AND m.admin_option AND NOT m.inherit_option AND NOT m.set_option) FROM pg_auth_members m JOIN pg_roles granted ON granted.oid=m.roleid JOIN pg_roles member ON member.oid=m.member WHERE m.member=(SELECT oid FROM pg_roles WHERE rolname=$1) OR m.roleid=(SELECT oid FROM pg_roles WHERE rolname=$1)`, role).Scan(&memberships, &canonical); err != nil {
		return err
	}
	if memberships != 1 || canonical != 1 {
		return errors.New("existing tenant role has unexpected role memberships")
	}
	return nil
}

func (p *AdminProvisioner) assertSchemaOwner(ctx context.Context, spec TenantSpec) error {
	var owner string
	if err := p.admin.QueryRow(ctx, `SELECT r.rolname FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname=$1`, spec.SchemaName).Scan(&owner); err != nil {
		return fmt.Errorf("reconcile tenant schema catalog: %w", err)
	}
	if owner != spec.DBRole {
		return fmt.Errorf("tenant schema owner is %q, want %q", owner, spec.DBRole)
	}
	return nil
}

type markerCatalog interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (p *AdminProvisioner) ensureMarker(ctx context.Context, catalog markerCatalog, spec TenantSpec) error {
	table := pgx.Identifier{spec.SchemaName, provisionMarkerTable}.Sanitize()
	if _, err := catalog.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+` (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), tenant_id uuid NOT NULL, operation_id uuid NOT NULL)`); err != nil {
		return fmt.Errorf("create tenant marker: %w", err)
	}
	var tenantID, operationID uuid.UUID
	err := catalog.QueryRow(ctx, "SELECT tenant_id, operation_id FROM "+table+" WHERE singleton=true").Scan(&tenantID, &operationID)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = catalog.Exec(ctx, "INSERT INTO "+table+" (tenant_id, operation_id) VALUES ($1,$2)", spec.TenantID, spec.OperationID)
		if err != nil {
			return fmt.Errorf("write tenant marker: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read tenant marker: %w", err)
	}
	if tenantID != spec.TenantID || operationID != spec.OperationID {
		return errors.New("existing schema marker does not match tenant provisioning operation")
	}
	return nil
}

func (p *AdminProvisioner) assertSchemaMarker(ctx context.Context, catalog markerCatalog, spec TenantSpec) error {
	table := pgx.Identifier{spec.SchemaName, provisionMarkerTable}.Sanitize()
	var tenantID, operationID uuid.UUID
	if err := catalog.QueryRow(ctx, "SELECT tenant_id, operation_id FROM "+table+" WHERE singleton=true").Scan(&tenantID, &operationID); err != nil {
		return fmt.Errorf("existing tenant schema has no valid provisioning marker: %w", err)
	}
	if tenantID != spec.TenantID || operationID != spec.OperationID {
		return errors.New("existing tenant schema marker does not match provisioning operation")
	}
	return nil
}

func ensureTenantTables(ctx context.Context, db *storage.DB) error {
	return db.MigrateSchemaContractContext(ctx)
}

func (p *AdminProvisioner) provisioningTenantConfig(spec TenantSpec) (*pgxpool.Config, error) {
	password, err := p.deriver.Derive(spec.TenantID, spec.DBRole, spec.CredentialVersion)
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(p.tenantBase)
	if err != nil {
		return nil, fmt.Errorf("parse tenant database base: %w", err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = spec.DBRole, password
	cfg.ConnConfig.RuntimeParams["search_path"] = spec.SchemaName
	cfg.MaxConns, cfg.MinConns = 2, 0
	return cfg, nil
}

// openProvisioningTenantPool is a short-lived pre-activation connection class.
// It authenticates as the future tenant role so provisioning proves real
// ownership and ACL isolation before the registry marks the tenant ready. It is
// never cached by Manager and never serves requests; using the registry
// connection here would bypass the proof this path is designed to perform.
func (p *AdminProvisioner) openProvisioningTenantPool(ctx context.Context, spec TenantSpec) (*pgxpool.Pool, error) {
	cfg, err := p.provisioningTenantConfig(spec)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func (p *AdminProvisioner) openProvisioningTenantDB(ctx context.Context, spec TenantSpec) (*storage.DB, error) {
	pool, err := p.openProvisioningTenantPool(ctx, spec)
	if err != nil {
		return nil, err
	}
	return storage.NewFromPool(pool), nil
}

func (p *AdminProvisioner) VerifyTenant(ctx context.Context, spec TenantSpec) error {
	if err := p.verifyRestrictedSchemaAndIsolation(ctx, spec); err != nil {
		return err
	}
	return p.verifyTenantIdentity(ctx, spec)
}

func (p *AdminProvisioner) verifyRestrictedSchemaAndIsolation(ctx context.Context, spec TenantSpec) error {
	db, err := p.openProvisioningTenantDB(ctx, spec)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.AssertIdentity(ctx, spec.DBRole, spec.SchemaName); err != nil {
		return err
	}
	if err := db.VerifyProvisionedSchema(); err != nil {
		return err
	}
	users, err := p.registry.ListActiveUsers(ctx)
	if err != nil {
		return fmt.Errorf("list existing tenants for isolation proof: %w", err)
	}
	forbidden := make([]string, 0, len(users))
	for _, user := range users {
		if user.SchemaName != spec.SchemaName {
			forbidden = append(forbidden, user.SchemaName)
		}
	}
	if err := db.VerifyTenantIsolation(ctx, forbidden...); err != nil {
		return err
	}
	return nil
}

func (p *AdminProvisioner) verifyTenantIdentity(ctx context.Context, spec TenantSpec) error {
	db, err := p.openProvisioningTenantDB(ctx, spec)
	if err != nil {
		return err
	}
	defer db.Close()
	marker, err := db.ReadTenantIdentity(ctx)
	if err != nil {
		return err
	}
	if marker.TenantID != spec.TenantID || marker.OperationID != spec.OperationID {
		return errors.New("permanent tenant identity does not match provisioning operation")
	}
	if marker.SchemaContractVersion != storage.SchemaContractVersion || marker.SchemaContractChecksum != storage.SchemaContractChecksum() {
		return errors.New("permanent tenant identity schema contract mismatch")
	}
	return nil
}

func (p *AdminProvisioner) RotateCredential(ctx context.Context, spec TenantSpec, nextVersion int) error {
	if err := validateTenantSpec(spec); err != nil {
		return err
	}
	if nextVersion <= 0 || nextVersion == spec.CredentialVersion {
		return errors.New("next credential version must be positive and different")
	}
	return ErrCredentialRotationDeferred
}

func (p *AdminProvisioner) Reconcile(ctx context.Context, operationID uuid.UUID) error {
	if p.registry == nil {
		return errors.New("provisioner has no registry reconciliation store")
	}
	return p.withProvisioningGuard(ctx, func() error {
		return p.reconcile(ctx, operationID)
	})
}

func (p *AdminProvisioner) reconcile(ctx context.Context, operationID uuid.UUID) error {
	op, err := p.registry.GetProvisioningOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if op.State == registry.ProvisioningStatePending {
		if err := p.registry.AdvanceProvisioning(ctx, operationID, registry.ProvisioningStatePending, registry.ProvisioningStateProvisioning, ""); err != nil {
			return err
		}
		op.State = registry.ProvisioningStateProvisioning
	}
	if op.State == registry.ProvisioningStateFailed {
		if err := p.registry.AdvanceProvisioning(ctx, operationID, registry.ProvisioningStateFailed, registry.ProvisioningStateProvisioning, ""); err != nil {
			return err
		}
		op.State = registry.ProvisioningStateProvisioning
	}
	if op.State != registry.ProvisioningStateProvisioning {
		return fmt.Errorf("operation %s is not reconcilable from state %q", operationID, op.State)
	}
	spec := TenantSpec{TenantID: op.TenantID, OperationID: op.OperationID, SchemaName: op.SchemaName, DBRole: op.DBRole, CredentialVersion: op.CredentialVersion}
	if err := p.ensureTenant(ctx, spec); err != nil {
		persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		persistErr := p.registry.AdvanceProvisioning(persistCtx, operationID, registry.ProvisioningStateProvisioning, registry.ProvisioningStateFailed, err.Error())
		if persistErr != nil {
			return fmt.Errorf("reconcile tenant: %v; persist failure: %w", err, persistErr)
		}
		return err
	}
	op.State = registry.ProvisioningStateProvisioning
	return p.registry.ActivateProvisioned(ctx, op, registry.SchemaContractMetadata{Version: storage.SchemaContractVersion, Checksum: storage.SchemaContractChecksum()})
}

// ReconcileNonterminal is the startup API consumed by Task 6 before tenant
// workers are exposed. It attempts every durable pending/provisioning row and
// surfaces all failures to keep readiness fail-closed.
func (p *AdminProvisioner) ReconcileNonterminal(ctx context.Context) error {
	if p.registry == nil {
		return errors.New("provisioner has no registry reconciliation store")
	}
	ops, err := p.registry.ListNonterminalProvisioningOperations(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, op := range ops {
		if err := p.Reconcile(ctx, op.OperationID); err != nil {
			failures = append(failures, fmt.Errorf("operation %s: %w", op.OperationID, err))
		}
	}
	return errors.Join(failures...)
}

// ProvisionOperation adapts the registry's durable workflow without exposing
// the administrative pool or accepting arbitrary identifiers.
func (p *AdminProvisioner) ProvisionOperation(ctx context.Context, op registry.ProvisioningOperation) error {
	return p.EnsureTenant(ctx, TenantSpec{TenantID: op.TenantID, OperationID: op.OperationID, SchemaName: op.SchemaName, DBRole: op.DBRole, CredentialVersion: op.CredentialVersion})
}

func (p *AdminProvisioner) CreateFirstTenant(ctx context.Context, req registry.CreateUserReq) (*registry.User, error) {
	if p.registry == nil {
		return nil, errors.New("provisioner has no registry")
	}
	u, op, err := p.registry.ReserveFirstUser(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.completeReserved(ctx, u, op)
}
func (p *AdminProvisioner) CreateTenant(ctx context.Context, req registry.CreateUserReq) (*registry.User, error) {
	if p.registry == nil {
		return nil, errors.New("provisioner has no registry")
	}
	u, op, err := p.registry.ReserveUser(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.completeReserved(ctx, u, op)
}
func (p *AdminProvisioner) completeReserved(ctx context.Context, u *registry.User, op registry.ProvisioningOperation) (*registry.User, error) {
	var completed *registry.User
	err := p.withProvisioningGuard(ctx, func() error {
		var err error
		completed, err = p.completeReservedWithGuard(ctx, u, op)
		return err
	})
	return completed, err
}

func (p *AdminProvisioner) completeReservedWithGuard(ctx context.Context, u *registry.User, op registry.ProvisioningOperation) (*registry.User, error) {
	if err := p.registry.AdvanceProvisioning(ctx, op.OperationID, registry.ProvisioningStatePending, registry.ProvisioningStateProvisioning, ""); err != nil {
		return nil, err
	}
	op.State = registry.ProvisioningStateProvisioning
	spec := TenantSpec{TenantID: op.TenantID, OperationID: op.OperationID, SchemaName: op.SchemaName, DBRole: op.DBRole, CredentialVersion: op.CredentialVersion}
	if err := p.ensureTenant(ctx, spec); err != nil {
		persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		persist := p.registry.AdvanceProvisioning(persistCtx, op.OperationID, registry.ProvisioningStateProvisioning, registry.ProvisioningStateFailed, err.Error())
		if persist != nil {
			return nil, fmt.Errorf("provision: %v; persist failure: %w", err, persist)
		}
		return nil, err
	}
	if err := p.VerifyTenant(ctx, spec); err != nil {
		return nil, err
	}
	if err := p.registry.ActivateProvisioned(ctx, op, registry.SchemaContractMetadata{Version: storage.SchemaContractVersion, Checksum: storage.SchemaContractChecksum()}); err != nil {
		return nil, err
	}
	u.ProvisioningState = registry.ProvisioningStateActive
	u.DBIsolationReady = true
	u.SchemaContractVersion = storage.SchemaContractVersion
	u.SchemaContractChecksum = storage.SchemaContractChecksum()
	return u, nil
}

// CleanupOwnedFixture is intentionally marker-scoped and exists for disposable
// integration fixtures. Production failure paths persist state for reconciliation.
func (p *AdminProvisioner) cleanupOwnedFixture(ctx context.Context, spec TenantSpec) error {
	if os.Getenv("HEALTH_DB_TESTS") != "1" {
		return errors.New("fixture cleanup requires HEALTH_DB_TESTS=1")
	}
	var disposable bool
	if err := p.admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM health_test_metadata WHERE key='disposable_database' AND value='true')`).Scan(&disposable); err != nil || !disposable {
		return fmt.Errorf("fixture cleanup requires disposable database marker: %w", err)
	}
	if err := p.assertSchemaOwner(ctx, spec); err != nil {
		return err
	}
	if p.registry == nil {
		return errors.New("fixture cleanup requires registry operation proof")
	}
	op, err := p.registry.GetProvisioningOperation(ctx, spec.OperationID)
	if err != nil {
		return err
	}
	if op.TenantID != spec.TenantID || op.SchemaName != spec.SchemaName || op.DBRole != spec.DBRole || (op.State != registry.ProvisioningStateProvisioning && op.State != registry.ProvisioningStateFailed && op.State != registry.ProvisioningStateActive) {
		return errors.New("fixture cleanup registry operation/state mismatch")
	}
	table := pgx.Identifier{spec.SchemaName, provisionMarkerTable}.Sanitize()
	var tenantID, operationID uuid.UUID
	if err := p.admin.QueryRow(ctx, "SELECT tenant_id, operation_id FROM "+table+" WHERE singleton=true").Scan(&tenantID, &operationID); err != nil || tenantID != spec.TenantID || operationID != spec.OperationID {
		return fmt.Errorf("fixture marker ownership mismatch: %w", err)
	}
	if _, err := p.admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{spec.SchemaName}.Sanitize()+" CASCADE"); err != nil {
		return err
	}
	_, err = p.admin.Exec(ctx, "DROP ROLE "+pgx.Identifier{spec.DBRole}.Sanitize())
	return err
}

func sqlState(err error, state string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == state
}
