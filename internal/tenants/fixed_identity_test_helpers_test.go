package tenants

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func requireFixedAdminTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("ADMIN_TEST_DSN")
	if dsn == "" {
		t.Fatal("HEALTH_DB_TESTS=1 requires ADMIN_TEST_DSN for exact health_admin identity")
	}
	return dsn
}

func testDSNForRole(t *testing.T, raw, user, password string) string {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	host := net.JoinHostPort(cfg.ConnConfig.Host, strconv.Itoa(int(cfg.ConnConfig.Port)))
	u := url.URL{
		Scheme: "postgres",
		Host:   host,
		Path:   "/" + cfg.ConnConfig.Database,
		User:   url.UserPassword(user, password),
	}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

func createLegacyOwnerTestIdentity(t *testing.T, ctx context.Context, root *pgxpool.Pool, rootDSN string) string {
	t.Helper()
	const password = "disposable-legacy-owner-password"
	var createRole string
	if err := root.QueryRow(ctx, `SELECT format('CREATE ROLE %I LOGIN PASSWORD %L',$1::text,$2::text)`, legacyDatabaseRole, password).Scan(&createRole); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exec(ctx, createRole); err != nil {
		t.Fatalf("create disposable legacy owner: %v", err)
	}
	t.Cleanup(func() {
		_, _ = root.Exec(context.Background(), `REVOKE health_user FROM health_admin`)
		_, _ = root.Exec(context.Background(), `DROP OWNED BY health_user`)
		if _, err := root.Exec(context.Background(), `DROP ROLE IF EXISTS health_user`); err != nil {
			t.Errorf("drop disposable legacy owner: %v", err)
		}
	})
	var grantDatabase string
	if err := root.QueryRow(ctx, `SELECT format('GRANT CONNECT,CREATE ON DATABASE %I TO health_user',current_database())`).Scan(&grantDatabase); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exec(ctx, grantDatabase); err != nil {
		t.Fatalf("grant disposable legacy database access: %v", err)
	}
	if _, err := root.Exec(ctx, `GRANT health_user TO health_admin WITH INHERIT FALSE, SET TRUE`); err != nil {
		t.Fatalf("grant disposable legacy SET bridge: %v", err)
	}
	return testDSNForRole(t, rootDSN, legacyDatabaseRole, password)
}

func requireFixedIdentityTestDSNs(t *testing.T) (string, string) {
	t.Helper()
	adminDSN := requireFixedAdminTestDSN(t)
	registryDSN := os.Getenv("REGISTRY_TEST_DSN")
	if registryDSN == "" {
		t.Fatal("HEALTH_DB_TESTS=1 requires REGISTRY_TEST_DSN for exact health_registry identity")
	}
	return adminDSN, registryDSN
}

func cleanupTenantFixtureAsRoot(ctx context.Context, root *pgxpool.Pool, spec TenantSpec) error {
	var disposable bool
	if err := root.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM health_test_metadata WHERE key='disposable_database' AND value='true')`).Scan(&disposable); err != nil || !disposable {
		return fmt.Errorf("root fixture cleanup requires disposable database marker: %w", err)
	}
	var owner string
	if err := root.QueryRow(ctx, `SELECT owner.rolname FROM pg_namespace n JOIN pg_roles owner ON owner.oid=n.nspowner WHERE n.nspname=$1`, spec.SchemaName).Scan(&owner); err != nil {
		return err
	}
	if owner != spec.DBRole {
		return fmt.Errorf("root fixture cleanup schema owner=%q want=%q", owner, spec.DBRole)
	}
	var tenantID, operationID uuid.UUID
	marker := pgx.Identifier{spec.SchemaName, provisionMarkerTable}.Sanitize()
	if err := root.QueryRow(ctx, "SELECT tenant_id,operation_id FROM "+marker+" WHERE singleton=true").Scan(&tenantID, &operationID); err != nil {
		return err
	}
	if tenantID != spec.TenantID || operationID != spec.OperationID {
		return fmt.Errorf("root fixture cleanup marker identity mismatch")
	}
	if _, err := root.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{spec.SchemaName}.Sanitize()+" CASCADE"); err != nil {
		return err
	}
	if _, err := root.Exec(ctx, "DROP OWNED BY "+pgx.Identifier{spec.DBRole}.Sanitize()); err != nil {
		return err
	}
	_, err := root.Exec(ctx, "DROP ROLE "+pgx.Identifier{spec.DBRole}.Sanitize())
	return err
}
