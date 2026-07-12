package tenants

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

type scriptedRegistry struct {
	mu    sync.Mutex
	users []*registry.User
	call  int
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

func TestGetOrCreateCachedHitEvictsInactivePool(t *testing.T) {
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
	if *closed != 1 || len(m.tenants) != 0 {
		t.Fatalf("closed=%d cached=%d", *closed, len(m.tenants))
	}
}

func TestGetOrCreateCachedHitEvictsIdentityDrift(t *testing.T) {
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
		m.tenants[current.SchemaName] = &cached
		if got, err := m.GetOrCreate(context.Background(), current.SchemaName); !errors.Is(err, ErrTenantMetadataChanged) || got != nil {
			t.Fatalf("got=%p err=%v", got, err)
		}
		if *closed != 1 || len(m.tenants) != 0 {
			t.Fatalf("closed=%d cached=%d", *closed, len(m.tenants))
		}
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

func TestGetOrCreateConcurrentWaiterRejectsWinnerAfterMetadataDrift(t *testing.T) {
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
	close(release)
	r1 := <-first
	r2 := <-second
	if r1.err != nil || r1.db != db {
		t.Fatalf("winner db=%p err=%v", r1.db, r1.err)
	}
	if !errors.Is(r2.err, ErrTenantMetadataChanged) || r2.db != nil {
		t.Fatalf("waiter db=%p err=%v", r2.db, r2.err)
	}
	if *closed != 1 || len(m.tenants) != 0 {
		t.Fatalf("closed=%d cached=%d", *closed, len(m.tenants))
	}
}
