package tenants

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

// TenantCallbacks holds per-tenant lifecycle functions registered by main.
type TenantCallbacks struct {
	Backfill      func(force bool)
	BackfillDates func(dates []string)
	// MorningTrigger is the opportunistic ingest-driven morning-report
	// trigger for this tenant. Called by the shared mux's onNewData hook
	// when fresh health data lands so the report can fire earlier than
	// the scheduled morning hour once SleepSettled. nil means the tenant
	// does not participate in ingest-driven sends (e.g. legacy single-
	// user mode wires the call directly without going through callbacks).
	MorningTrigger func()
	// MorningSendMu serialises the "check HasSentMorningReport → send →
	// MarkMorningReportSent" critical section across the scheduler loop
	// and the ingest-driven trigger. Without it the two paths can each
	// observe HasSent=false within the same narrow window and produce
	// duplicate Telegram messages (TOCTOU). nil means single-user legacy
	// mode where only one sender path exists.
	MorningSendMu  *sync.Mutex
	TestNotify     func(kind string) error
	NotifyDefaults storage.NotifyConfig
	AIDefaults     storage.AIConfig
}

type entry struct {
	db                *storage.DB
	callbacks         *TenantCallbacks
	tenantID          uuid.UUID
	dbRole            string
	credentialVersion int
	schemaName        string
}

// Manager holds one DB pool per tenant schema and routes requests by API key
// or username. Tenant pools are created lazily on first access.
type Manager struct {
	reg              managerRegistry
	connStr          string
	metadata         tenantMetadataLoader
	isolationEnabled bool
	deriver          CredentialDeriver
	openRestricted   restrictedPoolOpener
	assertIdentity   func(context.Context, *storage.DB, string, string) error
	closeDB          func(*storage.DB)
	mu               sync.RWMutex
	tenants          map[string]*entry // schema_name → entry

	// legacyMode is set when health_registry could not be created.
	// In this mode a single fallback DB is used for all requests.
	legacyMode bool
	legacyDB   *storage.DB
	legacyKey  string // API_KEY env value
	legacyHash string // sha256(UI_PASSWORD) env value
}

var (
	ErrIsolationMode         = errors.New("tenant manager isolation mode cannot be downgraded to legacy shared mode")
	ErrTenantMetadataChanged = errors.New("tenant registry metadata changed while opening pool; retry")
)

type restrictedPoolOpener func(context.Context, string, string, string, string) (*storage.DB, error)

type tenantMetadataLoader interface {
	GetBySchema(context.Context, string) (*registry.User, error)
}

type managerRegistry interface {
	tenantMetadataLoader
	GetByAPIKey(context.Context, string) (*registry.User, error)
	GetByUsername(context.Context, string) (*registry.User, error)
	GetByEmail(context.Context, string) (*registry.User, error)
	GetAllGlobalSettings(context.Context) map[string]string
}

// New creates a Manager backed by the given Registry.
func New(reg *registry.Registry, connStr string) *Manager {
	return &Manager{
		reg:      reg,
		connStr:  connStr,
		metadata: reg,
		tenants:  make(map[string]*entry),
	}
}

// NewIsolated creates a Manager that can open only active, metadata-backed
// tenant pools from a credential-free DSN base.
func NewIsolated(metadata managerRegistry, tenantDSNBase string, deriver CredentialDeriver) (*Manager, error) {
	if metadata == nil {
		return nil, fmt.Errorf("tenant metadata loader is required")
	}
	if err := validateTenantDSNBase(tenantDSNBase); err != nil {
		return nil, fmt.Errorf("tenant DSN base: %w", err)
	}
	if err := deriver.validate(); err != nil {
		return nil, err
	}
	m := &Manager{
		reg: metadata, metadata: metadata, connStr: tenantDSNBase,
		isolationEnabled: true, deriver: deriver,
		openRestricted: storage.NewRestrictedTenant,
		assertIdentity: func(ctx context.Context, db *storage.DB, role, schema string) error {
			return db.AssertIdentity(ctx, role, schema)
		},
		closeDB: func(db *storage.DB) { db.Close() },
		tenants: make(map[string]*entry),
	}
	return m, nil
}

// SetLegacyMode configures single-user fallback using env-var credentials.
func (m *Manager) SetLegacyMode(db *storage.DB, apiKey, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.isolationEnabled {
		return ErrIsolationMode
	}
	m.legacyMode = true
	m.legacyDB = db
	m.legacyKey = apiKey
	m.legacyHash = passwordHash
	return nil
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.tenants[schema]; ok {
		if !m.isolationEnabled {
			return e.db, nil
		}
		current, err := m.loadTenantIdentity(ctx, schema)
		if err == nil && current.matchesEntry(e) {
			return e.db, nil
		}
		delete(m.tenants, schema)
		m.closeDB(e.db)
		if err != nil {
			return nil, err
		}
		return nil, ErrTenantMetadataChanged
	}
	var db *storage.DB
	var err error
	if m.isolationEnabled {
		original, lookupErr := m.loadTenantIdentity(ctx, schema)
		if lookupErr != nil {
			return nil, lookupErr
		}
		password, deriveErr := m.deriver.Derive(original.tenantID, original.dbRole, original.credentialVersion)
		if deriveErr != nil {
			return nil, fmt.Errorf("derive tenant credential for schema %s: %w", schema, deriveErr)
		}
		db, err = m.openRestricted(ctx, m.connStr, original.dbRole, password, original.schemaName)
		if err == nil {
			err = m.assertIdentity(ctx, db, original.dbRole, original.schemaName)
		}
		if err != nil && db != nil {
			m.closeDB(db)
		}
		if err == nil {
			current, refreshErr := m.loadTenantIdentity(ctx, schema)
			if refreshErr != nil || current != original {
				m.closeDB(db)
				if refreshErr != nil {
					return nil, fmt.Errorf("%w: %v", ErrTenantMetadataChanged, refreshErr)
				}
				return nil, ErrTenantMetadataChanged
			}
			m.tenants[schema] = original.entry(db)
			return db, nil
		}
	} else {
		db, err = storage.NewWithSchema(ctx, m.connStr, schema)
	}
	if err != nil {
		return nil, fmt.Errorf("open pool for schema %s: %w", schema, err)
	}
	m.tenants[schema] = &entry{db: db}
	return db, nil
}

type tenantIdentity struct {
	schemaName        string
	tenantID          uuid.UUID
	dbRole            string
	credentialVersion int
}

func (m *Manager) loadTenantIdentity(ctx context.Context, schema string) (tenantIdentity, error) {
	user, err := m.metadata.GetBySchema(ctx, schema)
	if err != nil {
		return tenantIdentity{}, fmt.Errorf("load active tenant metadata for schema %s: %w", schema, err)
	}
	if user.ProvisioningState != registry.ProvisioningStateActive || !user.DBIsolationReady || user.TenantID == uuid.Nil || user.SchemaName != schema || user.DBRole != TenantRoleName(user.TenantID) || user.DBCredentialVersion <= 0 {
		return tenantIdentity{}, fmt.Errorf("active tenant metadata for schema %s is incomplete or inconsistent", schema)
	}
	return tenantIdentity{schemaName: user.SchemaName, tenantID: user.TenantID, dbRole: user.DBRole, credentialVersion: user.DBCredentialVersion}, nil
}

func (i tenantIdentity) entry(db *storage.DB) *entry {
	return &entry{db: db, schemaName: i.schemaName, tenantID: i.tenantID, dbRole: i.dbRole, credentialVersion: i.credentialVersion}
}

func (i tenantIdentity) matchesEntry(e *entry) bool {
	return e != nil && i.schemaName == e.schemaName && i.tenantID == e.tenantID && i.dbRole == e.dbRole && i.credentialVersion == e.credentialVersion
}

// DBForAPIKey looks up a tenant by API key and returns their DB.
func (m *Manager) DBForAPIKey(ctx context.Context, key string) (*storage.DB, string, bool, bool) {
	if m.LegacyMode() {
		if key == m.LegacyAPIKey() {
			return m.LegacyDB(), "health", true, true
		}
		return nil, "", false, false
	}
	user, err := m.reg.GetByAPIKey(ctx, key)
	if err != nil {
		return nil, "", false, false
	}
	db, err := m.GetOrCreate(ctx, user.SchemaName)
	if err != nil {
		return nil, "", false, false
	}
	return db, user.SchemaName, user.IsAdmin, true
}

// DBForUsername looks up a tenant by username and returns their DB.
func (m *Manager) DBForUsername(ctx context.Context, username string) (*storage.DB, string, bool, bool) {
	if m.LegacyMode() {
		if username == "admin" {
			return m.LegacyDB(), "health", true, true
		}
		return nil, "", false, false
	}
	user, err := m.reg.GetByUsername(ctx, username)
	if err != nil {
		return nil, "", false, false
	}
	db, err := m.GetOrCreate(ctx, user.SchemaName)
	if err != nil {
		return nil, "", false, false
	}
	return db, user.SchemaName, user.IsAdmin, true
}

// DBForEmail looks up a tenant by email address and returns their DB.
func (m *Manager) DBForEmail(ctx context.Context, email string) (*storage.DB, string, bool, bool) {
	if m.LegacyMode() {
		return nil, "", false, false
	}
	user, err := m.reg.GetByEmail(ctx, email)
	if err != nil {
		return nil, "", false, false
	}
	db, err := m.GetOrCreate(ctx, user.SchemaName)
	if err != nil {
		return nil, "", false, false
	}
	return db, user.SchemaName, user.IsAdmin, true
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

// BackfillDatesFor returns the date-aware backfill trigger for a schema, or nil.
// Caller passes the explicit set of YYYY-MM-DD dates that need to be rebuilt;
// the implementation typically debounces and runs UpsertRecentCache over the
// union after a short window.
func (m *Manager) BackfillDatesFor(schema string) func([]string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.tenants[schema]; ok && e.callbacks != nil {
		return e.callbacks.BackfillDates
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

// MorningTriggerFor returns the ingest-driven morning report trigger
// for a schema, or nil when the tenant did not register one (legacy
// single-user mode wires the call directly in its own onNewData).
// Callers should goroutine-dispatch the result so a slow Telegram
// send never blocks the ingest 200-response path.
func (m *Manager) MorningTriggerFor(schema string) func() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.tenants[schema]; ok && e.callbacks != nil {
		return e.callbacks.MorningTrigger
	}
	return nil
}

// MorningSendMuFor returns the per-tenant send-dedup mutex registered
// at startup. Both the scheduler loop and the ingest trigger must lock
// it around their "HasSentMorningReport → send → Mark" sequence so the
// two callers never race and produce a duplicate Telegram report.
// Returns nil for tenants without a registered mutex; callers must
// treat nil as "no dedup needed" (legacy single-sender mode).
func (m *Manager) MorningSendMuFor(schema string) *sync.Mutex {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.tenants[schema]; ok && e.callbacks != nil {
		return e.callbacks.MorningSendMu
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

// AIDefaultsFor returns the AI config defaults for a schema, layering
// installation-wide global_settings on top of env-derived defaults so a
// single admin-managed key reaches every tenant whose own settings are
// blank. Per-tenant overrides (in each schema's `settings` table) still
// win — global is a fallback, not a force.
//
// `ctx` is propagated into the registry lookup so request-scoped cancel
// / deadline shut down the DB query along with the HTTP request.
func (m *Manager) AIDefaultsFor(ctx context.Context, schema string) storage.AIConfig {
	m.mu.RLock()
	base := storage.AIConfig{}
	if e, ok := m.tenants[schema]; ok && e.callbacks != nil {
		base = e.callbacks.AIDefaults
	}
	m.mu.RUnlock()

	if m.reg == nil {
		return base
	}
	// Comma-ok presence check (not !=""): a row written by the admin with
	// an empty value should clear the env default, otherwise the precedence
	// `tenant.settings -> global -> env` is broken (an explicit blank in
	// global would silently fall through to env).
	g := m.reg.GetAllGlobalSettings(ctx)
	if v, ok := g["gemini_api_key"]; ok {
		base.APIKey = v
	}
	if v, ok := g["gemini_model"]; ok {
		base.Model = v
	}
	if v, ok := g["gemini_max_tokens"]; ok {
		if v == "" {
			base.MaxOutputTokens = 0 // gemini.go treats <=0 as "use default 5000"
		} else if n, err := strconv.Atoi(v); err == nil {
			base.MaxOutputTokens = n
		}
	}
	return base
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

// ActiveDBs returns cached pools that are still authorized by current ACTIVE
// registry metadata. A cached pool is a resource optimization, never an
// authorization grant. Registry errors and metadata drift fail closed.
func (m *Manager) ActiveDBs(ctx context.Context) map[string]*storage.DB {
	if !m.isolationEnabled {
		m.mu.RLock()
		if m.legacyMode && m.legacyDB != nil {
			db := m.legacyDB
			m.mu.RUnlock()
			return map[string]*storage.DB{"health": db}
		}
		m.mu.RUnlock()
		return m.AllDBs()
	}
	type candidate struct {
		db                *storage.DB
		schemaName        string
		tenantID          uuid.UUID
		dbRole            string
		credentialVersion int
	}
	m.mu.RLock()
	candidates := make(map[string]candidate, len(m.tenants))
	for schema, e := range m.tenants {
		candidates[schema] = candidate{db: e.db, schemaName: e.schemaName, tenantID: e.tenantID, dbRole: e.dbRole, credentialVersion: e.credentialVersion}
	}
	m.mu.RUnlock()
	active := make(map[string]*storage.DB, len(candidates))
	for schema, cached := range candidates {
		user, err := m.metadata.GetBySchema(ctx, schema)
		if err != nil || user.ProvisioningState != registry.ProvisioningStateActive || !user.DBIsolationReady || user.SchemaName != schema || user.SchemaName != cached.schemaName || user.TenantID != cached.tenantID || user.DBRole != cached.dbRole || user.DBCredentialVersion != cached.credentialVersion {
			continue
		}
		active[schema] = cached.db
	}
	return active
}

// Close shuts down all tenant DB pools.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	closed := map[*storage.DB]struct{}{}
	for _, e := range m.tenants {
		e.db.Close()
		closed[e.db] = struct{}{}
	}
	if m.legacyDB != nil {
		if _, ok := closed[m.legacyDB]; !ok {
			m.legacyDB.Close()
		}
	}
}
