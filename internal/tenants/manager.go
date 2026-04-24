package tenants

import (
	"context"
	"fmt"
	"sync"

	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

// TenantCallbacks holds per-tenant lifecycle functions registered by main.
type TenantCallbacks struct {
	Backfill       func(force bool)
	TestNotify     func(kind string) error
	NotifyDefaults storage.NotifyConfig
	AIDefaults     storage.AIConfig
}

type entry struct {
	db        *storage.DB
	callbacks *TenantCallbacks
}

// Manager holds one DB pool per tenant schema and routes requests by API key
// or username. Tenant pools are created lazily on first access.
type Manager struct {
	reg     *registry.Registry
	connStr string
	mu      sync.RWMutex
	tenants map[string]*entry // schema_name → entry

	// legacyMode is set when health_registry could not be created.
	// In this mode a single fallback DB is used for all requests.
	legacyMode bool
	legacyDB   *storage.DB
	legacyKey  string // API_KEY env value
	legacyHash string // sha256(UI_PASSWORD) env value
}

// New creates a Manager backed by the given Registry.
func New(reg *registry.Registry, connStr string) *Manager {
	return &Manager{
		reg:     reg,
		connStr: connStr,
		tenants: make(map[string]*entry),
	}
}

// SetLegacyMode configures single-user fallback using env-var credentials.
func (m *Manager) SetLegacyMode(db *storage.DB, apiKey, passwordHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.legacyMode = true
	m.legacyDB = db
	m.legacyKey = apiKey
	m.legacyHash = passwordHash
}

// LegacyMode reports whether the server is running in single-user fallback mode.
func (m *Manager) LegacyMode() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.legacyMode
}

// LegacyDB returns the fallback DB (only valid in legacy mode).
func (m *Manager) LegacyDB() *storage.DB {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.legacyDB
}

// LegacyAPIKey returns the fallback API key (only valid in legacy mode).
func (m *Manager) LegacyAPIKey() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.legacyKey
}

// LegacyPasswordHash returns the fallback password hash (only valid in legacy mode).
func (m *Manager) LegacyPasswordHash() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.legacyHash
}

// RegisterCallbacks attaches per-tenant operational callbacks after the tenant
// DB and schedulers have been set up in main.
func (m *Manager) RegisterCallbacks(schema string, cb TenantCallbacks) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.tenants[schema]; ok {
		e.callbacks = &cb
	}
}

// GetOrCreate returns the DB for schema, creating the pool on first call.
func (m *Manager) GetOrCreate(ctx context.Context, schema string) (*storage.DB, error) {
	m.mu.RLock()
	if e, ok := m.tenants[schema]; ok {
		m.mu.RUnlock()
		return e.db, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.tenants[schema]; ok {
		return e.db, nil
	}
	db, err := storage.NewWithSchema(ctx, m.connStr, schema)
	if err != nil {
		return nil, fmt.Errorf("open pool for schema %s: %w", schema, err)
	}
	m.tenants[schema] = &entry{db: db}
	return db, nil
}

// DBForAPIKey looks up a tenant by API key and returns their DB.
func (m *Manager) DBForAPIKey(ctx context.Context, key string) (*storage.DB, string, bool) {
	if m.LegacyMode() {
		if key == m.LegacyAPIKey() {
			return m.LegacyDB(), "health", true
		}
		return nil, "", false
	}
	user, err := m.reg.GetByAPIKey(ctx, key)
	if err != nil {
		return nil, "", false
	}
	db, err := m.GetOrCreate(ctx, user.SchemaName)
	if err != nil {
		return nil, "", false
	}
	return db, user.SchemaName, true
}

// DBForUsername looks up a tenant by username and returns their DB.
func (m *Manager) DBForUsername(ctx context.Context, username string) (*storage.DB, string, bool) {
	if m.LegacyMode() {
		if username == "admin" {
			return m.LegacyDB(), "health", true
		}
		return nil, "", false
	}
	user, err := m.reg.GetByUsername(ctx, username)
	if err != nil {
		return nil, "", false
	}
	db, err := m.GetOrCreate(ctx, user.SchemaName)
	if err != nil {
		return nil, "", false
	}
	return db, user.SchemaName, true
}

// DBForEmail looks up a tenant by email address and returns their DB.
func (m *Manager) DBForEmail(ctx context.Context, email string) (*storage.DB, string, bool) {
	if m.LegacyMode() {
		return nil, "", false
	}
	user, err := m.reg.GetByEmail(ctx, email)
	if err != nil {
		return nil, "", false
	}
	db, err := m.GetOrCreate(ctx, user.SchemaName)
	if err != nil {
		return nil, "", false
	}
	return db, user.SchemaName, true
}

// DBForSoleUser returns the DB for the only registered user.
// Used as a fallback when TRUST_FORWARD_AUTH=true and the Authentik username
// does not match any registered username (e.g., after migration from env vars
// where the user was created with username 'admin'). Only succeeds when exactly
// 1 user is registered — multi-user installs must have matching usernames.
func (m *Manager) DBForSoleUser(ctx context.Context) (*storage.DB, string, bool) {
	users, err := m.reg.ListUsers(ctx)
	if err != nil || len(users) != 1 {
		return nil, "", false
	}
	db, err := m.GetOrCreate(ctx, users[0].SchemaName)
	if err != nil {
		return nil, "", false
	}
	return db, users[0].SchemaName, true
}

// BackfillFor returns the backfill trigger for a schema, or nil.
func (m *Manager) BackfillFor(schema string) func(bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.tenants[schema]; ok && e.callbacks != nil {
		return e.callbacks.Backfill
	}
	return nil
}

// TestNotifyFor returns the test-notify function for a schema, or nil.
func (m *Manager) TestNotifyFor(schema string) func(string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.tenants[schema]; ok && e.callbacks != nil {
		return e.callbacks.TestNotify
	}
	return nil
}

// NotifyDefaultsFor returns the notify config defaults for a schema.
func (m *Manager) NotifyDefaultsFor(schema string) storage.NotifyConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.tenants[schema]; ok && e.callbacks != nil {
		return e.callbacks.NotifyDefaults
	}
	return storage.NotifyConfig{}
}

// AIDefaultsFor returns the AI config defaults for a schema.
func (m *Manager) AIDefaultsFor(schema string) storage.AIConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.tenants[schema]; ok && e.callbacks != nil {
		return e.callbacks.AIDefaults
	}
	return storage.AIConfig{}
}

// CreateUserSchema creates a new PostgreSQL schema and initialises all tables.
// Returns *registry.ErrNeedsManualSetup if CREATE SCHEMA fails due to permissions.
func (m *Manager) CreateUserSchema(ctx context.Context, schemaName string) error {
	// Use the registry pool to create the schema (same DB user, may fail on restricted setups).
	// We access it via a raw query through any existing tenant pool or registry.
	// For simplicity, attempt through an existing tenant's pool (same user).
	m.mu.RLock()
	var anyDB *storage.DB
	for _, e := range m.tenants {
		anyDB = e.db
		break
	}
	m.mu.RUnlock()

	if anyDB == nil && m.legacyDB != nil {
		anyDB = m.legacyDB
	}

	if anyDB != nil {
		if err := anyDB.CreateSchema(ctx, schemaName); err != nil {
			return err
		}
	}

	db, err := m.GetOrCreate(ctx, schemaName)
	if err != nil {
		return err
	}
	if err := db.EnsureAllTables(); err != nil {
		return fmt.Errorf("init tables for %s: %w", schemaName, err)
	}
	db.EnsureIndexes()
	db.EnsureAIBriefingsTable()
	return nil
}

// AllDBs returns a snapshot of all registered schema→DB pairs.
func (m *Manager) AllDBs() map[string]*storage.DB {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]*storage.DB, len(m.tenants))
	for schema, e := range m.tenants {
		out[schema] = e.db
	}
	return out
}

// Close shuts down all tenant DB pools.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.tenants {
		e.db.Close()
	}
}
