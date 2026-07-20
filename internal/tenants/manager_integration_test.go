package tenants

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestManagerRejectsPoolIdentityMismatch(t *testing.T) {
	fixture := newManagerFixture(t)
	wrong := *fixture.user
	wrong.SchemaName += "_missing"
	mgr, err := NewIsolated(fakeTenantMetadataLoader{user: &wrong}, fixture.baseDSN, fixture.deriver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	if _, err := mgr.GetOrCreate(context.Background(), wrong.SchemaName); err == nil {
		t.Fatal("accepted pool whose current_schema did not match active registry metadata")
	}
	if len(mgr.AllDBs()) != 0 {
		t.Fatal("identity-mismatched pool was cached")
	}
}

func TestManagerRejectsCurrentUserMismatchAndDoesNotCache(t *testing.T) {
	fixture := newManagerFixture(t)
	ctx := context.Background()
	otherRole := fmt.Sprintf("mgr_other_%d", os.Getpid())
	otherPassword := "disposable-other-role-password"
	var createRole string
	if err := fixture.admin.QueryRow(ctx, `SELECT format('CREATE ROLE %I LOGIN PASSWORD %L',$1::text,$2::text)`, otherRole, otherPassword).Scan(&createRole); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.admin.Exec(ctx, createRole); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fixture.admin.Exec(context.Background(), "REVOKE USAGE ON SCHEMA "+pgxIdent(fixture.user.SchemaName)+" FROM "+pgxIdent(otherRole))
		_, _ = fixture.admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgxIdent(otherRole))
	})
	if _, err := fixture.admin.Exec(ctx, "GRANT USAGE ON SCHEMA "+pgxIdent(fixture.user.SchemaName)+" TO "+pgxIdent(otherRole)); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewIsolated(fixture.reg, fixture.baseDSN, fixture.deriver)
	if err != nil {
		t.Fatal(err)
	}
	mgr.openRestricted = func(ctx context.Context, base, _, _, schema string) (*storage.DB, error) {
		return storage.NewRestrictedTenant(ctx, base, otherRole, otherPassword, schema)
	}
	t.Cleanup(mgr.Close)
	_, err = mgr.GetOrCreate(ctx, fixture.user.SchemaName)
	if err == nil || !strings.Contains(err.Error(), "current_user") {
		t.Fatalf("identity mismatch error=%v", err)
	}
	if len(mgr.AllDBs()) != 0 {
		t.Fatal("current_user-mismatched pool was cached")
	}
}

func TestManagerRejectsIncompleteActiveRegistryMetadata(t *testing.T) {
	fixture := newManagerFixture(t)
	ctx := context.Background()
	// This destructive catalog mutation is safe only because newManagerFixture
	// already required HEALTH_DB_TESTS=1 and the disposable database marker.
	if _, err := fixture.admin.Exec(ctx, `ALTER TABLE health_registry.users ALTER COLUMN db_role DROP NOT NULL`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fixture.admin.Exec(context.Background(), `UPDATE health_registry.users SET db_role=$1 WHERE username=$2`, fixture.user.DBRole, fixture.user.Username)
		_, _ = fixture.admin.Exec(context.Background(), `ALTER TABLE health_registry.users ALTER COLUMN db_role SET NOT NULL`)
	})
	if _, err := fixture.admin.Exec(ctx, `UPDATE health_registry.users SET db_role=NULL WHERE username=$1 AND provisioning_state='active'`, fixture.user.Username); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewIsolated(fixture.reg, fixture.baseDSN, fixture.deriver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	if _, err := mgr.GetOrCreate(ctx, fixture.user.SchemaName); err == nil {
		t.Fatal("GetOrCreate accepted incomplete ACTIVE registry metadata")
	}
	if db, _, _, ok := mgr.DBForAPIKey(ctx, fixture.user.APIKey); ok || db != nil {
		t.Fatal("DBForAPIKey returned a pool for incomplete ACTIVE metadata")
	}
	if len(mgr.AllDBs()) != 0 {
		t.Fatal("incomplete metadata pool was cached")
	}
}

func TestManagerRestrictedPoolIdentityAndConcurrentSinglePool(t *testing.T) {
	fixture := newManagerFixture(t)
	mgr, err := NewIsolated(fixture.reg, fixture.baseDSN, fixture.deriver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	const callers = 12
	results := make(chan *storage.DB, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := mgr.GetOrCreate(context.Background(), fixture.user.SchemaName)
			results <- db
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *storage.DB
	for db := range results {
		if first == nil {
			first = db
		} else if db != first {
			t.Fatal("concurrent GetOrCreate returned more than one pool")
		}
	}
	if len(mgr.AllDBs()) != 1 {
		t.Fatalf("cached pools=%d, want 1", len(mgr.AllDBs()))
	}
	if err := first.AssertIdentity(context.Background(), fixture.user.DBRole, fixture.user.SchemaName); err != nil {
		t.Fatal(err)
	}
	if err := first.VerifyProvisionedSchema(); err != nil {
		t.Fatalf("restricted pool cannot use own schema: %v", err)
	}
	if err := first.VerifyTenantIsolation(context.Background(), fixture.forbiddenSchema); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramRoutingRechecksActiveRegistryMetadata(t *testing.T) {
	fixture := newManagerFixture(t)
	ctx := context.Background()
	mgr, err := NewIsolated(fixture.reg, fixture.baseDSN, fixture.deriver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	db, err := mgr.GetOrCreate(ctx, fixture.user.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	const chatID = "manager-active-routing-fixture"
	if err := db.SaveSettings(map[string]string{"telegram_chat_id": chatID}); err != nil {
		t.Fatal(err)
	}
	if got, schema, ok := mgr.DBForTelegramChatID(ctx, storage.NotifyConfig{}, chatID); !ok || got != db || schema != fixture.user.SchemaName {
		t.Fatalf("active route db=%p schema=%q ok=%v", got, schema, ok)
	}
	if _, err := fixture.admin.Exec(ctx, `UPDATE health_registry.users SET provisioning_state='failed' WHERE username=$1`, fixture.user.Username); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fixture.admin.Exec(context.Background(), `UPDATE health_registry.users SET provisioning_state='active' WHERE username=$1`, fixture.user.Username)
	})
	if got, schema, ok := mgr.DBForTelegramChatID(ctx, storage.NotifyConfig{}, chatID); ok || got != nil || schema != "" {
		t.Fatal("inactive registry tenant retained Telegram authorization through cache")
	}
	if _, err := fixture.admin.Exec(ctx, `UPDATE health_registry.users SET provisioning_state='active' WHERE username=$1`, fixture.user.Username); err != nil {
		t.Fatal(err)
	}
	if got, schema, ok := mgr.DBForTelegramChatID(ctx, storage.NotifyConfig{}, chatID); !ok || got != db || schema != fixture.user.SchemaName {
		t.Fatalf("reactivated route db=%p schema=%q ok=%v", got, schema, ok)
	}
}

type managerFixture struct {
	admin           *pgxpool.Pool
	reg             *registry.Registry
	user            *registry.User
	baseDSN         string
	deriver         CredentialDeriver
	forbiddenSchema string
}

func newManagerFixture(t *testing.T) managerFixture {
	t.Helper()
	dsn := testdb.DSN(t)
	adminDSN := requireFixedAdminTestDSN(t)
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
	name := fmt.Sprintf("mgr%d", os.Getpid())
	user, op, err := reg.ReserveUser(ctx, registry.CreateUserReq{Username: name, SchemaName: name + "_schema", Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.DeleteUser(context.Background(), name) })
	deriver := CredentialDeriver{Current: SecretVersion{Version: 1, Secret: []byte("disposable-manager-master-secret-32-bytes")}}
	p, err := NewProvisioner(ctx, adminDSN, credentialFreeTestDSN(t, dsn), deriver, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	spec := TenantSpec{TenantID: op.TenantID, OperationID: op.OperationID, SchemaName: op.SchemaName, DBRole: op.DBRole, CredentialVersion: op.CredentialVersion}
	t.Cleanup(func() {
		if err := cleanupTenantFixtureAsRoot(context.Background(), admin, spec); err != nil {
			t.Errorf("cleanup manager tenant fixture: %v", err)
		}
	})
	if err := reg.AdvanceProvisioning(ctx, op.OperationID, registry.ProvisioningStatePending, registry.ProvisioningStateProvisioning, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureTenant(ctx, spec); err != nil {
		t.Fatal(err)
	}
	op.State = registry.ProvisioningStateProvisioning
	if err := reg.ActivateProvisioned(ctx, op, registry.SchemaContractMetadata{Version: storage.SchemaContractVersion, Checksum: storage.SchemaContractChecksum()}); err != nil {
		t.Fatal(err)
	}
	active, err := reg.GetBySchema(ctx, user.SchemaName)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenSchema := name + "_other"
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgxIdent(forbiddenSchema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgxIdent(forbiddenSchema)+" CASCADE")
	})
	if _, err := admin.Exec(ctx, "CREATE TABLE "+pgxIdent(forbiddenSchema)+".isolation_probe(id int)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "REVOKE ALL ON SCHEMA "+pgxIdent(forbiddenSchema)+" FROM PUBLIC"); err != nil {
		t.Fatal(err)
	}
	return managerFixture{admin: admin, reg: reg, user: active, baseDSN: credentialFreeTestDSN(t, dsn), deriver: deriver, forbiddenSchema: forbiddenSchema}
}

func credentialFreeTestDSN(t *testing.T, raw string) string {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	host := net.JoinHostPort(cfg.ConnConfig.Host, strconv.Itoa(int(cfg.ConnConfig.Port)))
	u := url.URL{Scheme: "postgres", Host: host, Path: "/" + cfg.ConnConfig.Database}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}
