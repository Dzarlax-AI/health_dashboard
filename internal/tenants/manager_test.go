package tenants

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
)

type fakeTenantMetadataLoader struct {
	user *registry.User
	err  error
}

func TestTelegramRoutingRejectsInactiveCachedTenantWithoutReadingPool(t *testing.T) {
	id := uuid.New()
	user := &registry.User{SchemaName: "health_a", TenantID: id, DBRole: TenantRoleName(id), DBCredentialVersion: 1, ProvisioningState: registry.ProvisioningStateFailed}
	mgr, err := NewIsolated(fakeTenantMetadataLoader{user: user}, "postgres://db.example/health", CredentialDeriver{Current: SecretVersion{Version: 1, Secret: []byte("test-master-secret-with-32-bytes-min")}})
	if err != nil {
		t.Fatal(err)
	}
	mgr.tenants[user.SchemaName] = &entry{db: nil, tenantID: id, dbRole: user.DBRole, credentialVersion: 1}
	if db, schema, ok := mgr.DBForTelegramChatID(context.Background(), storage.NotifyConfig{ChatID: "cached-chat"}, "cached-chat"); ok || db != nil || schema != "" {
		t.Fatal("inactive cached tenant authorized Telegram routing")
	}
}

func TestIsolatedManagerRejectsLegacyDowngradeWithoutStoringSharedState(t *testing.T) {
	id := uuid.New()
	user := &registry.User{SchemaName: "health_a", TenantID: id, DBRole: TenantRoleName(id), DBCredentialVersion: 1, ProvisioningState: registry.ProvisioningStateActive}
	mgr, err := NewIsolated(fakeTenantMetadataLoader{user: user}, "postgres://db.example/health", CredentialDeriver{Current: SecretVersion{Version: 1, Secret: []byte("test-master-secret-with-32-bytes-min")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetLegacyMode(nil, "shared-api-key", "shared-password-hash"); !errors.Is(err, ErrIsolationMode) {
		t.Fatalf("SetLegacyMode error=%v, want ErrIsolationMode", err)
	}
	if mgr.LegacyMode() || mgr.LegacyDB() != nil || mgr.LegacyAPIKey() != "" || mgr.LegacyPasswordHash() != "" {
		t.Fatal("failed downgrade stored shared legacy state")
	}
	if len(mgr.AllDBs()) != 0 {
		t.Fatal("failed downgrade populated tenant cache")
	}
	if db, _, _, ok := mgr.DBForAPIKey(context.Background(), "shared-api-key"); ok || db != nil {
		t.Fatal("DBForAPIKey returned shared pool after rejected downgrade")
	}
	if db, _, _, ok := mgr.DBForUsername(context.Background(), "admin"); ok || db != nil {
		t.Fatal("DBForUsername returned shared pool after rejected downgrade")
	}
	if db, _, _, ok := mgr.DBForEmail(context.Background(), "admin@example.test"); ok || db != nil {
		t.Fatal("DBForEmail returned shared pool after rejected downgrade")
	}
}

func TestLegacyActiveDBsIncludesFallbackPool(t *testing.T) {
	mgr := New(nil, "postgres://db.example/health")
	db := &storage.DB{}
	if err := mgr.SetLegacyMode(db, "key", "hash"); err != nil {
		t.Fatal(err)
	}
	active := mgr.ActiveDBs(context.Background())
	if len(active) != 1 || active["health"] != db {
		t.Fatalf("legacy active pools = %#v", active)
	}
}

func (f fakeTenantMetadataLoader) GetBySchema(context.Context, string) (*registry.User, error) {
	return f.user, f.err
}

func (f fakeTenantMetadataLoader) GetByAPIKey(context.Context, string) (*registry.User, error) {
	return nil, registry.ErrUserNotFound
}

func (f fakeTenantMetadataLoader) GetByUsername(context.Context, string) (*registry.User, error) {
	return nil, registry.ErrUserNotFound
}

func (f fakeTenantMetadataLoader) GetByEmail(context.Context, string) (*registry.User, error) {
	return nil, registry.ErrUserNotFound
}

func (f fakeTenantMetadataLoader) GetAllGlobalSettings(context.Context) map[string]string { return nil }

func TestRestrictedTenantPoolConfigInjectsOnlyDerivedIdentity(t *testing.T) {
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	role := TenantRoleName(id)
	deriver := CredentialDeriver{Current: SecretVersion{Version: 3, Secret: []byte("test-master-secret-with-32-bytes-min")}}
	password, err := deriver.Derive(id, role, 3)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := storage.RestrictedTenantPoolConfig("postgres://db.example/health?sslmode=require&application_name=health", role, password, "health_alex")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.User != role || cfg.ConnConfig.Password != password {
		t.Fatalf("identity user=%q password-match=%v", cfg.ConnConfig.User, cfg.ConnConfig.Password == password)
	}
	if got := cfg.ConnConfig.RuntimeParams["search_path"]; got != "health_alex" {
		t.Fatalf("search_path=%q", got)
	}
	if cfg.MaxConns != 8 || cfg.MinConns != 2 {
		t.Fatalf("pool budget max=%d min=%d", cfg.MaxConns, cfg.MinConns)
	}
	if strings.Contains(cfg.ConnString(), "test-master-secret") {
		t.Fatal("pool config exposed master secret instead of derived credential")
	}
}

func TestIsolationEnabledIncompleteMetadataFailsBeforePoolOpen(t *testing.T) {
	cases := []registry.User{
		{SchemaName: "health_a", ProvisioningState: registry.ProvisioningStateActive},
		{SchemaName: "health_a", TenantID: uuid.New(), ProvisioningState: registry.ProvisioningStateActive},
	}
	for _, user := range cases {
		mgr, err := NewIsolated(fakeTenantMetadataLoader{user: &user}, "postgres://db.example/health", CredentialDeriver{Current: SecretVersion{Version: 1, Secret: []byte("test-master-secret-with-32-bytes-min")}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.GetOrCreate(context.Background(), user.SchemaName); err == nil {
			t.Fatalf("accepted incomplete metadata: %+v", user)
		}
		if len(mgr.AllDBs()) != 0 {
			t.Fatal("failed pool was cached")
		}
	}
}
