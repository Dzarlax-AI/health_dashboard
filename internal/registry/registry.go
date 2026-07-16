package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUserNotFound is returned when a registry lookup matches no user.
var ErrUserNotFound = errors.New("user not found")
var ErrSetupClosed = errors.New("initial setup is closed")

var (
	usernameRE          = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)
	schemaRE            = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	reservedSchemaNames = map[string]struct{}{
		"health_registry":    {},
		"information_schema": {},
	}
)

// ErrNeedsManualSetup is returned when the database user lacks privileges to
// create the health_registry schema. The caller should log SQL and continue
// in legacy single-user mode.
type ErrNeedsManualSetup struct {
	SQL string
}

func (e *ErrNeedsManualSetup) Error() string {
	return fmt.Sprintf("insufficient privileges to create health_registry schema — run as PostgreSQL superuser:\n  %s\nThen restart the server.", e.SQL)
}

// User represents a registered health dashboard user.
type User struct {
	Username               string            `json:"username"`
	SchemaName             string            `json:"schema_name"`
	APIKey                 string            `json:"api_key"`
	PasswordHash           string            `json:"-"`
	Email                  string            `json:"email,omitempty"`
	IsAdmin                bool              `json:"is_admin"`
	CreatedAt              time.Time         `json:"created_at"`
	TenantID               uuid.UUID         `json:"tenant_id,omitempty"`
	DBRole                 string            `json:"db_role,omitempty"`
	DBCredentialVersion    int               `json:"db_credential_version,omitempty"`
	DBIsolationReady       bool              `json:"db_isolation_ready"`
	SchemaContractVersion  int               `json:"schema_contract_version,omitempty"`
	SchemaContractChecksum string            `json:"schema_contract_checksum,omitempty"`
	ProvisioningState      ProvisioningState `json:"provisioning_state"`
}

// Registry manages user accounts stored in the health_registry schema.
// All queries use fully-qualified table names so search_path doesn't matter.
type Registry struct {
	pool *pgxpool.Pool
}

// New opens a registry connection. The pool uses no fixed search_path so it
// works regardless of the role-level default.
func New(ctx context.Context, connStr string) (*Registry, error) {
	config, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	config.MaxConns = 5
	config.MinConns = 1
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Registry{pool: pool}, nil
}

// EnsureSchema creates the health_registry schema and users table if they do
// not exist. Returns *ErrNeedsManualSetup if the DB user lacks CREATE privilege.
func (r *Registry) EnsureSchema(ctx context.Context) error {
	// Check if schema already exists.
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.schemata
			WHERE schema_name = 'health_registry'
		)
	`).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check schema: %w", err)
	}

	if !exists {
		_, err = r.pool.Exec(ctx, `CREATE SCHEMA health_registry`)
		if err != nil {
			if isPermissionDenied(err) {
				return &ErrNeedsManualSetup{
					SQL: "CREATE SCHEMA health_registry AUTHORIZATION " + r.currentUser(ctx) + ";",
				}
			}
			return fmt.Errorf("create schema: %w", err)
		}
	}

	_, err = r.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS health_registry.users (
			username      TEXT PRIMARY KEY,
			schema_name   TEXT UNIQUE NOT NULL,
			api_key       TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			email         TEXT UNIQUE,
			is_admin      BOOLEAN NOT NULL DEFAULT false,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("create users table: %w", err)
	}
	// Add email column to existing installations that predate this field.
	_, _ = r.pool.Exec(ctx, `ALTER TABLE health_registry.users ADD COLUMN IF NOT EXISTS email TEXT UNIQUE`)
	if err := r.ensureProvisioningMetadata(ctx); err != nil {
		return err
	}

	// global_settings holds installation-wide config (e.g. shared Gemini API
	// key) that admins manage once for all tenants. Per-tenant overrides in
	// each schema's settings table still win — global is a fallback layered
	// on top of env defaults.
	_, err = r.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS health_registry.global_settings (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("create global_settings table: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS health_registry.sessions (
			id_hash     TEXT PRIMARY KEY,
			username    TEXT NOT NULL REFERENCES health_registry.users(username) ON DELETE CASCADE,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at  TIMESTAMPTZ NOT NULL,
			last_seen_at TIMESTAMPTZ
		)
	`)
	if err != nil {
		return fmt.Errorf("create sessions table: %w", err)
	}
	_, _ = r.pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_registry_sessions_user ON health_registry.sessions (username)`)
	_, _ = r.pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_registry_sessions_expires ON health_registry.sessions (expires_at)`)
	return nil
}

func (r *Registry) ensureProvisioningMetadata(ctx context.Context) error {
	statements := []string{
		`ALTER TABLE health_registry.users ADD COLUMN IF NOT EXISTS tenant_id UUID`,
		`ALTER TABLE health_registry.users ADD COLUMN IF NOT EXISTS db_role TEXT`,
		`ALTER TABLE health_registry.users ADD COLUMN IF NOT EXISTS db_credential_version INTEGER`,
		`ALTER TABLE health_registry.users ADD COLUMN IF NOT EXISTS db_isolation_ready BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE health_registry.users ADD COLUMN IF NOT EXISTS schema_contract_version INTEGER`,
		`ALTER TABLE health_registry.users ADD COLUMN IF NOT EXISTS schema_contract_checksum TEXT`,
		`ALTER TABLE health_registry.users ADD COLUMN IF NOT EXISTS provisioning_state TEXT NOT NULL DEFAULT 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_registry_users_tenant_id ON health_registry.users (tenant_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_registry_users_db_role ON health_registry.users (db_role)`,
		`CREATE INDEX IF NOT EXISTS idx_registry_users_provisioning_state ON health_registry.users (provisioning_state)`,
		`DO $$ BEGIN
			IF NOT EXISTS (
				SELECT 1
				FROM pg_constraint
				WHERE conname = 'registry_users_provisioning_state_check'
				  AND conrelid = 'health_registry.users'::regclass
			) THEN
				ALTER TABLE health_registry.users
				ADD CONSTRAINT registry_users_provisioning_state_check
				CHECK (provisioning_state IN ('pending', 'provisioning', 'active', 'failed'));
			END IF;
		END $$`,
		`CREATE TABLE IF NOT EXISTS health_registry.tenant_provisioning_operations (
			operation_id UUID PRIMARY KEY,
			tenant_id UUID NOT NULL,
			username TEXT NOT NULL,
			schema_name TEXT NOT NULL,
			db_role TEXT NOT NULL,
			credential_version INTEGER NOT NULL DEFAULT 1,
			state TEXT NOT NULL,
			error TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT tenant_provisioning_operations_state_check
				CHECK (state IN ('pending', 'provisioning', 'active', 'failed'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_registry_provisioning_tenant ON health_registry.tenant_provisioning_operations (tenant_id, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_registry_provisioning_state ON health_registry.tenant_provisioning_operations (state, updated_at)`,
		`ALTER TABLE health_registry.tenant_provisioning_operations ADD COLUMN IF NOT EXISTS credential_version INTEGER NOT NULL DEFAULT 1`,
	}
	for _, statement := range statements {
		if _, err := r.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("ensure provisioning metadata: %w", err)
		}
	}

	rows, err := r.pool.Query(ctx, `
		SELECT username, tenant_id
		FROM health_registry.users
		WHERE tenant_id IS NULL OR db_role IS NULL OR db_credential_version IS NULL
	`)
	if err != nil {
		return fmt.Errorf("list users missing provisioning metadata: %w", err)
	}
	type missingUser struct {
		username string
		tenantID *uuid.UUID
	}
	var missing []missingUser
	for rows.Next() {
		var user missingUser
		if err := rows.Scan(&user.username, &user.tenantID); err != nil {
			rows.Close()
			return fmt.Errorf("scan user provisioning metadata: %w", err)
		}
		missing = append(missing, user)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("iterate user provisioning metadata: %w", err)
	}
	for _, user := range missing {
		id := uuid.New()
		if user.tenantID != nil {
			id = *user.tenantID
		}
		if _, err := r.pool.Exec(ctx, `
			UPDATE health_registry.users
			SET tenant_id = COALESCE(tenant_id, $2),
			    db_role = COALESCE(db_role, $3),
			    db_credential_version = COALESCE(db_credential_version, 1)
			WHERE username = $1
		`, user.username, id, tenantDBRole(id)); err != nil {
			return fmt.Errorf("backfill provisioning metadata for %q: %w", user.username, err)
		}
	}
	rows, err = r.pool.Query(ctx, `
		SELECT username, tenant_id, db_role, db_credential_version
		FROM health_registry.users
		ORDER BY username
	`)
	if err != nil {
		return fmt.Errorf("validate user provisioning metadata: %w", err)
	}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.Username, &u.TenantID, &u.DBRole, &u.DBCredentialVersion); err != nil {
			rows.Close()
			return fmt.Errorf("scan required provisioning metadata: %w", err)
		}
		if err := validateUserProvisioningMetadata(&u); err != nil {
			rows.Close()
			return fmt.Errorf("validate provisioning metadata for %q: %w", u.Username, err)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("iterate required provisioning metadata: %w", err)
	}
	for _, statement := range []string{
		`ALTER TABLE health_registry.users ALTER COLUMN tenant_id SET NOT NULL`,
		`ALTER TABLE health_registry.users ALTER COLUMN db_role SET NOT NULL`,
		`ALTER TABLE health_registry.users ALTER COLUMN db_credential_version SET NOT NULL`,
		`ALTER TABLE health_registry.users ALTER COLUMN provisioning_state SET NOT NULL`,
	} {
		if _, err := r.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("enforce required provisioning metadata: %w", err)
		}
	}
	return nil
}

func tenantDBRole(id uuid.UUID) string {
	return "health_t_" + strings.ReplaceAll(id.String(), "-", "")
}

func validateUserProvisioningMetadata(u *User) error {
	if u.TenantID == uuid.Nil {
		return errors.New("tenant ID is missing or invalid")
	}
	if u.DBRole == "" || u.DBRole != tenantDBRole(u.TenantID) {
		return errors.New("database role does not match immutable tenant ID")
	}
	if u.DBCredentialVersion <= 0 {
		return errors.New("database credential version must be positive")
	}
	return nil
}

// GetGlobalSetting returns the stored value for `key`, or "" when unset.
func (r *Registry) GetGlobalSetting(ctx context.Context, key string) string {
	var v string
	_ = r.pool.QueryRow(ctx,
		`SELECT value FROM health_registry.global_settings WHERE key = $1`, key,
	).Scan(&v)
	return v
}

// GetAllGlobalSettings returns every key/value pair from global_settings.
func (r *Registry) GetAllGlobalSettings(ctx context.Context) map[string]string {
	out := make(map[string]string)
	rows, err := r.pool.Query(ctx, `SELECT key, value FROM health_registry.global_settings`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			out[k] = v
		}
	}
	return out
}

// SaveGlobalSettings upserts the supplied keys atomically.
func (r *Registry) SaveGlobalSettings(ctx context.Context, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for k, v := range kv {
		if _, err := tx.Exec(ctx, `
			INSERT INTO health_registry.global_settings (key, value, updated_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (key) DO UPDATE
			SET value = EXCLUDED.value, updated_at = NOW()
		`, k, v); err != nil {
			return fmt.Errorf("save %q: %w", k, err)
		}
	}
	return tx.Commit(ctx)
}

// IsEmpty reports whether the registry has no user reservation in any state.
// Pending and failed rows keep setup closed until Task 3 reconciliation.
func (r *Registry) IsEmpty(ctx context.Context) bool {
	exists := true // fail closed: a registry read error must never reopen setup
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM health_registry.users LIMIT 1)`).Scan(&exists); err != nil {
		return false
	}
	return !exists
}

func (r *Registry) HasActiveUsers(ctx context.Context) bool {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM health_registry.users WHERE provisioning_state = 'active' LIMIT 1)`).Scan(&exists); err != nil {
		return false
	}
	return exists
}

func (r *Registry) hasAnyUser(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM health_registry.users LIMIT 1)`).Scan(&exists)
	return exists, err
}

// DetectLegacyInstall returns true when a health schema with metric_points
// exists but no users are registered — i.e., an upgrade from single-user mode.
func (r *Registry) DetectLegacyInstall(ctx context.Context) bool {
	var exists bool
	r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'health' AND table_name = 'metric_points'
		)
	`).Scan(&exists)
	return exists && r.IsEmpty(ctx)
}

// GetByAPIKey looks up a user by their API key.
func (r *Registry) GetByAPIKey(ctx context.Context, key string) (*User, error) {
	return r.getUser(ctx, `
		SELECT username, schema_name, api_key, password_hash, email, is_admin, created_at,
		       tenant_id, db_role, db_credential_version, db_isolation_ready, schema_contract_version, schema_contract_checksum, provisioning_state
		FROM health_registry.users WHERE api_key = $1 AND provisioning_state = 'active'
	`, key)
}

// GetByUsername looks up a user by username.
func (r *Registry) GetByUsername(ctx context.Context, username string) (*User, error) {
	return r.getUser(ctx, `
		SELECT username, schema_name, api_key, password_hash, email, is_admin, created_at,
		       tenant_id, db_role, db_credential_version, db_isolation_ready, schema_contract_version, schema_contract_checksum, provisioning_state
		FROM health_registry.users WHERE username = $1 AND provisioning_state = 'active'
	`, username)
}

// GetByEmail looks up a user by email address.
func (r *Registry) GetByEmail(ctx context.Context, email string) (*User, error) {
	return r.getUser(ctx, `
		SELECT username, schema_name, api_key, password_hash, email, is_admin, created_at,
		       tenant_id, db_role, db_credential_version, db_isolation_ready, schema_contract_version, schema_contract_checksum, provisioning_state
		FROM health_registry.users WHERE email = $1 AND provisioning_state = 'active'
	`, email)
}

// GetBySchema looks up a user by their tenant schema name. Used by request
// handlers that have already resolved a tenant schema and want the owning
// user's identity (username) without re-running the original auth lookup.
func (r *Registry) GetBySchema(ctx context.Context, schema string) (*User, error) {
	return r.getUser(ctx, `
		SELECT username, schema_name, api_key, password_hash, email, is_admin, created_at,
		       tenant_id, db_role, db_credential_version, db_isolation_ready, schema_contract_version, schema_contract_checksum, provisioning_state
		FROM health_registry.users WHERE schema_name = $1 AND provisioning_state = 'active'
	`, schema)
}

func (r *Registry) getUser(ctx context.Context, query string, arg string) (*User, error) {
	var u User
	var email *string
	var tenantID *uuid.UUID
	var dbRole *string
	var credentialVersion *int
	var contractVersion *int
	var contractChecksum *string
	err := r.pool.QueryRow(ctx, query, arg).Scan(
		&u.Username, &u.SchemaName, &u.APIKey, &u.PasswordHash, &email, &u.IsAdmin, &u.CreatedAt,
		&tenantID, &dbRole, &credentialVersion, &u.DBIsolationReady, &contractVersion, &contractChecksum, &u.ProvisioningState,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if email != nil {
		u.Email = *email
	}
	if tenantID != nil {
		u.TenantID = *tenantID
	}
	if dbRole != nil {
		u.DBRole = *dbRole
	}
	if credentialVersion != nil {
		u.DBCredentialVersion = *credentialVersion
	}
	if contractVersion != nil {
		u.SchemaContractVersion = *contractVersion
	}
	if contractChecksum != nil {
		u.SchemaContractChecksum = *contractChecksum
	}
	if err := validateUserProvisioningMetadata(&u); err != nil {
		return nil, fmt.Errorf("invalid provisioning metadata for user %q: %w", u.Username, err)
	}
	return &u, nil
}

// ListUsers is the administrative inventory and intentionally includes
// non-active users. Routing callers must use ListActiveUsers or GetBySchema.
func (r *Registry) ListUsers(ctx context.Context) ([]User, error) {
	return r.listUsers(ctx, "")
}

func (r *Registry) ListActiveUsers(ctx context.Context) ([]User, error) {
	return r.listUsers(ctx, " WHERE provisioning_state = 'active'")
}

func (r *Registry) listUsers(ctx context.Context, where string) ([]User, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT username, schema_name, api_key, password_hash, email, is_admin, created_at,
		       tenant_id, db_role, db_credential_version, db_isolation_ready, schema_contract_version, schema_contract_checksum, provisioning_state
		FROM health_registry.users`+where+` ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var email *string
		var tenantID *uuid.UUID
		var dbRole *string
		var credentialVersion *int
		var contractVersion *int
		var contractChecksum *string
		if err := rows.Scan(&u.Username, &u.SchemaName, &u.APIKey, &u.PasswordHash, &email, &u.IsAdmin, &u.CreatedAt, &tenantID, &dbRole, &credentialVersion, &u.DBIsolationReady, &contractVersion, &contractChecksum, &u.ProvisioningState); err != nil {
			return nil, err
		}
		if email != nil {
			u.Email = *email
		}
		if tenantID != nil {
			u.TenantID = *tenantID
		}
		if dbRole != nil {
			u.DBRole = *dbRole
		}
		if credentialVersion != nil {
			u.DBCredentialVersion = *credentialVersion
		}
		if contractVersion != nil {
			u.SchemaContractVersion = *contractVersion
		}
		if contractChecksum != nil {
			u.SchemaContractChecksum = *contractChecksum
		}
		if err := validateUserProvisioningMetadata(&u); err != nil {
			return nil, fmt.Errorf("invalid provisioning metadata for user %q: %w", u.Username, err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CreateUserReq holds parameters for creating a new user.
type CreateUserReq struct {
	Username      string
	SchemaName    string // derived from username if empty
	Password      string
	Email         string // optional
	IsAdmin       bool
	InitialAPIKey string `json:"-"` // trusted startup compatibility only
}

// ValidateUsername enforces the registry username policy. Usernames become
// part of derived tenant schema names, so keep the accepted set narrow and
// predictable even though dynamic SQL identifiers are quoted separately.
func ValidateUsername(username string) error {
	if !usernameRE.MatchString(username) {
		return fmt.Errorf("username must match %s", usernameRE.String())
	}
	return nil
}

// ValidateSchemaName enforces a PostgreSQL-safe tenant schema name shape.
// This is a policy check; dynamic SQL still quotes identifiers separately.
func ValidateSchemaName(schema string) error {
	if !schemaRE.MatchString(schema) {
		return fmt.Errorf("schema_name must match %s", schemaRE.String())
	}
	if strings.HasPrefix(schema, "pg_") {
		return fmt.Errorf("schema_name %q is reserved", schema)
	}
	if _, ok := reservedSchemaNames[schema]; ok {
		return fmt.Errorf("schema_name %q is reserved", schema)
	}
	return nil
}

// CreateUser inserts a new user. Generates an API key automatically.
// Returns the created user (with APIKey populated).
func (r *Registry) CreateLegacyUser(ctx context.Context, req CreateUserReq) (*User, error) {
	u, email, err := prepareUser(req)
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, FleetMigrationAdvisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire legacy reservation fleet lock: %w", err)
	}
	if _, err = insertPreparedUser(ctx, u, email, tx); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return u, nil
}
func (r *Registry) CreateLegacyFirstUser(ctx context.Context, req CreateUserReq) (*User, error) {
	u, email, err := prepareUser(req)
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, FleetMigrationAdvisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire legacy first-user reservation fleet lock: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(918273645)`); err != nil {
		return nil, err
	}
	exists, err := r.hasAnyUser(ctx, tx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrSetupClosed
	}
	if _, err = insertPreparedUser(ctx, u, email, tx); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return u, nil
}

// ActivateLegacyReservation is explicit shared-login compatibility. It does
// not satisfy isolated activation and is never used by AdminProvisioner.
func (r *Registry) ActivateLegacyReservation(ctx context.Context, op ProvisioningOperation) error {
	current, err := r.GetProvisioningOperation(ctx, op.OperationID)
	if err != nil {
		return err
	}
	if current.TenantID != op.TenantID || current.Username != op.Username || current.SchemaName != op.SchemaName || current.DBRole != op.DBRole || current.CredentialVersion != op.CredentialVersion {
		return ErrProvisioningStateConflict
	}
	if current.State == ProvisioningStateActive {
		return nil
	}
	if current.State == ProvisioningStatePending {
		if err := r.transitionUserAndOperation(ctx, op.OperationID, ProvisioningStatePending, ProvisioningStateProvisioning, ""); err != nil {
			return err
		}
		current.State = ProvisioningStateProvisioning
	}
	if current.State != ProvisioningStateProvisioning {
		return ErrProvisioningStateConflict
	}
	return r.transitionUserAndOperation(ctx, op.OperationID, ProvisioningStateProvisioning, ProvisioningStateActive, "")
}

func (r *Registry) ReserveUser(ctx context.Context, req CreateUserReq) (*User, ProvisioningOperation, error) {
	u, email, err := prepareUser(req)
	if err != nil {
		return nil, ProvisioningOperation{}, err
	}
	u.ProvisioningState = ProvisioningStatePending
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, ProvisioningOperation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, FleetMigrationAdvisoryLockKey); err != nil {
		return nil, ProvisioningOperation{}, fmt.Errorf("acquire reservation fleet lock: %w", err)
	}
	if _, err := insertPreparedUser(ctx, u, email, tx); err != nil {
		return nil, ProvisioningOperation{}, err
	}
	op := ProvisioningOperation{OperationID: uuid.New(), TenantID: u.TenantID, Username: u.Username, SchemaName: u.SchemaName, DBRole: u.DBRole, CredentialVersion: u.DBCredentialVersion, State: ProvisioningStatePending}
	if _, err := tx.Exec(ctx, `INSERT INTO health_registry.tenant_provisioning_operations(operation_id,tenant_id,username,schema_name,db_role,credential_version,state) VALUES($1,$2,$3,$4,$5,$6,'pending')`, op.OperationID, op.TenantID, op.Username, op.SchemaName, op.DBRole, op.CredentialVersion); err != nil {
		return nil, ProvisioningOperation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		readCtx, cancel := detachedRegistryContext()
		defer cancel()
		if reread, readErr := r.GetProvisioningOperation(readCtx, op.OperationID); readErr != nil || reread.State != ProvisioningStatePending {
			return nil, ProvisioningOperation{}, fmt.Errorf("commit user reservation: %w", err)
		}
	}
	return u, op, nil
}

type userExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func prepareUser(req CreateUserReq) (*User, *string, error) {
	req.Username = strings.TrimSpace(req.Username)
	req.SchemaName = strings.TrimSpace(req.SchemaName)
	if err := ValidateUsername(req.Username); err != nil {
		return nil, nil, err
	}
	if req.SchemaName == "" {
		req.SchemaName = "health_" + strings.ToLower(req.Username)
	}
	if err := ValidateSchemaName(req.SchemaName); err != nil {
		return nil, nil, err
	}
	apiKey := req.InitialAPIKey
	if apiKey == "" {
		var err error
		apiKey, err = generateAPIKey()
		if err != nil {
			return nil, nil, fmt.Errorf("generate api key: %w", err)
		}
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		return nil, nil, fmt.Errorf("hash password: %w", err)
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email != "" {
		parsed, err := mail.ParseAddress(req.Email)
		if err != nil || parsed.Address != req.Email || len(req.Email) > 320 {
			return nil, nil, fmt.Errorf("invalid email address")
		}
	}

	var emailPtr *string
	if req.Email != "" {
		emailPtr = &req.Email
	}

	u := &User{
		Username:            req.Username,
		SchemaName:          req.SchemaName,
		APIKey:              apiKey,
		PasswordHash:        hash,
		Email:               req.Email,
		IsAdmin:             req.IsAdmin,
		CreatedAt:           time.Now(),
		TenantID:            uuid.New(),
		DBCredentialVersion: 1,
		ProvisioningState:   ProvisioningStateActive,
	}
	u.DBRole = tenantDBRole(u.TenantID)
	return u, emailPtr, nil
}
func insertPreparedUser(ctx context.Context, u *User, emailPtr *string, x userExecer) (*User, error) {
	_, err := x.Exec(ctx, `
		INSERT INTO health_registry.users
			(username, schema_name, api_key, password_hash, email, is_admin, tenant_id, db_role, db_credential_version, provisioning_state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, u.Username, u.SchemaName, u.APIKey, u.PasswordHash, emailPtr, u.IsAdmin, u.TenantID, u.DBRole, u.DBCredentialVersion, u.ProvisioningState)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// ReserveFirstUser serializes and durably reserves the first administrator.
// It never provisions or activates a tenant; that authority belongs to the
// tenant provisioning service.
func (r *Registry) ReserveFirstUser(ctx context.Context, req CreateUserReq) (*User, ProvisioningOperation, error) {
	u, email, err := prepareUser(req)
	if err != nil {
		return nil, ProvisioningOperation{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, ProvisioningOperation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, FleetMigrationAdvisoryLockKey); err != nil {
		return nil, ProvisioningOperation{}, fmt.Errorf("acquire first-user reservation fleet lock: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(918273645)`); err != nil {
		return nil, ProvisioningOperation{}, err
	}
	exists, err := r.hasAnyUser(ctx, tx)
	if err != nil {
		return nil, ProvisioningOperation{}, err
	}
	if exists {
		return nil, ProvisioningOperation{}, ErrSetupClosed
	}
	u.ProvisioningState = ProvisioningStatePending
	u, err = insertPreparedUser(ctx, u, email, tx)
	if err != nil {
		return nil, ProvisioningOperation{}, err
	}
	op := ProvisioningOperation{OperationID: uuid.New(), TenantID: u.TenantID, Username: u.Username, SchemaName: u.SchemaName, DBRole: u.DBRole, CredentialVersion: u.DBCredentialVersion, State: ProvisioningStatePending}
	if _, err = tx.Exec(ctx, `INSERT INTO health_registry.tenant_provisioning_operations(operation_id,tenant_id,username,schema_name,db_role,credential_version,state) VALUES($1,$2,$3,$4,$5,$6,'pending')`, op.OperationID, op.TenantID, op.Username, op.SchemaName, op.DBRole, op.CredentialVersion); err != nil {
		return nil, ProvisioningOperation{}, fmt.Errorf("reserve provisioning operation: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		// A transport error is ambiguous. Durable state is authoritative.
		readCtx, cancel := detachedRegistryContext()
		defer cancel()
		if reread, readErr := r.GetProvisioningOperation(readCtx, op.OperationID); readErr != nil || reread.State != ProvisioningStatePending {
			return nil, ProvisioningOperation{}, fmt.Errorf("commit first-user reservation: %w", err)
		}
	}
	return u, op, nil
}

// MigrateFromEnv creates the first admin user from env-var credentials.
// Used when upgrading from single-user mode or seeding from docker-compose env.
func (r *Registry) MigrateFromEnv(ctx context.Context, apiKey, passwordHash, schemaName, email string) error {
	if apiKey == "" {
		var err error
		apiKey, err = generateAPIKey()
		if err != nil {
			return err
		}
	}
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}
	tenantID := uuid.New()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, FleetMigrationAdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire environment migration fleet lock: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO health_registry.users
			(username, schema_name, api_key, password_hash, email, is_admin, tenant_id, db_role, db_credential_version, provisioning_state)
		VALUES ('admin', $1, $2, $3, $4, true, $5, $6, 1, 'active')
		ON CONFLICT (username) DO UPDATE SET email = EXCLUDED.email WHERE health_registry.users.email IS NULL
	`, schemaName, apiKey, passwordHash, emailPtr, tenantID, tenantDBRole(tenantID))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteUser removes a user by username. Does not drop their schema.
func (r *Registry) DeleteUser(ctx context.Context, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM health_registry.users WHERE username = $1`, username)
	return err
}

// UpdatePasswordHash replaces a user's stored password hash after a verified
// legacy login or an explicit password reset.
func (r *Registry) UpdatePasswordHash(ctx context.Context, username, passwordHash string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE health_registry.users SET password_hash = $2 WHERE username = $1
	`, username, passwordHash)
	return err
}

// Close releases the connection pool.
func (r *Registry) Close() {
	r.pool.Close()
}

func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (r *Registry) currentUser(ctx context.Context) string {
	var u string
	if err := r.pool.QueryRow(ctx, `SELECT current_user`).Scan(&u); err != nil {
		return "current_user"
	}
	return u
}

func isPermissionDenied(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42501" // insufficient_privilege
	}
	return false
}
