package tenants

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

type scriptedRegistry struct {
	mu    sync.Mutex
	users []*registry.User
	call  int
}

type schemaRegistry struct {
	users map[string]*registry.User
}

func (s *schemaRegistry) GetBySchema(_ context.Context, schema string) (*registry.User, error) {
	u := s.users[schema]
	if u == nil {
		return nil, registry.ErrUserNotFound
	}
	copy := *u
	return &copy, nil
}
func (*schemaRegistry) GetByAPIKey(context.Context, string) (*registry.User, error) {
	return nil, registry.ErrUserNotFound
}
func (*schemaRegistry) GetByUsername(context.Context, string) (*registry.User, error) {
	return nil, registry.ErrUserNotFound
}
func (*schemaRegistry) GetByEmail(context.Context, string) (*registry.User, error) {
	return nil, registry.ErrUserNotFound
}
func (*schemaRegistry) GetAllGlobalSettings(context.Context) map[string]string { return nil }

type blockingSchemaRegistry struct {
	schemaRegistry
	mu      sync.Mutex
	user    *registry.User
	calls   int
	entered chan struct{}
	release chan struct{}
}

type failingSchemaRegistry struct{ err error }

func (s failingSchemaRegistry) GetBySchema(context.Context, string) (*registry.User, error) {
	return nil, s.err
}
func (s failingSchemaRegistry) GetByAPIKey(context.Context, string) (*registry.User, error) {
	return nil, s.err
}
func (s failingSchemaRegistry) GetByUsername(context.Context, string) (*registry.User, error) {
	return nil, s.err
}
func (s failingSchemaRegistry) GetByEmail(context.Context, string) (*registry.User, error) {
	return nil, s.err
}
func (s failingSchemaRegistry) GetAllGlobalSettings(context.Context) map[string]string { return nil }

func (s *blockingSchemaRegistry) GetBySchema(context.Context, string) (*registry.User, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	u := *s.user
	s.mu.Unlock()
	if call == 1 {
		close(s.entered)
		<-s.release
	}
	return &u, nil
}

func (s *scriptedRegistry) GetBySchema(context.Context, string) (*registry.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) == 0 {
		return nil, registry.ErrUserNotFound
	}
	i := s.call
	if i >= len(s.users) {
		i = len(s.users) - 1
	}
	s.call++
	u := *s.users[i]
	return &u, nil
}
func (*scriptedRegistry) GetByAPIKey(context.Context, string) (*registry.User, error) {
	return nil, registry.ErrUserNotFound
}
func (*scriptedRegistry) GetByUsername(context.Context, string) (*registry.User, error) {
	return nil, registry.ErrUserNotFound
}
func (*scriptedRegistry) GetByEmail(context.Context, string) (*registry.User, error) {
	return nil, registry.ErrUserNotFound
}
func (*scriptedRegistry) GetAllGlobalSettings(context.Context) map[string]string { return nil }

func activeTestUser(schema string, id uuid.UUID, version int) *registry.User {
	return &registry.User{SchemaName: schema, TenantID: id, DBRole: TenantRoleName(id), DBCredentialVersion: version, DBIsolationReady: true, ProvisioningState: registry.ProvisioningStateActive}
}

func isolatedTestManager(t *testing.T, loader managerRegistry) (*Manager, *storage.DB, *int) {
	t.Helper()
	m, err := NewIsolated(loader, "postgres://db.example/health", CredentialDeriver{Current: SecretVersion{Version: 1, Secret: []byte("test-master-secret-with-32-bytes-min")}})
	if err != nil {
		t.Fatal(err)
	}
	db := &storage.DB{}
	closed := new(int)
	m.openRestricted = func(context.Context, string, string, string, string) (*storage.DB, error) { return db, nil }
	m.assertIdentity = func(context.Context, *storage.DB, string, string) error { return nil }
	m.closeDB = func(*storage.DB) { *closed++ }
	return m, db, closed
}

func TestGetOrCreateCachedHitRejectsInactiveWithoutClosingPool(t *testing.T) {
	id := uuid.New()
	active := activeTestUser("health_a", id, 1)
	failed := *active
	failed.ProvisioningState = registry.ProvisioningStateFailed
	loader := &scriptedRegistry{users: []*registry.User{&failed}}
	m, db, closed := isolatedTestManager(t, loader)
	m.tenants[active.SchemaName] = tenantIdentity{schemaName: active.SchemaName, tenantID: id, dbRole: active.DBRole, credentialVersion: 1}.entry(db)
	if got, err := m.GetOrCreate(context.Background(), active.SchemaName); err == nil || got != nil {
		t.Fatalf("got=%p err=%v", got, err)
	}
	if *closed != 0 || len(m.tenants) != 1 {
		t.Fatalf("closed=%d cached=%d", *closed, len(m.tenants))
	}
}

func TestGetOrCreateCachedMetadataErrorRetainsPool(t *testing.T) {
	for _, lookupErr := range []error{context.Canceled, errors.New("temporary registry outage")} {
		m, db, closed := isolatedTestManager(t, failingSchemaRegistry{err: lookupErr})
		id := uuid.New()
		cached := tenantIdentity{schemaName: "health_a", tenantID: id, dbRole: TenantRoleName(id), credentialVersion: 1}.entry(db)
		cached.callbacks = &TenantCallbacks{Backfill: func(bool) {}}
		m.tenants["health_a"] = cached
		if got, err := m.GetOrCreate(context.Background(), "health_a"); got != nil || !errors.Is(err, lookupErr) {
			t.Fatalf("lookup error=%v db=%p err=%v", lookupErr, got, err)
		}
		if *closed != 0 || m.tenants["health_a"] != cached || cached.callbacks == nil {
			t.Fatalf("lookup error=%v closed=%d cached=%v callbacks=%v", lookupErr, *closed, m.tenants["health_a"] == cached, cached.callbacks != nil)
		}
	}
}

func TestGetOrCreateCachedHitRetainsPoolAndCallbacksOnIdentityDrift(t *testing.T) {
	id := uuid.New()
	current := activeTestUser("health_a", id, 2)
	cases := []entry{
		{schemaName: "health_other", tenantID: id, dbRole: current.DBRole, credentialVersion: 2},
		{schemaName: current.SchemaName, tenantID: uuid.New(), dbRole: current.DBRole, credentialVersion: 2},
		{schemaName: current.SchemaName, tenantID: id, dbRole: TenantRoleName(uuid.New()), credentialVersion: 2},
		{schemaName: current.SchemaName, tenantID: id, dbRole: current.DBRole, credentialVersion: 1},
	}
	for _, cached := range cases {
		loader := &scriptedRegistry{users: []*registry.User{current}}
		m, db, closed := isolatedTestManager(t, loader)
		cached.db = db
		callback := func(bool) {}
		cached.callbacks = &TenantCallbacks{Backfill: callback}
		m.tenants[current.SchemaName] = &cached
		if got, err := m.GetOrCreate(context.Background(), current.SchemaName); !errors.Is(err, ErrTenantMetadataChanged) || got != nil {
			t.Fatalf("got=%p err=%v", got, err)
		}
		if *closed != 0 || len(m.tenants) != 1 || m.tenants[current.SchemaName] != &cached || m.tenants[current.SchemaName].callbacks == nil {
			t.Fatalf("closed=%d cached=%d", *closed, len(m.tenants))
		}
	}
}

func TestGetOrCreateCoalescesConcurrentCachedValidation(t *testing.T) {
	active := activeTestUser("health_a", uuid.New(), 1)
	loader := &blockingSchemaRegistry{user: active, entered: make(chan struct{}), release: make(chan struct{})}
	m, db, closed := isolatedTestManager(t, loader)
	m.tenants[active.SchemaName] = tenantIdentity{schemaName: active.SchemaName, tenantID: active.TenantID, dbRole: active.DBRole, credentialVersion: 1}.entry(db)
	type result struct {
		db  *storage.DB
		err error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() { got, err := m.GetOrCreate(context.Background(), active.SchemaName); first <- result{got, err} }()
	<-loader.entered
	go func() { got, err := m.GetOrCreate(context.Background(), active.SchemaName); second <- result{got, err} }()
	waitForOperationWaiters(t, m, active.SchemaName, 1)
	close(loader.release)
	for _, got := range []result{<-first, <-second} {
		if got.err != nil || got.db != db {
			t.Fatalf("coalesced validation db=%p err=%v", got.db, got.err)
		}
	}
	if *closed != 0 {
		t.Fatalf("cached pool closed during concurrent validation: %d", *closed)
	}
	loader.mu.Lock()
	loader.user = func() *registry.User {
		failed := *active
		failed.ProvisioningState = registry.ProvisioningStateFailed
		return &failed
	}()
	loader.mu.Unlock()
	if got, err := m.GetOrCreate(context.Background(), active.SchemaName); err == nil || got != nil {
		t.Fatalf("later inactive metadata db=%p err=%v", got, err)
	}
	if *closed != 0 || len(m.tenants) != 1 {
		t.Fatalf("closed=%d cached=%d after later drift", *closed, len(m.tenants))
	}
}

func TestGetOrCreateMetadataChangeBetweenOpenAndCacheClosesPool(t *testing.T) {
	id := uuid.New()
	original := activeTestUser("health_a", id, 1)
	changed := activeTestUser("health_a", id, 2)
	loader := &scriptedRegistry{users: []*registry.User{original, changed}}
	m, _, closed := isolatedTestManager(t, loader)
	got, err := m.GetOrCreate(context.Background(), original.SchemaName)
	if !errors.Is(err, ErrTenantMetadataChanged) || got != nil {
		t.Fatalf("got=%p err=%v", got, err)
	}
	if *closed != 1 || len(m.tenants) != 0 {
		t.Fatalf("closed=%d cached=%d", *closed, len(m.tenants))
	}
}

func TestGetOrCreateConcurrentWaiterSharesWinnerBeforeLaterMetadataDrift(t *testing.T) {
	idA, idB := uuid.New(), uuid.New()
	a := activeTestUser("health_a", idA, 1)
	b := activeTestUser("health_a", idB, 1)
	loader := &scriptedRegistry{users: []*registry.User{a, a, b}}
	m, db, closed := isolatedTestManager(t, loader)
	opened := make(chan struct{})
	release := make(chan struct{})
	m.openRestricted = func(context.Context, string, string, string, string) (*storage.DB, error) {
		close(opened)
		<-release
		return db, nil
	}
	type result struct {
		db  *storage.DB
		err error
	}
	first := make(chan result, 1)
	second := make(chan result, 1)
	go func() { got, err := m.GetOrCreate(context.Background(), a.SchemaName); first <- result{got, err} }()
	<-opened
	go func() { got, err := m.GetOrCreate(context.Background(), a.SchemaName); second <- result{got, err} }()
	waitForOperationWaiters(t, m, a.SchemaName, 1)
	close(release)
	r1 := <-first
	r2 := <-second
	if r1.err != nil || r1.db != db {
		t.Fatalf("winner db=%p err=%v", r1.db, r1.err)
	}
	if r2.err != nil || r2.db != db {
		t.Fatalf("waiter db=%p err=%v", r2.db, r2.err)
	}
	if got, err := m.GetOrCreate(context.Background(), a.SchemaName); !errors.Is(err, ErrTenantMetadataChanged) || got != nil {
		t.Fatalf("later metadata drift db=%p err=%v", got, err)
	}
	if *closed != 0 || len(m.tenants) != 1 {
		t.Fatalf("closed=%d cached=%d", *closed, len(m.tenants))
	}
}

func waitForOperationWaiters(t *testing.T, m *Manager, schema string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.RLock()
		pending := m.operations[schema]
		got := 0
		if pending != nil {
			got = pending.waiters
		}
		m.mu.RUnlock()
		if got >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("schema %s did not register %d operation waiters", schema, want)
}

func TestGetOrCreateFailedOpenIsSharedWithWaiters(t *testing.T) {
	user := activeTestUser("health_a", uuid.New(), 1)
	loader := &schemaRegistry{users: map[string]*registry.User{user.SchemaName: user}}
	m, _, _ := isolatedTestManager(t, loader)
	wantErr := errors.New("database unavailable")
	opened := make(chan struct{})
	release := make(chan struct{})
	opens := 0
	m.openRestricted = func(context.Context, string, string, string, string) (*storage.DB, error) {
		opens++
		close(opened)
		<-release
		return nil, wantErr
	}
	type result struct {
		db  *storage.DB
		err error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() { db, err := m.GetOrCreate(context.Background(), user.SchemaName); first <- result{db, err} }()
	<-opened
	go func() { db, err := m.GetOrCreate(context.Background(), user.SchemaName); second <- result{db, err} }()
	waitForOperationWaiters(t, m, user.SchemaName, 1)
	close(release)
	for _, got := range []result{<-first, <-second} {
		if got.db != nil || !errors.Is(got.err, wantErr) {
			t.Fatalf("shared failure db=%p err=%v", got.db, got.err)
		}
	}
	if opens != 1 {
		t.Fatalf("pool opens=%d, want 1", opens)
	}
}

func TestGetOrCreateWaiterCancellationDoesNotCancelLeader(t *testing.T) {
	user := activeTestUser("health_a", uuid.New(), 1)
	loader := &schemaRegistry{users: map[string]*registry.User{user.SchemaName: user}}
	m, db, _ := isolatedTestManager(t, loader)
	opened := make(chan struct{})
	release := make(chan struct{})
	m.openRestricted = func(context.Context, string, string, string, string) (*storage.DB, error) {
		close(opened)
		<-release
		return db, nil
	}
	leader := make(chan error, 1)
	go func() { _, err := m.GetOrCreate(context.Background(), user.SchemaName); leader <- err }()
	<-opened
	waitCtx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() { _, err := m.GetOrCreate(waitCtx, user.SchemaName); waiter <- err }()
	waitForOperationWaiters(t, m, user.SchemaName, 1)
	cancel()
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error=%v", err)
	}
	close(release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
}

func TestGetOrCreateLeaderCancellationDoesNotPoisonHealthyWaiter(t *testing.T) {
	user := activeTestUser("health_a", uuid.New(), 1)
	loader := &schemaRegistry{users: map[string]*registry.User{user.SchemaName: user}}
	m, db, _ := isolatedTestManager(t, loader)
	opened := make(chan struct{})
	release := make(chan struct{})
	m.openRestricted = func(ctx context.Context, _ string, _, _, _ string) (*storage.DB, error) {
		close(opened)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return db, nil
		}
	}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() { _, err := m.GetOrCreate(leaderCtx, user.SchemaName); leader <- err }()
	<-opened
	cancelLeader()
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error=%v", err)
	}
	healthy := make(chan struct {
		db  *storage.DB
		err error
	}, 1)
	go func() {
		got, err := m.GetOrCreate(context.Background(), user.SchemaName)
		healthy <- struct {
			db  *storage.DB
			err error
		}{got, err}
	}()
	waitForOperationWaiters(t, m, user.SchemaName, 1)
	close(release)
	got := <-healthy
	if got.err != nil || got.db != db {
		t.Fatalf("healthy waiter db=%p err=%v", got.db, got.err)
	}
}

func TestGetOrCreateSlowSchemaDoesNotBlockUnrelatedSchema(t *testing.T) {
	a := activeTestUser("health_a", uuid.New(), 1)
	b := activeTestUser("health_b", uuid.New(), 1)
	loader := &schemaRegistry{users: map[string]*registry.User{a.SchemaName: a, b.SchemaName: b}}
	m, err := NewIsolated(loader, "postgres://db.example/health", CredentialDeriver{Current: SecretVersion{Version: 1, Secret: []byte("test-master-secret-with-32-bytes-min")}})
	if err != nil {
		t.Fatal(err)
	}
	aDB, bDB := &storage.DB{}, &storage.DB{}
	aOpened := make(chan struct{})
	releaseA := make(chan struct{})
	m.openRestricted = func(_ context.Context, _ string, role, _, _ string) (*storage.DB, error) {
		if role == a.DBRole {
			close(aOpened)
			<-releaseA
			return aDB, nil
		}
		return bDB, nil
	}
	m.assertIdentity = func(context.Context, *storage.DB, string, string) error { return nil }
	m.closeDB = func(*storage.DB) {}

	aResult := make(chan error, 1)
	go func() {
		_, err := m.GetOrCreate(context.Background(), a.SchemaName)
		aResult <- err
	}()
	<-aOpened

	bResult := make(chan struct {
		db  *storage.DB
		err error
	}, 1)
	go func() {
		db, err := m.GetOrCreate(context.Background(), b.SchemaName)
		bResult <- struct {
			db  *storage.DB
			err error
		}{db: db, err: err}
	}()
	select {
	case got := <-bResult:
		if got.err != nil || got.db != bDB {
			t.Fatalf("unrelated schema db=%p err=%v", got.db, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated schema was blocked by slow tenant pool creation")
	}
	close(releaseA)
	if err := <-aResult; err != nil {
		t.Fatal(err)
	}
}

func TestGetOrCreateCloseDuringOpenClosesUncachedPool(t *testing.T) {
	user := activeTestUser("health_a", uuid.New(), 1)
	loader := &schemaRegistry{users: map[string]*registry.User{user.SchemaName: user}}
	m, db, closed := isolatedTestManager(t, loader)
	opened := make(chan struct{})
	release := make(chan struct{})
	m.openRestricted = func(context.Context, string, string, string, string) (*storage.DB, error) {
		close(opened)
		<-release
		return db, nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.GetOrCreate(context.Background(), user.SchemaName)
		result <- err
	}()
	<-opened
	m.Close()
	close(release)
	if err := <-result; !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("GetOrCreate error=%v", err)
	}
	if *closed != 1 || len(m.AllDBs()) != 0 {
		t.Fatalf("closed=%d cached=%d", *closed, len(m.AllDBs()))
	}
}
